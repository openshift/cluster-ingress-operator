package canary

import (
	"github.com/prometheus/client_golang/prometheus"
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

// RegisterMetrics calls prometheus.Register on each metric in metricsList, and
// returns on errors.
func RegisterMetrics() error {
	for _, metric := range metricsList {
		err := prometheus.Register(metric)
		if err != nil {
			return err
		}
	}
	return nil
}
