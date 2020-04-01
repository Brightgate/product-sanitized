/*
 * COPYRIGHT 2020 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 */

// A lookup classifier for the manufacturer of the hardware Ethernet
// interface, based on the IEEE OUI database.
package main

import "bg/cl-obs/defs"

func initInterfaceMfgLookupClassifier() lookupClassifier {
	return lookupClassifier{
		name:               "lookup-mfg",
		level:              productionClassifier,
		certainAbove:       0.9,
		uncertainBelow:     0.5,
		unknownValue:       defs.UnknownMfg,
		classificationProp: "oui_mfg",
		TargetValue:        lookupInterfaceMfgTargetValue,
	}
}

func lookupInterfaceMfgTargetValue(rdi RecordedDevice) string {
	return ""
}

func trainInterfaceMfgLookupClassifier(B *backdrop) {
	ims := initInterfaceMfgLookupClassifier()

	ims.train(B)
}
