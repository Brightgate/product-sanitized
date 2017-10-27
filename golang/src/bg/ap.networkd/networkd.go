/*
 * COPYRIGHT 2017 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 *
 */

/*
 * hostapd instance monitor
 *
 * Responsibilities:
 * - to run one instance of hostapd
 * - to create a configuration file for that hostapd instance that reflects the
 *   desired configuration state of the appliance
 * - to restart or signal that hostapd instance when a relevant configuration
 *   event is received
 * - to emit availability events when the hostapd instance fails or is
 *   launched
 *
 * Questions:
 * - does a monitor offer statistics to Prometheus?
 * - can we update ourselves if the template file is updated (by a
 *   software update)?
 */

package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"

	"bg/ap_common/apcfg"
	"bg/ap_common/aputil"
	"bg/ap_common/broker"
	"bg/ap_common/mcp"
	"bg/ap_common/network"
	"bg/base_def"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	addr = flag.String("listen-address", base_def.HOSTAPDM_PROMETHEUS_PORT,
		"address to listen on for HTTP requests")
	platform = flag.String("platform", "rpi3",
		"hardware platform name")
	templateDir = flag.String("template_dir", "golang/src/ap.networkd",
		"location of hostapd templates")
	rulesDir = flag.String("rules_dir", "./", "Location of the filter rules")

	aps         = make(apMap)
	physDevices = make(physDevMap)

	config  *apcfg.APConfig
	clients apcfg.ClientMap // macaddr -> ClientInfo
	rings   apcfg.RingMap   // ring -> config

	setupNic string
	wanNic   string
	wifiNic  string

	hostapdLog   *log.Logger
	childProcess *os.Process // track the hostapd proc
	mcpd         *mcp.MCP

	running      bool
	setupNetwork bool
)

type apMap map[string]*APConfig
type physDevMap map[string]*physDevice

const (
	// Allow up to 4 failures in a 1 minute period before giving up
	failures_allowed = 4
	period           = time.Duration(time.Minute)

	confdir        = "/tmp"
	hostapdPath    = "/usr/sbin/hostapd"
	hostapdOptions = "-dKt"
	brctlCmd       = "/sbin/brctl"
	sysctlCmd      = "/sbin/sysctl"
	ipCmd          = "/sbin/ip"
	pname          = "ap.networkd"
	setupPortal    = "_0"
)

type physDevice struct {
	name         string
	hwaddr       string
	wireless     bool
	supportVLANs bool
	multipleAPs  bool
}

type APConfig struct {
	Interface string // Linux device name
	Hwaddr    string // Mac address to use
	Status    error  // collect hostapd failures

	SSID          string
	HardwareModes string
	Channel       int
	Passphrase    string

	ConfDir      string // Location of hostapd.conf, etc.
	ConfFile     string // Name of this NIC's hostapd.conf
	VLANComment  string // Used to disable vlan params in .conf template
	SetupSSID    string // SSID to broadcast for setup network
	SetupComment string // Used to disable setup net in .conf template
}

//////////////////////////////////////////////////////////////////////////
//
// Interaction with the rest of the ap daemons
//

func apReset(conf *APConfig) {
	generateHostAPDConf(conf)
	if childProcess != nil {
		//
		// A SIGHUP will cause hostapd to reload its configuration.
		// However, it seems that we really need to kill and restart the
		// process for the changes to be propagated down to the wifi
		// hardware
		//
		childProcess.Signal(syscall.SIGINT)
	}
}

func configRingChanged(path []string, val string) {
	hwaddr := path[1]
	newRing := val
	c, ok := clients[hwaddr]
	if !ok {
		c := apcfg.ClientInfo{Ring: newRing}
		log.Printf("New client %s in %s\n", hwaddr, newRing)
		clients[hwaddr] = &c
	} else if c.Ring != newRing {
		log.Printf("Moving %s from %s to %s\n", hwaddr, c.Ring, newRing)
		c.Ring = newRing
	} else {
		// False alarm.
		return
	}

	conf := aps[wifiNic]
	apReset(conf)
}

func configNetworkChanged(path []string, val string) {
	conf := aps[wifiNic]

	// Watch for changes to the network conf
	switch path[1] {
	case "ssid":
		conf.SSID = val

	case "passphrase":
		conf.Passphrase = val

	case "setupssid":
		conf.SetupSSID = val

	default:
		return
	}
	apReset(conf)
}

