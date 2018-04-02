/*
 * COPYRIGHT 2018 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 *
 */

/*
 * DHCPv4 daemon
 */

package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"bg/ap_common/apcfg"
	"bg/ap_common/aputil"
	"bg/ap_common/broker"
	"bg/ap_common/mcp"
	"bg/ap_common/network"
	"bg/base_def"
	"bg/base_msg"

	"github.com/golang/protobuf/proto"
	dhcp "github.com/krolaw/dhcp4"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/ipv4"
)

var (
	addr = flag.String("promhttp-address", base_def.DHCPD_PROMETHEUS_PORT,
		"Prometheus publication HTTP port.")

	handlers = make(map[string]*ringHandler)

	brokerd *broker.Broker

	config    *apcfg.APConfig
	clients   apcfg.ClientMap
	clientMtx sync.Mutex

	domainName string

	sharedRouter net.IP     // without vlans, all rings share a
	sharedSubnet *net.IPNet // subnet and a router node

	wanMacs = make(map[string]bool) // mac(s) connected to the wan

	defaultRing = base_def.RING_UNENROLLED

	lastRequestOn string // last DHCP request arrived on this interface
)

const pname = "ap.dhcp4d"

func getRing(hwaddr string) string {
	var ring string

	clientMtx.Lock()
	if client := clients[hwaddr]; client != nil {
		ring = client.Ring
	}
	clientMtx.Unlock()

	return ring
}

func setRing(hwaddr, ring string) bool {
	updated := false

	clientMtx.Lock()
	if client := clients[hwaddr]; client != nil {
		if client.Ring != ring {
			client.Ring = ring
			updated = true
		}
	} else {
		clients[hwaddr] = &apcfg.ClientInfo{Ring: ring}
		updated = true
	}
	clientMtx.Unlock()

	return updated
}

func updateRing(hwaddr, old, new string) bool {
	updated := false

	clientMtx.Lock()
	client := clients[hwaddr]
	if client == nil && old == "" {
		client = &apcfg.ClientInfo{Ring: ""}
		clients[hwaddr] = client
	}

	if client != nil && client.Ring == old {
		client.Ring = new
		updated = true
	}
	clientMtx.Unlock()

	return updated
}

/*******************************************************
 *
 * Communication with message broker
 */
func configExpired(path []string) {
	//
	// Watch for lease expirations in @/clients/<macaddr>/ipv4.  We actually
	// clean up expired leases as a side effect of handing out new ones, so
	// all we do here is log it.
	if len(path) == 3 && path[0] == "clients" && path[2] == "ipv4" {
		log.Printf("Lease for %s expired\n", path[2])
	}
}

func configIPv4Changed(path []string, val string, expires *time.Time) {
	//
	// In most cases, this change will just be a broadcast notification of a
	// lease change we just registered.  All other cases should be an IP
	// address being statically assigned.  Anything else is user error.
	//

	ipaddr := val
	hwaddr := path[1]
	ipv4 := net.ParseIP(val)
	if ipv4 == nil {
		log.Printf("Invalid IP address %s for %s\n", ipaddr, hwaddr)
		return
	}

	ring := getRing(hwaddr)
	if ring == "" {
		// While we could assign an address to a client we've never seen
		// before, it's up to somebody else to create the initial client
		// record for us to work with.
		log.Printf("Attempted to assign %s to non-existent client %s\n",
			ipaddr, hwaddr)
		return
	}

	h := handlers[ring]
	if !dhcp.IPInRange(h.rangeStart, h.rangeEnd, ipv4) {
		log.Printf("%s assigned %s, out of its ring range (%v - %v)\n",
			hwaddr, ipaddr, h.rangeStart, h.rangeEnd)
		return
	}

	var oldipv4 net.IP
	l := h.leaseSearch(hwaddr)
	if l != nil {
		if ipv4.Equal(l.ipaddr) {
			new := time.Now()
			lease := new

			if expires != nil {
				new = *expires
			}
			if l.expires != nil {
				lease = *l.expires
			}

			if new.Equal(lease) {
				// The expiration times are missing or the same,
				// so we're resetting the same static IP or
				// seeing our own notificiation
				return
			}
		}

		if expires != nil {
			// Either somebody else explicitly made a dynamic IP
			// assignment (which would be user error), or we've
			// already changed the assignment since this
			// notification was sent.  Either way, we're ignoring
			// it.
			log.Printf("Rejecting ipv4 assignment %s->%s\n",
				ipaddr, hwaddr)
			return
		}

		oldipv4 = l.ipaddr
	}

	if !ipv4.Equal(oldipv4) {
		if oldipv4 != nil {
			notifyRelease(oldipv4)
		}
		notifyProvisioned(ipv4)
	}
	l = h.getLease(ipv4)
	h.recordLease(l, hwaddr, "", ipv4, nil)
}

