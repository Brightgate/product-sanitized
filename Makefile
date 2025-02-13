#
# Copyright 2020 Brightgate Inc.
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
#


# 1. (a) On MacOS
#
#	 $ sudo brew install protobuf
#	 [Retrieve and install Go pkg from golang.org]
#
#	 You will not be able to build the packages target on MacOS.
#
#    (b) On Debian/Ubuntu
#
#	 $ make tools
#	 [Follow the directions]
#	 [If in the Brightgate cloud, all tools should be installed.  Else,
#	  follow the directions at
#	  https://ph0.b10e.net/w/testing-raspberry-pi/#installing-prerequisite
#	  (and modify from ARM as necessary) to install other build requirements]
#
#    (c) On Raspberry Pi
#
#	 $ make tools
#	 [Follow the directions]
#	 [Follow the directions at
#	  https://ph0.b10e.net/w/testing-raspberry-pi/#installing-prerequisite
#	  to install other build requirements]
#
# 2. To clean out local binaries, use
#
#	 $ make clobber
#
# 3. On x86_64, the build constructs all components, whether for appliance or
#    for cloud.  On ARM, only appliance components are built.
#
# 4. The DISTRO variable dictates which form of packages are built.  For
#    x86_64, the default of "debian" is sufficient.  For ARM, OpenWrt (ipk
#    packages) and Raspbian (deb packages) are supported distros.  These can be
#    built, respectively, by the invocations
#
#	$ make packages DISTRO=openwrt GOARCH=arm
#	$ make packages DISTRO=debian GOARCH=arm

#
# OS definitions
#
# note: These are constants; := avoids repeated shell invocations
UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)

#
# Git related definitions
#
# note: These are constants; := avoids repeated shell invocations
GITROOT := $(shell git rev-parse --show-toplevel)
GITHASH := $(shell git describe --always --long --dirty)
GITHASH_FULL := $(shell git rev-parse HEAD)
# Needs deferred expansion, can't use :=
GITCHANGED = $(shell grep -s -q '"$(GITHASH)"' $(GOSRCBG)/common/version.go || echo FRC)

# GO111MODULE defaults to "auto", which is "on" when you're in a directory with
# a go.mod file and not in a directory beneath $GOPATH.  We turn it on
# explicitly because we will be in a subdirectory of $GOPATH, and we need to
# set the latter because that's the only way to tell go where to put the module
# cache.
export GOPATH=$(GITROOT)/golang
export GO111MODULE=on

export GOPROXY=http://build0.b10e.net:8888/

#
# Go environment setup
#
ifeq ("$(GOROOT)","")
ifeq ("$(UNAME_S)","Darwin")
# On macOS, install the .pkg provided by golang.org.
export GOROOT=/usr/local/go
GOROOT_SEARCH += /usr/local/go
ifeq ($(MAKE_RESTARTS),)
$(info operating-system macOS)
endif
else
# On Linux
export GOROOT=$(wildcard /opt/net.b10e/go-1.14.6)
GOROOT_SEARCH += /opt/net.b10e/go-1.14.6
ifeq ("$(GOROOT)","")
export GOROOT=$(HOME)/go
GOROOT_SEARCH += $(HOME)/go
endif
ifeq ($(MAKE_RESTARTS),)
$(info operating-system Linux)
endif
endif
endif

export PATH=$(GOPATH)/bin:$(GOROOT)/bin:$(shell echo $$PATH)

GO = $(GOROOT)/bin/go
GOFMT = $(GOROOT)/bin/gofmt
GOLINT = $(GOROOT)/bin/golint
GO_CLEAN_FLAGS = -i -x
GO_GET_FLAGS = -v
# Add -trimpath when upgrading to 1.13
# Add -v (et al) by setting GOFLAGS=-v
GO_MOD_FLAG = -mod=readonly
GO_BUILD_FLAGS = $(GO_MOD_FLAG)

ifeq ("$(wildcard $(GO))","")
ifeq ("$(findstring $(origin GOROOT), "command line|environment")","")
$(error go does not exist in any known $$GOROOT: $(GOROOT_SEARCH))
else
$(error go does not exist at specified $$GOROOT: $(GOROOT))
endif
endif

# note: These are constants; := avoids repeated invocation of shell
GOOS := $(shell GOROOT=$(GOROOT) $(GO) env GOOS)
GOARCH := $(shell GOROOT=$(GOROOT) $(GO) env GOARCH)
GOHOSTARCH := $(shell GOROOT=$(GOROOT) $(GO) env GOHOSTARCH)
GOVERSION := $(shell GOROOT=$(GOROOT) $(GO) version)
GOVERSION_STAMP := $(shell GOROOT=$(GOROOT) $(GO) version | awk '{print $$3}')

GOWS = golang
GOSRCBG = $(GOWS)/src/bg
# Where we stick build tools
GOBIN = $(GOPATH)/bin

BGDEPDIR = $(GOTOOLS_DIR)/bgdepstamps

#
# Miscellaneous environment setup
#
INSTALL = install
ifeq ("$(UNAME_S)","Darwin")
SHA256SUM = shasum -a 256
else
SHA256SUM = sha256sum
endif
MKDIR = mkdir
RM = rm

DOWNLOAD_CACHEDIR ?= $(GITROOT)/../download-cache

NODE = node
NODEVERSION = $(shell $(NODE) --version)

# Python3 installation.  Use only the first 8 chars of the SHA256 sum because
# we can wind up with extremely long pathnames otherwise, potentially breaking
# shell scripts with large #! lines; see Linux's BINPRM_BUF_SIZE.
VENV_NAME := _venv.$(shell $(SHA256SUM) build/requirements.txt | awk '{print substr($$1,1,8)}')
VENV_INSTALLED := $(VENV_NAME)/.installed
HOSTPYTHON3 = python3
PYTHON3VERSION = $(shell $(HOSTPYTHON3) -V)
PYTHON3 = $(VENV_NAME)/bin/python3

#
# ARCH dependent setup
# - Select proto area name
# - Select default target list
#
ifeq ("$(GOARCH)","amd64")
ROOT=proto.$(UNAME_M)
PKG_DEB_ARCH=amd64
TARGETS=appliance cloud
endif

ifeq ("$(GOARCH)","arm")
# UNAME_M will read armv7l on Raspbian and on Ubuntu for Banana Pi.
# Both use armhf as the architecture for .deb files.
ROOT=proto.armv7l
PKG_DEB_ARCH=armhf
TARGETS=appliance
endif

