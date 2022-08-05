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
	// Write will initialize routeMetricsControllerRoutesPerShard if not already initialized. If it is already initialized,
	// then it will just fetch the existing value.
	routeMetricsControllerRoutesPerShard.WithLabelValues(shardName).Write(&dto.Metric{})
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