func configRingChanged(path []string, val string, expires *time.Time) {
	client := path[1]

	old := getRing(client)
	if (old != val) && updateRing(client, old, val) {
		if old == "" {
			log.Printf("config reports new client %s is %s\n",
				client, val)
		} else {
			log.Printf("config moves client %s from %s to  %s\n",
				client, old, val)
		}
	}
}

func propPath(hwaddr, prop string) string {
	return fmt.Sprintf("@/clients/%s/%s", hwaddr, prop)
}

/*
 * This is the first time we've seen this device.  Send an ENTITY message with
 * its hardware address, name, and any IP address it's requesting.
 */
func notifyNewEntity(p dhcp.Packet, options dhcp.Options, ring string) {
	ipaddr := p.CIAddr()
	hwaddr := network.HWAddrToUint64(p.CHAddr())
	hostname := string(options[dhcp.OptionHostName])

	log.Printf("New client %s (name: %q incoming IP address: %s)\n",
		p.CHAddr().String(), hostname, ipaddr.String())
	entity := &base_msg.EventNetEntity{
		Timestamp:   aputil.NowToProtobuf(),
		Sender:      proto.String(brokerd.Name),
		Debug:       proto.String("-"),
		Ring:        proto.String(ring),
		MacAddress:  proto.Uint64(hwaddr),
		Ipv4Address: proto.Uint32(network.IPAddrToUint32(ipaddr)),
		Hostname:    proto.String(hostname),
	}

	err := brokerd.Publish(entity, base_def.TOPIC_ENTITY)
	if err != nil {
		log.Printf("couldn't publish %s: %v\n", base_def.TOPIC_ENTITY, err)
	}
}

/*
 * A provisioned IP address has now been claimed by a client.
 */
func notifyClaimed(p dhcp.Packet, ipaddr net.IP, name string,
	dur time.Duration) {

	ttl := uint32(dur.Seconds())

	action := base_msg.EventNetResource_CLAIMED
	resource := &base_msg.EventNetResource{
		Timestamp:   aputil.NowToProtobuf(),
		Sender:      proto.String(brokerd.Name),
		Debug:       proto.String("-"),
		Action:      &action,
		Ipv4Address: proto.Uint32(network.IPAddrToUint32(ipaddr)),
		Hostname:    proto.String(name),
		Duration:    proto.Uint32(ttl),
	}

	err := brokerd.Publish(resource, base_def.TOPIC_RESOURCE)
	if err != nil {
		log.Printf("couldn't publish %s: %v\n", base_def.TOPIC_RESOURCE, err)
	}
}

/*
 * We've have provisionally assigned an IP address to a client.  Send a
 * net.resource message indicating that that address is no longer available.
 */
