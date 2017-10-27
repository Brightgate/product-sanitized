/*
 * COPYRIGHT 2017 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 */

/*
 * Appliance configuration server.
 *
 *
 * Property namespace.
 *
 * All configuration is accessible via a unified namespace, which is
 * filesystem-like.
 *
 * /customer/customer_id/site/site_id/appliance/appliance_id/ is the
 * full path to a per-appliance configuration space.  A shorthand for
 * each of these is defined:
 *
 * @@@/ is equivalent to /customer/customer_id for this appliance's
 * customer.
 * @@/ is equivalent to /customer/customer_id/site/site_id for this
 * appliance's site.
 * @/ is equivalent to
 * /customer/customer_id/site/site_id/appliance/appliance_id/ for this
 *  appliance.
 *
 */

package main

import (
	"container/heap"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"bg/ap_common/aputil"
	"bg/ap_common/broker"
	"bg/ap_common/mcp"
	"bg/ap_common/network"

	"bg/base_def"
	"bg/base_msg"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/golang/protobuf/proto"
	"github.com/satori/uuid"

	// Ubuntu: requires libzmq3-dev, which is 0MQ 4.2.1.
	zmq "github.com/pebbe/zmq4"
)

const (
	property_filename = "ap_props.json"
	backup_filename   = "ap_props.json.bak"
	default_filename  = "ap_defaults.json"
	pname             = "ap.configd"

	minConfigVersion = 3
	curConfigVersion = 6
)

// Allow for significant variation in the processing of subtrees
type subtreeOps struct {
	get    func(*base_msg.ConfigQuery) (string, error)
	set    func(*base_msg.ConfigQuery, bool) error
	delete func(*base_msg.ConfigQuery) error
}

type subtreeMatch struct {
	match *regexp.Regexp
	ops   *subtreeOps
}

var defaultSubtreeOps = subtreeOps{getPropHandler, setPropHandler, delPropHandler}
var devSubtreeOps = subtreeOps{getDevHandler, setDevHandler, delDevHandler}

var subtreeMatchTable = []subtreeMatch{
	{regexp.MustCompile(`^@/devices`), &devSubtreeOps},
}

// Allow for specific properties to have their own handlers as well
type propertyOps struct {
	get    func(*pnode) (string, error)
	set    func(*pnode, string, *time.Time) (bool, error)
	expire func(*pnode)
}

type propertyMatch struct {
	match *regexp.Regexp
	ops   *propertyOps
}

var defaultPropOps = propertyOps{defaultGetter, defaultSetter, defaultExpire}
var ssidPropOps = propertyOps{defaultGetter, ssidUpdate, defaultExpire}
var uuidPropOps = propertyOps{defaultGetter, uuidUpdate, defaultExpire}
var ringPropOps = propertyOps{defaultGetter, defaultSetter, ringExpire}
var dnsPropOps = propertyOps{defaultGetter, dnsSetter, defaultExpire}
var cnamePropOps = propertyOps{defaultGetter, cnameSetter, defaultExpire}

var propertyMatchTable = []propertyMatch{
	{regexp.MustCompile(`^@/uuid$`), &uuidPropOps},
	{regexp.MustCompile(`^@/network/ssid$`), &ssidPropOps},
	{regexp.MustCompile(`^@/clients/.*/ring$`), &ringPropOps},
	{regexp.MustCompile(`^@/clients/.*/dns_name$`), &dnsPropOps},
	{regexp.MustCompile(`^@/dns/cnames/`), &cnamePropOps},
}

/*
 * All properties are currently represented as strings, but will presumably have
 * more varied types in the future.  Expires contains the time at which a
 * property will expire.  A property with a nil Expires field has no expiraton
 * date.
 */
type pnode struct {
	Name     string
	Value    string     `json:"Value,omitempty"`
	Modified *time.Time `json:"Modified,omitempty"`
	Expires  *time.Time `json:"Expires,omitempty"`
	Children []*pnode   `json:"Children,omitempty"`
	parent   *pnode
	path     string
	ops      *propertyOps

	// Used and maintained by the heap interface methods
	index int
}

