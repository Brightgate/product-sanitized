/*
 * COPYRIGHT 2019 Brightgate Inc.  All rights reserved.
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
	"net"
	"time"

	"bg/ap_common/dhcp"
	"bg/base_def"
)

// Try to identify the NIC connecting us to the world.  We should have at most
// one with a DHCP address, and it should be on an expected interface.
func findWan() (string, *dhcp.Info) {
	var wanNic string
	var wanLease *dhcp.Info

	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		name := iface.Name
		hwaddr := iface.HardwareAddr.String()

		if plat.NicIsVirtual(name) {
			continue
		}

		lease, _ := dhcp.GetLease(name)
		expectedWan := plat.NicIsWan(name, hwaddr)

		if expectedWan {
			if wanNic != "" {
				logWarn("%s and %s both appear to be WAN nics",
					wanNic, name)
			} else {
				wanNic = name
				wanLease = lease
			}
		} else if lease != nil {
			logWarn("internal NIC %s has a dhcp lease: %v",
				name, lease)
		}
	}

	return wanNic, wanLease
}

// If we can't determine the mode when we first start up, we need to keep
// checking until we get a DHCP lease, which will give us the answer.
func modeMonitor() {
	var nic, oldMode, newMode string

	if oldMode = nodeMode; oldMode != base_def.MODE_GATEWAY {
		logPanic("should not enter nodeMonitor() in %s mode", oldMode)
	}

	for {
		var lease *dhcp.Info

		if oldMode != nodeMode {
			logPanic("mode unexpectedly changed from %s to %s",
				oldMode, newMode)
		}

		if nic, lease = findWan(); lease != nil {
			newMode = lease.Mode
			break
		}
		time.Sleep(time.Second)
	}

	if newMode != base_def.MODE_SATELLITE {
		logInfo("DHCP lease confirms %s mode", nodeMode)
		return
	}

	logInfo("Switching from %s to %s mode", oldMode, newMode)

	all := "all"
	handleStop(selectTargets(&all))

	// Just in case the networkd shutdown/cleanup changed the wan interface,
	// we let the DHCP daemon reconfigure it.
	logInfo("Renewing DHCP leases")
	dhcp.RenewLease(nic)
	time.Sleep(2 * time.Second)

	nodeMode = newMode
	nodeName, _ = plat.GetNodeID()
	daemonReinit()

	handleStart(selectTargets(&all))
	go satelliteLoop()
}