func notifyProvisioned(ipaddr net.IP) {
	action := base_msg.EventNetResource_PROVISIONED
	resource := &base_msg.EventNetResource{
		Timestamp:   aputil.NowToProtobuf(),
		Sender:      proto.String(brokerd.Name),
		Debug:       proto.String("-"),
		Action:      &action,
		Ipv4Address: proto.Uint32(network.IPAddrToUint32(ipaddr)),
	}

	err := brokerd.Publish(resource, base_def.TOPIC_RESOURCE)
	if err != nil {
		log.Printf("couldn't publish %s: %v\n", base_def.TOPIC_RESOURCE, err)
	}
}

/*
 * An IP address has been released.  It may have been released or declined by
 * the client, or the lease may have expired.
 */
func notifyRelease(ipaddr net.IP) {
	action := base_msg.EventNetResource_RELEASED
	resource := &base_msg.EventNetResource{
		Timestamp:   aputil.NowToProtobuf(),
		Sender:      proto.String(brokerd.Name),
		Debug:       proto.String("-"),
		Action:      &action,
		Ipv4Address: proto.Uint32(network.IPAddrToUint32(ipaddr)),
	}

	err := brokerd.Publish(resource, base_def.TOPIC_RESOURCE)
	if err != nil {
		log.Printf("couldn't publish %s: %v\n", base_def.TOPIC_RESOURCE, err)
	}
}

/*
 * Report on the DHCP options used by the client. Useed for DHCP fingerprinting.
 */
func notifyOptions(hwaddr net.HardwareAddr, options dhcp.Options, msgType dhcp.MessageType) {
	msg := &base_msg.DHCPOptions{
		Timestamp:     aputil.NowToProtobuf(),
		Sender:        proto.String(brokerd.Name),
		Debug:         proto.String("-"),
		MacAddress:    proto.Uint64(network.HWAddrToUint64(hwaddr)),
		MsgType:       proto.Uint32(uint32(msgType)),
		ParamReqList:  options[dhcp.OptionParameterRequestList],
		VendorClassId: options[dhcp.OptionVendorClassIdentifier],
	}

	err := brokerd.Publish(msg, base_def.TOPIC_OPTIONS)
	if err != nil {
		log.Printf("couldn't publish %s: %v\n", base_def.TOPIC_OPTIONS, err)
	}
}

/*******************************************************
 *
 * Implementing the DHCP protocol
 */

type lease struct {
	name     string     // Client's name from DHCP packet
	hwaddr   string     // Client's CHAddr
	ipaddr   net.IP     // Client's IP address
	expires  *time.Time // When the lease expires
	static   bool       // Statically assigned?
	assigned bool       // Lease assigned to a client?
}

type ringHandler struct {
	ring       string        // Client ring eligible for this server
	subnet     net.IPNet     // Subnet being managed
	serverIP   net.IP        // DHCP server's IP
	options    dhcp.Options  // Options to send to DHCP Clients
	rangeStart net.IP        // Start of IP range to distribute
	rangeEnd   net.IP        // End of IP range to distribute
	rangeSpan  int           // Number of IPs to distribute (starting from start)
	duration   time.Duration // Lease period
	leases     []lease       // Per-lease state
}

/*
 * Construct a DHCP NAK message
 */
func (h *ringHandler) nak(p dhcp.Packet) dhcp.Packet {
	return dhcp.ReplyPacket(p, dhcp.NAK, h.serverIP, nil, 0, nil)
}

/*
 * Handle DISCOVER messages
 */
func (h *ringHandler) discover(p dhcp.Packet, options dhcp.Options) dhcp.Packet {
	hwaddr := p.CHAddr().String()
	log.Printf("DISCOVER %s\n", hwaddr)

	notifyOptions(p.CHAddr(), options, dhcp.Discover)

	l := h.leaseAssign(hwaddr)
	if l == nil {
		log.Printf("Out of %s leases\n", h.ring)
		return h.nak(p)
	}
	log.Printf("  OFFER %s to %s\n", l.ipaddr, l.hwaddr)

	notifyProvisioned(l.ipaddr)
	return dhcp.ReplyPacket(p, dhcp.Offer, h.serverIP, l.ipaddr, h.duration,
		h.options.SelectOrderOrAll(options[dhcp.OptionParameterRequestList]))
}