#
# Cross compilation setup
#

DISTRO = debian
PKG = deb

# Break if undefined and cross compiling.
ifneq ($(GOHOSTARCH),$(GOARCH))
ifeq ($(GOARCH),arm)
# Put the ARM proto area in a distro-specific place when cross-compiling.
ROOT = proto.armv7l.$(DISTRO)
ifeq ("$(DISTRO)","debian")
SYSROOT_CFG = build/cross-compile/raspbian-stretch.multistrap
SYSROOT_CFG_LOCAL = $(subst build/cross-compile/,,$(SYSROOT_CFG))
SYSROOT = build/cross-compile/sysroot.$(GOARCH).$(SYSROOT_SUM)
BUILDTOOLS_CROSS = crossbuild-essential-armhf
CROSS_CC = /usr/bin/arm-linux-gnueabihf-gcc
CROSS_CXX = /usr/bin/arm-linux-gnueabihf-g++
CROSS_CGO_LDFLAGS = --sysroot $(CROSS_SYSROOT) -Lusr/local/lib -Lusr/lib
CROSS_CGO_CFLAGS = --sysroot $(CROSS_SYSROOT) -Iusr/local/include -Iusr/include

# This is the checksum of the sysroot blob to be used; it's outside the
# cross-compile conditional because the build-sysroot target uses this to see
# whether the sysroot has changed and needs to be re-uploaded.
SYSROOT_SUM=41c3448d0fc9f58e9fcef5be5384482d7b6fe4269ffd74041b72a0f15b4fed27
SYSROOT_LOCAL_FLAGS = -f $(SYSROOT_CFG_LOCAL)

DISTRO_OTHER = openwrt
else
ifeq ("$(DISTRO)","openwrt")
CROSS_CC = $(CROSS_SYSROOT)/../toolchain.$(DISTRO)/bin/arm-openwrt-linux-gcc
CROSS_CXX = $(CROSS_SYSROOT)/../toolchain.$(DISTRO)/bin/arm-openwrt-linux-g++
CROSS_CGO_LDFLAGS = --sysroot $(CROSS_SYSROOT)
CROSS_CGO_CFLAGS = --sysroot $(CROSS_SYSROOT) -I$(CROSS_SYSROOT)/usr/include

SYSROOT = build/cross-compile/sysroot.$(DISTRO).$(SYSROOT_SUM)
SYSROOT_SUM_arm_openwrt=b0f06cb0486a7ffceb575f4b0ec569c5cc85a2af
SYSROOT_SUM=$(SYSROOT_SUM_arm_openwrt)
SYSROOT_LOCAL_FLAGS =

DISTRO_OTHER = debian
PKG = ipk
else
$(error DISTRO must be set to 'openwrt' or 'debian' [deprecated] for cross)
endif
endif

# The command used to build the sysroot.
BUILD_SYSROOT_CMD = \
	cd build/cross-compile && \
	DISTRO=$(DISTRO) SYSROOT=$(SYSROOT) SYSROOT_SUM=$(SYSROOT_SUM) \
	    GCS_KEY_FILE=$(GCS_KEY_SYSROOT) \
	    ./build-multistrap-sysroot.sh $(SYSROOT_LOCAL_FLAGS)
CROSS_SYSROOT = $(shell realpath -m $(SYSROOT))
SYSROOT_BLOB_NAME = $(shell $(BUILD_SYSROOT_CMD) name)
else
$(error 'arm' is the only supported cross target)
endif

# SYSROOT doesn't work right if isn't an absolute path.  We need to use the
# external realpath so that we can tell it to ignore the possibility that the
# path and its components don't exist.

CROSS_ENV = \
	export \
	SYSROOT=$(CROSS_SYSROOT) \
	STAGING_DIR=$(CROSS_SYSROOT) \
	CC="$(CROSS_CC) -DSTAGING_DIR=$(CROSS_SYSROOT)" \
	CXX="$(CROSS_CXX) -DSTAGING_DIR=$(CROSS_SYSROOT)" \
	CGO_LDFLAGS="$(CROSS_CGO_LDFLAGS)" \
	CGO_CFLAGS="$(CROSS_CGO_CFLAGS)" \
	CGO_ENABLED=1 &&
CROSS_DEP = $(SYSROOT)/.$(SYSROOT_SUM)
endif

DISTRODIR_debian = build/debian-deb
DISTRODIR_openwrt = build/openwrt-ipk
DISTRODIR = $(DISTRODIR_$(DISTRO))

BUILDTOOLS = \
	$(BUILDTOOLS_CROSS) \
	protobuf-compiler \
	libprotobuf-dev \
	libpcap-dev \
	lintian \
	pngquant \
	tidy \
	python3 \
	python3-pip \
	mercurial

#
# Announce some things about the build
#
define report
#        TARGETS: $(TARGETS)
#         DISTRO: $(DISTRO)
#         KERNEL: UNAME_S=$(UNAME_S) UNAME_M=$(UNAME_M)
#        GITHASH: $(GITHASH)
#             GO: $(GO)
#      GOVERSION: $(GOVERSION)
#         GOROOT: $(GOROOT)
#         GOPATH: $(GOPATH)
#           GOOS: $(GOOS)
#     GOHOSTARCH: $(GOHOSTARCH)
#         GOARCH: $(GOARCH)
# PYTHON3VERSION: $(PYTHON3VERSION)
#    NODEVERSION: $(NODEVERSION)
endef
ifeq ($(MAKE_RESTARTS),)
$(info $(report))
endif
undefine report
ifneq ($(GOHOSTARCH),$(GOARCH))
define report
#     CROSSBUILD: $(GOHOSTARCH) -> $(GOARCH)
#        SYSROOT: $(SYSROOT)
#  CROSS_SYSROOT: $(CROSS_SYSROOT)
#    SYSROOT_SUM: $(SYSROOT_SUM)
endef
ifeq ($(MAKE_RESTARTS),)
$(info $(report))
endif
undefine report
endif

#
# Appliance components and supporting definitions
#

