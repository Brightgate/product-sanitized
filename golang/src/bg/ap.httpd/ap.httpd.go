/*
 * COPYRIGHT 2018 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 */

// appliance HTTPD front end
// no fishing picture: https://pixabay.com/p-1191938/?no_redirect

package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bg/ap_common/apcfg"
	"bg/ap_common/aputil"
	"bg/ap_common/broker"
	"bg/ap_common/certificate"
	"bg/ap_common/data"
	"bg/ap_common/mcp"
	"bg/ap_common/network"
	"bg/base_def"
	"bg/base_msg"
	"bg/common/cfgapi"

	"github.com/golang/protobuf/proto"
	"go.uber.org/zap"

	"github.com/gorilla/mux"
	"github.com/gorilla/securecookie"
	"github.com/lestrrat/go-apache-logformat"

	"github.com/NYTimes/gziphandler"
	"github.com/unrolled/secure"
	"github.com/urfave/negroni"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	clientWebDir = flag.String("client-web_dir", "client-web",
		"location of httpd client web root")
	ports         = listFlag([]string{":80", ":443"})
	developerHTTP = flag.String("developer-http", "",
		"Developer http port (disabled by default)")

	cert      string
	key       string
	certValid bool

	cutter *securecookie.SecureCookie

	config     *cfgapi.Handle
	domainname string
	slog       *zap.SugaredLogger

	metrics struct {
		latencies prometheus.Summary
	}
)

var (
	pings     = 0
	configs   = 0
	entities  = 0
	resources = 0
	requests  = 0
)

const (
	pname = "ap.httpd"

	cookiehmackeyprop = "@/httpd/cookie-hmac-key"
	cookieaeskeyprop  = "@/httpd/cookie-aes-key"

	// 'unsafe-inline' is needed because current HTML pages are
	// using inline <script> tags.  'unsafe-eval' is needed by
	// vue.js's template compiler.  'img-src' relaxed to allow
	// inline SVG elements.
	contentSecurityPolicy = "default-src 'self' 'unsafe-inline' 'unsafe-eval'; img-src 'self' data: 'unsafe-inline' 'unsafe-eval'; frame-ancestors 'none'"
)

// listFlag is a flag type that turns a comma-separated input into a slice of
// strings.
type listFlag []string

func (l listFlag) String() string {
	return strings.Join(l, ",")
}

func (l listFlag) Set(value string) error {
	l = strings.Split(value, ",")
	return nil
}

func handlePing(event []byte) { pings++ }

func handleConfig(event []byte) { configs++ }

func handleEntity(event []byte) { entities++ }

func handleResource(event []byte) { resources++ }

func handleRequest(event []byte) { requests++ }

func handleError(event []byte) {
	syserror := &base_msg.EventSysError{}
	proto.Unmarshal(event, syserror)

	slog.Debugf("sys.error received by handler: %v", *syserror)

	// Check if event is a certificate error
	if *syserror.Reason == base_msg.EventSysError_RENEWED_SSL_CERTIFICATE {
		slog.Infof("exiting due to renewed certificate")
		os.Exit(0)
	}
}

// StatsContent contains information for filling out the stats request
// Policy: GET(*)
type StatsContent struct {
	URLPath string

	NPings     string
	NConfigs   string
	NEntities  string
	NResources string
	NRequests  string

	Host string
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	lt := time.Now()

	conf := StatsContent{
		URLPath:    r.URL.Path,
		NPings:     strconv.Itoa(pings),
		NConfigs:   strconv.Itoa(configs),
		NEntities:  strconv.Itoa(entities),
		NResources: strconv.Itoa(resources),
		NRequests:  strconv.Itoa(requests),
		Host:       r.Host,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(conf); err != nil {
		http.Error(w, "Internal server error", 501)
		return
	}

	metrics.latencies.Observe(time.Since(lt).Seconds())
}

// hostInMap returns a Gorilla Mux matching function that checks to see if
// the host is in the given map.
func hostInMap(hostMap map[string]bool) mux.MatcherFunc {
	return func(r *http.Request, match *mux.RouteMatch) bool {
		return hostMap[r.Host]
	}
}

func phishHandler(w http.ResponseWriter, r *http.Request) {
	slog.Infof("Phishing request: %v\n", *r)

	scheme := r.URL.Scheme
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}

	phishu := fmt.Sprintf("%s://phishing.%s/client-web/malwareWarn.html?host=%s",
		scheme, domainname, r.Host)
	http.Redirect(w, r, phishu, http.StatusSeeOther)
}