/*
 * Handle REQUEST messages
 */
func (h *ringHandler) request(p dhcp.Packet, options dhcp.Options) dhcp.Packet {
	var reqIP net.IP

	hwaddr := p.CHAddr().String()
	log.Printf("REQUEST for %s\n", hwaddr)

	notifyOptions(p.CHAddr(), options, dhcp.Request)

	server, ok := options[dhcp.OptionServerIdentifier]
	if ok && !net.IP(server).Equal(h.serverIP) {
		return nil // Message not for this dhcp server
	}
	requestOption := net.IP(options[dhcp.OptionRequestedIPAddress])

	current := h.leaseSearch(hwaddr)

	/*
	 * If this client already has an IP address assigned (either statically,
	 * or a previously assigned dynamic address), that overrides any address
	 * it might ask for.
	 */
	action := ""
	if current != nil {
		reqIP = current.ipaddr
		if requestOption != nil {
			if reqIP.Equal(requestOption) {
				action = "renewing"
			} else {
				/*
				 * XXX: this is potentially worth of a
				 * NetException message
				 */
				action = "overriding client"
			}
		} else if current.static {
			action = "using static lease"
		} else {
			action = "found existing lease"
		}
	} else if requestOption != nil {
		reqIP = requestOption
		action = "granting request"
	} else {
		reqIP = net.IP(p.CIAddr())
		action = "CLAIMED"
	}
	log.Printf("   REQUEST %s %s\n", action, reqIP.String())

	if len(reqIP) != 4 || reqIP.Equal(net.IPv4zero) {
		log.Printf("Invalid reqIP %s from %s\n", reqIP.String(), hwaddr)
		return h.nak(p)
	}

	l := h.getLease(reqIP)
	if l == nil || !l.assigned || l.hwaddr != hwaddr {
		log.Printf("Invalid lease of %s for %s\n", reqIP.String(), hwaddr)
		return h.nak(p)
	}

	l.name = string(options[dhcp.OptionHostName])
	if l.static {
		l.expires = nil
	} else {
		expires := time.Now().Add(h.duration)
		l.expires = &expires
	}

	log.Printf("   REQUEST assigned %s to %s (%q) until %s\n",
		l.ipaddr, hwaddr, l.name, l.expires)

	ops := []apcfg.PropertyOp{
		{
			Op:      apcfg.PropCreate,
			Name:    propPath(hwaddr, "ipv4"),
			Value:   l.ipaddr.String(),
			Expires: l.expires,
		},
		{
			Op:    apcfg.PropCreate,
			Name:  propPath(hwaddr, "dhcp_name"),
			Value: l.name,
		},
	}
	config.Execute(ops)
	notifyClaimed(p, l.ipaddr, l.name, h.duration)

	if h.ring == base_def.RING_INTERNAL {
		// Clients asking for addresses on the internal network are
		// notified that they are expected to operate as satellite nodes
		o := []dhcp.Option{
			{
				Code:  1,
				Value: []byte(base_def.MODE_SATELLITE),
			},
		}

		vendorOpt, _ := aputil.DHCPEncodeOptions(o)
		h.options[dhcp.OptionVendorSpecificInformation] = vendorOpt
	}

	// Note: even for static IP assignments, we tell the requesting client
	// that it needs to renew at the regular period for the ring.  This lets
	// us revoke a static assignment at some point in the future.
	return dhcp.ReplyPacket(p, dhcp.ACK, h.serverIP, l.ipaddr, h.duration,
		h.options.SelectOrderOrAll(options[dhcp.OptionParameterRequestList]))
}

/*
 * If this lease is unassigned, or assigned to somebody else, return 'false'.
 * Otherwise, release it, update the configuration, send a notification, and
 * return 'true'
 */