APPROOT=$(ROOT)/appliance
APPBASE=$(APPROOT)/opt/com.brightgate
APPBIN=$(APPBASE)/bin
APPSNMAP=$(APPBASE)/share/nmap/scripts
# APPCSS
# APPJS
# APPHTML
APPETC=$(APPBASE)/etc
APPROOTLIB=$(APPROOT)/lib
APPVAR=$(APPBASE)/var
APPSECRET=$(APPDATA)/secret
APPSECRETRPCD=$(APPSECRET)/rpcd
APPSECRETSSL=$(APPSECRET)/ssl
APPDATAANTIPHISH=$(APPDATA)/antiphishing
APPDATACONFIGD=$(APPDATA)/configd
APPDATAIDENTIFIERD=$(APPDATA)/identifierd
APPDATALOGD=$(APPDATA)/logd
APPDATAMCP=$(APPDATA)/mcp
APPDATAPASSWORDS=$(APPDATA)/defaultpass
APPDATARPCD=$(APPDATA)/rpcd
APPDATAWATCHD=$(APPDATA)/watchd
APPETCIDENTIFIERD=$(APPETC)/identifierd
APPMODEL=$(APPETCIDENTIFIERD)/device_model
APPRULES=$(APPETC)/filter.rules.d

APPDATA_debian=$(APPVAR)/spool
APPDATA_openwrt=$(APPROOT)/data

APPDATA=$(APPDATA_$(DISTRO))

ROOTETC=$(APPROOT)/etc
ROOTETCCHRONY=$(ROOTETC)/chrony
ROOTETCCRONTABS=$(ROOTETC)/crontabs
ROOTETCINITD=$(ROOTETC)/init.d
ROOTETCIPTABLES=$(ROOTETC)/iptables
ROOTETCLOGROTATED=$(ROOTETC)/logrotate.d
ROOTETCRSYSLOGD=$(ROOTETC)/rsyslog.d
ROOTETCSYSCTLD=$(ROOTETC)/sysctl.d

HTTPD_CLIENTWEB_DIR=$(APPVAR)/www/client-web
NETWORKD_TEMPLATE_DIR=$(APPETC)/templates/ap.networkd
RPCD_TEMPLATE_DIR=$(APPETC)/templates/ap.rpcd
WIFID_TEMPLATE_DIR=$(APPETC)/templates/ap.wifid

COMMON_GOPKGS = \
	bg/common/...

APPCOMMON_GOPKGS = \
	$(COMMON_GOPKGS) \
	bg/ap_common/...

APPCOMMAND_GOPKGS = \
	bg/ap-defaultpass \
	bg/ap-diag \
	bg/ap-inspect \
	bg/ap-factory \
	bg/ap-tools \
	bg/ap-vuln-aggregate

APPDAEMON_GOPKGS = \
	bg/ap.brokerd \
	bg/ap.configd \
	bg/ap.httpd \
	bg/ap.identifierd \
	bg/ap.logd \
	bg/ap.mcp \
	bg/ap.networkd \
	bg/ap.rpcd \
	bg/ap.serviced \
	bg/ap.tron \
	bg/ap.watchd \
	bg/ap.wifid

APP_GOPKGS = $(APPCOMMON_GOPKGS) $(APPCOMMAND_GOPKGS) $(APPDAEMON_GOPKGS)

APPTOOLS = \
	ap-arpspoof \
	ap-complete \
	ap-configctl \
	ap-ctl \
	ap-observation \
	ap-scan \
	ap-speedtest \
	ap-userctl \
	ap-vpntool \
	ap-watchctl \
	ap-wg

MISCCOMMANDS = \
	ap-publiclog \
	ap-rpc

APPBINARIES = \
	$(APPCOMMAND_GOPKGS:bg/%=$(APPBIN)/%) \
	$(APPDAEMON_GOPKGS:bg/%=$(APPBIN)/%) \
	$(APPTOOLS:%=$(APPBIN)/%) \
	$(MISCCOMMANDS:%=$(APPBIN)/%)

# XXX Common configurations?

GO_AP_TESTABLES = \
	bg/ap_common/certificate \
	bg/ap.configd \
	bg/ap-defaultpass\
	bg/ap.logd \
	bg/ap.networkd \
	bg/ap.rpcd \
	bg/ap.wifid \
	bg/ap_common/aputil \
	bg/ap_common/comms \
	bg/ap_common/platform \
	bg/common/grpcutils \
	bg/common/network \
	bg/common/release \
	bg/common/zaperr

NETWORKD_TEMPLATE_FILES = \
	bg-chrony.client.got \
	bg-chrony.server.got

RPCD_TEMPLATE_FILES = sshd_config.got

WIFID_TEMPLATE_FILES = \
	hostapd.radius.got \
	hostapd.radius_clients.got \
	hostapd.conf.got \
	hostapd.users.got \
	virtualap.conf.got

NETWORKD_TEMPLATES = $(NETWORKD_TEMPLATE_FILES:%=$(NETWORKD_TEMPLATE_DIR)/%)
RPCD_TEMPLATES = $(RPCD_TEMPLATE_FILES:%=$(RPCD_TEMPLATE_DIR)/%)
WIFID_TEMPLATES = $(WIFID_TEMPLATE_FILES:%=$(WIFID_TEMPLATE_DIR)/%)
APPTEMPLATES = $(NETWORKD_TEMPLATES) $(RPCD_TEMPLATES) $(WIFID_TEMPLATES)

FILTER_RULES = \
	$(APPRULES)/base.rules \
	$(APPRULES)/local.rules \
	$(APPRULES)/relay.rules

APPCONFIGS_debian = \
	$(APPROOTLIB)/systemd/system/ap.mcp.service \
	$(ROOTETCCHRONY)/chrony.conf

APPCONFIGS_openwrt = \
	$(APPETC)/chronyd-init.sh \
	$(APPETC)/com-brightgate-profile \
	$(ROOTETCCHRONY)/bg-chrony.base.conf \
	$(ROOTETCCRONTABS)/root \
	$(ROOTETCINITD)/ap.mcp \
	$(ROOTETCINITD)/ap.tron \
	$(ROOTETCLOGROTATED)/com-brightgate-logrotate-rsyslog \
	$(ROOTETCSYSCTLD)/50-com-brightgate.conf

APPCONFIGS = \
	$(APPCONFIGS_$(DISTRO)) \
	$(APPDATAWATCHD)/vuln-db.json \
	$(APPETC)/configd.json \
	$(APPETC)/devices.json \
	$(APPETC)/mcp.json \
	$(ROOTETCCHRONY)/bg-chrony.client \
	$(ROOTETCCHRONY)/bg-chrony.platform \
	$(ROOTETCIPTABLES)/rules.v4 \
	$(ROOTETCLOGROTATED)/com-brightgate-logrotate-logd \
	$(ROOTETCLOGROTATED)/com-brightgate-logrotate-mcp \
	$(ROOTETCRSYSLOGD)/com-brightgate-rsyslog.conf