type pnode_queue []*pnode

var (
	property_root = pnode{Name: "root"}
	addr          = flag.String("listen-address",
		base_def.CONFIGD_PROMETHEUS_PORT,
		"The address to listen on for HTTP requests.")
	brokerd *broker.Broker
	propdir = flag.String("propdir", "./",
		"directory in which the property files should be stored")

	ApVersion    string
	upgradeHooks []func() error

	exp_heap  pnode_queue
	exp_timer *time.Timer
	exp_mutex sync.Mutex
	expired   []string
)

/*******************************************************************
 *
 * Implement the functions required by the container/heap interface
 */
func (q pnode_queue) Len() int { return len(q) }

func (q pnode_queue) Less(i, j int) bool {
	return (q[i].Expires).Before(*q[j].Expires)
}

func (q pnode_queue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *pnode_queue) Push(x interface{}) {
	n := len(*q)
	prop := x.(*pnode)
	prop.index = n
	*q = append(*q, prop)
}

func (q *pnode_queue) Pop() interface{} {
	old := *q
	n := len(old)
	prop := old[n-1]
	prop.index = -1 // for safety
	*q = old[0 : n-1]
	return prop
}

/*************************************************************************
 *
 * Functions to implement property expiration and maintain the associated
 * datastructures.
 */
func expiration_handler() {
	reset := time.Duration(time.Minute)
	for true {
		<-exp_timer.C
		exp_mutex.Lock()

		for len(exp_heap) > 0 {
			next := exp_heap[0]
			now := time.Now()

			if now.Before(*next.Expires) {
				break
			}

			delay := now.Sub(*next.Expires)
			if delay.Seconds() > 1.0 {
				log.Printf("Missed expiration for %s by %s\n",
					next.Name, delay)
			}
			log.Printf("Expiring: %s at %v\n", next.Name, time.Now())
			heap.Pop(&exp_heap)

			next.index = -1
			next.Expires = nil

			next.ops.expire(next)
		}

		if len(exp_heap) > 0 {
			next := exp_heap[0]
			reset = time.Until(*next.Expires)
		}
		exp_timer.Reset(reset)
		exp_mutex.Unlock()
	}
}

func nextExpiration() *pnode {
	if len(exp_heap) == 0 {
		return nil
	}

	return exp_heap[0]
}

/*
 * Update the expiration time of a single property (possibly setting an
 * expiration for the first time).  If this property either starts or ends at
 * the top of the expiration heap, reset the expiration timer accordingly.
 */
func expirationUpdate(node *pnode) {
	reset := false

	exp_mutex.Lock()

	if node == nextExpiration() {
		reset = true
	}

	if node.Expires == nil {
		// This node doesn't have an expiration.  If it's in the heap,
		// it's probably because we just made the setting permanent.
		// Pull it out of the heap.
		if node.index != -1 {
			heap.Remove(&exp_heap, node.index)
			node.index = -1
		}
	} else {
		if node.index == -1 {
			heap.Push(&exp_heap, node)
		}
		heap.Fix(&exp_heap, node.index)
	}

	if node == nextExpiration() {
		reset = true
	}

	if reset {
		if next := nextExpiration(); next != nil {
			exp_timer.Reset(time.Until(*next.Expires))
		}
	}
	exp_mutex.Unlock()
}

/*
 * Remove a single property from the expiration heap
 */
func expirationRemove(node *pnode) {
	exp_mutex.Lock()
	if node.index != -1 {
		heap.Remove(&exp_heap, node.index)
		node.index = -1
	}
	exp_mutex.Unlock()
}

/*
 * Walk the list of expired properties and remove them from the tree
 */
func expirationPurge() {
	count := 0
	for len(expired) > 0 {
		exp_mutex.Lock()
		copy := expired
		expired = make([]string, 0)
		exp_mutex.Unlock()

		for _, prop := range copy {
			count++
			propertyDelete(prop)
		}
	}
	if count > 0 {
		prop_tree_store()
	}
}