func (h *ringHandler) releaseLease(l *lease, hwaddr string) bool {
	if l == nil || !l.assigned || l.hwaddr != hwaddr {
		return false
	}
	if l.expires == nil {
		return false
	}

	l.assigned = false
	notifyRelease(l.ipaddr)
	config.DeleteProp(propPath(l.hwaddr, "ipv4"))
	return true
}

/*
 * Handle RELEASE message for a specific IP address
 */
func (h *ringHandler) release(p dhcp.Packet) {
	hwaddr := p.CHAddr().String()
	ipaddr := p.CIAddr()

	l := h.getLease(ipaddr)
	if l == nil {
		log.Printf("Client %s RELEASE unsupported address: %s\n",
			hwaddr, ipaddr.String())
		return
	}
	if h.releaseLease(l, hwaddr) {
		log.Printf("RELEASE %s\n", hwaddr)
	}
}

/*
 * Handle DECLINE message.  We only get the client's MAC address, so we have to
 * scan all possible leases to find the one being declined
 */
func (h *ringHandler) decline(p dhcp.Packet) {
	hwaddr := p.CHAddr().String()

	l := h.leaseSearch(hwaddr)
	if h.releaseLease(l, hwaddr) {
		log.Printf("DECLINE for %s\n", hwaddr)
	}
}

//
// Based on the client's MAC address and/or the network the request arrived on,
// identify its ring and return the appropriate DHCP handler.  The use of
// lastRequestOn relies on the knowledge that the DHCP library (krolaw/dhcp4)
// will call the handler immediately after the call to ReadFrom(), where
// lastRequestOn gets set.
//
func selectRingHandler(p dhcp.Packet, options dhcp.Options) *ringHandler {
	hwaddr := p.CHAddr().String()
	oldRing := getRing(hwaddr)
	if oldRing == "" {
		// If this client isn't already assigned to a ring, we
		// put it in the default ring
		updateRing(hwaddr, oldRing, defaultRing)
	}

	ring := getRing(hwaddr)
	if oldRing == "" {
		log.Printf("New client %s on ring %s\n", hwaddr, ring)
		notifyNewEntity(p, options, ring)
	}

	ringHandler, ok := handlers[ring]
	if !ok {
		log.Printf("Client %s identified on unknown ring '%s'\n",
			hwaddr, ring)
	} else if ring != oldRing {
		config.CreateProp(propPath(hwaddr, "ring"), ring, nil)
	}
	return ringHandler
}

func (h *ringHandler) ServeDHCP(p dhcp.Packet, msgType dhcp.MessageType,
	options dhcp.Options) (d dhcp.Packet) {

	ringHandler := selectRingHandler(p, options)
	if ringHandler == nil {
		return nil
	}

	switch msgType {

	case dhcp.Discover:
		return ringHandler.discover(p, options)

	case dhcp.Request:
		return ringHandler.request(p, options)

	case dhcp.Release:
		ringHandler.release(p)

	case dhcp.Decline:
		ringHandler.decline(p)
	}
	return nil
}

func (h *ringHandler) recordLease(l *lease, hwaddr, name string, ipv4 net.IP,
	etime *time.Time) {
	l.name = name
	l.hwaddr = hwaddr
	l.ipaddr = ipv4.To4()
	l.expires = etime
	l.static = (etime == nil)
	l.assigned = true
}

/*
 * If this nic already has a live lease, return that.  Otherwise, assign an
 * available lease at random.  A 'nil' response indicates that all leases are
 * currently assigned.
 */
func (h *ringHandler) leaseAssign(hwaddr string) *lease {
	var rval *lease

	now := time.Now()
	target := rand.Intn(h.rangeSpan)
	assigned := -1

	for i, l := range h.leases {
		if l.assigned && l.expires != nil && l.expires.Before(now) {
			/*
			 * We don't actively handle lease expiration messages;
			 * they get cleaned up lazily here.
			 */
			l.assigned = false
		}

		if l.assigned && l.hwaddr == hwaddr {
			rval = &l
			break
		}

		if !l.assigned && assigned < target {
			assigned = i
		}
	}

	if rval == nil && assigned >= 0 {
		ipv4 := dhcp.IPAdd(h.rangeStart, assigned)
		rval = &h.leases[assigned]
		expires := time.Now().Add(h.duration)
		h.recordLease(rval, hwaddr, "", ipv4, &expires)
	}
	return rval
}