ifeq ("$(DISTRO)","openwrt")
DISTROAPPDIRS = \
	$(ROOTETCCRONTABS) \
	$(ROOTETCINITD) \
	$(ROOTETCSYSCTLD)
endif

APPDIRS = \
	$(DISTROAPPDIRS) \
	$(APPBIN) \
	$(APPDATA) \
	$(APPETC) \
	$(APPRULES) \
	$(APPSECRET) \
	$(APPSECRETRPCD) \
	$(APPSECRETSSL) \
	$(APPSNMAP) \
	$(APPVAR) \
	$(APPDATAANTIPHISH) \
	$(APPDATACONFIGD) \
	$(APPETCIDENTIFIERD) \
	$(APPDATAIDENTIFIERD) \
	$(APPDATALOGD) \
	$(APPDATAMCP) \
	$(APPDATAPASSWORDS) \
	$(APPDATARPCD) \
	$(APPDATAWATCHD) \
	$(HTTPD_CLIENTWEB_DIR) \
	$(NETWORKD_TEMPLATE_DIR) \
	$(ROOTETC) \
	$(ROOTETCCHRONY) \
	$(ROOTETCIPTABLES) \
	$(ROOTETCLOGROTATED) \
	$(ROOTETCRSYSLOGD) \
	$(RPCD_TEMPLATE_DIR) \
	$(WIFID_TEMPLATE_DIR)

APPCOMPONENTS = \
	$(APPBINARIES) \
	$(APPCONFIGS) \
	$(APPDIRS) \
	$(APPMODEL) \
	$(APPTEMPLATES) \
	$(FILTER_RULES)

# Cloud components and supporting definitions.

CLOUDROOT=$(ROOT)/cloud
CLOUDBASE=$(CLOUDROOT)/opt/net.b10e
CLOUDBIN=$(CLOUDBASE)/bin
CLOUDETC=$(CLOUDBASE)/etc
CLOUDETCDEVICESJSON=$(CLOUDETC)/devices.json
CLOUDETCOUITXT=$(CLOUDETC)/oui.txt
CLOUDETCSCHEMA=$(CLOUDETC)/schema
CLOUDETCSCHEMAAPPLIANCEDB=$(CLOUDETCSCHEMA)/appliancedb
CLOUDETCSCHEMASESSIONDB=$(CLOUDETCSCHEMA)/sessiondb
CLOUDETCSSHDCONFIG=$(CLOUDETC)/sshd_config.got
CLOUDETCSSHCONFIG=$(CLOUDETC)/ssh_config.got
CLOUDLIB=$(CLOUDBASE)/lib
CLOUDLIBCLHTTPDWEB=$(CLOUDLIB)/cl.httpd-web
CLOUDLIBCLHTTPDWEBCLIENTWEB=$(CLOUDLIBCLHTTPDWEB)/client-web
CLOUDLIBCRONCLOBS=$(CLOUDLIB)/cron-cl-obs
CLOUDROOTLIB=$(CLOUDROOT)/lib
CLOUDROOTLIBSYSTEMDSYSTEM=$(CLOUDROOTLIB)/systemd/system
CLOUDVAR=$(CLOUDBASE)/var
CLOUDSPOOL=$(CLOUDVAR)/spool

CLOUDDAEMON_GOPKGS = \
	bg/cl.configd \
	bg/cl.eventd \
	bg/cl.httpd \
	bg/cl.identifierd \
	bg/cl.rpcd

CLOUDCOMMON_GOPKGS = \
	$(COMMON_GOPKGS) \
	bg/cl_common/... \
	bg/cloud_models/... \
	bg/cl-obs/...

CLOUDCOMMAND_GOPKGS = \
	bg/cl-aggregate \
	bg/cl-cert \
	bg/cl-configctl \
	bg/cl-dtool \
	bg/cl-obs \
	bg/cl-reg \
	bg/cl-release \
	bg/cl-service \
	bg/cl-vpntool

CLOUD_GOPKGS = $(CLOUDCOMMON_GOPKGS) $(CLOUDDAEMON_GOPKGS) $(CLOUDCOMMAND_GOPKGS)

CLOUDDAEMONS = $(CLOUDDAEMON_GOPKGS:bg/%=%)

CLOUDCOMMANDS = $(CLOUDCOMMAND_GOPKGS:bg/%=%)

GO_CLOUD_TESTABLES = \
	bg/cl_common/auth/m2mauth \
	bg/cl_common/daemonutils \
	bg/cl_common/deviceinfo \
	bg/cl_common/registry \
	bg/cl_common/vaultdb \
	bg/cloud_models/appliancedb \
	bg/cloud_models/sessiondb \
	bg/common/mfg \
	bg/cl-cert \
	bg/cl-obs/classifier \
	bg/cl-obs/extract \
	bg/cl-obs/sentence \
	bg/cl.configd \
	bg/cl.eventd \
	bg/cl.identifierd \
	bg/cl.httpd

CLOUDSERVICES = \
	cl-cert.service \
	cl-cert.timer \
	cl.configd.service \
	cl.eventd.service \
	cl.httpd.service \
	cl.identifierd.service \
	cl-obs.service \
	cl-obs.timer \
	cl.rpcd.service

CLOUDSYSTEMDSERVICES = $(CLOUDSERVICES:%=$(CLOUDROOTLIBSYSTEMDSYSTEM)/%)

CLOUDETCFILES = \
	$(CLOUDETCDEVICESJSON) \
	$(CLOUDETCOUITXT) \
	$(CLOUDETCSSHCONFIG) \
	$(CLOUDETCSSHDCONFIG)

CLOUDLIBFILES = \
	$(CLOUDLIBCRONCLOBS)

# For appliancedb and sessiondb schema files, we use wildcard to glob the list.
# This saves us a headache when we forget to update the Makefile for this
# otherwise minor change.
APPLIANCEDBSCHEMASRCDIR = $(GOSRCBG)/cloud_models/appliancedb/schema
APPLIANCEDBSCHEMAFILES = $(wildcard $(APPLIANCEDBSCHEMASRCDIR)/schema*.sql)
APPLIANCEDBSCHEMAS = $(APPLIANCEDBSCHEMAFILES:$(APPLIANCEDBSCHEMASRCDIR)/%=$(CLOUDETCSCHEMAAPPLIANCEDB)/%)

