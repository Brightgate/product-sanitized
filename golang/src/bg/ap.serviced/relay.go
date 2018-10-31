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

package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"bg/ap_common/aputil"
	"bg/ap_common/network"
	"bg/base_def"
	"bg/base_msg"
	"bg/common/cfgapi"

	"github.com/golang/protobuf/proto"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/ipv4"
)

type endpoint struct {
	conn  *ipv4.PacketConn
	iface *net.Interface
	ip    net.IP
	port  int
	ring  string
}

type service struct {
	name    string
	address net.IP
	port    int
	init    func()
	handler func(*endpoint, []byte) error
}

var multicastServices = []service{
	{"mDNS", net.IPv4(224, 0, 0, 251), 5353, nil, mDNSHandler},
	{"SSDP", net.IPv4(239, 255, 255, 250), 1900, ssdpInit, ssdpHandler},
}

var ringLevel = map[string]int{
	base_def.RING_CORE:     0,
	base_def.RING_STANDARD: 1,
	base_def.RING_DEVICES:  2,
	base_def.RING_GUEST:    3,
}

var (
	ssdpBase = flag.Int("sbase", 31000, "start of SSDP response ports")
	ssdpMax  = flag.Int("smax", 20, "Max # of open M-SEARCH requests")

	rings cfgapi.RingMap

	ifaceToRing    map[int]string
	ringToIface    map[string]*net.Interface
	ipv4ToIface    map[string]*net.Interface
	ifaceBroadcast map[string]net.IP

	ssdpSearches   *ssdpSearchState
	ssdpSearchLock sync.Mutex

	relayMetric struct {
		mdnsRequests  prometheus.Counter
		mdnsReplies   prometheus.Counter
		ssdpSearches  prometheus.Counter
		ssdpTimeouts  prometheus.Counter
		ssdpNotifies  prometheus.Counter
		ssdpResponses prometheus.Counter
	}
)

type ssdpSearchState struct {
	buf       []byte
	port      int
	addr      *net.UDPAddr
	requestor *ipv4.PacketConn
	listener  *ipv4.PacketConn
	next      *ssdpSearchState
}

func vlanBridge(vlan int) string {
	return "brvlan" + strconv.Itoa(vlan)
}

func initListener(s service) (p *ipv4.PacketConn, err error) {
	var c net.PacketConn

	if s.init != nil {
		s.init()
	}

	portStr := ":" + strconv.Itoa(s.port)
	if c, err = net.ListenPacket("udp4", portStr); err != nil {
		err = fmt.Errorf("failed to listen on port %d: %v", s.port, err)
		return
	}

	if p = ipv4.NewPacketConn(c); p == nil {
		err = fmt.Errorf("couldn't create PacketConn")
		return
	}

	if err = p.SetControlMessage(ipv4.FlagSrc, true); err != nil {
		err = fmt.Errorf("couldn't set ControlMessage: %v", err)
		return
	}

	if s.address != nil {
		udpaddr := &net.UDPAddr{IP: s.address}
		for _, iface := range ringToIface {
			if err = p.JoinGroup(iface, udpaddr); err != nil {
				break
			}
		}

		if err != nil {
			err = fmt.Errorf("failed to join multicast group: %v",
				err)
		}
	}

	return
}

func mDNSEvent(addr net.IP, requests, responses []string) {
	event := &base_msg.EventmDNS{
		Request:  requests,
		Response: responses,
	}

	listenType := base_msg.EventListen_mDNS
	listen := &base_msg.EventListen{
		Timestamp:   aputil.NowToProtobuf(),
		Sender:      proto.String(brokerd.Name),
		Debug:       proto.String("-"),
		Ipv4Address: proto.Uint32(network.IPAddrToUint32(addr)),
		Type:        &listenType,
		Mdns:        event,
	}

	if err := brokerd.Publish(listen, base_def.TOPIC_LISTEN); err != nil {
		slog.Warnf("Error sending mDNS listen event: %v", err)
	}
}

func mDNSHandler(source *endpoint, b []byte) error {
	msg := new(dns.Msg)
	if err := msg.Unpack(b); err != nil {
		return fmt.Errorf("malformed mDNS packet: %v", err)
	}

	requests := make([]string, 0)
	responses := make([]string, 0)

	if len(msg.Question) > 0 {
		relayMetric.mdnsRequests.Inc()
		slog.Debugf("mDNS request from %v", source.ip)
		for _, question := range msg.Question {
			slog.Debugf("   %s", question.String())
			requests = append(requests, question.String())
		}
	}

	if len(msg.Answer) > 0 {
		relayMetric.mdnsReplies.Inc()
		slog.Debugf("mDNS reply from %v", source.ip)
		for _, answer := range msg.Answer {
			slog.Debugf("   %s", answer.String())
			responses = append(responses, answer.String())
		}
	}

	mDNSEvent(source.ip, requests, responses)

	return nil
}

