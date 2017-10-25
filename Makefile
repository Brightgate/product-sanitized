#
# COPYRIGHT 2017 Brightgate Inc. All rights reserved.
#
# This copyright notice is Copyright Management Information under 17 USC 1202
# and is included to protect this work and deter copyright infringement.
# Removal or alteration of this Copyright Management Information without the
# express written permission of Brightgate Inc is prohibited, and any
# such unauthorized removal or alteration will be a violation of federal law.
#

# 1. (a) On MacOS
#
#	 $ sudo brew install protobuf zmq
#	 [Retrieve and install Go pkg from golang.org]
#
#	 You will not be able to build the packages target on MacOS.
#
#    (b) On Ubuntu
#
#	 # apt-get install protobuf-compiler libzmq5-dev libpcap-dev vlan \
#		 bridge-utils lintian
#	 # pip3 install sh
#	 [Retrieve Go tar archive from golang.org and unpack in $HOME.]
#
#    (c) On Debian
#
#	 # apt-get install protobuf-compiler libzmq3-dev libpcap-dev vlan \
#		bridge-utils lintian
#	 # pip3 install sh
#	 [Retrieve Go tar archive from golang.org and unpack in $HOME.]
#
#    (d) on raspberry pi
#
#	 # apt-get install protobuf-compiler libzmq3-dev libpcap-dev vlan \
#		 bridge-utils lintian python3
#	 # pip3 install sh
#	 [Retrieve Go tar archive from golang.org and unpack in $HOME.]
#	 [Retrieve the TensorFlow C library from
#	  https://ph0.b10e.net/w/testing-raspberry-pi/ or
#	  https://ph0.b10e.net/w/testing-banana-pi/]
#
# 2. Each new shell,
#
#	 $ . ./env.sh
#
# 3. To clean out local binaries, use
#
#	 $ make plat-clobber
#
# 4. On x86_64, the build constructs all components, whether for appliance or
#    for cloud.  On ARM, only appliance components are built.

#
# OS definitions
#
UNAME_S = $(shell uname -s)
UNAME_M = $(shell uname -m)
$(info kernel UNAME_S=$(UNAME_S))

#
# Git related definitions
#
export GITROOT = $(shell git rev-parse --show-toplevel)
export GOPATH=$(GITROOT)/golang
GITHASH=$(shell git describe --always --long --dirty)
$(info GITHASH $(GITHASH))

#
# Go environment setup
#
ifeq ("$(GOROOT)","")
ifeq ("$(UNAME_S)","Darwin")
# On macOS, install the .pkg provided by golang.org.
export GOROOT=/usr/local/go
$(info operating-system macOS)
else
# On Linux
export GOROOT=$(HOME)/go
$(info operating-system Linux)
endif
endif

GO = $(GOROOT)/bin/go
GOFMT = $(GOROOT)/bin/gofmt
GO_CLEAN_FLAGS = -i -x
GO_GET_FLAGS = -v

$(info go-version $(shell $(GO) version))
$(info GOROOT $(GOROOT))
$(info GOPATH $(GOPATH))
GOOS = $(shell $(GO) env GOOS)
$(info GOOS = $(GOOS))
GOARCH = $(shell $(GO) env GOARCH)
$(info GOARCH = $(GOARCH))
GOSRC = golang/src
GOSRCBG = $(GOSRC)/bg
# Vendoring directory, where external deps are placed
GOSRCBGVENDOR = $(GOSRCBG)/vendor
# Where we stick build tools
GOBIN = golang/bin

GOVERFLAGS=-ldflags="-X main.ApVersion=$(GITHASH)"

#
# Miscellaneous environment setup
#
# Use "make PKG_LINT= packages" to skip lintian pass.
PKG_LINT = --lint

INSTALL = install
MKDIR = mkdir
RM = rm

PYTHON3 = python3
$(info python3-version $(PYTHON3) -> $(shell $(PYTHON3) -V))

PROTOC_PLUGINS = \
	$(GOPATH)/bin/protoc-gen-doc \
	$(GOPATH)/bin/protoc-gen-go

#
# ARCH dependent setup
# - Select proto area name
# - Select default target list
#
ifeq ("$(GOARCH)","amd64")
$(info --> Building appliance and cloud components for x86_64.)
ROOT=proto.$(UNAME_M)
PKG_DEB_ARCH=amd64
TARGETS=$(APPCOMPONENTS) $(CLOUDCOMPONENTS)
endif

