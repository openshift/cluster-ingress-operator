package routemetrics

import (
	"context"
	"fmt"
	"strings"
	"testing"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ctrltestutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/openshift/cluster-ingress-operator/test/unit"

	"github.com/openshift/api/operator"
	v1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/scheme"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Test_Reconcile verifies Reconcile
// Note: This is intended to be a generic unit test. Edge cases will be tested in unit tests for specific functions.
func Test_Reconcile(t *testing.T) {
	reconcileRequest := reconcile.Request{NamespacedName: types.NamespacedName{Name: "foo-ic", Namespace: "openshift-ingress-operator"}}
	testCases := []struct {
		name                 string
		request              reconcile.Request
		initObjs             []client.Object
		ingressController    *v1.IngressController
		routes               routev1.RouteList
		expectedMetricFormat string
	}{
		{
			name:    "ingress controller doesn't exist",
			request: reconcileRequest,
		},
		{
			name: "ingress controller is being deleted",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					IsDeleting().
					WithAdmitted(false).
					Build(),
			},
			request: reconcileRequest,
		},
		{
			name: "ingress controller is not admitted",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(false).
					Build(),
			},
			request: reconcileRequest,
		},
		{
			name: "ingress controller is admitted with no routes admitted",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 0),
		},
		{
			name: "ingress controller is admitted with a route admitted",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					Build(),
				unit.NewRouteBuilder().
					WithName("foo-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					Build(),
				unit.NewNamespaceBuilder().
					WithName("foo-ns").
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 1),
		},
		{
			name: "ingress controller with routeSelector and with no namespaceSelector with 2 routes with correct labels in namespace with no labels",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					WithRouteSelector("type", "shard").
					Build(),
				unit.NewRouteBuilder().
					WithName("foo-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					WithLabel("type", "shard").
					Build(),
				unit.NewRouteBuilder().
					WithName("bar-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					WithLabel("type", "shard").
					Build(),
				unit.NewNamespaceBuilder().
					WithName("foo-ns").
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 2),
		},
		{
			name: "ingress controller with no routeSelector and with namespaceSelector with route with no labels in namespace with correct labels",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					WithNamespaceSelector("type", "shard").
					Build(),
				unit.NewRouteBuilder().
					WithName("foo-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					Build(),
				unit.NewNamespaceBuilder().
					WithName("foo-ns").
					WithLabel("type", "shard").
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 1),
		},
		{
			name: "ingress controller with expression routeSelector and with no namespaceSelector with route with correct labels in namespace with no labels",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					WithRouteExpressionSelector("type", metav1.LabelSelectorOpIn, []string{"shard"}).
					Build(),
				unit.NewRouteBuilder().
					WithName("foo-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					WithLabel("type", "shard").
					Build(),
				unit.NewNamespaceBuilder().
					WithName("foo-ns").
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 1),
		},
		{
			name: "ingress controller with no routeSelector and with expression namespaceSelector with route with no labels in namespace with correct labels",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					WithNamespaceExpressionSelector("type", metav1.LabelSelectorOpIn, []string{"shard"}).
					Build(),
				unit.NewRouteBuilder().
					WithName("foo-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					Build(),
				unit.NewNamespaceBuilder().
					WithName("foo-ns").
					WithLabel("type", "shard").
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 1),
		},
		{
			name: "ingress controller with routeSelector and with namespaceSelector with route with correct labels in namespace with correct labels",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					WithRouteSelector("type", "shard").
					WithNamespaceSelector("type", "shard").
					Build(),
				unit.NewRouteBuilder().
					WithName("foo-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					WithLabel("type", "shard").
					Build(),
				unit.NewNamespaceBuilder().
					WithName("foo-ns").
					WithLabel("type", "shard").
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 1),
		},
		{
			name: "ingress controller with routeSelector and with no namespaceSelector with route with incorrect labels in namespace with no labels",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					WithRouteSelector("type", "shard").
					Build(),
				unit.NewRouteBuilder().
					WithName("foo-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					WithLabel("type", "not-shard").
					Build(),
				unit.NewNamespaceBuilder().
					WithName("foo-ns").
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 0),
		},
		{
			name: "ingress controller with no routeSelector and with namespaceSelector with route with no labels in namespace with incorrect labels",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					WithNamespaceSelector("type", "shard").
					Build(),
				unit.NewRouteBuilder().
					WithName("foo-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					Build(),
				unit.NewNamespaceBuilder().
					WithName("foo-ns").
					WithLabel("type", "not-shard").
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 0),
		},
		{
			name: "ingress controller with routeSelector and with namespaceSelector with route with correct labels in namespace with incorrect labels",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					WithRouteSelector("type", "shard").
					WithNamespaceSelector("type", "shard").
					Build(),
				unit.NewRouteBuilder().
					WithName("foo-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					WithLabel("type", "shard").
					Build(),
				unit.NewNamespaceBuilder().
					WithName("foo-ns").
					WithLabel("type", "not-shard").
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 0),
		},
		{
			name: "ingress controller with expression routeSelector and with expression namespaceSelector with route with correct labels in namespace with correct labels",
			initObjs: []client.Object{
				unit.NewIngressControllerBuilder().
					WithName("foo-ic").
					WithAdmitted(true).
					WithRouteExpressionSelector("type", metav1.LabelSelectorOpIn, []string{"shard"}).
					WithNamespaceExpressionSelector("type", metav1.LabelSelectorOpIn, []string{"shard"}).
					Build(),
				unit.NewRouteBuilder().WithName("foo-route").
					WithNamespace("foo-ns").
					WithAdmittedICs("foo-ic").
					WithLabel("type", "shard").
					Build(),
				unit.NewNamespaceBuilder().
					WithName("foo-ns").
					WithLabel("type", "shard").
					Build(),
			},
			request:              reconcileRequest,
			expectedMetricFormat: routePerShardMetric("foo-ic", 1),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err, _, cache := newFakeClient(tc.initObjs...)
			if err != nil {
				t.Fatalf("error creating fake client: %v", err)
			}
			r := reconciler{
				cache:            cache,
				routeToIngresses: make(map[types.NamespacedName]sets.String),
				namespace:        operatorcontroller.DefaultOperatorNamespace,
			}

			// Cleanup the routes per shard metrics.
			routeMetricsControllerRoutesPerShard.Reset()

			if _, err := r.Reconcile(context.Background(), tc.request); err != nil {
				t.Errorf("got unexpected error: %v", err)
			} else {
				err := testutil.CollectAndCompare(routeMetricsControllerRoutesPerShard, strings.NewReader(tc.expectedMetricFormat))
				if err != nil {
					t.Error(err)
				}
			}
		})
	}
}

