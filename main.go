//go:generate stringer -type MystromReqStatus main.go
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"

	"mystrom-exporter/pkg/discover"
	"mystrom-exporter/pkg/mystrom"
	"mystrom-exporter/pkg/version"
)

// MystromReqStatus -- represents the request to MyStrom device status
type MystromReqStatus uint32

// values for the MystromReqStatus
const (
	OK MystromReqStatus = iota
	ErrorSocket
	ErrorTimeout
	ErrorParsingValue
)

const namespace = "mystrom_exporter"

var (
	listenAddress = flag.String("web.listen-address", ":9452",
		"Address to listen on")
	metricsPath = flag.String("web.metrics-path", "/metrics",
		"Path under which to expose exporters own metrics")
	devicePath = flag.String("web.device-path", "/device",
		"Path under which the metrics of the devices are fetched")
	showVersion = flag.Bool("version", false,
		"Show version information.")
	enableDiscovery = flag.Bool("discovery.enabled", false,
		"Enable the mystrom autodiscovery")
)
var (
	mystromDurationCounterVec *prometheus.CounterVec
	mystromRequestsCounterVec *prometheus.CounterVec
)
var landingPage = []byte(`<html>
<head>
	<title>myStrom switch report Exporter</title>
	<style>
		label{
		display:inline-block;
		width:75px;
		}
		form label {
		margin: 10px;
		}
		form input {
		margin: 10px;
		}
	</style>
</head>
<body>
<h1>myStrom Exporter</h1>
<form action="` + *devicePath + `">
	<label>Target:</label> <input type="text" name="target" placeholder="X.X.X.X" value="1.2.3.4"><br>
	<input type="submit" value="Submit">
</form>
<p><a href='` + *metricsPath + `'>Metrics</a></p>
</body>
</html>`)

func main() {
	flag.Parse()

	// log.Base().SetLevel("debug")

	// -- show version information
	if *showVersion {
		v, err := version.Print("mystrom_exporter")
		if err != nil {
			log.Fatalf("Failed to print version information: %#v", err)
		}

		fmt.Fprintln(os.Stdout, v)
		os.Exit(0)
	}

	// -- create a new registry for the exporter telemetry
	telemetryRegistry := setupMetrics()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	// -- startup the discover engine
	if *enableDiscovery {
		discover.Initialize(*listenAddress)
	}

	// -- create the mux router config
	router := mux.NewRouter()
	router.Handle(*metricsPath, promhttp.HandlerFor(telemetryRegistry, promhttp.HandlerOpts{}))
	router.HandleFunc(*devicePath, scrapeHandler)
	if *enableDiscovery {
		router.HandleFunc("/device_by_mac/{macaddr}", scrapeHandlerByMac)
		router.HandleFunc("/discover", discoverHandler)
	}
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(landingPage)
	})

	defer os.Exit(0)
	defer func() {
		log.Info("exiting.")
	}()
	if *enableDiscovery {
		defer discover.ConnClose()
	}

	go func() {
		log.Infoln("Listening on address " + *listenAddress)
		if err := http.ListenAndServe(*listenAddress, router); err != nil {
			log.Fatal(err)
		}
	}()

	<-c
}

// scrapeHandlerByMac --
func scrapeHandlerByMac(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)

	target := discover.TargetByMacaddr(params["macaddr"])

	rq := r.URL.Query()
	rq.Set("target", target)
	r.URL.RawQuery = rq.Encode()

	scrapeHandler(w, r)
}

// scrapeHandler --
func scrapeHandler(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "'target' parameter must be specified", http.StatusBadRequest)
		return
	}

	log.Infof("got scrape request for target '%v'", target)
	exporter := mystrom.NewExporter(target)

	start := time.Now()
	gatherer, err := exporter.Scrape()
	duration := time.Since(start).Seconds()
	if err != nil {
		if strings.Contains(fmt.Sprintf("%v", err), "unable to connect with target") {
			mystromRequestsCounterVec.WithLabelValues(target, ErrorSocket.String()).Inc()
		} else if strings.Contains(fmt.Sprintf("%v", err), "i/o timeout") {
			mystromRequestsCounterVec.WithLabelValues(target, ErrorTimeout.String()).Inc()
		} else {
			mystromRequestsCounterVec.WithLabelValues(target, ErrorParsingValue.String()).Inc()
		}
		http.Error(
			w,
			fmt.Sprintf("failed to scrape target '%v': %v", target, err),
			http.StatusInternalServerError,
		)
		log.Error(err)
		return
	}
	mystromDurationCounterVec.WithLabelValues(target).Add(duration)
	mystromRequestsCounterVec.WithLabelValues(target, OK.String()).Inc()

	promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

// -- setupMetrics creates a new registry for the exporter telemetry
func setupMetrics() *prometheus.Registry {
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGoCollector())
	registry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	mystromDurationCounterVec = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "request_duration_seconds_total",
			Help:      "Total duration of mystrom successful requests by target in seconds",
		},
		[]string{"target"})
	registry.MustRegister(mystromDurationCounterVec)

	mystromRequestsCounterVec = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "requests_total",
			Help:      "Number of mystrom request by status and target",
		},
		[]string{"target", "status"})
	registry.MustRegister(mystromRequestsCounterVec)

	// -- make the build information is available through a metric
	buildInfo := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "build_info",
			Help:      "A metric with a constant '1' value labeled by build information.",
		},
		[]string{"version", "revision", "branch", "goversion", "builddate", "builduser"},
	)
	buildInfo.WithLabelValues(version.Version, version.Revision, version.Branch, version.GoVersion, version.BuildDate, version.BuildUser).Set(1)
	registry.MustRegister(buildInfo)

	return registry
}

// discoerHandler
func discoverHandler(w http.ResponseWriter, r *http.Request) {
	log.Infof("got discover request from '%v' for %v", r.Host, r.URL.String())
	if data, e := discover.Discover(); e == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
		return
	}
	w.WriteHeader(http.StatusInternalServerError)

}