ifeq ("$(GOARCH)","arm")
# UNAME_M will read armv7l on Raspbian and on Ubuntu for  Banana Pi.
# Both use armhf as the architecture for .deb files.
$(info --> Building appliance components for ARM.)
ROOT=proto.armv7l
PKG_DEB_ARCH=armhf
TARGETS=$(APPCOMPONENTS)
endif

# Appliance components and supporting definitions

APPROOT=$(ROOT)/appliance
APPBASE=$(APPROOT)/opt/com.brightgate
APPBIN=$(APPBASE)/bin
APPDOC=$(APPBASE)/share/doc
APPWEB=$(APPBASE)/share/web
# APPCSS
# APPJS
# APPHTML
APPETC=$(APPBASE)/etc
APPETCCROND=$(APPROOT)/etc/cron.d
APPETCRSYSLOGD=$(APPROOT)/etc/rsyslog.d
APPROOTLIB=$(APPROOT)/lib
APPVAR=$(APPBASE)/var
APPSSL=$(APPETC)/ssl
APPSPOOL=$(APPVAR)/spool
APPSPOOLANTIPHISH=$(APPVAR)/spool/antiphishing
APPRULES=$(APPETC)/filter.rules.d
APPMODEL=$(APPETC)/device_model

HTTPD_CLIENTWEB_DIR=$(APPVAR)/www/client-web
HTTPD_TEMPLATE_DIR=$(APPETC)/templates/ap.httpd
NETWORK_TEMPLATE_DIR=$(APPETC)/templates/ap.networkd

APPDAEMONS = \
	ap.brokerd \
	ap.configd \
	ap.dhcp4d \
	ap.dns4d \
	ap.httpd \
	ap.identifierd \
	ap.logd \
	ap.mcp \
	ap.networkd \
	ap.relayd \
	ap.watchd

APPCOMMANDS = \
	ap-arpspoof \
	ap-configctl \
	ap-ctl \
	ap-msgping \
	ap-ouisearch \
	ap-rpc \
	ap-start \
	ap-stats

APPBINARIES = $(APPCOMMANDS:%=$(APPBIN)/%) $(APPDAEMONS:%=$(APPBIN)/%)

# XXX Common configurations?

HTTPD_TEMPLATE_FILES = \
	connect_apple.html.got \
	stats.html.got

GO_TESTABLES = \
	bg/ap_common/apcfg \
	bg/ap_common/network

NETWORK_TEMPLATE_FILES = hostapd.conf.got

HTTPD_TEMPLATES = $(HTTPD_TEMPLATE_FILES:%=$(HTTPD_TEMPLATE_DIR)/%)
NETWORK_TEMPLATES = $(NETWORK_TEMPLATE_FILES:%=$(NETWORK_TEMPLATE_DIR)/%)
APPTEMPLATES = $(HTTPD_TEMPLATES) $(NETWORK_TEMPLATES)

FILTER_RULES = \
	$(APPRULES)/base.rules \
	$(APPRULES)/local.rules

APPCONFIGS = \
	$(APPETC)/ap_defaults.json \
	$(APPETC)/ap_identities.csv \
	$(APPETC)/ap_mfgid.json \
	$(APPETCCROND)/com-brightgate-appliance-cron \
	$(APPETCRSYSLOGD)/com-brightgate-rsyslog.conf \
	$(APPETC)/devices.json \
	$(APPETC)/mcp.json \
	$(APPETC)/oui.txt \
	$(APPETC)/prometheus.yml \
	$(APPROOTLIB)/systemd/system/ap.mcp.service \
	$(APPROOTLIB)/systemd/system/brightgate-appliance.service \
	$(APPSPOOLANTIPHISH)/example_blacklist.csv \
	$(APPSPOOLANTIPHISH)/whitelist.csv

APPDIRS = \
	$(APPBIN) \
	$(APPDOC) \
	$(APPETC) \
	$(APPETCCROND) \
	$(APPETCRSYSLOGD) \
	$(APPROOTLIB) \
	$(APPRULES) \
	$(APPSSL) \
	$(APPSPOOL) \
	$(APPVAR) \
	$(APPSPOOLANTIPHISH) \
	$(HTTPD_CLIENTWEB_DIR) \
	$(HTTPD_TEMPLATE_DIR) \
	$(NETWORK_TEMPLATE_DIR)

