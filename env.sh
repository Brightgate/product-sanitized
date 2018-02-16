#!/bin/bash -p
#
# COPYRIGHT 2017 Brightgate Inc. All rights reserved.
#
# This copyright notice is Copyright Management Information under 17 USC 1202
# and is included to protect this work and deter copyright infringement.
# Removal or alteration of this Copyright Management Information without the
# express written permission of Brightgate Inc is prohibited, and any
# such unauthorized removal or alteration will be a violation of federal law.
#

if [[ -d /opt/net.b10e/go ]]; then
	export GOROOT=/opt/net.b10e/go
elif [[ -z $GOROOT ]]; then
	export GOROOT=$HOME/go
fi
export GOPATH=$(pwd)/golang
export PATH=$GOPATH/bin:$GOROOT/bin:$PATH
