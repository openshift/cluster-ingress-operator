package routemetrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRouteMetricsControllerRoutesPerShardMetric(t *testing.T) {
	testCases := []struct {
		name                  string
		shardNames            []string
		actions               []string
		metricValues          []float64
		expectedMetricFormats []string
	}{
		{
			name:         "routes per shard metrics test shard",
			shardNames:   []string{"test", "test", "test", "test", "newtest1", "newtest2"},
			actions:      []string{"Set", "Set", "Set", "Delete", "Set", "Set"},
			metricValues: []float64{0, 2, 1, 0, 4, 5},
			expectedMetricFormats: []string{`
			# HELP route_metrics_controller_routes_per_shard Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard gauge
			route_metrics_controller_routes_per_shard{shard_name="test"} 0
			`, `
			# HELP route_metrics_controller_routes_per_shard Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard gauge
			route_metrics_controller_routes_per_shard{shard_name="test"} 2
			`, `
			# HELP route_metrics_controller_routes_per_shard Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard gauge
			route_metrics_controller_routes_per_shard{shard_name="test"} 1
			`, ``, `
			# HELP route_metrics_controller_routes_per_shard Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard gauge
			route_metrics_controller_routes_per_shard{shard_name="newtest1"} 4
			`, `
			# HELP route_metrics_controller_routes_per_shard Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard gauge
			route_metrics_controller_routes_per_shard{shard_name="newtest1"} 4
			route_metrics_controller_routes_per_shard{shard_name="newtest2"} 5
			`},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// cleanup the routes per shard metrics
			routeMetricsControllerRoutesPerShard.Reset()

			// Iterate through each action and compare the output with the corresponding expected metrics format.
			for index, action := range tc.actions {
				switch action {
				case "Set":
					SetRouteMetricsControllerRoutesPerShardMetric(tc.shardNames[index], tc.metricValues[index])

					err := testutil.CollectAndCompare(routeMetricsControllerRoutesPerShard, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}

				case "Delete":
					DeleteRouteMetricsControllerRoutesPerShardMetric(tc.shardNames[index])

					err := testutil.CollectAndCompare(routeMetricsControllerRoutesPerShard, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}
				}

			}
		})
	}
}
