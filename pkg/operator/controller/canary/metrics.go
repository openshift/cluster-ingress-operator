package canary

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	ctrlruntimemetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	CanaryRequestTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ingress_canary_check_duration",
			Help:    "Canary endpoint request time in ms",
			Buckets: []float64{25, 50, 100, 200, 400, 800, 1600},
		}, []string{"host"})

	CanaryEndpointWrongPortEcho = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "ingress_canary_endpoint_wrong_port_echo",
			Help: "The ingress canary application received a test request on an incorrect port which may indicate that the router is \"wedged\"",
		})

	CanaryRouteReachable = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ingress_canary_route_reachable",
			Help: "A gauge set to 0 or 1 to signify whether or not the canary application is reachable via a route",
		}, []string{"host"})

	CanaryRouteDNSError = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ingress_canary_route_DNS_error",
			Help: "A counter tracking canary route DNS lookup errors",
		}, []string{"host", "dnsServer"})

	// Populate prometheus collector.
	// Individual metrics are stored as public variables
	// so that metrics can be globally controlled.
	metricsList = []prometheus.Collector{
		CanaryRequestTime,
		CanaryEndpointWrongPortEcho,
		CanaryRouteReachable,
		CanaryRouteDNSError,
	}
)

// SetCanaryRouteMetric is a wrapper function to
// mark the canary route as either online or offline.
func SetCanaryRouteReachableMetric(host string, status bool) {
	if status {
		CanaryRouteReachable.WithLabelValues(host).Set(1)
	} else {
		CanaryRouteReachable.WithLabelValues(host).Set(0)
	}
}

// registerCanaryMetrics calls prometheus.Register
// on each metric in metricsList, and returns on errors.
func registerCanaryMetrics() error {
	for _, metric := range metricsList {
		err := prometheus.Register(metric)
		if err != nil {
			return err
		}
	}
	return nil
}

// StartMetricsListener starts the metrics listener on addr.
func StartMetricsListener(addr string, stopCh chan struct{}) {
	// These metrics get registered in controller-runtime's registry via an init in the internal/controller/metrics package.
	// Unregister the controller-runtime metrics, so that we can combine the controller-runtime metric's registry
	// with that of the ingress-operator. This shouldn't have any side effects, as long as no 2 metrics across
	// controller runtime or the ingress operator share the same name (which is unlikely). See
	// https://github.com/kubernetes/test-infra/blob/master/prow/metrics/metrics.go for additional context.
	ctrlruntimemetrics.Registry.Unregister(prometheus.NewGoCollector())
	ctrlruntimemetrics.Registry.Unregister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	// Create prometheus handler by combining the ingress-operator registry
	// with the ingress-operator's controller runtime metrics registry.
	handler := promhttp.HandlerFor(
		prometheus.Gatherers{prometheus.DefaultGatherer, ctrlruntimemetrics.Registry},
		promhttp.HandlerOpts{},
	)

	log.Info("registering Prometheus metrics")
	if err := registerCanaryMetrics(); err != nil {
		log.Error(err, "unable to register metrics")
	}

	log.Info("starting metrics listener on ", "addr", addr)
	mux := http.NewServeMux()
	mux.Handle("/metrics", handler)
	s := http.Server{Addr: addr, Handler: mux}

	go func() {
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "metrics listener exited")
		}
	}()
	<-stopCh
	if err := s.Shutdown(context.Background()); err != http.ErrServerClosed {
		log.Error(err, "error stopping metrics listener")
	}
}