/*
 * Scan all leases in all ranges, looking for an IP address assigned to this
 * NIC.
 */
func (h *ringHandler) leaseSearch(hwaddr string) *lease {
	for i := 0; i < h.rangeSpan; i++ {
		l := &h.leases[i]
		if l.assigned && l.hwaddr == hwaddr {
			return l
		}
	}
	return nil
}

func (h *ringHandler) getLease(ip net.IP) *lease {
	if !dhcp.IPInRange(h.rangeStart, h.rangeEnd, ip) {
		return nil
	}

	slot := dhcp.IPRange(h.rangeStart, ip) - 1
	return &h.leases[slot]
}

func ipRange(ring, subnet string) (start net.IP, ipnet *net.IPNet, span int) {
	var err error

	start, ipnet, err = net.ParseCIDR(subnet)
	if err != nil {
		log.Fatalf("Invalid subnet %v for ring %s: %v\n",
			subnet, ring, err)
	}

	ones, bits := ipnet.Mask.Size()
	span = (1<<uint32(bits-ones) - 2)
	return
}

//
// Instantiate a new DHCP handler for the given ring
//
func newHandler(name string, rings apcfg.RingMap) *ringHandler {
	ring := rings[name]
	duration := time.Duration(ring.LeaseDuration) * time.Minute
	start, subnet, span := ipRange(name, ring.Subnet)

	myip := dhcp.IPAdd(start, 1)
	if name == base_def.RING_INTERNAL {
		// Shrink the range to exclude the router
		span--
		start = dhcp.IPAdd(start, 1)
	} else {
		// Exclude the lower addresses that are reserved for the routers
		// on each of the mesh APs
		iname := base_def.RING_INTERNAL
		isub := rings[base_def.RING_INTERNAL].Subnet
		_, _, reserved := ipRange(iname, isub)
		start = dhcp.IPAdd(start, reserved)
		span -= reserved
	}
	// Exclude the broadcast address
	span--

	h := ringHandler{
		ring:       name,
		subnet:     *subnet,
		serverIP:   myip,
		rangeStart: start,
		rangeEnd:   dhcp.IPAdd(start, span),
		rangeSpan:  span,
		duration:   duration,
		options: dhcp.Options{
			dhcp.OptionSubnetMask:       subnet.Mask,
			dhcp.OptionRouter:           myip,
			dhcp.OptionDomainNameServer: myip,
		},
		leases: make([]lease, span, span),
	}
	h.options[dhcp.OptionDomainName] = []byte(domainName)
	h.options[dhcp.OptionVendorClassIdentifier] = []byte("Brightgate, Inc.")

	return &h
}

func (h *ringHandler) recoverLeases() {
	// Preemptively pull the network and DHCP server from the pool
	h.leases[0].assigned = true
	h.leases[1].assigned = true

	for macaddr, client := range clients {
		if client.IPv4 == nil {
			continue
		}

		if l := h.getLease(client.IPv4); l != nil {
			h.recordLease(l, macaddr, client.DHCPName, client.IPv4,
				client.Expires)
		}
	}
}

func getMac(name string) string {
	mac := ""

	if name != "" {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			log.Printf("Can't get interface '%s': %v\n", name, err)
		} else {
			mac = iface.HardwareAddr.String()
		}
	}
	return mac
}