APPCOMPONENTS = \
	$(APPBINARIES) \
	$(APPCONFIGS) \
	$(APPDIRS) \
	$(APPMODEL) \
	$(APPTEMPLATES) \
	$(FILTER_RULES)

APP_COMMON_SRCS = \
	$(GOSRCBG)/ap_common/apcfg/apcfg.go \
	$(GOSRCBG)/ap_common/apcfg/events.go \
	$(GOSRCBG)/ap_common/aputil/aputil.go \
	$(GOSRCBG)/ap_common/broker/broker.go \
	$(GOSRCBG)/ap_common/mcp/mcp_client.go \
	$(GOSRCBG)/ap_common/network/network.go \
	$(GOSRCBG)/ap_common/watchd/watchd_client.go \
	$(GOSRCBG)/base_def/base_def.go \
	$(GOSRCBG)/base_msg/base_msg.pb.go

# Cloud components and supporting definitions.

CLOUDROOT=$(ROOT)/cloud
CLOUDBASE=$(CLOUDROOT)/opt/net.b10e
CLOUDBIN=$(CLOUDBASE)/bin
CLOUDETC=$(CLOUDBASE)/etc
CLOUDROOTLIB=$(CLOUDROOT)/lib
CLOUDVAR=$(CLOUDBASE)/var
CLOUDSPOOL=$(CLOUDVAR)/spool

CLOUDDAEMONS = \
	cl.httpd \
	cl.rpcd

CLOUDCOMMANDS =

CLOUDCONFIGS = \
	$(CLOUDROOTLIB)/systemd/system/cl.httpd.service \
	$(CLOUDROOTLIB)/systemd/system/cl.rpcd.service

CLOUDBINARIES = $(CLOUDCOMMANDS:%=$(CLOUDBIN)/%) $(CLOUDDAEMONS:%=$(CLOUDBIN)/%)

CLOUDDIRS = \
	$(CLOUDBIN) \
	$(CLOUDETC) \
	$(CLOUDROOTLIB) \
	$(CLOUDSPOOL) \
	$(CLOUDVAR)

CLOUDCOMPONENTS = $(CLOUDBINARIES) $(CLOUDCONFIGS) $(CLOUDDIRS)

CLOUD_COMMON_SRCS = \
    $(GOSRCBG)/cloud_rpc/cloud_rpc.pb.go

#

# -zcompress-level
#      Specify  which compression level to use on the compressor backend, when
#      building a package (default is 9 for gzip  and bzip2,  6  for  xz  and
#      lzma).   The accepted values are 0-9 with: 0 being mapped to compressor
#      none for gzip and 0 mapped to 1 for bzip2. Before dpkg 1.16.2  level  0
#      was equivalent to compressor none for all compressors.
#
# -Zcompress-type
#      Specify which compression type to use when building a package.  Allowed
#      values  are  gzip,  xz  (since  dpkg 1.15.6), bzip2 (deprecated), lzma
#      (since dpkg 1.14.0; deprecated), and none (default is xz).

install: $(TARGETS)

appliance: $(APPCOMPONENTS)

cloud: $(CLOUDCOMPONENTS)


packages: install
	$(PYTHON3) build/deb-pkg.py $(PKG_LINT) -a $(PKG_DEB_ARCH) -Z gzip -z 5

test: test-go

test-go: install
	go test $(GO_TESTABLES)

coverage: coverage-go

coverage-go: install
	go test -cover $(GO_TESTABLES)

docs: | $(PROTOC_PLUGINS)

$(APPDOC)/: base/base_msg.proto | $(PROTOC_PLUGINS) $(APPDOC)
	cd base && \
		protoc --plugin $(GOPATH)/bin \
		    --doc_out $(APPDOC) $(notdir $<)

# Installation of appliance configuration files

$(APPETC)/ap_defaults.json: $(GOSRCBG)/ap.configd/ap_defaults.json | $(APPETC)
	$(INSTALL) -m 0644 $< $(APPETC)

$(APPETC)/ap_identities.csv: ap_identities.csv | $(APPETC)
	$(INSTALL) -m 0644 $< $(APPETC)

$(APPETC)/ap_mfgid.json: ap_mfgid.json | $(APPETC)
	$(INSTALL) -m 0644 $< $(APPETC)