SESSIONDBSCHEMASRCDIR = $(GOSRCBG)/cloud_models/sessiondb/schema
SESSIONDBSCHEMAFILES = $(wildcard $(SESSIONDBSCHEMASRCDIR)/schema*.sql)
SESSIONDBSCHEMAS = $(SESSIONDBSCHEMAFILES:$(SESSIONDBSCHEMASRCDIR)/%=$(CLOUDETCSCHEMASESSIONDB)/%)

CLOUDSCHEMAS = $(APPLIANCEDBSCHEMAS) $(SESSIONDBSCHEMAS)

CLOUDBINARIES = $(CLOUDCOMMANDS:%=$(CLOUDBIN)/%) $(CLOUDDAEMONS:%=$(CLOUDBIN)/%)

CLOUDDIRS = \
	$(CLOUDBIN) \
	$(CLOUDETC) \
	$(CLOUDLIB) \
	$(CLOUDLIBCLHTTPDWEB) \
	$(CLOUDLIBCLHTTPDWEBCLIENTWEB) \
	$(CLOUDETCSCHEMA) \
	$(CLOUDETCSCHEMAAPPLIANCEDB) \
	$(CLOUDETCSCHEMASESSIONDB) \
	$(CLOUDROOTLIB) \
	$(CLOUDSPOOL) \
	$(CLOUDVAR)

CLOUDCOMPONENTS = $(CLOUDBINARIES) $(CLOUDSYSTEMDSERVICES) $(CLOUDDIRS) $(CLOUDSCHEMAS) $(CLOUDETCFILES) $(CLOUDLIBFILES)

ALL_GOPKGS = $(APP_GOPKGS) $(CLOUD_GOPKGS)

ALL_GOBINS = \
	     $(APPCOMMAND_GOPKGS) $(APPDAEMON_GOPKGS) \
	     $(CLOUDCOMMAND_GOPKGS) $(CLOUDDAEMON_GOPKGS)

COVERAGE_DIR = $(GITROOT)/coverage

#
# Go Tools: Install versioned binaries for 'mockery', etc.
#
include ./Makefile.gotools

#
# Go Dependencies: Targets to make module dependency maintenance easier
#
include ./Makefile.godeps

#
# Documentation: Targets for product documentation builds.
#
include ./Makefile.doc

.DEFAULT_GOAL = install

install: mocks $(TARGETS)

appliance: $(APPCOMPONENTS)

cloud: $(CLOUDCOMPONENTS)

# This will create the sysroot.
build-sysroot:
	$(BUILD_SYSROOT_CMD) build

# This will create the sysroot and, if it's new, upload it as a blob, given a
# credential file in $KEY_SYSROOT_UPLOADER.
ifeq ("$(DISTRO)","debian")
upload-sysroot:
	$(BUILD_SYSROOT_CMD) build -u
endif
ifeq ("$(DISTRO)","openwrt")
upload-sysroot:
	$(BUILD_SYSROOT_CMD) upload
endif

download-sysroot: build/cross-compile/$(SYSROOT_BLOB_NAME)

unpack-sysroot: $(SYSROOT)/.$(SYSROOT_SUM)

build/cross-compile/$(SYSROOT_BLOB_NAME):
	$(BUILD_SYSROOT_CMD) download

$(SYSROOT)/.$(SYSROOT_SUM): build/cross-compile/$(SYSROOT_BLOB_NAME)
	$(BUILD_SYSROOT_CMD) unpack -d $(subst build/cross-compile/,,$(@D))
	touch $@

archives: install client-web $(VENV_INSTALLED)
	$(PYTHON3) build/package.py --distro archive --arch $(PKG_DEB_ARCH) --proto $(ROOT)

packages: install client-web $(VENV_INSTALLED)
	$(PYTHON3) build/package.py --distro $(DISTRO) --arch $(PKG_DEB_ARCH) --proto $(ROOT)

packages-lint: install client-web $(VENV_INSTALLED)
	$(PYTHON3) build/package.py --lint --distro $(DISTRO) --arch $(PKG_DEB_ARCH) --proto $(ROOT)

GCS_WRAPPER = $(GITROOT)/build/gcs-wrapper.sh
APPLIANCE_BUCKET = bg-appliance-artifacts

packages-upload: export GCS_KEY_FILE=$(GCS_KEY_ARTIFACT)

packages-upload:
	$(GCS_WRAPPER) vcp bg-appliance_*_amd64.deb gs://$(APPLIANCE_BUCKET)/x86/PS/$(GITHASH_FULL)/
	$(GCS_WRAPPER) vcp bg-appliance_*_armhf.deb gs://$(APPLIANCE_BUCKET)/rpi3/PS/$(GITHASH_FULL)/
	$(GCS_WRAPPER) vcp bg-appliance_*_arm_*.ipk gs://$(APPLIANCE_BUCKET)/mt7623/PS/$(GITHASH_FULL)/

GO_MOCK_CLOUDRPC_SRCS = \
	$(GOSRCBG)/cloud_rpc/cloud_rpc.pb.go \
	$(GOSRCBG)/base_def/base_def.go \
	$(GOSRCBG)/base_msg/base_msg.pb.go \
	$(GOSRCBG)/common/cfgmsg/cfgmsg.go \
	$(GOSRCBG)/common/cfgmsg/cfgmsg.pb.go
GO_MOCK_APPLIANCEDB_SRCS = \
	$(GOSRCBG)/cloud_models/appliancedb/account.go \
	$(GOSRCBG)/cloud_models/appliancedb/appliancedb.go \
	$(GOSRCBG)/cloud_models/appliancedb/certs.go \
	$(GOSRCBG)/cloud_models/appliancedb/cmdqueue.go \
	$(GOSRCBG)/cloud_models/appliancedb/releases.go \
	$(GOSRCBG)/base_def/base_def.go
GO_MOCK_APPLIANCEDB = $(GOSRCBG)/cloud_models/appliancedb/mocks/DataStore.go
GO_MOCK_CLOUDRPC = $(GOSRCBG)/cloud_rpc/mocks/EventClient.go
GO_MOCK_SRCS = \
	$(GO_MOCK_APPLIANCEDB) \
	$(GO_MOCK_CLOUDRPC)

mocks: $(GO_MOCK_SRCS)

$(GO_MOCK_CLOUDRPC): MOCK_NAME = 'EventClient'
$(GO_MOCK_CLOUDRPC): $(GO_MOCK_CLOUDRPC_SRCS)
$(GO_MOCK_APPLIANCEDB): MOCK_NAME = 'DataStore'
$(GO_MOCK_APPLIANCEDB):  $(GO_MOCK_APPLIANCEDB_SRCS)
$(GO_MOCK_SRCS): $(GOTOOLS_BIN_MOCKERY)