//
// Get network settings from configd and use them to initialize the AP
//
func getAPConfig(d *physDevice, props *apcfg.PropertyNode) error {
	var ssid, passphrase, setupSSID string
	var vlanComment, setupComment string
	var node *apcfg.PropertyNode

	if node = props.GetChild("ssid"); node == nil {
		return fmt.Errorf("no SSID configured")
	}
	ssid = node.GetValue()

	if node = props.GetChild("passphrase"); node == nil {
		return fmt.Errorf("no passphrase configured")
	}
	passphrase = node.GetValue()

	if node = props.GetChild("setupssid"); node != nil {
		setupSSID = node.GetValue()
	}

	if d.multipleAPs && len(setupSSID) > 0 {
		// If we create a second SSID for new clients to setup to,
		// its mac address will be derived from the nic's mac address by
		// adding 1 to the final octet.  To accomodate that, hostapd
		// wants the final nybble of the final octet to be 0.
		octets := strings.Split(d.hwaddr, ":")
		if len(octets) != 6 {
			return fmt.Errorf("%s has an invalid mac address: %s",
				d.name, d.hwaddr)
		}
		b, _ := strconv.ParseUint(octets[5], 16, 32)
		if b&0xff != 0 {
			b &= 0xf0
			octets[5] = fmt.Sprintf("%02x", b)

			// Since we changed the mac address, we need to set the
			// 'locally administered' bit in the first octet
			b, _ = strconv.ParseUint(octets[0], 16, 32)
			b |= 0x02 // Set the "locally administered" bit
			octets[0] = fmt.Sprintf("%02x", b)
			o := d.hwaddr
			d.hwaddr = strings.Join(octets, ":")
			log.Printf("Changed mac from %s to %s\n", o, d.hwaddr)
		}
		setupNetwork = true
	} else {
		setupComment = "#"
		setupNetwork = false
	}

	data := APConfig{
		Interface:     d.name,
		Hwaddr:        d.hwaddr,
		SSID:          ssid,
		HardwareModes: "g",
		Channel:       6,
		Passphrase:    passphrase,
		ConfFile:      "hostapd.conf." + d.name,
		ConfDir:       confdir,
		VLANComment:   vlanComment,
		SetupComment:  setupComment,
		SetupSSID:     setupSSID,
	}
	aps[d.name] = &data
	return nil
}

//////////////////////////////////////////////////////////////////////////
//
// hostapd configuration and monitoring
//

//
// Generate the 3 configuration files needed for hostapd.
//
func generateHostAPDConf(conf *APConfig) string {
	var err error
	tfile := *templateDir + "/hostapd.conf.got"

	// Create hostapd.conf, using the APConfig contents to fill out the .got
	// template
	t, err := template.ParseFiles(tfile)
	if err != nil {
		log.Fatal(err)
		os.Exit(2)
	}

	fn := conf.ConfDir + "/" + conf.ConfFile
	cf, _ := os.Create(fn)
	defer cf.Close()

	err = t.Execute(cf, conf)
	if err != nil {
		log.Fatal(err)
		os.Exit(1)
	}

	// Create the 'accept_macs' file, which tells hostapd how to map clients
	// to VLANs.
	mfn := conf.ConfDir + "/" + "hostapd.macs"
	mf, err := os.Create(mfn)
	if err != nil {
		log.Fatalf("Unable to create %s: %v\n", mfn, err)
	}
	defer mf.Close()

	// One client per line, containing "<mac addr> <vlan_id>"
	for client, info := range clients {
		vlan := 0
		if ring, ok := rings[info.Ring]; ok {
			vlan = ring.Vlan
		}
		if vlan > 0 {
			fmt.Fprintf(mf, "%s %d\n", client, vlan)
		}
	}

	// Create the 'vlan' file, which tells hostapd which vlans to create
	vfn := conf.ConfDir + "/" + "hostapd.vlan"
	vf, err := os.Create(vfn)
	if err != nil {
		log.Fatalf("Unable to create %s: %v\n", vfn, err)
	}
	defer vf.Close()

	for _, ring := range rings {
		if ring.Vlan > 0 {
			fmt.Fprintf(vf, "%d\tvlan.%d\n", ring.Vlan, ring.Vlan)
		}
	}

	return fn
}

