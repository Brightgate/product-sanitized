#!/bin/bash -px
#
# COPYRIGHT 2019 Brightgate Inc.  All rights reserved.
#
# This copyright notice is Copyright Management Information under 17 USC 1202
# and is included to protect this work and deter copyright infringement.
# Removal or alteration of this Copyright Management Information without the
# express written permission of Brightgate Inc is prohibited, and any
# such unauthorized removal or alteration will be a violation of federal law.
#

# Run through complete operations of cl-obs.
#
# A typical run, using cloud data:
#
# $ make
# $ export GOOGLE_APPLICATION_CREDENTIALS=/path/to/creds
# $ bash golang/src/bg/test-cl-obs.bash cloud ingest

set -o errexit

CL_OBS=./proto.x86_64/cloud/opt/net.b10e/bin/cl-obs

FACTS=./golang/src/bg/cl-obs/facts.sqlite3
OBSERVATIONS=./golang/src/bg/cl-obs/observations.db
TRAINED_MODELS=./golang/src/bg/cl-obs/trained-models.db

function orun {
	echo "###" "$@"
	time "$@"
}

FILES_SRC=${FILES_SRC:-/home/stephen/staging-spools/svc-1/spool}
CLOUD_SRC=${CLOUD_SRC:-staging-168518}

if [ "$1" == "files" ]; then
	export CL_SRC="--dir $FILES_SRC"
	OBSERVATIONS=./golang/src/bg/cl-obs/observations-files.db
	TRAINED_MODELS=./golang/src/bg/cl-obs/trained-models-files.db
elif [ "$1" == "cloud" ]; then
	export CL_SRC="--project $CLOUD_SRC"

	if [ -z "$GOOGLE_APPLICATION_CREDENTIALS" ]; then
		echo "must define GOOGLE_APPLICATION_CREDENTIALS for cloud runs"
		exit 2
	fi
else
	echo "need input source ('cloud' or 'files')"
	exit 2
fi
echo "### CL_SRC is '$CL_SRC'"

if [ "$2" == "ingest" ]; then
	$CL_OBS ingest --cpuprofile=ingest-1.prof \
		$CL_SRC \
		--observations-file=$OBSERVATIONS
fi

# Re-apply facts
sqlite3 "$OBSERVATIONS" < "$FACTS"

# Extract options

orun $CL_OBS extract --dhcp \
	$CL_SRC \
	--observations-file $OBSERVATIONS
orun $CL_OBS extract --dns  \
	$CL_SRC \
	--observations-file $OBSERVATIONS
orun $CL_OBS extract --mfg  \
	$CL_SRC \
	--observations-file $OBSERVATIONS
orun $CL_OBS extract --device  \
	$CL_SRC \
	--observations-file $OBSERVATIONS

# Site options

orun $CL_OBS site \
	--observations-file $OBSERVATIONS
orun $CL_OBS site --verbose \
	--observations-file $OBSERVATIONS

orun $CL_OBS ls \
	$CL_SRC \
	--observations-file $OBSERVATIONS \
	"00:11:d9:95:3d:b2"

# Train and review
#   Output to the combined models file.
orun $CL_OBS train --cpuprofile=train-0.prof \
	$CL_SRC \
	--model-file $TRAINED_MODELS \
	--observations-file $OBSERVATIONS

#   Review the training set for validity and redundancy.
orun $CL_OBS review \
	$CL_SRC \
	--model-file $TRAINED_MODELS \
	--observations-file $OBSERVATIONS

# Classify
#   Use the combined models file.
orun $CL_OBS classify \
	--model-file $TRAINED_MODELS \
	--observations-file $OBSERVATIONS \
	cc4f2549-5e64-4710-b63b-64ab5558aeee

# Classify all known sites.
$CL_OBS classify \
	--persist \
	--model-file $TRAINED_MODELS \
	--observations-file $OBSERVATIONS \
	'*'

# Site, with predictions.
orun $CL_OBS site \
	--verbose \
	$CL_SRC \
	--model-file $TRAINED_MODELS \
	--observations-file $OBSERVATIONS \
	dogfood-mt7623

orun $CL_OBS site \
	--verbose \
	$CL_SRC \
	--model-file $TRAINED_MODELS \
	--observations-file $OBSERVATIONS \
	stephen-osage