# The use of 'realpath' avoids an issue in mockery for workspaces with
# symlinks (https://github.com/vektra/mockery/issues/157).
$(GO_MOCK_SRCS):
	cd $(realpath $(dir $<)) && GOPATH=$(realpath $(GOPATH)) $(GOTOOLS_BIN_MOCKERY) --name $(MOCK_NAME) --log-level warn

test: test-go

# The user might set GO_TESTABLES, in which case, honor it
ifeq ("$(GO_TESTABLES)","")
  ifeq ("$(filter appliance, $(TARGETS))", "appliance")
    GO_TESTABLES += $(GO_AP_TESTABLES)
  endif
  ifeq ("$(filter cloud, $(TARGETS))", "cloud")
    GO_TESTABLES += $(GO_CLOUD_TESTABLES)
  endif
endif

test-go: install
	cd $(GOSRCBG) && APROOT=$(GITROOT)/$(APPROOT) $(GO) test $(GO_TESTFLAGS) $(GO_TESTABLES)

coverage: coverage-go

space := $() $()
comma := ,

coverage-go: install
	$(MKDIR) -p $(COVERAGE_DIR)
	cd $(GOSRCBG); \
	err=""; of=$$(mktemp coverXXXXXX); for p in $(GO_TESTABLES); do \
		pkgs=$(subst $(space),$(comma),$(APPCOMMON_GOPKGS)); \
		pkgs=$$pkgs,$(subst $(space),$(comma),$(CLOUDCOMMON_GOPKGS)); \
		pkgs=$$pkgs,$$p; \
		APROOT=$(GITROOT)/$(APPROOT) \
			$(GO) test $(GO_TESTFLAGS) -cover \
			-coverprofile $(COVERAGE_DIR)/$$(echo $$p | tr / -).out \
			-coverpkg $$pkgs \
			$$p > $$of 2>&1 || err="$$err $$p"; \
		grep -v "no packages being tested depend on matches for pattern" $$of; \
	done; \
	rm $$of; \
	if [ -n "$${err}" ]; then echo "Failures in the following packages:$$err"; exit 1; fi
	echo "mode: set" > $(COVERAGE_DIR)/cover.out
	grep -h -v "^mode:" $(COVERAGE_DIR)/bg*.out | sort -u >> $(COVERAGE_DIR)/cover.out
	cd $(GOSRCBG) && $(GO) tool cover \
		-html=$(COVERAGE_DIR)/cover.out -o $(COVERAGE_DIR)/coverage.html

vet-go: $(GENERATED_GO_FILES) $(GO_MOCK_SRCS)
	cd $(GOSRCBG) && $(GO) vet $(APP_GOPKGS)
	cd $(GOSRCBG) && $(GO) vet $(CLOUD_GOPKGS)

# sort to remove dups
LINT_GOPKGS = $(sort $(ALL_GOPKGS))

lint-go: $(GENERATED_GO_FILES) $(GO_MOCK_SRCS)
	$(GOLINT) -set_exit_status $(LINT_GOPKGS)

fmt-go:
	build/check-gofmt.sh

CILINT_GOPKGS = $(LINT_GOPKGS:bg/%=%)

# See also .golangci.yaml, where we specify some defaults
cilint-go: $(GOTOOLS_BIN_GOLANGCI_LINT)
	cd $(GOSRCBG) && $(GOTOOLS_BIN_GOLANGCI_LINT) run $(CILINT_FLAGS) $(CILINT_GOPKGS)

# ordered in most-to-least useful to most developers
check-go: vet-go lint-go fmt-go

# Installation of appliance configuration files

$(APPETC)/configd.json: $(GOSRCBG)/ap.configd/configd.json | $(APPETC)
	$(INSTALL) -m 0644 $< $@

$(APPETC)/devices.json: $(GOSRCBG)/ap.configd/devices.json | $(APPETC)
	$(INSTALL) -m 0644 $< $@

$(APPETC)/mcp.json: $(GOSRCBG)/ap.mcp/mcp.json | $(APPETC)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCCHRONY)/bg-chrony.client: $(DISTRODIR)/bg-chrony.client | $(ROOTETCCHRONY)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCCHRONY)/bg-chrony.platform: $(DISTRODIR)/bg-chrony.platform | $(ROOTETCCHRONY)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCIPTABLES)/rules.v4: $(GOSRCBG)/ap.networkd/rules.v4 | $(ROOTETCIPTABLES)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCLOGROTATED)/com-brightgate-logrotate-logd: build/$(DISTRO)-$(PKG)/com-brightgate-logrotate-logd | $(ROOTETCLOGROTATED)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCLOGROTATED)/com-brightgate-logrotate-mcp: build/$(DISTRO)-$(PKG)/com-brightgate-logrotate-mcp | $(ROOTETCLOGROTATED)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCLOGROTATED)/com-brightgate-logrotate-rsyslog: build/$(DISTRO)-$(PKG)/com-brightgate-logrotate-rsyslog | $(ROOTETCLOGROTATED)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCRSYSLOGD)/com-brightgate-rsyslog.conf: $(GOSRCBG)/ap.watchd/com-brightgate-rsyslog.conf | $(ROOTETCRSYSLOGD)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCSYSCTLD)/50-com-brightgate.conf: build/$(DISTRO)-$(PKG)/sysctl.conf | $(ROOTETCSYSCTLD)
	$(INSTALL) -m 0644 $< $@

$(APPDATAWATCHD)/vuln-db.json: $(GOSRCBG)/ap-vuln-aggregate/sample-db.json | $(APPDATAWATCHD)
	$(INSTALL) -m 0644 $< $@

$(NETWORKD_TEMPLATE_DIR)/%: $(GOSRCBG)/ap.networkd/% | $(APPETC)
	$(INSTALL) -m 0644 $< $@

$(RPCD_TEMPLATE_DIR)/%: $(GOSRCBG)/ap.rpcd/% | $(APPETC)
	$(INSTALL) -m 0644 $< $@

$(WIFID_TEMPLATE_DIR)/%: $(GOSRCBG)/ap.wifid/% | $(APPETC)
	$(INSTALL) -m 0644 $< $@

$(APPRULES)/%: $(GOSRCBG)/ap.networkd/% | $(APPRULES)
	$(INSTALL) -m 0644 $< $@

