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
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"bg/ap_common/netctl"
	"bg/base_def"
	"bg/common/cfgapi"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	vpnPublicProp  = "@/network/vpn/public_key"
	vpnPrivateProp = "@/network/vpn/private_key"
	vpnPortProp    = "@/network/vpn/port"

	vpnNic = "wg0"
)

type vpnKeyInfo struct {
	publicKey  *wgtypes.Key
	allowedIPs string
	assignedIP string
}

var (
	vpnInfo struct {
		publicKey  *wgtypes.Key
		privateKey *wgtypes.Key
		listenPort int

		keys map[string]*vpnKeyInfo
		sync.Mutex

		updated chan bool
	}
)

func vpnKeySet(name, key string) *wgtypes.Key {
	var rval *wgtypes.Key

	parsed, err := wgtypes.ParseKey(key)
	if err != nil {
		slog.Infof("invalid %s key %s: %v", key, err)
	} else {
		rval = &parsed
	}

	return rval
}

func vpnUpdateUser(path []string, val string, expires *time.Time) {
	var updated bool

	mac := path[3]
	field := path[4]

	slog.Debugf("updating %s for %s", strings.Join(path, "/"), field)
	vpnInfo.Lock()

	key := vpnInfo.keys[mac]
	if key == nil {
		key = &vpnKeyInfo{}
		vpnInfo.keys[mac] = key
	}

	switch field {
	case "assigned_ip":
		key.assignedIP = val + "/32"
		updated = true

	case "allowed_ips":
		key.allowedIPs = val
		updated = true

	case "public_key":
		key.publicKey = vpnKeySet("user public", val)
		updated = true
	}
	vpnInfo.Unlock()

	vpnInfo.updated <- updated
}

func vpnDeleteUser(path []string) {
	var updated bool

	mac := path[3]

	vpnInfo.Lock()
	if key, ok := vpnInfo.keys[mac]; ok {
		updated = true
		if len(path) == 4 {
			delete(vpnInfo.keys, mac)
		} else if path[4] == "public_key" {
			key.publicKey = nil
		} else if path[4] == "allowed_ips" {
			key.allowedIPs = ""
		} else if path[4] == "assigned_ip" {
			key.assignedIP = ""
		}
	}
	vpnInfo.Unlock()
	vpnInfo.updated <- updated
}

func vpnUpdate(path []string, val string, expires *time.Time) {
	var updated bool

	vpnInfo.Lock()

	if len(path) == 3 {
		switch path[2] {
		case "public_key":
			vpnInfo.publicKey = vpnKeySet("system public", val)
		case "private_key":
			vpnInfo.privateKey = vpnKeySet("system private", val)
		case "port":
			var err error

			vpnInfo.listenPort, err = strconv.Atoi(val)
			if err != nil {
				slog.Warn("invalid vpn port %s: %v", val, err)
			}
		}
	}
	vpnInfo.Unlock()

	vpnInfo.updated <- updated
}

func vpnDelete(path []string) {
	vpnInfo.Lock()
	vpnInfo.privateKey = nil
	vpnInfo.publicKey = nil
	vpnInfo.listenPort = 0
	vpnInfo.Unlock()

	vpnInfo.updated <- true
}

// using the information already pulled from the config tree, generate a
// wireguard config.
func vpnReconfig() {
	vpnInfo.Lock()
	defer vpnInfo.Unlock()

	if vpnInfo.privateKey == nil || vpnInfo.listenPort == 0 {
		slog.Infof("vpn configuration incomplete")
		return
	}

	peers := make([]wgtypes.PeerConfig, 0)
	for _, key := range vpnInfo.keys {
		if key.publicKey == nil || key.assignedIP == "" {
			continue
		}
		_, ipnet, _ := net.ParseCIDR(key.assignedIP)
		if ipnet == nil {
			continue
		}

		peer := wgtypes.PeerConfig{
			PublicKey:  *key.publicKey,
			AllowedIPs: []net.IPNet{*ipnet},
		}
		peers = append(peers, peer)
	}

	client, err := wgctrl.New()
	if err != nil {
		slog.Errorf("creating wgctrl client: %v", err)
		return
	}
	defer client.Close()

	c := wgtypes.Config{
		PrivateKey:   vpnInfo.privateKey,
		ListenPort:   &vpnInfo.listenPort,
		ReplacePeers: true,
		Peers:        peers,
	}

	slog.Debugf("applying wireguard config: %v", c)
	if err = client.ConfigureDevice(vpnNic, c); err != nil {
		slog.Errorf("configuring %s: %v", vpnNic, err)
	}
}

