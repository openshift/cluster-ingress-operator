package routestatus

import (
	"context"
	"fmt"
	"testing"

	v1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/test/unit"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stretchr/testify/assert"
)

var (
	ns1Name              = "ns1"
	ns2Name              = "ns2"
	fooRouteNsName       = types.NamespacedName{Name: "foo-route", Namespace: ns1Name}
	barRouteNsName       = types.NamespacedName{Name: "bar-route", Namespace: ns1Name}
	barRouteNs2Name      = types.NamespacedName{Name: "bar-route", Namespace: ns2Name}
	fooIcName            = "foo-ic"
	fooIcNsName          = types.NamespacedName{Name: fooIcName, Namespace: operatorcontroller.DefaultOperatorNamespace}
	shardLabel           = map[string]string{"type": "shard-label"}
	notShardLabel        = map[string]string{"type": "not-shard-label"}
	shardLabelExpression = metav1.LabelSelectorRequirement{
		Key:      "type",
		Operator: metav1.LabelSelectorOpIn,
		Values:   []string{"shard-label"},
	}
	invalidExpression = metav1.LabelSelectorRequirement{
		Key:      "type",
		Operator: "invalid-operator",
		Values:   []string{"shard-label"},
	}
)

// Test_ClearAllRoutesStatusForIngressController verifies that ClearAllRoutesStatusForIngressController behaves correctly.
func Test_ClearAllRoutesStatusForIngressController(t *testing.T) {
	testCases := []struct {
		name              string
		routes            routev1.RouteList
		ingressController *v1.IngressController
		expectedRoutes    routev1.RouteList
	}{
		{
			name: "clear ingress controller foo status that admitted route test",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithAdmittedICs("baz-ic", "bar-ic").Build(),
				},
			},
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).Build(),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs("bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithAdmittedICs("baz-ic", "bar-ic").Build(),
				},
			},
		},
		{
			name: "don't clear ingress controller foo status that didn't admit anything",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs("dog-ic", "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithAdmittedICs("baz-ic", "bar-ic").Build(),
				},
			},
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).Build(),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs("dog-ic", "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithAdmittedICs("baz-ic", "bar-ic").Build(),
				},
			},
		},
		{
			name: "clear ingress controller foo status that admitted everything",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcName).Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithAdmittedICs(fooIcName).Build(),
				},
			},
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).Build(),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).Build(),
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Load up client with test objects.
			client := unit.NewFakeClient(tc.ingressController)
			for _, route := range tc.routes.Items {
				if err := client.Create(context.Background(), &route); err != nil {
					t.Fatalf("error creating route: %v", err)
				}
			}

			if errs := ClearAllRoutesStatusForIngressController(context.Background(), client, tc.ingressController.Name); len(errs) != 0 {
				t.Fatalf("did not expected errors: %v", errs)
			}

			actualRoutes := routev1.RouteList{}
			if err := client.List(context.Background(), &actualRoutes); err != nil {
				t.Fatalf("error retrieving routes from client: %v", err)
			}
			for _, expectedRoute := range tc.expectedRoutes.Items {
				// Find the actual route that we should compare status with this expected route
				var actualRoute routev1.Route
				for _, route := range actualRoutes.Items {
					if route.Name == expectedRoute.Name {
						actualRoute = route
					}
				}
				assert.Equal(t, expectedRoute.Status, actualRoute.Status, "route name", expectedRoute.Name)
			}
		})
	}
}