// Test_routeStatusAdmitted verifies that routeStatusAdmitted behaves correctly.
func Test_routeStatusAdmitted(t *testing.T) {
	testCases := []struct {
		name                  string
		route                 routev1.Route
		ingressControllerName string
		expectedResult        bool
	}{
		{
			name: "route admitted by default",
			route: routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{
						{
							RouterName: "default",
							Conditions: []routev1.RouteIngressCondition{
								{
									Type:   routev1.RouteAdmitted,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
				},
			},
			ingressControllerName: "default",
			expectedResult:        true,
		},
		{
			name: "route not admitted by sharded",
			route: routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{
						{
							RouterName: "sharded",
							Conditions: []routev1.RouteIngressCondition{
								{
									Type:   routev1.RouteAdmitted,
									Status: corev1.ConditionFalse,
								},
							},
						},
					},
				},
			},
			ingressControllerName: "sharded",
			expectedResult:        false,
		},
		{
			name: "route admitted by default, not admitted by sharded",
			route: routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{
						{
							RouterName: "default",
							Conditions: []routev1.RouteIngressCondition{
								{
									Type:   routev1.RouteAdmitted,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
				},
			},
			ingressControllerName: "sharded",
			expectedResult:        false,
		},
		{
			name: "route not admitted by sharded without Conditions",
			route: routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{
						{
							RouterName: "sharded",
						},
					},
				},
			},
			ingressControllerName: "sharded",
			expectedResult:        false,
		},
		{
			name: "route not admitted by any shard",
			route: routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{},
				},
			},
			ingressControllerName: "default",
			expectedResult:        false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if actualResult := routeStatusAdmitted(tc.route, tc.ingressControllerName); actualResult != tc.expectedResult {
				t.Errorf("expected result %v, got %v", tc.expectedResult, actualResult)
			}
		})
	}
}

// newFakeClient builds a fake client and cache for testing.
func newFakeClient(initObjs ...client.Object) (error, client.Client, cache.Cache) {
	// Create fake client
	clientBuilder := fake.NewClientBuilder()
	s := scheme.Scheme
	if err := routev1.Install(s); err != nil {
		return err, nil, nil
	}
	if err := operator.Install(s); err != nil {
		return err, nil, nil
	}
	client := clientBuilder.WithScheme(s).WithObjects(initObjs...).Build()
	informer := informertest.FakeInformers{
		Scheme: client.Scheme(),
	}
	// Create fake cache
	cache := ctrltestutil.FakeCache{Informers: &informer, Reader: client}

	return nil, client, cache
}

// routePerShardMetric returns a formatted Prometheus string for comparison
func routePerShardMetric(icName string, routesAdmitted int) string {
	return fmt.Sprintf(`
	# HELP route_metrics_controller_routes_per_shard Report the number of routes for shards (ingress controllers).
	# TYPE route_metrics_controller_routes_per_shard gauge
	route_metrics_controller_routes_per_shard{shard_name="%s"} %d
    `, icName, routesAdmitted)
}