$(APPROOTLIB)/systemd/system/ap.mcp.service: ap.mcp.service | $(APPROOTLIB)/systemd/system
	$(INSTALL) -m 0644 $< $(APPROOTLIB)/systemd/system

$(APPROOTLIB)/systemd/system/brightgate-appliance.service: brightgate-appliance.service | $(APPROOTLIB)/systemd/system
	$(INSTALL) -m 0644 $< $(APPROOTLIB)/systemd/system

$(APPETC)/devices.json: $(GOSRCBG)/ap.configd/devices.json | $(APPETC)
	$(INSTALL) -m 0644 $< $(APPETC)

$(APPETC)/mcp.json: $(GOSRCBG)/ap.mcp/mcp.json | $(APPETC)
	$(INSTALL) -m 0644 $< $(APPETC)

$(APPETC)/oui.txt: | $(APPETC)
	cd $(APPETC) && curl -s -S -O http://standards-oui.ieee.org/oui.txt

$(APPETC)/datasources.json: datasources.json | $(APPETC)
	$(INSTALL) -m 0644 $< $(APPETC)

$(APPETC)/prometheus.yml: prometheus.yml | $(APPETC)
	$(INSTALL) -m 0644 $< $(APPETC)

$(APPETCCROND)/com-brightgate-appliance-cron: com-brightgate-appliance-cron | $(APPETCCROND)
	$(INSTALL) -m 0644 $< $(APPETCCROND)

$(APPETCRSYSLOGD)/com-brightgate-rsyslog.conf: $(GOSRCBG)/ap.watchd/com-brightgate-rsyslog.conf | $(APPETCRSYSLOGD)
	$(INSTALL) -m 0644 $< $(APPETCRSYSLOGD)

$(APPSPOOLANTIPHISH)/example_blacklist.csv: $(GOSRCBG)/data/phishtank/example_blacklist.csv | $(APPSPOOLANTIPHISH)
	$(INSTALL) -m 0644 $< $(APPSPOOLANTIPHISH)

$(APPSPOOLANTIPHISH)/whitelist.csv: $(GOSRCBG)/data/phishtank/whitelist.csv | $(APPSPOOLANTIPHISH)
	$(INSTALL) -m 0644 $< $(APPSPOOLANTIPHISH)

$(NETWORK_TEMPLATE_DIR)/%: $(GOSRCBG)/ap.networkd/% | $(APPETC)
	$(INSTALL) -m 0644 $< $(NETWORK_TEMPLATE_DIR)

$(HTTPD_TEMPLATE_DIR)/%: $(GOSRCBG)/ap.httpd/% | $(APPETC)
	$(INSTALL) -m 0644 $< $(HTTPD_TEMPLATE_DIR)

$(APPRULES)/%: $(GOSRCBG)/ap.networkd/% | $(APPRULES)
	$(INSTALL) -m 0644 $< $(APPRULES)