func ssdpEvent(addr net.IP, mtype base_msg.EventSSDP_MessageType,
	req *http.Request) {

	msg := &base_msg.EventSSDP{}
	msg.Type = &mtype

	// only stores first value for each header
	msg.Server = proto.String(req.Header.Get("Server"))
	req.Header.Del("Server")
	msg.UniqueServiceName = proto.String(req.Header.Get("Usn"))
	req.Header.Del("Usn")
	msg.Location = proto.String(req.Header.Get("Location"))
	req.Header.Del("Location")
	msg.SearchTarget = proto.String(req.Header.Get("St"))
	req.Header.Del("St")
	msg.NotificationType = proto.String(req.Header.Get("Nt"))
	req.Header.Del("Nt")

	headers := map[string][]string(req.Header)
	hs := make([]*base_msg.Pair, 0)
	for k, v := range headers {
		if len(v) > 0 {
			p := &base_msg.Pair{
				Header: proto.String(k),
				Value:  proto.String(v[0]),
			}
			hs = append(hs, p)
		}
	}
	msg.ExtraHeaders = hs

	listenType := base_msg.EventListen_SSDP
	listen := &base_msg.EventListen{
		Timestamp:   aputil.NowToProtobuf(),
		Sender:      proto.String(brokerd.Name),
		Debug:       proto.String("-"),
		Ipv4Address: proto.Uint32(network.IPAddrToUint32(addr)),
		Type:        &listenType,
		Ssdp:        msg,
	}

	if err := brokerd.Publish(listen, base_def.TOPIC_LISTEN); err != nil {
		slog.Warnf("Error sending SSDP listen event: %v", err)
	}
}

func ssdpSearchAlloc(source *endpoint, mx int) (*ssdpSearchState, error) {
	ssdpSearchLock.Lock()
	defer ssdpSearchLock.Unlock()

	sss := ssdpSearches
	if sss == nil {
		return nil, fmt.Errorf("too many outstanding M-SEARCH requests")
	}

	// MX is the maximum time the device should wait before responding.  We
	// will leave our port open for 2x that long.
	deadline := time.Now().Add(time.Duration(mx*2) * time.Second)
	if err := sss.listener.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("unable to set UDP deadline: %v", err)
	}

	ssdpSearches = sss.next
	sss.requestor = source.conn
	sss.addr = &net.UDPAddr{IP: source.ip, Port: source.port}

	return sss, nil
}

func ssdpSearchFree(sss *ssdpSearchState) {
	ssdpSearchLock.Lock()
	defer ssdpSearchLock.Unlock()

	sss.requestor = nil
	sss.addr = nil
	sss.next = ssdpSearches
	ssdpSearches = sss
}

// Currently we just check an SSDP packet to be sure that it's a correctly
// structured HTTP response.  We don't examine its contents, but an OK may
// contain information that would be useful to identifierd.
func ssdpResponseCheck(rdr io.Reader) error {
	_, err := http.ReadResponse(bufio.NewReader(rdr), nil)
	if err != nil {
		err = fmt.Errorf("malformed HTTP: %v", err)
	}
	return err
}

func ssdpResponseRelay(sss *ssdpSearchState) {
	defer ssdpSearchFree(sss)

	buf := sss.buf
	addr := sss.addr

	for {
		n, _, src, err := sss.listener.ReadFrom(buf)
		if err != nil {
			// This port has a deadline set, so we expect to hit a
			// timeout.  Any other error is worth noting.
			e, _ := err.(net.Error)
			if !e.Timeout() {
				slog.Warnf("Failed to read from %v: %v",
					sss.listener.LocalAddr(), err)
			}
			relayMetric.ssdpTimeouts.Inc()
			return
		}
		if err = ssdpResponseCheck(bytes.NewReader(buf)); err != nil {
			slog.Warnf("Bad SSDP response from %v: %v", src, err)
			return
		}

		slog.Debugf("Forwarding SSDP response from/to %v", src, addr)
		relayMetric.ssdpResponses.Inc()
		l, err := sss.requestor.WriteTo(buf[:n], nil, addr)
		if err != nil {
			slog.Warnf("    Forward to %v failed: %v", addr, err)
			return
		} else if l != n {
			slog.Warnf("    Forwarded %d of %d to %v", l, n, addr)
			return
		}
	}
}

// The response to an SSDP M-SEARCH request is a unicast packet back to the
// originating port.  We create a new local UDP port from which to forward the
// SEARCH packet, and on which we will listen for responses.  We also make a
// static copy of the originating endpoint structure, so we know where the
// response packet needs to be forwarded.
func ssdpSearchHandler(source *endpoint, mx int) error {
	sss, err := ssdpSearchAlloc(source, mx)
	if err != nil {
		return err
	}
	slog.Debugf("Forwarding SSDP M-SEARCH from %v", sss.addr)

	// Replace the original PacketConn in the source structure with our new
	// PacketConn, causing the SEARCH request to be forwarded from our newly
	// opened UDP port instead of the standard SSDP port (1900).
	source.conn = sss.listener

	go ssdpResponseRelay(sss)
	return nil
}