func expirationInit() {
	exp_heap = make(pnode_queue, 0)
	heap.Init(&exp_heap)

	expired = make([]string, 0)
	exp_timer = time.NewTimer(time.Duration(time.Minute))
	go expiration_handler()
}

/*************************************************************************
 *
 * Broker notifications
 */
func prop_notify(prop, val string, action base_msg.EventConfig_Type) {
	entity := &base_msg.EventConfig{
		Timestamp: aputil.NowToProtobuf(),
		Sender:    proto.String(brokerd.Name),
		Type:      &action,
		Property:  proto.String(prop),
		NewValue:  proto.String(val),
	}

	err := brokerd.Publish(entity, base_def.TOPIC_CONFIG)
	if err != nil {
		log.Printf("Failed to propagate config update: %v", err)
	}
}

func updateNotify(prop, val string) {
	prop_notify(prop, val, base_msg.EventConfig_CHANGE)
}

func deleteNotify(prop string) {
	prop_notify(prop, "-", base_msg.EventConfig_DELETE)
}

func expirationNotify(prop, val string) {
	prop_notify(prop, val, base_msg.EventConfig_EXPIRE)
}

func entity_handler(event []byte) {
	updated := false
	entity := &base_msg.EventNetEntity{}
	proto.Unmarshal(event, entity)

	if entity.MacAddress == nil {
		log.Printf("Received a NET.ENTITY event with no MAC: %v\n",
			entity)
		return
	}
	hwaddr := network.Uint64ToHWAddr(*entity.MacAddress)
	path := "@/clients/" + hwaddr.String()
	node := propertyInsert(path)

	/*
	 * Determine which client properties are already known
	 */
	fields := make(map[string]*pnode)
	for _, c := range node.Children {
		fields[c.Name] = c
	}

	var n *pnode
	var ok bool
	if entity.Ipv4Address != nil {
		if n, ok = fields["ipv4_observed"]; !ok {
			n = propertyAdd(node, "ipv4_observed")
		}
		ipv4 := network.Uint32ToIPAddr(*entity.Ipv4Address).String()
		if n.Value != ipv4 {
			n.Value = ipv4
			updated = true
		}
	}

	if n, ok = fields["ring"]; !ok {
		n = propertyAdd(node, "ring")
		n.Value = base_def.RING_UNENROLLED
		updated = true
	}

	if updated {
		prop_tree_store()
	}
}

/*************************************************************************
 *
 * Generic and property-specific setter/getter routines
 */
func defaultSetter(node *pnode, val string, expires *time.Time) (bool, error) {
	updated := false

	if node.Value != val {
		node.Value = val
		updated = true
	}

	if node.Expires != nil || expires != nil {
		node.Expires = expires
		updated = true
	}
	return updated, nil
}

func defaultGetter(node *pnode) (string, error) {
	var rval string

	b, err := json.Marshal(node)
	if err == nil {
		rval = string(b)
	}

	return rval, err
}

func defaultExpire(node *pnode) {
	expirationNotify(node.path, node.Value)
	expired = append(expired, node.path)

	node.Value = ""
}

func uuidUpdate(node *pnode, uuid string, expires *time.Time) (bool, error) {
	const null_uuid = "00000000-0000-0000-0000-000000000000"

	if node.Value != null_uuid {
		return false, fmt.Errorf("Cannot change an appliance's UUID")
	}
	node.Value = uuid
	return true, nil
}

func ssidValidate(ssid string) error {
	if len(ssid) == 0 || len(ssid) > 32 {
		return fmt.Errorf("SSID must be between 1 and 32 characters")
	}

	for _, c := range ssid {
		// XXX: this is overly strict, but safe.  We'll need to support
		// a broader range eventually.
		if c > unicode.MaxASCII || !unicode.IsPrint(c) {
			return fmt.Errorf("Invalid characters in SSID name")
		}
	}

	return nil
}