func initHandlers() error {
	nodes, err := config.GetProps("@/nodes")
	if err != nil {
		return fmt.Errorf("unable to get NIC ring assignments: %v", err)
	}
	for _, node := range nodes.Children {
		for name, nic := range node.Children {
			if nic.Value == base_def.RING_WAN {
				wanMacs[name] = true
			}
		}
	}

	// Iterate over the known rings.  For each one, create a DHCP handler to
	// manage its subnet.
	rings := config.GetRings()
	for name := range rings {
		h := newHandler(name, rings)
		h.recoverLeases()
		handlers[h.ring] = h
	}

	return nil
}

type multiConn struct {
	conn *ipv4.PacketConn
	cm   *ipv4.ControlMessage
}

func (s *multiConn) ReadFrom(b []byte) (n int, addr net.Addr, err error) {
	var iface *net.Interface
	var requestMac string

	n, s.cm, addr, err = s.conn.ReadFrom(b)
	if err == nil && s.cm != nil {
		if iface, err = net.InterfaceByIndex(s.cm.IfIndex); err == nil {
			requestMac = iface.HardwareAddr.String()
		}
	}
	if requestMac == "" {
		n = 0
	} else {
		lastRequestOn = requestMac
		if wanMacs[requestMac] {
			// If the request arrives on the WAN port, drop it.
			n = 0
		} else {
			log.Printf("Request arrived on %s\n", requestMac)
		}
	}
	return
}

func (s *multiConn) WriteTo(b []byte, addr net.Addr) (n int, err error) {
	s.cm.Src = nil
	return s.conn.WriteTo(b, s.cm, addr)
}

func listenAndServeIf(handler dhcp.Handler) error {
	l, err := net.ListenPacket("udp4", ":67")
	if err != nil {
		return err
	}
	defer l.Close()

	p := ipv4.NewPacketConn(l)
	err = p.SetControlMessage(ipv4.FlagInterface, true)
	if err != nil {
		return err
	}
	serveConn := multiConn{
		conn: p,
	}

	return dhcp.Serve(&serveConn, handler)
}

func mainLoop() {
	/*
	 * Even with multiple VLANs and/or address ranges, we still only have a
	 * single UDP broadcast address.  We create a metahandler that receives
	 * all of the requests at that address, and routes them to the correct
	 * per-ring handler.
	 */
	h := ringHandler{
		ring: "_metahandler",
	}
	for {
		err := listenAndServeIf(&h)
		if err != nil {
			log.Fatalf("DHCP server failed: %v\n", err)
		} else {
			log.Printf("%s DHCP server exited\n", err)
		}
	}
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	mcpd, err := mcp.New(pname)
	if err != nil {
		log.Printf("Failed to connect to mcp\n")
	}

	flag.Parse()

	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(*addr, nil)

	brokerd = broker.New(pname)
	defer brokerd.Fini()

	// Interface to config
	config, err = apcfg.NewConfig(brokerd, pname)
	if err != nil {
		log.Fatalf("cannot connect to configd: %v\n", err)
	}
	config.HandleExpire(`^@/clients/.*/ipv4$`, configExpired)
	config.HandleChange(`^@/clients/.*/ipv4$`, configIPv4Changed)
	config.HandleChange(`^@/clients/.*/ring$`, configRingChanged)

	clients = config.GetClients()

	domainName, err = config.GetDomain()
	if err != nil {
		log.Fatalf("failed to fetch gateway domain: %v\n", err)
	}

	if ring, err := config.GetProp("@/network/default_ring"); err == nil {
		if _, ok := apcfg.ValidRings[ring]; !ok {
			log.Printf("Unknown default_ring: %s\n", ring)
		} else {
			defaultRing = ring
		}
	}

	err = initHandlers()

	if err != nil {
		if mcpd != nil {
			mcpd.SetState(mcp.BROKEN)
		}
		log.Fatalf("DHCP server failed to start: %v\n", err)
	}

	if mcpd != nil {
		mcpd.SetState(mcp.ONLINE)
	}
	log.Printf("DHCP server online\n")
	mainLoop()
	log.Printf("shutting down\n")

	os.Exit(0)
}