func ssdpHandler(source *endpoint, buf []byte) error {
	var req *http.Request

	rdr := bytes.NewReader(buf)
	req, err := http.ReadRequest(bufio.NewReader(rdr))
	if err != nil {
		// If we failed to parse the packet as a request, attempt it as
		// a response.
		rdr.Seek(0, io.SeekStart)
		return ssdpResponseCheck(rdr)

	}

	var mtype base_msg.EventSSDP_MessageType
	if req.Method == "M-SEARCH" {
		uri := req.Header.Get("Man")
		if uri == "\"ssdp:discover\"" {
			mtype = base_msg.EventSSDP_DISCOVER
			mxHdr := req.Header.Get("MX")
			mx, _ := strconv.Atoi(mxHdr)
			if mxHdr == "" {
				err = fmt.Errorf("M-SEARCH missing MX header")
			} else if mx < 1 || mx > 5 {
				err = fmt.Errorf("M-SEARCH bad MX header: %s",
					mxHdr)
			} else {
				err = ssdpSearchHandler(source, mx)
			}
		} else if uri == "" {
			err = fmt.Errorf("missing M-SEARCH uri")
		} else {
			err = fmt.Errorf("unrecognized M-SEARCH uri: %s", uri)
		}
		if err == nil {
			relayMetric.ssdpSearches.Inc()
		}
	} else if req.Method == "NOTIFY" {
		nts := req.Header.Get("NTS")
		if nts == "ssdp:alive" {
			mtype = base_msg.EventSSDP_ALIVE
			slog.Debugf("Forwarding SSDP ALIVE from %v", source.ip)
		} else if nts == "ssdp:byebye" {
			mtype = base_msg.EventSSDP_BYEBYE
			slog.Debugf("Forwarding SSDP BYEBYE from %v", source.ip)
		} else if nts == "" {
			err = fmt.Errorf("missing NOTIFY nts")
		} else {
			err = fmt.Errorf("unrecognized NOTIFY nts: %s", nts)

		}
		if err == nil {
			relayMetric.ssdpNotifies.Inc()
		}
	} else {
		err = fmt.Errorf("invalid HTTP Method: %s (%v)", req.Method, req)
	}

	if err == nil {
		ssdpEvent(source.ip, mtype, req)
	}

	return err
}

func ssdpInit() {
	low := *ssdpBase
	high := *ssdpBase + *ssdpMax

	for port := low; port < high; port++ {
		p, err := net.ListenPacket("udp4", ":"+strconv.Itoa(port))
		if err != nil {
			slog.Warnf("unable to init SEARCH handler on %d: %v",
				port, err)
		} else {
			ssdpSearches = &ssdpSearchState{
				buf:      make([]byte, 4096),
				port:     port,
				next:     ssdpSearches,
				listener: ipv4.NewPacketConn(p),
			}
		}
	}

	propBase := "@/firewall/rules/sonos/"
	rule := fmt.Sprintf("ACCEPT UDP FROM IFACE NOT wan TO AP DPORTS %d:%d",
		low, high-1)
	ops := []cfgapi.PropertyOp{
		{
			Op:    cfgapi.PropCreate,
			Name:  propBase + "rule",
			Value: rule,
		},
		{
			Op:    cfgapi.PropCreate,
			Name:  propBase + "active",
			Value: "true",
		},
	}

	config.Execute(nil, ops)
}

//
// Read the next message for this protocol.  Return the length and the interface
// on which it arrived.
func getPacket(conn *ipv4.PacketConn, buf []byte) (int, *endpoint) {
	for {
		var ip net.IP
		var portno int

		n, cm, src, err := conn.ReadFrom(buf)
		if n == 0 || err != nil {
			if err != nil {
				slog.Warnf("Read failed: %v", err)
			}
			continue
		}

		ipv4 := ""
		if host, port, serr := net.SplitHostPort(src.String()); serr == nil {
			if ip = net.ParseIP(host); ip != nil {
				ipv4 = ip.To4().String()
			}
			portno, _ = strconv.Atoi(port)
		}
		if ipv4 == "" {
			slog.Warnf("Not an valid source: %s", src.String())
			continue
		}
		if _, ok := ipv4ToIface[ipv4]; ok {
			// If this came from one of our addresses, it's a packet
			// we just forwarded.  Ignore it.
			continue
		}

		iface, ierr := net.InterfaceByIndex(cm.IfIndex)
		if ierr != nil {
			slog.Warnf("Receive error from %s: %v", ipv4, err)
			continue
		}

		ring, ok := ifaceToRing[iface.Index]
		if !ok {
			// This packet isn't from a ring we relay UDP to/from
			continue
		}

		source := endpoint{
			conn:  conn,
			iface: iface,
			ip:    ip,
			port:  portno,
			ring:  ring,
		}
		return n, &source
	}
}

