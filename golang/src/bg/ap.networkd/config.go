/*
 * COPYRIGHT 2020 Brightgate Inc.  All rights reserved.
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
	"strconv"
	"strings"
	"time"

	"bg/ap_common/publiclog"
	"bg/base_def"
	"bg/common/cfgapi"
	"bg/common/wifi"
)

func configNicChanged(path []string, val string, expires *time.Time) {
	var eval bool

	if len(path) != 5 {
		return
	}
	p := physDevices[path[3]]
	if p == nil || p.pseudo {
		return
	}

	switch path[4] {
	case "cfg_channel":
		x, _ := strconv.Atoi(val)
		if eval = (p.wifi != nil && p.wifi.configChannel != x); eval {
			p.wifi.configChannel = x
		}
	case "cfg_width":
		x, _ := strconv.Atoi(val)
		if eval = (p.wifi != nil && p.wifi.configWidth != x); eval {
			p.wifi.configWidth = x
		}
	case "cfg_band":
		if eval = (p.wifi != nil && p.wifi.configBand != val); eval {
			p.wifi.configBand = val
		}
	case "ring":
		if p.ring != val {
			p.ring = val
			networkdStop("exiting to rebuild network")
		}
	case "state":
		newState := strings.ToLower(val)
		if newState == wifi.DevDisabled || newState == wifi.DevOK {
			oldVal := p.disabled
			p.disabled = (newState == wifi.DevDisabled)
			eval = (oldVal != p.disabled)
			setState(p)
		}
	}

	if eval {
		wifiEvaluate = true
		hostapd.reset()
	}
}

func configNicDeleted(path []string) {
	if len(path) == 5 {
		switch path[4] {
		case "cfg_channel", "cfg_width", "cfg_band", "ring", "state":
			configNicChanged(path, "", nil)
		}
	}
}

func configClientNodeChanged(path []string, val string, expires *time.Time) {
	hwaddr := path[1]
	newNode := val
	c, ok := clients[hwaddr]

	if ok && c.ConnNode != newNode {
		slog.Infof("Moving %s from %s to %s", hwaddr, c.ConnNode, newNode)
		c.ConnNode = newNode
	}
}

func configClientRingChanged(path []string, val string, expires *time.Time) {
	hwaddr := path[1]
	newRing := val
	c, ok := clients[hwaddr]

	if !ok {
		c := cfgapi.ClientInfo{Ring: newRing}
		slog.Infof("New client %s in %s", hwaddr, newRing)
		clients[hwaddr] = &c
		hostapd.disassociate(hwaddr)
	} else if c.Ring != newRing {
		slog.Infof("Moving %s from %s to %s", hwaddr, c.Ring, newRing)
		c.Ring = newRing
		hostapd.reload()
		hostapd.disassociate(hwaddr)
	} else {
		// False alarm.
		return
	}

	if newRing == base_def.RING_QUARANTINE {
		publiclog.SendLogDeviceQuarantine(brokerd, hwaddr)
	}

	hostapd.reload()
}

func configUserDeleted(path []string) {
	if len(path) == 2 {
		hostapd.deauthUser(path[1])
	}
}

func configRingSubnetDeleted(path []string) {
	ring := path[1]

	if _, ok := rings[ring]; !ok {
		slog.Warnf("Unknown ring: %s", ring)
	} else {
		slog.Infof("Deleted subnet for ring %s", ring)
		networkdStop("exiting to rebuild network")
	}
}

func configRingChanged(path []string, val string, expires *time.Time) {

	if len(path) != 3 {
		return
	}

	ring := path[1]
	r, ok := rings[ring]
	if !ok {
		slog.Warnf("Unknown ring: %s", ring)
		return
	}

	switch path[2] {
	case "vap":
		if r.VirtualAP != val {
			slog.Infof("Changing VAP for ring %s from %s to %s",
				ring, r.VirtualAP, val)
			r.VirtualAP = val
			hostapd.reset()
		}
	case "subnet":
		if r.Subnet != val {
			slog.Infof("Changing subnet for ring %s from %s to %s",
				ring, r.Subnet, val)
			networkdStop("exiting to rebuild network")
		}
	}
}

func configSet(name, val string) bool {
	var reload bool

	switch name {
	case "base_address":
		networkdStop("base_address changed - exiting to rebuild network")
		return false

	case "radius_auth_secret":
		prop := &wconf.radiusSecret
		if prop != nil && *prop != val {
			slog.Infof("%s changed to '%s'", name, val)
			*prop = val
			reload = true
		}

	case "dnsserver":
		wanStaticChanged(name, val)
	}

	return reload
}

func configNetworkDeleted(path []string) {
	if configSet(path[1], "") {
		wifiEvaluate = true
		hostapd.reload()
	} else if len(path) >= 3 && path[1] == "wan" && path[2] == "static" {
		field := "all"
		if len(path) > 3 {
			field = path[3]
		}
		wanStaticDeleted(field)
	}
}

func configSiteIndexChanged(path []string, val string, expires *time.Time) {
	networkdStop("site_index changed - exiting to rebuild network")
}

func configNetworkChanged(path []string, val string, expires *time.Time) {
	var reload bool

	switch len(path) {
	case 2:
		reload = configSet(path[1], val)
	case 4:
		if path[1] == "vap" {
			hostapd.reload()
		} else if path[1] == "wan" && path[2] == "static" {
			wanStaticChanged(path[3], val)
		}
	}

	if reload {
		wifiEvaluate = true
		hostapd.reload()
	}
}