//
// When we get a signal, set the 'running' flag to false and signal any hostapd
// process we're monitoring.  We want to be sure the wireless interface has been
// released before we give mcp a chance to restart the whole stack.
//
func signalHandler() {
	attempts := 0
	sig := make(chan os.Signal)
	for {
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig

		running = false
		if childProcess != nil {
			if attempts < 5 {
				childProcess.Signal(syscall.SIGINT)
			} else {
				childProcess.Signal(syscall.SIGKILL)
			}
			attempts++
		}
	}
}

func resetInterfaces() {
	for ring := range rings {
		if ring != "setup" || setupNetwork {
			prepareRingBridge(ring)
		}
	}

	// Any wired NICs that aren't connecting us to the WAN are put on the
	// standard ring's bridge.
	// XXX: this should be a per-interface configuration setting.
	bridge := rings[base_def.RING_STANDARD].Bridge
	for _, dev := range physDevices {
		name := dev.name
		if !dev.wireless && name != wanNic {
			cmd := exec.Command(brctlCmd, "addif", bridge, name)
			if err := cmd.Run(); err != nil {
				log.Printf("Failed to add %s to %s\n",
					name, bridge)
			}
		}
	}
}

//
// Launch, monitor, and maintain the hostapd process for a single interface
//
func runOne(conf *APConfig, done chan *APConfig) {
	fn := generateHostAPDConf(conf)

	start_times := make([]time.Time, failures_allowed)
	for running {
		deleteBridges()

		child := aputil.NewChild(hostapdPath, fn)
		child.LogOutput("hostapd: ", log.Ldate|log.Ltime)

		start_time := time.Now()
		start_times = append(start_times[1:failures_allowed], start_time)

		log.Printf("Starting hostapd for %s\n", conf.Interface)

		if err := child.Start(); err != nil {
			conf.Status = fmt.Errorf("Failed to launch: %v", err)
			break
		}

		childProcess = child.Process
		resetInterfaces()
		mcpd.SetState(mcp.ONLINE)

		child.Wait()

		log.Printf("hostapd for %s exited after %s\n",
			conf.Interface, time.Since(start_time))
		if time.Since(start_times[0]) < period {
			conf.Status = fmt.Errorf("Dying too quickly")
			break
		}

		// Give everything a chance to settle before we attempt to
		// restart the daemon and reconfigure the wifi hardware
		time.Sleep(time.Second)
	}
	done <- conf
}

//
// Kick off the monitor routines for all of our NICs, and then wait until
// they've all exited.  (Since we only support a single AP right now, this is
// overkill, but harmless.)
//
func runAll() int {
	done := make(chan *APConfig)
	running := 0
	errors := 0

	for _, c := range aps {
		if c.Interface == wifiNic {
			running++
			go runOne(c, done)
		}
	}

	for running > 0 {
		c := <-done
		if c.Status != nil {
			log.Printf("%s hostapd failed: %v\n", c.Interface,
				c.Status)
			errors++
		} else {
			log.Printf("%s hostapd exited\n", c.Interface)
		}
		running--
	}
	deleteBridges()

	return errors
}

//////////////////////////////////////////////////////////////////////////
//
// Low-level network manipulation.
//

// Delete the bridges associated with each ring.  This gets us back to a known
// ground state, simplifying the task of rebuilding everything when hostapd
// starts back up.
func deleteBridges() {
	for _, conf := range rings {
		bridge := conf.Bridge
		exec.Command(ipCmd, "link", "set", "down", bridge).Run()
		exec.Command(brctlCmd, "delbr", bridge).Run()
	}
}

// Create a ring's network bridge.  If a nic is provided, attach it to the
// bridge.
func createBridge(ring *apcfg.RingConfig, nic string) error {
	bridge := ring.Bridge

	cmd := exec.Command(brctlCmd, "addbr", bridge)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("addbr %s failed: %v", bridge, err)
	}

	cmd = exec.Command(ipCmd, "link", "set", "up", bridge)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bridge %s failed to come up: %v", bridge,
			err)
	}

	if nic != "" {
		cmd = exec.Command(brctlCmd, "addif", bridge, nic)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("addif %s %s failed: %v", bridge,
				nic, err)
		}
	}

	return nil
}