//
// Process all the multicast messages for a single service.  Each message is
// read, parsed, possibly evented to identifierd, and then forwarded to each
// ring allowed to receive it.
func mrelay(s service) {
	conn, err := initListener(s)
	if err != nil {
		slog.Warnf("Unable to init relay for %s: %v", s.name, err)
		return
	}

	fw := &net.UDPAddr{IP: s.address, Port: s.port}
	buf := make([]byte, 4096)
	for {
		var err error

		n, source := getPacket(conn, buf)
		if source.iface == nil {
			slog.Warnf("multicast packet arrived on bad source: %v",
				source)
		}

		//
		// Currently we relay all messages up and down the rings.  It
		// may make sense for this to be a per-device and/or
		// per-protocol policy.
		relayUp := true
		relayDown := true

		if err = s.handler(source, buf); err != nil {
			slog.Warnf("Bad %s packet: %v", s.name, err)
			continue
		}

		srcLevel := ringLevel[source.ring]
		for dstRing, dstLevel := range ringLevel {
			dstIface := ringToIface[dstRing]
			if dstIface == nil {
				slog.Fatalf("missing interface for ring %s",
					dstRing)
			}
			if dstIface.Index == source.iface.Index {
				// Don't repeat the message on the interface it
				// arrived on
				continue
			}

			if !relayDown && (srcLevel > dstLevel) {
				continue
			}

			if !relayUp && (srcLevel < dstLevel) {
				continue
			}

			source.conn.SetMulticastInterface(dstIface)
			source.conn.SetMulticastTTL(255)
			l, err := source.conn.WriteTo(buf[:n], nil, fw)
			if err != nil {
				slog.Warnf("    Forward to %s failed: %v",
					dstIface.Name, err)
			} else if l != n {
				slog.Warnf("    Forwarded %d of %d to %s",
					l, n, dstIface.Name)
			} else {
				slog.Debugf("    Forwarded %d bytes to %s",
					n, dstIface.Name)
			}
		}
	}
}

func initInterfaces() {
	rings = config.GetRings()

	ifaceToRing = make(map[int]string)
	ringToIface = make(map[string]*net.Interface)
	ipv4ToIface = make(map[string]*net.Interface)
	ifaceBroadcast = make(map[string]net.IP)

	//
	// Iterate over all of the rings to/which we will relay UDP broadcasts.
	// Find the interface that serves that ring and the IP address of the
	// router for that subnet.
	//
	for ring, conf := range rings {
		var name string

		// Find the interface that serves this ring, so we can add the
		// interface to the multicast groups on which we listen.
		if _, ok := ringLevel[ring]; !ok {
			slog.Debugf("No relaying from %s", ring)
			continue
		}

		bridge := vlanBridge(conf.Vlan)
		iface, err := net.InterfaceByName(bridge)
		if iface == nil || err != nil {
			slog.Warnf("No interface %s: %v", bridge, err)
			continue
		}

		ifaceBroadcast[name] = network.SubnetBroadcast(conf.Subnet)
		ipv4ToIface[network.SubnetRouter(conf.Subnet)] = iface
		ringToIface[ring] = iface
		ifaceToRing[iface.Index] = ring
	}
}

func relayPrometheusInit() {
	relayMetric.mdnsRequests = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "relayd_mdns_requests",
		Help: "mDNS requests handled",
	})
	relayMetric.mdnsReplies = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "relayd_mdns_replies",
		Help: "mDNS replies handled",
	})
	relayMetric.ssdpSearches = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "relayd_ssdp_searches",
		Help: "SSDP search requests handled",
	})
	relayMetric.ssdpTimeouts = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "relayd_ssdp_timeouts",
		Help: "SSDP search timeouts",
	})
	relayMetric.ssdpNotifies = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "relayd_ssdp_notifies",
		Help: "SSDP notifies handled",
	})
	relayMetric.ssdpResponses = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "relayd_ssdp_requests",
		Help: "SSDP requests handled",
	})

	prometheus.MustRegister(relayMetric.mdnsRequests)
	prometheus.MustRegister(relayMetric.mdnsReplies)
	prometheus.MustRegister(relayMetric.ssdpSearches)
	prometheus.MustRegister(relayMetric.ssdpNotifies)
	prometheus.MustRegister(relayMetric.ssdpResponses)
}

func relayInit() {
	initInterfaces()
	relayPrometheusInit()
	for _, s := range multicastServices {
		go mrelay(s)
	}
}