$(APPMODEL): $(GOSRCBG)/ap.identifierd/linear_model_deviceID/* | $(DIRS)
	$(MKDIR) -p $@
	cp -r $^ $@
	touch $@

$(APPROOTLIB)/systemd/system: | $(APPROOTLIB)
	mkdir -p $(APPROOTLIB)/systemd/system

$(APPDIRS):
	$(MKDIR) -p $@

COMMON_SRCS = \
	$(GOSRCBG)/base_def/base_def.go \
	$(GOSRCBG)/base_msg/base_msg.pb.go \
	$(GOSRCBG)/ap_common/broker/broker.go \
	$(GOSRCBG)/ap_common/apcfg/apcfg.go \
	$(GOSRCBG)/ap_common/aputil/aputil.go \
	$(GOSRCBG)/ap_common/mcp/mcp_client.go \
	$(GOSRCBG)/ap_common/network/network.go

PHISH_SRCS = \
	$(GOSRCBG)/data/phishtank/datasource.go \
	$(GOSRCBG)/data/phishtank/csv.go \
	$(GOSRCBG)/data/phishtank/remote.go \
	$(GOSRCBG)/data/phishtank/safebrowsing.go

$(APPBINARIES): $(APP_COMMON_SRCS) | $(APPBIN) deps-ensured

$(APPBIN)/ap-start: ap-start.sh
	$(INSTALL) -m 0755 $< $@

$(APPBIN)/%:
	GOBIN=$(realpath $(APPBIN)) $(GO) install $(GOVERFLAGS) bg/$*

$(APPBIN)/ap.brokerd: $(GOSRCBG)/ap.brokerd/brokerd.go
$(APPBIN)/ap.configd: \
	$(GOSRCBG)/ap.configd/configd.go \
	$(GOSRCBG)/ap.configd/devices.go \
	$(GOSRCBG)/ap.configd/upgrade_v1.go \
	$(GOSRCBG)/ap.configd/upgrade_v2.go \
	$(GOSRCBG)/ap.configd/upgrade_v4.go \
	$(GOSRCBG)/ap.configd/upgrade_v5.go
$(APPBIN)/ap.dhcp4d: $(GOSRCBG)/ap.dhcp4d/dhcp4d.go
$(APPBIN)/ap.dns4d: \
	$(GOSRCBG)/ap.dns4d/dns4d.go \
	$(PHISH_SRCS)
$(APPBIN)/ap.httpd: \
	$(GOSRCBG)/ap.httpd/ap.httpd.go \
	$(GOSRCBG)/ap.httpd/api-demo.go \
	$(PHISH_SRCS)
$(APPBIN)/ap.identifierd: $(GOSRCBG)/ap.identifierd/identifierd.go
$(APPBIN)/ap.logd: $(GOSRCBG)/ap.logd/logd.go
$(APPBIN)/ap.mcp: $(GOSRCBG)/ap.mcp/mcp.go
$(APPBIN)/ap.networkd: \
	$(GOSRCBG)/ap.networkd/filterd.go \
	$(GOSRCBG)/ap.networkd/networkd.go \
	$(GOSRCBG)/ap.networkd/parse.go
$(APPBIN)/ap.relayd: $(GOSRCBG)/ap.relayd/relayd.go
$(APPBIN)/ap.watchd: \
	$(GOSRCBG)/ap.watchd/api.go \
	$(GOSRCBG)/ap.watchd/droplog.go \
	$(GOSRCBG)/ap.watchd/metrics.go \
	$(GOSRCBG)/ap.watchd/sampler.go \
	$(GOSRCBG)/ap.watchd/scanner.go \
	$(GOSRCBG)/ap.watchd/watchd.go

$(APPBIN)/ap-arpspoof: $(GOSRCBG)/ap-arpspoof/arpspoof.go
$(APPBIN)/ap-configctl: $(GOSRCBG)/ap-configctl/configctl.go
$(APPBIN)/ap-ctl: $(GOSRCBG)/ap-ctl/ctl.go
$(APPBIN)/ap-msgping: $(GOSRCBG)/ap-msgping/msgping.go
$(APPBIN)/ap-ouisearch: $(GOSRCBG)/ap-ouisearch/ouisearch.go
$(APPBIN)/ap-rpc: \
	$(GOSRCBG)/ap-rpc/rpc.go \
	$(CLOUD_COMMON_SRCS)
$(APPBIN)/ap-stats: $(GOSRCBG)/ap-stats/stats.go

LOCAL_BINARIES=$(APPBINARIES:$(APPBIN)/%=$(GOPATH)/bin/%)

# Cloud components

# Installation of cloud configuration files

$(CLOUDETC)/datasources.json: datasources.json | $(CLOUDETC)
	$(INSTALL) -m 0644 $< $(CLOUDETC)

$(CLOUDROOTLIB)/systemd/system/cl.httpd.service: cl.httpd.service | $(CLOUDROOTLIB)/systemd/system
	$(INSTALL) -m 0644 $< $(CLOUDROOTLIB)/systemd/system

$(CLOUDROOTLIB)/systemd/system/cl.rpcd.service: cl.rpcd.service | $(CLOUDROOTLIB)/systemd/system
	$(INSTALL) -m 0644 $< $(CLOUDROOTLIB)/systemd/system

$(CLOUDBINARIES): $(COMMON_SRCS) | deps-ensured

$(CLOUDBIN)/%: | $(CLOUDBIN)
	GOBIN=$(realpath $(CLOUDBIN)) $(GO) install $(GOVERFLAGS) bg/$*

$(CLOUDBIN)/cl.httpd: $(GOSRCBG)/cl.httpd/cl.httpd.go
$(CLOUDBIN)/cl.rpcd: \
	$(GOSRCBG)/cl.rpcd/rpcd.go \
	$(CLOUD_COMMON_SRCS)

$(CLOUDROOTLIB)/systemd/system: | $(CLOUDROOTLIB)
	mkdir -p $(CLOUDROOTLIB)/systemd/system

$(CLOUDDIRS):
	$(MKDIR) -p $@

# Common definitions

$(GOSRCBG)/base_def/base_def.go: base/generate-base-def.py | $(GOSRCBG)/base_def
	$(PYTHON3) $< --go | $(GOFMT) > $@

base/base_def.py: base/generate-base-def.py
	$(PYTHON3) $< --python3 > $@

$(GOSRCBG)/base_def:
	$(MKDIR) -p $(GOSRCBG)/base_def

# Protocol buffers

$(GOSRCBG)/base_msg/base_msg.pb.go: base/base_msg.proto | \
	$(PROTOC_PLUGINS) $(GOSRCBG)/base_msg
	cd base && \
		protoc --plugin $(GOPATH)/bin \
		    --go_out ../$(GOSRCBG)/base_msg $(notdir $<)

base/base_msg_pb2.py: base/base_msg.proto
	protoc --python_out . $<

$(GOSRCBG)/base_msg:
	$(MKDIR) -p $(GOSRCBG)/base_msg

$(GOSRCBG)/cloud_rpc/cloud_rpc.pb.go: base/cloud_rpc.proto | \
	$(PROTOC_PLUGINS) $(GOSRCBG)/cloud_rpc
	cd base && \
		protoc --plugin $(GOPATH)/bin \
			-I/usr/local/include \
			-I . \
			-I$(GOPATH)/src \
			-I$(GOPATH)/src/github.com/golang/protobuf/protoc-gen-go/descriptor \
			--go_out=plugins=grpc,Mbase_msg.proto=bg/base_msg:../$(GOSRCBG)/cloud_rpc \
			$(notdir $<)

base/cloud_rpc_pb2.py: base/cloud_rpc.proto
	python3 -m grpc_tools.protoc \
		-I. \
		-Ibase \
		--python_out=. --grpc_python_out=. $<

$(GOSRCBG)/cloud_rpc:
	mkdir -p golang/src/cloud_rpc

$(PROTOC_PLUGINS):
	$(GO) get -u github.com/golang/protobuf/proto
	$(GO) get -u github.com/golang/protobuf/protoc-gen-go
	$(GO) get -u sourcegraph.com/sourcegraph/prototools/cmd/protoc-gen-doc

LOCAL_COMMANDS=$(COMMANDS:$(APPBIN)/%=$(GOPATH)/bin/%)
LOCAL_DAEMONS=$(DAEMONS:$(APPBIN)/%=$(GOPATH)/bin/%)

#
# Go Dependencies: Pull in definitions for 'dep'
#
include Makefile.godeps

NPM = npm
client-web/.npm-installed: client-web/package.json
	(cd client-web && $(NPM) install)
	touch $@

client-web: client-web/.npm-installed FRC | $(HTTPD_CLIENTWEB_DIR)
	$(RM) -fr $(HTTPD_CLIENTWEB_DIR)/*
	(cd client-web && $(NPM) run build)
	tar -C client-web/dist -c -f - . | tar -C $(HTTPD_CLIENTWEB_DIR) -xvf -

FRC:

clobber: clean clobber-packages clobber-godeps
	$(RM) -fr $(ROOT)
	$(RM) -fr $(GOSRC)/pkg
	$(RM) -fr $(GOSRC)/bin

clobber-packages:
	-$(RM) -fr bg-appliance_*.*.*-*_* bg-cloud_*.*.*-*_*

clean:
	$(RM) -f \
		base/base_def.py \
		base/base_msg_pb2.py \
		base/cloud_rpc_pb2.py \
		$(GOSRCBG)/base_def/base_def.go \
		$(GOSRCBG)/base_msg/base_msg.pb.go \
		$(GOSRCBG)/cloud_rpc/cloud_rpc.pb.go

plat-clobber: clobber
	-$(GO) clean $(GO_CLEAN_FLAGS) github.com/golang/protobuf/protoc-gen-go
	-$(GO) clean $(GO_CLEAN_FLAGS) github.com/golang/protobuf/proto
	-$(GO) clean $(GO_CLEAN_FLAGS) sourcegraph.com/sourcegraph/prototools/cmd/protoc-gen-doc
	-$(RM) -fr golang/src/github.com golang/src/golang.org golang/src/google.golang.org golang/src/sourcegraph.com