//
// Prepare a ring's bridge: clean up any old state, assign a new address, set up
// routes, etc.
//
func prepareRingBridge(ringName string) {
	ring := rings[ringName]
	bridge := ring.Bridge

	log.Printf("Preparing %s %s\n", bridge, ring.Subnet)

	if ringName == base_def.RING_UNENROLLED {
		// Unenrolled / unknown devices end up on the 'default' wireless
		// interface, which is not part of a vlan and does not have a
		// bridge created by hostapd.  To prevent the rest of the system
		// from needing to understand that implementation detail, we
		// create a bridge for vlan '0', which will have just that one
		// interface attached to it.
		if err := createBridge(ring, wifiNic); err != nil {
			log.Printf("failed to create unenrolled bridge: %v\n",
				err)
			return
		}
	}

	err := network.WaitForDevice(bridge, 5*time.Second)
	if err != nil {
		log.Printf("%s failed to come online: %v\n", bridge, err)
		return
	}

	// ip addr flush dev wlan0
	cmd := exec.Command(ipCmd, "addr", "flush", "dev", bridge)
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to remove existing IP address: %v\n", err)
	}

	// ip route del 192.168.136.0/24
	cmd = exec.Command(ipCmd, "route", "del", ring.Subnet)
	cmd.Run()

	// ip addr add 192.168.136.1 dev wlan0
	router := network.SubnetRouter(ring.Subnet)
	cmd = exec.Command(ipCmd, "addr", "add", router, "dev", bridge)
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to set the router address: %v\n", err)
	}

	// ip link set up wlan0
	cmd = exec.Command(ipCmd, "link", "set", "up", bridge)
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to enable bridge: %v\n", err)
	}
	// ip route add 192.168.136.0/24 dev wlan0
	cmd = exec.Command(ipCmd, "route", "add", ring.Subnet, "dev", bridge)
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to add %s as the new route: %v\n",
			ring.Subnet, err)
	}
}

//
// Identify and prepare the WAN port.
//
func prepareWan() {
	var err error

	// Enable packet forwarding
	cmd := exec.Command(sysctlCmd, "-w", "net.ipv4.ip_forward=1")
	if err = cmd.Run(); err != nil {
		log.Fatalf("Failed to enable packet forwarding: %v\n", err)
	}

	//
	// Identify the on-board ethernet port, which will connect us to the
	// WAN.  All other wired ports will be connected to the client bridge.
	//
	for name, dev := range physDevices {
		if dev.wireless {
			continue
		}

		// On Raspberry Pi 3, use the OUI to identify the
		// on-board port.
		if *platform == "rpi3" {
			if !strings.HasPrefix(dev.hwaddr, "b8:27:eb:") {
				continue
			}
		} else if !strings.HasPrefix(name, "eth") &&
			!strings.HasPrefix(name, "enx") {
			continue
		}

		log.Printf("Using %s for WAN\n", name)
		wanNic = dev.name
		return
	}

	log.Printf("No WAN connection available\n")
}

//
// Choose a wifi NIC to host our wireless clients, and build a list of the
// wireless interfaces we'll be supporting
//
func prepareWireless(props *apcfg.PropertyNode) error {
	var wifi *physDevice

	for _, dev := range physDevices {
		if dev.wireless {
			if err := getAPConfig(dev, props); err != nil {
				return err
			}

			if wifi == nil || dev.supportVLANs {
				wifi = dev
			}
		}
	}
	if wifi == nil {
		return fmt.Errorf("couldn't find a wifi device to use")
	}

	if !wifi.supportVLANs {
		return fmt.Errorf("no VLAN-enabled wifi device found")
	}

	wifiNic = wifi.name
	log.Printf("Hosting wireless network on %s\n", wifiNic)
	if setupNetwork {
		setupNic = wifiNic + setupPortal
		log.Printf("Hosting setup network on %s\n", setupNic)
	}
	return nil
}

func getEthernet(i net.Interface) *physDevice {
	d := physDevice{
		name:         i.Name,
		hwaddr:       i.HardwareAddr.String(),
		wireless:     false,
		supportVLANs: false,
	}
	return &d
}