func ssidUpdate(node *pnode, ssid string, expires *time.Time) (bool, error) {
	err := ssidValidate(ssid)
	if err == nil && node.Value != ssid {
		node.Value = ssid
		return true, nil
	}
	return false, err
}

//
// Check to see whether the given hostname is already inuse as either a device's
// dns_name or as the left hand side of a cname.  We can optionally indicate a
// device to ignore, allowing us to answer the question "is any other device
// using this hostname?"
//
func dnsNameInuse(ignore *pnode, hostname string) bool {
	lower := strings.ToLower(hostname)

	clients := propertySearch("@/clients")
	for _, device := range clients.Children {
		if device == ignore {
			continue
		}
		for _, prop := range device.Children {
			if prop.Name == "dns_name" {
				if strings.ToLower(prop.Value) == lower {
					return true
				}
				break
			}
		}
	}

	if cnames := propertySearch("@/dns/cnames"); cnames != nil {
		for _, record := range cnames.Children {
			if record == ignore {
				continue
			}
			if strings.ToLower(record.Name) == lower {
				return true
			}
		}
	}

	return false
}

// Validate and record the hostname that will be used to generate DNS A records
// for this device
func dnsSetter(node *pnode, hostname string, expires *time.Time) (bool, error) {
	if !network.ValidHostname(hostname) {
		return false, fmt.Errorf("invalid hostname: %s", hostname)
	}

	if dnsNameInuse(node.parent, hostname) {
		return false, fmt.Errorf("duplicate hostname")
	}

	return defaultSetter(node, hostname, expires)
}

// Validate and record both the hostname and the canonical name that will be
// used to generate DNS CNAME records
func cnameSetter(node *pnode, hostname string, expires *time.Time) (bool, error) {
	if !network.ValidHostname(node.Name) {
		return false, fmt.Errorf("invalid hostname: %s", node.Name)
	}

	if !network.ValidHostname(hostname) {
		return false, fmt.Errorf("invalid canonical name: %s", hostname)
	}

	if dnsNameInuse(node, node.Name) {
		return false, fmt.Errorf("duplicate hostname")
	}

	return defaultSetter(node, hostname, expires)
}

//
// When a client's ring assignment expires, it returns to the Unenrolled ring
//
func ringExpire(node *pnode) {
	node.Value = base_def.RING_UNENROLLED
	updateNotify(node.path, node.Value)
}

/*
 * To determine whether this new property has non-default operations, we walk
 * through the property_match_table, looking for any matching patterns
 */
func property_attach_ops(node *pnode, path string) {
	for _, r := range propertyMatchTable {
		if r.match.MatchString(path) {
			node.ops = r.ops
			return
		}
	}
	node.ops = &defaultPropOps
}

/*************************************************************************
 *
 * Functions to walk and maintain the property tree
 */

/*
 * Updated the modified timestamp for a node and its ancestors
 */
func markUpdated(node *pnode) {
	now := time.Now()

	for node != nil {
		// We want each node in the chain to have the same time, but it
		// can't be a pointer to the same time.
		copy := now
		node.Modified = &copy
		node = node.parent
	}
}

/*
 * Allocate a new property node and insert it into the property tree
 */
func propertyAdd(parent *pnode, property string) *pnode {
	path := parent.path + "/" + property

	n := pnode{Name: property,
		parent: parent,
		path:   path,
		index:  -1}

	parent.Children = append(parent.Children, &n)
	property_attach_ops(&n, path)
	return &n
}

func childSearch(node *pnode, name string) *pnode {
	for _, n := range node.Children {
		if name == n.Name {
			return n
		}

	}
	return nil
}

func propertyParse(prop string) []string {
	/*
	 * Only accept properties that start with exactly one '@', meaning they
	 * are local to this device
	 */
	if len(prop) < 2 || prop[0] != '@' || prop[1] != '/' {
		return nil
	}

	if prop == "@/" {
		return make([]string, 0)
	}

	return strings.Split(prop[2:], "/")
}

/*
 * Insert an empty property into the tree, returning the leaf node.  If the
 * property already exists, the tree is left unchanged.
 */