func defaultHandler(w http.ResponseWriter, r *http.Request) {
	var gatewayu string

	scheme := r.URL.Scheme
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}

	if r.Host == "localhost" || r.Host == "127.0.0.1" {
		gatewayu = fmt.Sprintf("%s://%s/client-web/",
			scheme, r.Host)
	} else {
		gatewayu = fmt.Sprintf("%s://gateway.%s/client-web/",
			scheme, domainname)
	}
	http.Redirect(w, r, gatewayu, http.StatusFound)
}

func listen(addr string, port string, ring string, cfg *tls.Config,
	certfn string, keyfn string, handler http.Handler) {
	if port == ":443" {
		go func() {
			srv := &http.Server{
				Addr:         addr + port,
				Handler:      handler,
				TLSConfig:    cfg,
				TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler), 0),
			}
			err := srv.ListenAndServeTLS(certfn, keyfn)
			slog.Infof("TLS Listener on %s (%s) exited: %v\n", addr+port, ring, err)
		}()
	} else {
		go func() {
			err := http.ListenAndServe(addr+port, handler)
			slog.Infof("Listener on %s (%s) exited: %v\n", addr+port, ring, err)
		}()
	}
}

func establishHttpdKeys() ([]byte, []byte) {
	var hs, as []byte

	// If @/httpd/cookie-hmac-key is already set, retrieve its value.
	hs64, err := config.GetProp(cookiehmackeyprop)
	if err != nil {
		hs = securecookie.GenerateRandomKey(base_def.HTTPD_HMAC_SIZE)
		if hs == nil {
			slog.Fatalf("could not generate random key of size %d\n",
				base_def.HTTPD_HMAC_SIZE)
		}
		hs64 = base64.StdEncoding.EncodeToString(hs)

		err = config.CreateProp(cookiehmackeyprop, hs64, nil)
		if err != nil {
			slog.Fatalf("could not create '%s': %v\n", cookiehmackeyprop, err)
		}
	} else {
		hs, err = base64.StdEncoding.DecodeString(hs64)
		if err != nil {
			slog.Fatalf("'%s' contains invalid b64 representation: %v\n", cookiehmackeyprop, err)
		}

		if len(hs) != base_def.HTTPD_HMAC_SIZE {
			// Delete
			err = config.DeleteProp(cookiehmackeyprop)
			if err != nil {
				slog.Fatalf("could not delete invalid size HMAC key: %v\n", err)
			} else {
				return establishHttpdKeys()
			}
		}
	}

	as64, err := config.GetProp(cookieaeskeyprop)
	if err != nil {
		as = securecookie.GenerateRandomKey(base_def.HTTPD_AES_SIZE)
		as64 = base64.StdEncoding.EncodeToString(as)

		err = config.CreateProp(cookieaeskeyprop, as64, nil)
		if err != nil {
			slog.Fatalf("could not create '%s': %v\n", cookieaeskeyprop, err)
		}
	} else {
		as, err = base64.StdEncoding.DecodeString(as64)
		if err != nil {
			slog.Fatalf("'%s' contains invalid b64 representation: %v\n", cookieaeskeyprop, err)
		}

		if len(as) != base_def.HTTPD_AES_SIZE {
			// Delete
			err = config.DeleteProp(cookieaeskeyprop)
			if err != nil {
				slog.Fatalf("could not delete invalid size AES key: %v\n", err)
			} else {
				return establishHttpdKeys()
			}
		}
	}

	return hs, as
}

func blocklistUpdateEvent(path []string, val string, expires *time.Time) {
	data.LoadDNSBlocklist(data.DefaultDataDir)
}

func prometheusInit() {
	metrics.latencies = prometheus.NewSummary(prometheus.SummaryOpts{
		Name: "http_render_seconds",
		Help: "HTTP page render time",
	})
	prometheus.MustRegister(metrics.latencies)

	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(base_def.HTTPD_PROMETHEUS_PORT, nil)
}