// After any change is made to the user- or system-level vpn configuration,
// regenerate the wireguard configuration.
func vpnLoop(wg *sync.WaitGroup, doneChan chan bool) {
	defer wg.Done()

	done := false
	updateNeeded := true
	for !done {
		if updateNeeded {
			vpnReconfig()
		}

		select {
		case done = <-doneChan:
		case updateNeeded = <-vpnInfo.updated:
		}
	}

	_ = netctl.LinkDelete(vpnNic)
}

// Load the per-user vpn configuration from the config tree, and insert it into
// our user-indexed map
func vpnUserInit() {
	vpnInfo.keys = make(map[string]*vpnKeyInfo)
	vpnInfo.updated = make(chan bool, 4)

	for _, user := range config.GetUsers() {
		for _, key := range user.WGConfig {
			if key.WGAssignedIP == "" || key.WGPublicKey == "" {
				slog.Warnf("skipping incomplete vpn key %q",
					key.GetMac())
			}
			public := vpnKeySet("user public", key.WGPublicKey)
			vpnInfo.keys[key.GetMac()] = &vpnKeyInfo{
				publicKey:  public,
				assignedIP: key.WGAssignedIP + "/32",
				allowedIPs: key.WGAllowedIPs,
			}
		}
	}
}

// Look up a single property.  It's OK for the property not to exist.  Any other
// error should be returned.
func getStr(p string) (string, error) {
	v, err := config.GetProp(p)
	if err != nil && err != cfgapi.ErrNoProp {
		return "", fmt.Errorf("fetching %s: %v", p, err)
	}

	return v, nil
}

// Attempt to pull the system-level vpn configuration from the config tree.  If
// it doesn't exist, create it and insert into the tree.
func vpnSystemInit() error {
	var err error
	var public, private, port string

	// XXX: First look for private key in a file.  If it's not there, then
	// the config tree.  If all else fails, generate a new one.  When
	// generating a new key, insert it into the config tree.  It can be
	// removed after being escrowed.

	if public, err = getStr(vpnPublicProp); err == nil {
		if private, err = getStr(vpnPrivateProp); err == nil {
			port, err = getStr(vpnPortProp)
		}
	}

	if err != nil {
		return err
	}

	if public == "" || private == "" {
		if public == "" && private == "" {
			slog.Infof("generating initial wireguard config")
		} else {
			slog.Infof("replacing incomplete wireguard config")
		}

		newPrivate, err := wgtypes.GeneratePrivateKey()
		if err != nil {
			slog.Warnf("generating wireguard private key: %v", err)
			return err
		}

		private = newPrivate.String()
		config.CreateProp(vpnPrivateProp, private, nil)

		public = newPrivate.PublicKey().String()
		config.CreateProp(vpnPublicProp, public, nil)
	}

	if port == "" {
		port = "51820"
		config.CreateProp(vpnPortProp, port, nil)
	}

	vpnInfo.publicKey = vpnKeySet("system public", public)
	vpnInfo.privateKey = vpnKeySet("system private", private)
	vpnInfo.listenPort, err = strconv.Atoi(port)
	if err != nil {
		slog.Warnf("invalid vpn listen port: %s", port)
	}

	return err
}

func vpnInit() error {
	slog.Infof("vpninit")

	ring, ok := rings[base_def.RING_VPN]
	if !ok {
		return fmt.Errorf("vpn ring is undefined")
	}

	err := netctl.LinkDelete(vpnNic)
	if err != nil && err != netctl.ErrNoDevice {
		slog.Warnf("LinkDelete(%s) failed: %v", vpnNic, err)
	}
	if err = netctl.LinkAddWireguard(vpnNic); err != nil {
		return fmt.Errorf("creating %s: %v", vpnNic, err)
	}

	plumbBridge(ring, vpnNic)

	vpnUserInit()
	return vpnSystemInit()
}