//
// Given the name of a wireless NIC, construct a device structure for it
func getWireless(i net.Interface) *physDevice {
	if strings.HasSuffix(i.Name, setupPortal) {
		return nil
	}

	d := physDevice{
		name:     i.Name,
		hwaddr:   i.HardwareAddr.String(),
		wireless: true,
	}

	data, err := ioutil.ReadFile("/sys/class/net/" + i.Name +
		"/phy80211/name")
	if err != nil {
		log.Printf("Couldn't get phy for %s: %v\n", i.Name, err)
		return nil
	}
	phy := strings.TrimSpace(string(data))

	//
	// The following is a hack.  This should (and will) be accomplished by
	// asking the nl80211 layer through the netlink interface.
	//
	out, err := exec.Command("/sbin/iw", "phy", phy, "info").Output()
	if err != nil {
		log.Printf("Failed to get %s capabilities: %v\n", i.Name, err)
		return nil
	}
	capabilities := string(out)

	//
	// Look for "AP/VLAN" as a supported "software interface mode"
	//
	vlanRE := regexp.MustCompile(`AP/VLAN`)
	vlanModes := vlanRE.FindAllStringSubmatch(capabilities, -1)
	d.supportVLANs = (len(vlanModes) > 0)

	//
	// Examine the "valid interface combinations" to see if any include more
	// than one AP.  This one does:
	//    #{ AP, mesh point } <= 8,
	// This one doesn't:
	//    #{ managed } <= 1, #{ AP } <= 1, #{ P2P-client } <= 1,
	//
	comboRE := regexp.MustCompile(`#{ [\w\-, ]+ } <= [0-9]+`)
	combos := comboRE.FindAllStringSubmatch(capabilities, -1)

	for _, line := range combos {
		for _, combo := range line {
			if strings.Contains(combo, "AP") {
				s := strings.Split(combo, " ")
				if len(s) > 0 {
					cnt, _ := strconv.Atoi(s[len(s)-1])
					if cnt > 1 {
						d.multipleAPs = true
					}
				}
			}
		}
	}

	return &d
}

//
// Inventory the physical network devices in the system
//
func getDevices() {
	all, err := net.Interfaces()
	if err != nil {
		log.Fatalf("Unable to inventory network devices: %v\n", err)
	}

	for _, i := range all {
		var d *physDevice
		if strings.HasPrefix(i.Name, "eth") ||
			strings.HasPrefix(i.Name, "enx") {
			d = getEthernet(i)
		} else if strings.HasPrefix(i.Name, "wlan") ||
			strings.HasPrefix(i.Name, "wlx") {
			d = getWireless(i)
		}

		if d != nil {
			physDevices[i.Name] = d
		}
	}
}

func updateNetworkProp(props *apcfg.PropertyNode, prop, new string) {
	old := ""
	if node := props.GetChild(prop); node != nil {
		old = node.GetValue()
	}
	if old != new {
		path := "@/network/" + prop
		err := config.CreateProp(path, new, nil)
		if err != nil {
			log.Printf("Failed to update %s: %v\n", path, err)
		}
	}
}

//
// If our device inventory caused us to change any of the old network choices,
// update the config now.
//
func updateNetworkConfig(props *apcfg.PropertyNode) {
	updateNetworkProp(props, "setup_nic", setupNic)
	updateNetworkProp(props, "wan_nic", wanNic)
}

func main() {
	var err error

	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	flag.Parse()
	*templateDir = aputil.ExpandDirPath(*templateDir)
	*rulesDir = aputil.ExpandDirPath(*rulesDir)

	if mcpd, err = mcp.New(pname); err != nil {
		log.Printf("cannot connect to mcp: %v\n", err)
	} else {
		mcpd.SetState(mcp.INITING)
	}

	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(*addr, nil)

	b := broker.New(pname)
	defer b.Fini()

	config, err = apcfg.NewConfig(b, pname)
	if err != nil {
		log.Fatalf("cannot connect to configd: %v\n", err)
	}
	config.HandleChange(`^@/clients/.*/ring$`, configRingChanged)
	config.HandleChange(`^@/network/`, configNetworkChanged)

	rings = config.GetRings()
	clients = config.GetClients()

	props, err := config.GetProps("@/network")
	if err != nil {
		err = fmt.Errorf("unable to fetch configuration: %v", err)
	} else {
		getDevices()
		prepareWan()
		err = prepareWireless(props)
	}
	if err == nil {
		err = loadFilterRules()
	}

	if err != nil {
		if mcpd != nil {
			mcpd.SetState(mcp.BROKEN)
		}
		log.Fatalf("networkd failed to start: %v\n", err)
	}

	updateNetworkConfig(props)
	applyFilters()

	running = true
	go signalHandler()

	os.Exit(runAll())
}