func main() {
	var err error
	var rings cfgapi.RingMap

	flag.Var(ports, "http-ports", "The ports to listen on for HTTP requests.")
	flag.Parse()
	slog = aputil.NewLogger(pname)
	defer slog.Sync()
	slog.Infof("starting")

	*clientWebDir = aputil.ExpandDirPath(*clientWebDir)

	mcpd, err := mcp.New(pname)
	if err != nil {
		slog.Warnf("Failed to connect to mcp\n")
	}

	prometheusInit()

	// Set up connection with the broker daemon
	brokerd := broker.New(pname)
	brokerd.Handle(base_def.TOPIC_PING, handlePing)
	brokerd.Handle(base_def.TOPIC_CONFIG, handleConfig)
	brokerd.Handle(base_def.TOPIC_ENTITY, handleEntity)
	brokerd.Handle(base_def.TOPIC_RESOURCE, handleResource)
	brokerd.Handle(base_def.TOPIC_REQUEST, handleRequest)
	brokerd.Handle(base_def.TOPIC_ERROR, handleError)
	defer brokerd.Fini()

	config, err = apcfg.NewConfigd(brokerd, pname, cfgapi.AccessInternal)
	if err == nil {
		rings = config.GetRings()
	}

	if rings == nil {
		mcpd.SetState(mcp.BROKEN)
		if err != nil {
			slog.Fatalf("cannot connect to configd: %v\n", err)
		} else {
			slog.Fatal("can't get ring configuration\n")
		}
	}

	domainname, err = config.GetDomain()
	if err != nil {
		mcpd.SetState(mcp.BROKEN)
		slog.Fatalf("failed to fetch gateway domain: %v\n", err)
	}
	demoHostname := fmt.Sprintf("gateway.%s", domainname)
	keyfn, _, _, fullchainfn, err := certificate.GetKeyCertPaths(brokerd, demoHostname, time.Now(), false)
	if err != nil {
		// We can still run plain HTTP ports, such as the developer port.
		slog.Warnf("Couldn't get SSL key/fullchain: %v", err)
	}

	data.LoadDNSBlocklist(data.DefaultDataDir)
	config.HandleChange(`^@/updates/dns_.*list$`, blocklistUpdateEvent)

	secureMW := secure.New(secure.Options{
		SSLRedirect:           true,
		HostsProxyHeaders:     []string{"X-Forwarded-Host"},
		STSSeconds:            315360000,
		STSIncludeSubdomains:  true,
		STSPreload:            true,
		FrameDeny:             true,
		ContentTypeNosniff:    true,
		BrowserXssFilter:      true,
		ContentSecurityPolicy: contentSecurityPolicy,
	})

	// routing
	mainRouter := mux.NewRouter()

	demoAPIRouter := makeDemoAPIRouter()
	applianceAuthRouter := makeApplianceAuthRouter()

	phishRouter := mainRouter.MatcherFunc(
		func(r *http.Request, match *mux.RouteMatch) bool {
			return data.BlockedHostname(r.Host)
		}).Subrouter()
	phishRouter.HandleFunc("/", phishHandler)

	mainRouter.HandleFunc("/", defaultHandler)
	mainRouter.HandleFunc("/stats", statsHandler).Methods("GET")
	mainRouter.PathPrefix("/api/").Handler(
		http.StripPrefix("/api", demoAPIRouter))
	mainRouter.PathPrefix("/auth/").Handler(
		http.StripPrefix("/auth", applianceAuthRouter))
	mainRouter.PathPrefix("/client-web/").Handler(
		http.StripPrefix("/client-web/",
			gziphandler.GzipHandler(
				http.FileServer(http.Dir(*clientWebDir)))))

	hashKey, blockKey := establishHttpdKeys()

	cutter = securecookie.New(hashKey, blockKey)

	nMain := negroni.New(negroni.NewRecovery())
	nMain.Use(negroni.HandlerFunc(secureMW.HandlerFuncWithNext))
	nMain.UseHandler(apachelog.CombinedLog.Wrap(mainRouter, os.Stderr))

	tlsCfg := &tls.Config{
		MinVersion:               tls.VersionTLS12,
		CurvePreferences:         []tls.CurveID{tls.CurveP521, tls.CurveP384, tls.CurveP256},
		PreferServerCipherSuites: true,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
	}

	for ring, config := range rings {
		router := network.SubnetRouter(config.Subnet)
		// The secure middleware effectively links the ports, as
		// http/80 requests redirect to https/443.
		for _, port := range ports {
			listen(router, port, ring, tlsCfg, fullchainfn, keyfn, nMain)
		}
	}

	if *developerHTTP != "" {
		developerMW := secure.New(secure.Options{
			HostsProxyHeaders:     []string{"X-Forwarded-Host"},
			STSSeconds:            315360000,
			STSIncludeSubdomains:  true,
			STSPreload:            true,
			FrameDeny:             true,
			ContentTypeNosniff:    true,
			BrowserXssFilter:      true,
			ContentSecurityPolicy: contentSecurityPolicy,
			IsDevelopment:         true,
		})

		nDev := negroni.New(negroni.NewRecovery())
		nDev.Use(negroni.HandlerFunc(developerMW.HandlerFuncWithNext))
		nDev.UseHandler(apachelog.CombinedLog.Wrap(mainRouter, os.Stderr))

		slog.Debugf("Developer Port configured at %s", *developerHTTP)
		go func() {
			err := http.ListenAndServe(*developerHTTP, nDev)
			slog.Infof("Developer listener on %s exited: %v\n",
				*developerHTTP, err)
		}()
	}

	mcpd.SetState(mcp.ONLINE)

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	s := <-sig
	slog.Fatalf("Signal (%v) received", s)
}
