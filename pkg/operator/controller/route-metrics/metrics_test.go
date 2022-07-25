package routemetrics

import (
	"strings"
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRouteMetricsControllerRoutesPerShardMetric(t *testing.T) {
	testCases := []struct {
		name                  string
		shardName             string
		actions               []string
		expectedMetricFormats []string
	}{
		{
			name:      "routes per shard metrics test shard",
			shardName: "test",
			actions:   []string{"Initialize", "Inc", "Dec", "Delete"},
			expectedMetricFormats: []string{`
			# HELP route_metrics_controller_routes_per_shard_total Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard_total gauge
			route_metrics_controller_routes_per_shard_total{name="test"} 0
			`, `
			# HELP route_metrics_controller_routes_per_shard_total Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard_total gauge
			route_metrics_controller_routes_per_shard_total{name="test"} 1
			`, `
			# HELP route_metrics_controller_routes_per_shard_total Report the number of routes for shards (ingress controllers).
			# TYPE route_metrics_controller_routes_per_shard_total gauge
			route_metrics_controller_routes_per_shard_total{name="test"} 0
			`, ``},
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

				case "Inc":
					IncrementRouteMetricsControllerRoutesPerShardMetric(tc.shardName)

					err := testutil.CollectAndCompare(routeMetricsControllerRoutesPerShard, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}

				case "Dec":
					DecrementRouteMetricsControllerRoutesPerShardMetric(tc.shardName)

					err := testutil.CollectAndCompare(routeMetricsControllerRoutesPerShard, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}

				case "Delete":
					DeleteRouteMetricsControllerRoutesPerShardMetric(tc.shardName)

					err := testutil.CollectAndCompare(routeMetricsControllerRoutesPerShard, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}
				}

			}
		})
	}
}

func TestRouteMetricsControllerRouteTypeMetric(t *testing.T) {
	testCases := []struct {
		name                  string
		tlsType               routev1.TLSTerminationType
		actions               []string
		expectedMetricFormats []string
	}{
		{
			name:    "route type metrics edge",
			tlsType: routev1.TLSTerminationEdge,
			actions: []string{"Inc", "Dec"},
			expectedMetricFormats: []string{`
			# HELP route_metrics_controller_route_type_total Report the number of routes for tls termination type.
			# TYPE route_metrics_controller_route_type_total gauge
			route_metrics_controller_route_type_total{type="edge"} 1
			`, `
			# HELP route_metrics_controller_route_type_total Report the number of routes for tls termination type.
			# TYPE route_metrics_controller_route_type_total gauge
			route_metrics_controller_route_type_total{type="edge"} 0
			`},
		},
		{
			name:    "route type metrics passthrough",
			tlsType: routev1.TLSTerminationPassthrough,
			actions: []string{"Inc", "Dec"},
			expectedMetricFormats: []string{`
			# HELP route_metrics_controller_route_type_total Report the number of routes for tls termination type.
			# TYPE route_metrics_controller_route_type_total gauge
			route_metrics_controller_route_type_total{type="passthrough"} 1
			`, `
			# HELP route_metrics_controller_route_type_total Report the number of routes for tls termination type.
			# TYPE route_metrics_controller_route_type_total gauge
			route_metrics_controller_route_type_total{type="passthrough"} 0
			`},
		},
		{
			name:    "route type metrics reencrypt",
			tlsType: routev1.TLSTerminationReencrypt,
			actions: []string{"Inc", "Dec"},
			expectedMetricFormats: []string{`
			# HELP route_metrics_controller_route_type_total Report the number of routes for tls termination type.
			# TYPE route_metrics_controller_route_type_total gauge
			route_metrics_controller_route_type_total{type="reencrypt"} 1
			`, `
			# HELP route_metrics_controller_route_type_total Report the number of routes for tls termination type.
			# TYPE route_metrics_controller_route_type_total gauge
			route_metrics_controller_route_type_total{type="reencrypt"} 0
			`},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// cleanup the route type metrics
			routeMetricsControllerRouteType.Reset()

			// Iterate through each action and compare the output with the corresponding expected metrics format.
			for index, action := range tc.actions {
				switch action {
				case "Inc":
					IncrementRouteMetricsControllerRouteTypeMetric(tc.tlsType)

					err := testutil.CollectAndCompare(routeMetricsControllerRouteType, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}

				case "Dec":
					DecrementRouteMetricsControllerRouteTypeMetric(tc.tlsType)

					err := testutil.CollectAndCompare(routeMetricsControllerRouteType, strings.NewReader(tc.expectedMetricFormats[index]))
					if err != nil {
						t.Error(err)
					}
				}

			}
		})
	}
}