$(APPMODEL): | $(APPETCIDENTIFIERD)
	$(MKDIR) -p $@

# Raspbian/Debian-specific appliance files
$(APPROOTLIB)/systemd/system:
	$(MKDIR) -p $(APPROOTLIB)/systemd/system

$(APPROOTLIB)/systemd/system/ap.mcp.service: build/debian-deb/ap.mcp.service | $(APPROOTLIB)/systemd/system
	$(INSTALL) -m 0644 $< $@

$(ROOTETCCHRONY)/chrony.conf: $(GOSRCBG)/ap.networkd/bg-chrony.base.conf | $(ROOTETCCHRONY)
	$(INSTALL) -m 0644 $< $@

# OpenWrt-specific appliance files
$(APPETC)/chronyd-init.sh: build/openwrt-ipk/chronyd-init.sh | $(APPETC)
	$(INSTALL) -m 0755 $< $@

$(APPETC)/com-brightgate-profile: build/openwrt-ipk/com-brightgate-profile | $(APPETC)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCCHRONY)/bg-chrony.base.conf: $(GOSRCBG)/ap.networkd/bg-chrony.base.conf | $(ROOTETCCHRONY)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCCRONTABS)/root: build/openwrt-ipk/etc-crontabs-root | $(ROOTETCCRONTABS)
	$(INSTALL) -m 0644 $< $@

$(ROOTETCINITD)/ap.mcp: build/openwrt-ipk/ap.mcp | $(ROOTETCINITD)
	$(INSTALL) -m 0755 $< $@

$(ROOTETCINITD)/ap.tron: build/openwrt-ipk/ap.tron | $(ROOTETCINITD)
	$(INSTALL) -m 0755 $< $@

$(APPDIRS):
	$(MKDIR) -p $@

$(APPBINARIES): | $(APPBIN)

# Build rules for go binaries.

COMPUTE_DEPS = $(GOTOOLS_DIR)/bin/compute-deps
$(COMPUTE_DEPS): export GOBIN=$(GOTOOLS_DIR)/bin

$(COMPUTE_DEPS): build/tools/compute-deps.go
	unset GOARCH && cd $(^D) && $(GO) install $(GO_BUILD_FLAGS) $(^F)

GENERATED_GO_FILES = \
	$(GOSRCBG)/base_def/base_def.go \
	$(GOSRCBG)/base_msg/base_msg.pb.go \
	$(GOSRCBG)/cloud_rpc/cloud_rpc.pb.go \
	$(GOSRCBG)/common/cfgmsg/cfgmsg.pb.go \
	$(GOSRCBG)/common/version.go

# When we've moved go files around, godeps.mk is no longer valid and must be
# rebuilt.
%.go:
	rm -f $(GOTOOLS_DIR)/godeps.mk

$(GOTOOLS_DIR)/godeps.mk: | $(COMPUTE_DEPS) $(GENERATED_GO_FILES)
	unset GOARCH && $(COMPUTE_DEPS) $(ALL_GOBINS) > $@ || $(RM) -f $@

include $(GOTOOLS_DIR)/godeps.mk

$(APPTOOLS:%=$(APPBIN)/%): $(APPBIN)/ap-tools
	ln -sf $(<F) $@

# As of golang 1.10, 'go build' and 'go install' both cache their results, so
# the latter isn't any faster.  We use 'go build' because because 'go install'
# refuses to install cross-compiled binaries into GOBIN.
$(APPBIN)/%: $(CROSS_DEP)
	$(CROSS_ENV) cd $(GOSRCBG) && $(GO) build $(GO_BUILD_FLAGS) -o ../../../$(@) bg/$*

$(GOSRCBG)/common/version.go: $(GITCHANGED)
	sed "s/GITHASH/$(GITHASH)/" $(GOSRCBG)/common/version.base > $@

$(APPBIN)/ap-publiclog: $(APPBIN)/ap.logd
	ln -sf $(<F) $@

$(APPBIN)/ap-rpc: $(APPBIN)/ap.rpcd
	ln -sf $(<F) $@

LOCAL_BINARIES=$(APPBINARIES:$(APPBIN)/%=$(GOBIN)/%)

# Cloud components

# Installation of cloud configuration files

$(CLOUDETCDEVICESJSON): $(GOSRCBG)/ap.configd/devices.json | $(CLOUDETC)
	$(INSTALL) -m 0644 $< $@

# Install appliancedb database schema files
$(CLOUDETCSCHEMAAPPLIANCEDB)/%: $(APPLIANCEDBSCHEMASRCDIR)/% | $(CLOUDETCSCHEMAAPPLIANCEDB)
	$(INSTALL) -m 0644 $< $@

# Install sessiondb database schema files
$(CLOUDETCSCHEMASESSIONDB)/%: $(SESSIONDBSCHEMASRCDIR)/% | $(CLOUDETCSCHEMASESSIONDB)
	$(INSTALL) -m 0644 $< $@

$(CLOUDETCSSHDCONFIG): $(GOSRCBG)/cl-service/sshd_config.got | $(CLOUDETC)
	$(INSTALL) -m 0644 $< $@

$(CLOUDETCSSHCONFIG): $(GOSRCBG)/cl-service/ssh_config.got | $(CLOUDETC)
	$(INSTALL) -m 0644 $< $@

$(CLOUDETCOUITXT):
	-curl --connect-timeout 5 -s -S -R -o $@ http://standards-oui.ieee.org/oui.txt
	@if [ -s $@ ]; then \
		mkdir -p $(DOWNLOAD_CACHEDIR); \
		echo Copying $@ to $(DOWNLOAD_CACHEDIR); \
		cp -p $@ $(DOWNLOAD_CACHEDIR)/oui.txt; \
	else \
		echo Copying $@ from $(DOWNLOAD_CACHEDIR); \
		cp -p $(DOWNLOAD_CACHEDIR)/oui.txt $@; \
	fi

$(CLOUDLIBCRONCLOBS): $(GOSRCBG)/cl-obs/cron-cl-obs.bash
	$(INSTALL) -m 0755 $< $@

# Install service descriptions
$(CLOUDROOTLIBSYSTEMDSYSTEM)/%: build/cl-systemd/% | $(CLOUDROOTLIBSYSTEMDSYSTEM)
	$(INSTALL) -m 0644 $< $@

$(CLOUDBIN)/%: | $(CLOUDBIN)
	cd $(GOSRCBG) && $(GO) build $(GO_BUILD_FLAGS) -o ../../../$(@) bg/$*

