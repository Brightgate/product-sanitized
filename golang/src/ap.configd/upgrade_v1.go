/*
 * COPYRIGHT 2017 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 */

package main

import (
	"log"
)

func upgradeV1() error {
	log.Printf("Adding @/apversion property\n")
	property_update("@/apversion", ApVersion, nil, true)
	return nil
}

func init() {
	addUpgradeHook(1, upgradeV1)
}