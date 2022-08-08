package routemetrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRouteMetricsControllerRoutesPerShardMetric(t *testing.T) {
	testCases := []struct {
		name                  string
		shardName             string
		actions               []string
		expectedMetricFormats []string
		expectedMetricResults []float64
	}{
		{
			name:      "routes per shard metrics test shard",
			shardName: "test",
			actions:   []string{"Initialize", "Inc", "Dec", "Delete"},
			expectedMetricFormats: []string{`
			# HELP route_metrics_controller_routes_per_shard Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard gauge
			route_metrics_controller_routes_per_shard{name="test"} 0
			`, `
			# HELP route_metrics_controller_routes_per_shard Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard gauge
			route_metrics_controller_routes_per_shard{name="test"} 1
			`, `
			# HELP route_metrics_controller_routes_per_shard Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard gauge
			route_metrics_controller_routes_per_shard{name="test"} 0
			`, ``},
			expectedMetricResults: []float64{0, 1, 0, 0},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// cleanup the routes per shard metrics
			routeMetricsControllerRoutesPerShard.Reset()

			// Iterate through each action and compare the output with the corresponding expected metrics format.
			for index, action := range tc.actions {
				switch action {
				case "Initialize":
					InitializeRouteMetricsControllerRoutesPerShardMetric(tc.shardName)

					err := testutil.CollectAndCompare(routeMetricsControllerRoutesPerShard, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}

					if actualResult := GetRouteMetricsControllerRoutesPerShardMetric(tc.shardName); actualResult != tc.expectedMetricResults[index] {
						t.Errorf("expected result %v, got %v", tc.expectedMetricResults[index], actualResult)
					}

				case "Inc":
					IncrementRouteMetricsControllerRoutesPerShardMetric(tc.shardName)

					err := testutil.CollectAndCompare(routeMetricsControllerRoutesPerShard, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}

					if actualResult := GetRouteMetricsControllerRoutesPerShardMetric(tc.shardName); actualResult != tc.expectedMetricResults[index] {
						t.Errorf("expected result %v, got %v", tc.expectedMetricResults[index], actualResult)
					}

				case "Dec":
					DecrementRouteMetricsControllerRoutesPerShardMetric(tc.shardName)

					err := testutil.CollectAndCompare(routeMetricsControllerRoutesPerShard, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}

					if actualResult := GetRouteMetricsControllerRoutesPerShardMetric(tc.shardName); actualResult != tc.expectedMetricResults[index] {
						t.Errorf("expected result %v, got %v", tc.expectedMetricResults[index], actualResult)
					}

				case "Delete":
					DeleteRouteMetricsControllerRoutesPerShardMetric(tc.shardName)

					err := testutil.CollectAndCompare(routeMetricsControllerRoutesPerShard, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}

					if actualResult := GetRouteMetricsControllerRoutesPerShardMetric(tc.shardName); actualResult != tc.expectedMetricResults[index] {
						t.Errorf("expected result %v, got %v", tc.expectedMetricResults[index], actualResult)
					}
				}

			}
		})
	}
}
