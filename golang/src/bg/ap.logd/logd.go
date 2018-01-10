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
 * message logger
 */

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"bg/ap_common/aputil"
	"bg/ap_common/broker"
	"bg/ap_common/mcp"
	"bg/ap_common/network"
	"bg/base_def"
	"bg/base_msg"

	"github.com/golang/protobuf/proto"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	addr = flag.String("listen-address", base_def.LOGD_PROMETHEUS_PORT,
		"The address to listen on for HTTP requests.")
	logDir  = flag.String("logdir", "", "Log file directory")
	logFile *os.File

	eventsHandled = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "events_handled",
			Help: "Number of events logged.",
		})
)

const pname = "ap.logd"

func handlePing(event []byte) {
	ping := &base_msg.EventPing{}
	proto.Unmarshal(event, ping)
	log.Printf("[sys.ping] %v", ping)
	eventsHandled.Inc()
}

func handleConfig(event []byte) {
	config := &base_msg.EventConfig{}
	proto.Unmarshal(event, config)
	log.Printf("[sys.config] %v", config)
	eventsHandled.Inc()
}

func handleEntity(event []byte) {
	entity := &base_msg.EventNetEntity{}
	proto.Unmarshal(event, entity)
	log.Printf("[net.entity] %v", entity)
	eventsHandled.Inc()
}

func extendMsg(msg *string, field, value string) {
	new := field + ": " + value
	if len(*msg) > 0 {
		*msg += ", "
	}
	*msg += new
}

func handleException(event []byte) {
	exception := &base_msg.EventNetException{}
	proto.Unmarshal(event, exception)
	log.Printf("[net.exception] %v", exception)
	eventsHandled.Inc()

	// Construct a user-friendly message to push to the system log
	time := aputil.ProtobufToTime(exception.Timestamp)
	timestamp := time.Format("2006/01/02 13:04:05")

	msg := ""
	if exception.Sender != nil {
		extendMsg(&msg, "Sender", *exception.Sender)
	}

	if exception.Protocol != nil {
		protocols := base_msg.Protocol_name
		num := int32(*exception.Protocol)
		extendMsg(&msg, "Protocol", protocols[num])
	}

	if exception.Reason != nil {
		reasons := base_msg.EventNetException_Reason_name
		num := int32(*exception.Reason)
		extendMsg(&msg, "Reason", reasons[num])
	}

	if exception.MacAddress != nil {
		mac := network.Uint64ToHWAddr(*exception.MacAddress)
		extendMsg(&msg, "hwaddr", mac.String())
	}

	if exception.Ipv4Address != nil {
		ip := network.Uint32ToIPAddr(*exception.Ipv4Address)
		extendMsg(&msg, "IP", ip.String())
	}

	if exception.Hostname != nil {
		extendMsg(&msg, "Hostname", *exception.Hostname)
	}

	if exception.Message != nil {
		extendMsg(&msg, "Message", *exception.Message)

	}

	fmt.Printf("%s Handled exception event: %s\n", timestamp, msg)
}

func handleResource(event []byte) {
	resource := &base_msg.EventNetResource{}
	proto.Unmarshal(event, resource)
	log.Printf("[net.resource] %v", resource)
	eventsHandled.Inc()
}

func handleRequest(event []byte) {
	request := &base_msg.EventNetRequest{}
	proto.Unmarshal(event, request)
	log.Printf("[net.request] %v", request)
	eventsHandled.Inc()
}

func handleIdentity(event []byte) {
	identity := &base_msg.EventNetIdentity{}
	proto.Unmarshal(event, identity)
	log.Printf("[net.identity] %v", identity)
	eventsHandled.Inc()
}

func openLog(path string) (*os.File, error) {
	fp, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("couldn't get absolute path: %v", err)
	}

	if err := os.MkdirAll(fp, 0755); err != nil {
		return nil, fmt.Errorf("failed to make path: %v", err)
	}

	logfile := fp + "/events.log"
	file, err := os.OpenFile(logfile, os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0600)
	if err != nil {
		return nil, fmt.Errorf("error opening log file: %v", err)
	}
	return file, nil
}

func reopenLogfile() error {
	newLog, err := openLog(*logDir)
	if err != nil {
		return err
	}
	log.SetOutput(newLog)
	if logFile != nil {
		logFile.Close()
	}
	logFile = newLog
	return nil
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	flag.Parse()
	*logDir = aputil.ExpandDirPath(*logDir)

	err := reopenLogfile()
	if err != nil {
		log.Fatalf("Failed to setup logging: %s\n", err)
	}

	mcpd, err := mcp.New(pname)
	if err != nil {
		log.Println("Failed to connect to mcp")
	}

	prometheus.MustRegister(eventsHandled)

	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(*addr, nil)

	log.Println("prometheus client launched")

	b := broker.New(pname)
	b.Handle(base_def.TOPIC_PING, handlePing)
	b.Handle(base_def.TOPIC_CONFIG, handleConfig)
	b.Handle(base_def.TOPIC_ENTITY, handleEntity)
	b.Handle(base_def.TOPIC_EXCEPTION, handleException)
	b.Handle(base_def.TOPIC_RESOURCE, handleResource)
	b.Handle(base_def.TOPIC_REQUEST, handleRequest)
	b.Handle(base_def.TOPIC_IDENTITY, handleIdentity)
	defer b.Fini()

	if mcpd != nil {
		mcpd.SetState(mcp.ONLINE)
	}

	sig := make(chan os.Signal)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	for {
		switch s := <-sig; s {
		case syscall.SIGHUP:
			log.Printf("Signal (%v) received, reopening logs.\n", s)
			err = reopenLogfile()
			if err != nil {
				log.Fatalf("Exiting.  Fatal error reopening log: %s\n", err)
			}
		default:
			log.Fatalf("Signal (%v) received, stopping\n", s)
		}
	}
}