func propertyInsert(prop string) *pnode {
	components := propertyParse(prop)

	if components == nil || len(components) < 1 {
		return nil
	}

	node := &property_root
	path := "@"
	for _, name := range components {
		next := childSearch(node, name)
		if next == nil {
			next = propertyAdd(node, name)
		}
		path += "/" + name
		node = next
	}

	return node
}

/*
 * Walk the tree looking for the given property.
 */
func propertySearch(prop string) *pnode {
	components := propertyParse(prop)
	if components == nil {
		return nil
	}

	node := &property_root
	for _, name := range components {
		if node = childSearch(node, name); node == nil {
			break
		}
	}

	return node
}

func deleteChild(parent, child *pnode) {
	siblings := parent.Children
	for i, n := range siblings {
		if n == child {
			parent.Children =
				append(siblings[:i], siblings[i+1:]...)
			markUpdated(parent)
			break
		}
	}
	deleteSubtree(child)
}

func propertyDelete(property string) error {
	log.Printf("delete property: %s\n", property)
	node := propertySearch(property)
	if node == nil {
		return fmt.Errorf("deleting a nonexistent property: %s",
			property)
	}

	deleteChild(node.parent, node)
	return nil
}

func propertyUpdate(property, value string, expires *time.Time,
	insert bool) (bool, error) {
	var err error
	var updated, inserted bool

	log.Printf("set property %s -> %s\n", property, value)
	node := propertySearch(property)
	if node == nil && insert {
		node = propertyInsert(property)
		inserted = true
	}

	if node == nil {
		if insert {
			err = fmt.Errorf("Failed to insert a new property")
		} else {
			err = fmt.Errorf("Updating a nonexistent property: %s",
				property)
		}
	} else if len(node.Children) > 0 {
		err = fmt.Errorf("Can only modify leaf properties")
	} else {
		updated, err = node.ops.set(node, value, expires)
	}

	if err != nil {
		log.Println("property update failed: ", err)
		if inserted {
			deleteChild(node.parent, node)
		}
	} else {
		markUpdated(node)
		if node.Expires != nil {
			expirationUpdate(node)
		}
	}

	return updated, err
}

func propertyGet(property string) (string, error) {
	var err error
	var rval string

	if node := propertySearch(property); node != nil {
		b, err := json.Marshal(node)
		if err == nil {
			rval = string(b)
		}
	} else {
		err = fmt.Errorf("No such property")
	}

	if err != nil {
		log.Printf("property get for %s failed: %v\n", property, err)
	}

	return rval, err
}

/*************************************************************************
 *
 * Reading and writing the persistent property file
 */
func prop_tree_store() error {
	propfile := *propdir + property_filename
	backupfile := *propdir + backup_filename

	node := propertyInsert("@/apversion")
	node.Value = ApVersion

	s, err := json.MarshalIndent(property_root, "", "  ")
	if err != nil {
		log.Fatalf("Failed to construct properties JSON: %v\n", err)
	}

	if aputil.FileExists(propfile) {
		/*
		 * XXX: could store multiple generations of backup files,
		 * allowing for arbitrary rollback.  Could also take explicit
		 * 'checkpoint' snapshots.
		 */
		os.Rename(propfile, backupfile)
	}

	err = ioutil.WriteFile(propfile, s, 0644)
	if err != nil {
		log.Printf("Failed to write properties file: %v\n", err)
	}

	return err
}

func prop_tree_load(name string) error {
	var file []byte
	var err error

	file, err = ioutil.ReadFile(name)
	if err != nil {
		log.Printf("Failed to load properties file %s: %v\n", name, err)
		return err
	}

	err = json.Unmarshal(file, &property_root)
	if err != nil {
		log.Printf("Failed to import properties from %s: %v\n",
			name, err)
		return err
	}

	return nil
}