$(CLOUDROOTLIBSYSTEMDSYSTEM): | $(CLOUDROOTLIB)
	$(MKDIR) -p $@

$(CLOUDDIRS):
	$(MKDIR) -p $@

#
# Common definitions
#

$(GOSRCBG)/base_def/base_def.go: base/generate-base-def.py $(VENV_INSTALLED) | $(GOSRCBG)/base_def
	$(PYTHON3) $< --go | $(GOFMT) > $@

base/base_def.py: base/generate-base-def.py $(VENV_INSTALLED)
	$(PYTHON3) $< --python3 > $@

$(BGDEPDIR):
	@$(MKDIR) -p $@

$(BGDEPDIR)/%: | $(BGDEPDIR)
	@touch $@

#
# Protocol buffers
#

$(GOSRCBG)/base_msg/base_msg.pb.go: base/base_msg.proto $(GOTOOLS_BIN_PROTOCGENGO)
	cd base && \
		protoc --plugin=$(GOTOOLS_BIN_PROTOCGENGO) \
		    --go_out ../$(GOSRCBG)/base_msg $(notdir $<)

base/base_msg_pb2.py: base/base_msg.proto
	protoc --python_out . $<

$(GOSRCBG)/common/cfgmsg/cfgmsg.pb.go: base/cfgmsg.proto $(GOTOOLS_BIN_PROTOCGENGO)
	cd base && \
		protoc --plugin=$(GOTOOLS_BIN_PROTOCGENGO) \
		    --go_out=../$(GOSRCBG)/common/cfgmsg \
		    $(notdir $<)

$(GOSRCBG)/cloud_rpc/cloud_rpc.pb.go: base/cloud_rpc.proto $(GOTOOLS_BIN_PROTOCGENGO)
	cd base && \
		protoc --plugin=$(GOTOOLS_BIN_PROTOCGENGO) \
			-I/usr/local/include \
			-I . \
			--go_out=plugins=grpc,Mbase_msg.proto=bg/base_msg,Mcfgmsg.proto=bg/common/cfgmsg:../$(GOSRCBG)/cloud_rpc \
			$(notdir $<)

LOCAL_COMMANDS=$(COMMANDS:$(APPBIN)/%=$(GOBIN)/%)
LOCAL_DAEMONS=$(DAEMONS:$(APPBIN)/%=$(GOBIN)/%)

# Generate a hash of the contents of BUILDTOOLS, so that if the required
# packages change, we'll rerun the check.
# note: The hash is constant; := avoids repeated shell invocations
BUILDTOOLS_HASH := $(shell echo $(BUILDTOOLS) | $(SHA256SUM) | awk '{print $$1}')
BUILDTOOLS_FILE = .make-buildtools-$(BUILDTOOLS_HASH)

.PHONY: tools
tools: $(BUILDTOOLS_FILE) $(GOTOOLS)

install-tools: FRC
	build/check-tools.sh -i $(BUILDTOOLS)
	touch $@

$(BUILDTOOLS_FILE):
	build/check-tools.sh $(BUILDTOOLS)
	touch $@

# Use python3 to invoke pip; else, the long pathnames involved can cause
# pip to fail thanks to Linux's BINPRM_BUF_SIZE limit on #! lines.
$(VENV_INSTALLED):
	$(RM) -fr $(VENV_NAME)
	$(HOSTPYTHON3) -m venv $(VENV_NAME)
	$(PYTHON3) -m pip --no-cache-dir --log $(VENV_NAME)/pip.log install -r build/requirements.txt > /dev/null
	touch $@

NPM = npm
NPM_QUIET = --loglevel warn --no-progress
# Prefer to use npm ci if it is available; there's no good test for its
# presence other than seeing how much help exists for the command.
.make-npm-installed: client-web/package.json
	(cd client-web && \
		ci=$$($(NPM) help ci | wc -l) && \
		if [ $$ci -gt 10 ]; then \
			$(NPM) ci $(NPM_QUIET); \
		else \
			$(NPM) install $(NPM_QUIET); \
		fi; )
	touch $@

CLIENT_WEB_BUILD_TARGET = build
CLIENT_WEB_LINT_TARGET = lint
client-web-dev:: CLIENT_WEB_BUILD_TARGET = build-dev
client-web-dev:: CLIENT_WEB_LINT_TARGET = lint-fix
client-web-dev: client-web FRC

client-web: doc .make-npm-installed FRC | $(HTTPD_CLIENTWEB_DIR) $(CLOUDLIBCLHTTPDWEBCLIENTWEB)
	$(RM) -fr $(HTTPD_CLIENTWEB_DIR)/* $(CLOUDLIBCLHTTPDWEBCLIENTWEB)/*
	(cd client-web && $(NPM) run $(CLIENT_WEB_LINT_TARGET))
	(cd client-web && $(NPM) run $(CLIENT_WEB_BUILD_TARGET))
	tar -C client-web/dist -c -f - . | tar -C $(HTTPD_CLIENTWEB_DIR) -xvf -
	tar -C client-web/dist -c -f - . | tar -C $(CLOUDLIBCLHTTPDWEBCLIENTWEB) -xvf -

FRC:

.PHONY: clobber
clobber: clean packages-clobber gotools-clobber doc-clobber
	chmod -R u+w $(GOWS)/pkg
	$(RM) -fr $(ROOT) $(GOWS)/pkg $(GOWS)/bin $(SYSROOT)
	$(RM) -fr _venv.*
	$(RM) -f .make-*

.PHONY: packages-clobber
packages-clobber:
	$(RM) -fr bg-appliance_*.*.*-*_* bg-cloud_*.*.*-*_*

clean: doc-clean
	$(RM) -f \
		base/base_def.py \
		base/base_msg_pb2.py \
		base/cloud_rpc_pb2.py \
		$(GENERATED_GO_FILES) \
		$(APPBINARIES) \
		$(CLOUDBINARIES) \
		$(GO_MOCK_SRCS)
	$(RM) -fr $(COVERAGE_DIR)
	find $(GOSRCBG)/ap_common -name \*.pem | xargs --no-run-if-empty $(RM) -f

.PHONY: check-dirty
check-dirty:
	@c=$$(git status -s); \
	if [ -n "$$c" ]; then \
		echo "Workspace is dirty:"; \
		echo "$$c"; \
		echo "--"; \
		git diff; \
		exit 1; \
	fi

