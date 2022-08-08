package routemetrics

import (
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

var (
	// routeMetricsControllerRoutesPerShard reports the number of routes belonging to each
	// Shard (IngressController) using the route_metrics_controller_routes_per_shard metric.
	routeMetricsControllerRoutesPerShard = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "route_metrics_controller_routes_per_shard",
		Help: "Report the number of routes for shards (ingress controllers).",
	}, []string{"name"})

	// metricsList is a list of metrics for this package.
	metricsList = []prometheus.Collector{
		routeMetricsControllerRoutesPerShard,
	}
)

func InitializeRouteMetricsControllerRoutesPerShardMetric(shardName string) {
	SetRouteMetricsControllerRoutesPerShardMetric(shardName, 0)
}

func GetRouteMetricsControllerRoutesPerShardMetric(shardName string) float64 {
	metric := &dto.Metric{}
	routeMetricsControllerRoutesPerShard.WithLabelValues(shardName).Write(metric)
	return *metric.Gauge.Value

}

func SetRouteMetricsControllerRoutesPerShardMetric(shardName string, value float64) {
	routeMetricsControllerRoutesPerShard.WithLabelValues(shardName).Set(value)
}

func DeleteRouteMetricsControllerRoutesPerShardMetric(shardName string) {
	routeMetricsControllerRoutesPerShard.DeleteLabelValues(shardName)
}

func IncrementRouteMetricsControllerRoutesPerShardMetric(shardName string) {
	routeMetricsControllerRoutesPerShard.WithLabelValues(shardName).Inc()
}

func DecrementRouteMetricsControllerRoutesPerShardMetric(shardName string) {
	routeMetricsControllerRoutesPerShard.WithLabelValues(shardName).Dec()
}

// RegisterMetrics calls prometheus.Register on each metric in metricsList, and
// returns on errors.
func RegisterMetrics() error {
	for _, metric := range metricsList {
		if err := prometheus.Register(metric); err != nil {
			return err
		}
	}
	return nil
}