func addUpgradeHook(version int, hook func() error) {
	if version > curConfigVersion {
		msg := fmt.Sprintf("Upgrade hook %d > current max of %d\n",
			version, curConfigVersion)
		panic(msg)
	}

	if upgradeHooks == nil {
		upgradeHooks = make([]func() error, curConfigVersion+1)
	}
	upgradeHooks[version] = hook
}

func versionTree() error {
	upgraded := false
	version := 0

	node := propertyInsert("@/cfgversion")
	if node.Value != "" {
		version, _ = strconv.Atoi(node.Value)
	}
	if version < minConfigVersion {
		return fmt.Errorf("Obsolete properties file.")
	}
	if version > curConfigVersion {
		log.Fatalf("Properties file is newer than the software\n")
	}

	for version < curConfigVersion {
		log.Printf("Upgrading properties from version %d to %d\n",
			version, version+1)
		version++
		if upgradeHooks[version] != nil {
			if err := upgradeHooks[version](); err != nil {
				return fmt.Errorf("upgrade failed: %v", err)
			}
		}
		node.Value = strconv.Itoa(version)
		upgraded = true
	}

	if upgraded {
		if err := prop_tree_store(); err != nil {
			return fmt.Errorf("Failed to write properties: %v", err)
		}
	}
	return nil
}

/*
 * After loading the initial property values, we need to walk the tree to set
 * the parent pointers, attach any non-default operations, and possibly insert
 * into the expiration heap
 */
func patchTree(node *pnode, path string) {
	property_attach_ops(node, path)
	for _, n := range node.Children {
		n.parent = node
		patchTree(n, path+"/"+n.Name)
	}
	node.path = path
	node.index = -1
	if node.Expires != nil {
		expirationUpdate(node)
	}
}

func dumpTree(node *pnode, level int) {
	indent := ""
	for i := 0; i < level; i++ {
		indent += "  "
	}
	e := ""
	if node.Expires != nil {
		e = node.Expires.Format("2006-02-01T15:04:05")
	}
	fmt.Printf("%s%s: %s  %s\n", indent, node.Name, node.Value, e)
	for _, n := range node.Children {
		dumpTree(n, level+1)
	}
}

func deleteSubtree(node *pnode) {
	if node.Expires != nil {
		expirationRemove(node)
	}
	for _, n := range node.Children {
		deleteSubtree(n)
	}
}

func prop_tree_init() {
	var err error

	propfile := *propdir + property_filename
	backupfile := *propdir + backup_filename
	default_file := *propdir + default_filename

	if aputil.FileExists(propfile) {
		err = prop_tree_load(propfile)
	} else {
		err = fmt.Errorf("File missing")
	}

	if err != nil {
		log.Printf("Unable to load properties: %v", err)
		if aputil.FileExists(backupfile) {
			err = prop_tree_load(backupfile)
		} else {
			err = fmt.Errorf("File missing")
		}

		if err != nil {
			log.Printf("Unable to load backup properties: %v", err)
		} else {
			log.Printf("Loaded properties from backup file")
		}
	}

	if err != nil {
		log.Printf("No usable properties files.  Loading defaults.\n")
		err := prop_tree_load(default_file)
		if err != nil {
			log.Fatal("Unable to load default properties")
		}
		patchTree(&property_root, "@")
		appliance_uuid := uuid.NewV4().String()
		propertyUpdate("@/uuid", appliance_uuid, nil, true)

		// XXX: this needs to come from the cloud - not hardcoded
		appliance_siteid := "7410"
		propertyUpdate("@/siteid", appliance_siteid, nil, true)
	}

	if err == nil {
		patchTree(&property_root, "@")
		if err = versionTree(); err != nil {
			log.Printf("Failed version check: %v\n", err)
		}
	}

	dumpTree(&property_root, 0)
}

/*************************************************************************
 *
 * Handling incoming requests from other daemons
 */
func getPropHandler(q *base_msg.ConfigQuery) (string, error) {
	return propertyGet(*q.Property)
}