// Test_ClearRoutesNotAdmittedByIngress verifies that ClearRoutesNotAdmittedByIngress behaves correctly.
func Test_ClearRoutesNotAdmittedByIngress(t *testing.T) {
	testCases := []struct {
		name              string
		routes            routev1.RouteList
		namespace         corev1.NamespaceList
		ingressController *v1.IngressController
		expectedRoutes    routev1.RouteList
		expectedErr       bool
	}{
		{
			name: "don't clear anything: all IC admissions are valid",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcName, "baz-ic", "bar-ic").Build(),
				},
			},
			namespace: corev1.NamespaceList{
				Items: []corev1.Namespace{
					*unit.NewNamespaceBuilder().WithName(ns1Name).WithLabels(shardLabel).Build(),
				},
			},
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteSelectors(shardLabel).WithNamespaceSelectors(shardLabel).Build(),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcName, "baz-ic", "bar-ic").Build(),
				},
			},
		},
		{
			name: "clear route that is no longer admitted by ingress controller by routeSelectors",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithAdmittedICs(fooIcName, "baz-ic", "bar-ic").Build(),
				},
			},
			namespace: corev1.NamespaceList{
				Items: []corev1.Namespace{
					*unit.NewNamespaceBuilder().WithName(ns1Name).Build(),
				},
			},
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteSelectors(shardLabel).Build(),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithAdmittedICs("baz-ic", "bar-ic").Build(),
				},
			},
		},
		{
			name: "clear route that is no longer admitted by ingress controller by routeSelectors expression",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithAdmittedICs(fooIcName, "baz-ic", "bar-ic").Build(),
				},
			},
			namespace: corev1.NamespaceList{
				Items: []corev1.Namespace{
					*unit.NewNamespaceBuilder().WithName(ns1Name).Build(),
				},
			},
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteExpressionSelector(shardLabelExpression).Build(),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNsName).WithAdmittedICs("baz-ic", "bar-ic").Build(),
				},
			},
		},
		{
			name: "clear route that is no longer admitted by ingress controller by namespaceSelectors",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNs2Name).WithAdmittedICs(fooIcName, "baz-ic", "bar-ic").Build(),
				},
			},
			namespace: corev1.NamespaceList{
				Items: []corev1.Namespace{
					*unit.NewNamespaceBuilder().WithName(ns1Name).WithLabels(shardLabel).Build(),
					*unit.NewNamespaceBuilder().WithName(ns2Name).Build(),
				},
			},
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithNamespaceSelectors(shardLabel).Build(),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNs2Name).WithAdmittedICs("baz-ic", "bar-ic").Build(),
				},
			},
		},
		{
			name: "clear route that is no longer admitted by ingress controller by namespaceSelectors expression",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNs2Name).WithAdmittedICs(fooIcName, "baz-ic", "bar-ic").Build(),
				},
			},
			namespace: corev1.NamespaceList{
				Items: []corev1.Namespace{
					*unit.NewNamespaceBuilder().WithName(ns1Name).WithLabels(shardLabel).Build(),
					*unit.NewNamespaceBuilder().WithName(ns2Name).Build(),
				},
			},
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithNamespaceExpressionSelector(shardLabelExpression).Build(),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcName, "bar-ic").Build(),
					*unit.NewRouteBuilder().WithName(barRouteNs2Name).WithAdmittedICs("baz-ic", "bar-ic").Build(),
				},
			},
		},
		{
			name:              "nil ingress controller",
			routes:            routev1.RouteList{},
			namespace:         corev1.NamespaceList{},
			ingressController: nil,
			expectedRoutes:    routev1.RouteList{},
			expectedErr:       true,
		},
		{
			name:              "ingress controller with invalid route selector",
			routes:            routev1.RouteList{},
			namespace:         corev1.NamespaceList{},
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteExpressionSelector(invalidExpression).Build(),
			expectedRoutes:    routev1.RouteList{},
			expectedErr:       true,
		},
		{
			name:              "ingress controller with invalid route selector",
			routes:            routev1.RouteList{},
			namespace:         corev1.NamespaceList{},
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithNamespaceExpressionSelector(invalidExpression).Build(),
			expectedRoutes:    routev1.RouteList{},
			expectedErr:       true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Load up client with test objects.
			client := unit.NewFakeClient()
			if tc.ingressController != nil {
				if err := client.Create(context.Background(), tc.ingressController); err != nil {
					t.Fatalf("error ingress controller route: %v", err)
				}
			}
			for _, route := range tc.routes.Items {
				if err := client.Create(context.Background(), &route); err != nil {
					t.Fatalf("error creating route: %v", err)
				}
			}
			for _, ns := range tc.namespace.Items {
				if err := client.Create(context.Background(), &ns); err != nil {
					t.Fatalf("error creating ns: %v", err)
				}
			}

			if errs := ClearRoutesNotAdmittedByIngress(context.Background(), client, tc.ingressController); tc.expectedErr && len(errs) == 0 {
				t.Fatal("expected errors, got no errors")
			} else if !tc.expectedErr && len(errs) != 0 {
				t.Fatalf("did not expected errors: %v", errs)
			}

			actualRoutes := routev1.RouteList{}
			if err := client.List(context.Background(), &actualRoutes); err != nil {
				t.Fatalf("error retrieving routes from client: %v", err)
			}
			for _, expectedRoute := range tc.expectedRoutes.Items {
				// Find the actual route that we should compare status with this expected route
				var actualRoute routev1.Route
				for _, route := range actualRoutes.Items {
					if route.Name == expectedRoute.Name {
						actualRoute = route
					}
				}
				assert.Equal(t, expectedRoute.Status, actualRoute.Status, "route name", expectedRoute.Name)
			}
		})
	}
}

// Test_IsRouteStatusAdmitted verifies that IsRouteStatusAdmitted behaves correctly.
func Test_IsRouteStatusAdmitted(t *testing.T) {
	testCases := []struct {
		name                  string
		route                 routev1.Route
		ingressControllerName string
		expectedResult        bool
	}{
		{
			name:                  "route admitted",
			route:                 *unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcName).Build(),
			ingressControllerName: fooIcName,
			expectedResult:        true,
		},
		{
			name:                  "route with false admitted condition",
			route:                 *unit.NewRouteBuilder().WithName(fooRouteNsName).WithUnAdmittedICs(fooIcName).Build(),
			ingressControllerName: fooIcName,
			expectedResult:        false,
		},
		{
			name:                  "route admitted by foo, not admitted by bar",
			route:                 *unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcName).Build(),
			ingressControllerName: "bar-ic",
			expectedResult:        false,
		},
		{
			name: "route not admitted without conditions",
			route: routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{
						{
							RouterName: fooIcName,
						},
					},
				},
			},
			ingressControllerName: fooIcName,
			expectedResult:        false,
		},
		{
			name:                  "route not admitted by any ic",
			route:                 *unit.NewRouteBuilder().WithName(fooRouteNsName).Build(),
			ingressControllerName: fooIcName,
			expectedResult:        false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if actualResult := IsRouteStatusAdmitted(tc.route, tc.ingressControllerName); actualResult != tc.expectedResult {
				t.Fatalf("expected result %v, got %v", tc.expectedResult, actualResult)
			}
		})
	}
}

// Test_IsInvalidSelectorError verifies that IsInvalidSelectorError behaves correctly.
func Test_IsInvalidSelectorError(t *testing.T) {
	testCases := []struct {
		name                         string
		error                        error
		expectedInvalidSelectorError bool
	}{
		{
			name:  "is not InvalidSelectorError",
			error: fmt.Errorf("not InvalidSelectorError"),
		},
		{
			name:  "is InvalidSelectorError",
			error: &InvalidSelectorError{"InvalidSelectorError"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if isInvalidSelectorError := IsInvalidSelectorError(tc.error); tc.expectedInvalidSelectorError && !isInvalidSelectorError {
				t.Fatal("expected InvalidSelectorError, was not InvalidSelectorError")
			}
		})
	}
}
