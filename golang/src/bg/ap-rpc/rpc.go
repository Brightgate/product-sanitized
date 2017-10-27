/*
 * COPYRIGHT 2017 Brightgate Inc. All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 */

package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"bg/ap_common/apcfg"
	"bg/base_msg"
	"bg/cloud_rpc"

	"github.com/golang/protobuf/proto"
	"github.com/tomazk/envcfg"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/grpclog"
	// "github.com/golang/protobuf/proto"
)

type Cfg struct {
	// Override configuration value.
	B10E_LOCAL_MODE bool
	B10E_SVC_URL    string
}

const pname = "ap-rpc"

var services = map[string]bool{
	"upbeat":    true,
	"inventory": true,
}

var (
	aproot = flag.String("root", "proto.armv7l/appliance/opt/com.brightgate",
		"Root of AP installation")

	environ    Cfg
	serverAddr string
	config     *apcfg.APConfig

	apuuid   string
	aphwaddr []string

	// ApVersion will be replaced by go build step.
	ApVersion = "undefined"
)

// Return the MAC address for the defined WAN interface.
func getWanInterface(config *apcfg.APConfig) string {
	wan_nic, err := config.GetProp("@/network/wan_nic")
	if err != nil {
		log.Fatalf("property get @/network/wan_nic failed: %v\n", err)
	}

	iface, err := net.InterfaceByName(wan_nic)
	if err != nil {
		log.Fatalf("could not retrieve %s interface: %v\n", wan_nic, err)
	}

	return iface.HardwareAddr.String()
}

func firstVersion() string {
	return "git:rPS" + ApVersion
}

// Retrieve the instance uptime. os.Stat("/proc/1") returns
// start-of-epoch for the creation time on Raspbian, so we will instead
// use the contents of /proc/uptime.  uptime records in seconds, so we
// multiply by 10^9 to create a time.Duration.
func retrieveUptime() time.Duration {
	uptime, err := os.Open("/proc/uptime")
	if err != nil {
		log.Printf("could not open /proc/uptime: %v\n", err)
		os.Exit(1)
	}

	scanner := bufio.NewScanner(uptime)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), " ")
		val, err := strconv.ParseFloat(fields[0], 10)
		if err != nil {
			log.Printf("/proc/uptime contents unusual: %v\n", err)
			os.Exit(1)
		}
		return time.Duration(val * 1e9)
	}
	if err = scanner.Err(); err != nil {
		log.Printf("/proc/uptime scan failed: %v\n", err)
		os.Exit(1)
	}

	log.Printf("/proc/uptime possibly empty\n")
	os.Exit(1)

	// Not reached.
	return time.Duration(0)
}

func dial() (*grpc.ClientConn, error) {
	var opts []grpc.DialOption

	if !environ.B10E_LOCAL_MODE {
		cp, nocperr := x509.SystemCertPool()
		if nocperr != nil {
			return nil, fmt.Errorf("no system certificate pool: %v", nocperr)
		}

		tc := tls.Config{
			RootCAs: cp,
		}

		ctls := credentials.NewTLS(&tc)
		opts = append(opts, grpc.WithTransportCredentials(ctls))
	} else {
		opts = append(opts, grpc.WithInsecure())
	}

	opts = append(opts, grpc.WithUserAgent(pname))

	conn, err := grpc.Dial(serverAddr, opts...)
	if err != nil {
		return nil, fmt.Errorf("grpc Dial() to '%s' failed: %v", serverAddr, err)
	}
	return conn, nil
}

func sendUpbeat() {
	var elapsed time.Duration

	// Retrieve appliance uptime.
	elapsed = retrieveUptime()

	// Retrieve component versions.
	versions := make([]string, 0)
	versions = append(versions, firstVersion())

	year := time.Now().Year()
	rhmac := hmac.New(sha256.New, cloud_rpc.HMACKeys[year])
	data := fmt.Sprintf("%x %d", aphwaddr, int64(elapsed))
	rhmac.Write([]byte(data))

	// Build UpcallRequest
	request := &cloud_rpc.UpcallRequest{
		HMAC:             rhmac.Sum(nil),
		Uuid:             proto.String(apuuid),
		UptimeElapsed:    proto.Int64(int64(elapsed)),
		WanHwaddr:        aphwaddr,
		ComponentVersion: versions,
		NetHostCount:     proto.Int32(0), // XXX not finished
	}

	conn, err := dial()
	if err != nil {
		grpclog.Fatalf("dial() failed: %v", err)
	}
	defer conn.Close()

	client := cloud_rpc.NewUpbeatClient(conn)

	response, err := client.Upcall(context.Background(), request)
	if err != nil {
		grpclog.Fatalf("%v.Upcall(_) = _, %v: ", client, err)
	}

	log.Println(response)
	grpclog.Println(response)
}

func sendInventory() {
	// Read device inventory from disk
	in, err := ioutil.ReadFile(*aproot + "/var/spool/identifierd/observations.pb")
	if err != nil {
		log.Println("failed to read device inventory")
		return
	}
	inventory := &base_msg.DeviceInventory{}
	proto.Unmarshal(in, inventory)

	// HMAC
	year := time.Now().Year()
	rhmac := hmac.New(sha256.New, cloud_rpc.HMACKeys[year])
	rhmac.Write([]byte(inventory.String()))

	// Build InventoryReport
	report := &cloud_rpc.InventoryReport{
		HMAC:      rhmac.Sum(nil),
		Uuid:      proto.String(apuuid),
		WanHwaddr: aphwaddr,
		Devices:   inventory,
	}

	conn, err := dial()
	if err != nil {
		grpclog.Fatalf("dial() failed: %v", err)
	}
	defer conn.Close()

	client := cloud_rpc.NewInventoryClient(conn)

	response, err := client.Upcall(context.Background(), report)
	if err != nil {
		grpclog.Fatalf("%v.Upcall(_) = _, %v: ", client, err)
	}

	log.Println(response)
	grpclog.Println(response)
}

func main() {
	var err error
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	flag.Parse()

	svc := flag.Args()[0]
	if !services[svc] {
		log.Fatalf("Unknown service %s\n", svc)
	}

	envcfg.Unmarshal(&environ)

	config, err := apcfg.NewConfig(nil, pname)
	if err != nil {
		log.Fatalf("cannot connect to configd: %v\n", err)
	}

	// Retrieve appliance UUID
	apuuid, err = config.GetProp("@/uuid")
	if err != nil {
		log.Fatalf("property get failed: %v\n", err)
	}

	// Retrieve appliance MAC.
	aphwaddr = make([]string, 0)
	aphwaddr = append(aphwaddr, getWanInterface(config))

	if len(environ.B10E_SVC_URL) == 0 {
		// XXX ap.configd lookup.
		serverAddr = "svc0.b10e.net:4430"
	} else {
		serverAddr = environ.B10E_SVC_URL
	}

	switch svc {
	case "upbeat":
		sendUpbeat()
	case "inventory":
		sendInventory()
	}
}