func setPropHandler(q *base_msg.ConfigQuery, add bool) error {
	expires := aputil.ProtobufToTime(q.Expires)
	updated, err := propertyUpdate(*q.Property, *q.Value, expires, add)
	if updated {
		prop_tree_store()
		updateNotify(*q.Property, *q.Value)
	}
	return err
}

func delPropHandler(q *base_msg.ConfigQuery) error {
	err := propertyDelete(*q.Property)
	if err == nil {
		prop_tree_store()
		deleteNotify(*q.Property)
	}
	return err
}

func processOneEvent(query *base_msg.ConfigQuery) *base_msg.ConfigResponse {
	var err error

	prop := *query.Property
	ops := &defaultSubtreeOps
	val := "-"
	rc := base_msg.ConfigResponse_OK

	for _, r := range subtreeMatchTable {
		if r.match.MatchString(prop) {
			ops = r.ops
			break
		}
	}

	switch *query.Operation {
	case base_msg.ConfigQuery_GET:
		if val, err = ops.get(query); err != nil {
			rc = base_msg.ConfigResponse_FAILED
		}
	case base_msg.ConfigQuery_CREATE:
		if err = ops.set(query, true); err != nil {
			rc = base_msg.ConfigResponse_FAILED
		}
	case base_msg.ConfigQuery_SET:
		if err = ops.set(query, false); err != nil {
			rc = base_msg.ConfigResponse_FAILED
		}
	case base_msg.ConfigQuery_DELETE:
		if err = ops.delete(query); err != nil {
			rc = base_msg.ConfigResponse_FAILED
		}
	default:
		rc = base_msg.ConfigResponse_UNSUPPORTED
		err = fmt.Errorf("unrecognized operation")
	}

	if rc != base_msg.ConfigResponse_OK {
		log.Printf("Config operation failed: %v\n", err)
		val = fmt.Sprintf("%v", err)
	}

	response := &base_msg.ConfigResponse{
		Timestamp: aputil.NowToProtobuf(),
		Sender:    proto.String(pname + "(" + strconv.Itoa(os.Getpid()) + ")"),
		Debug:     proto.String("-"),
		Response:  &rc,
		Property:  proto.String("-"),
		Value:     proto.String(val),
	}

	return response
}

func eventLoop(incoming *zmq.Socket) {
	errs := 0
	for {
		msg, err := incoming.RecvMessageBytes(0)
		if err != nil {
			log.Printf("Error receiving message: %v\n", err)
			errs++
			if errs > 10 {
				log.Fatalf("Too many errors - giving up\n")
			}
			continue
		}

		errs = 0
		expirationPurge()
		query := &base_msg.ConfigQuery{}
		proto.Unmarshal(msg[0], query)

		response := processOneEvent(query)
		data, err := proto.Marshal(response)
		if err != nil {
			log.Printf("Failed to marshall response to %v: %v\n",
				*query, err)
		} else {
			incoming.SendBytes(data, 0)
		}
	}
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	flag.Parse()
	if !strings.HasSuffix(*propdir, "/") {
		*propdir = *propdir + "/"
	}
	*propdir = aputil.ExpandDirPath(*propdir)

	mcpd, err := mcp.New(pname)
	if err != nil {
		log.Printf("Failed to connect to mcp\n")
	}

	if !aputil.FileExists(*propdir) {
		log.Fatalf("Properties directory %s doesn't exist", *propdir)
	}

	expirationInit()

	// Prometheus setup
	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(*addr, nil)

	brokerd = broker.New(pname)
	brokerd.Handle(base_def.TOPIC_ENTITY, entity_handler)
	defer brokerd.Fini()

	if err = DeviceDBInit(); err != nil {
		log.Printf("Failed to import devices database: %v\n", err)
		if mcpd != nil {
			mcpd.SetState(mcp.BROKEN)
			os.Exit(1)
		}
	}

	prop_tree_init()

	incoming, _ := zmq.NewSocket(zmq.REP)
	incoming.Bind(base_def.CONFIGD_ZMQ_REP_URL)

	if mcpd != nil {
		mcpd.SetState(mcp.ONLINE)
	}

	eventLoop(incoming)
}