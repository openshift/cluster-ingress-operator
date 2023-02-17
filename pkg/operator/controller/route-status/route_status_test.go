package routestatus

import (
	"context"
	"fmt"
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
	v1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/test/unit"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test_ClearAllRoutesStatusForIngressController verifies that ClearAllRoutesStatusForIngressController behaves correctly.
func Test_ClearAllRoutesStatusForIngressController(t *testing.T) {
	testCases := []struct {
		name              string
		routes            routev1.RouteList
		ingressController *v1.IngressController
		expectedRoutes    routev1.RouteList
		expectedErr       bool
	}{
		{
			name: "clear ingress controller foo status that admitted route test",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithAdmittedStatuses("test", "foo-ic", "bar-ic"),
					*newRouteWithAdmittedStatuses("test2", "baz-ic", "bar-ic"),
				},
			},
			ingressController: &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo-ic",
					Namespace: operatorcontroller.DefaultOperatorNamespace,
				},
			},
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithAdmittedStatuses("test", "bar-ic"),
					*newRouteWithAdmittedStatuses("test2", "baz-ic", "bar-ic"),
				},
			},
		},
		{
			name: "don't clear ingress controller foo status that didn't admit anything",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithAdmittedStatuses("test", "dog-ic", "bar-ic"),
					*newRouteWithAdmittedStatuses("test2", "baz-ic", "bar-ic"),
				},
			},
			ingressController: &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo-ic",
					Namespace: operatorcontroller.DefaultOperatorNamespace,
				},
			},
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithAdmittedStatuses("test", "dog-ic", "bar-ic"),
					*newRouteWithAdmittedStatuses("test2", "baz-ic", "bar-ic"),
				},
			},
		},
		{
			name: "clear ingress controller foo status that admitted everything",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithAdmittedStatuses("test", "foo-ic"),
					*newRouteWithAdmittedStatuses("test2", "foo-ic"),
				},
			},
			ingressController: &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo-ic",
					Namespace: operatorcontroller.DefaultOperatorNamespace,
				},
			},
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithAdmittedStatuses("test"),
					*newRouteWithAdmittedStatuses("test2"),
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
					t.Errorf("error creating route: %v", err)
				}
			}

			if errs := ClearAllRoutesStatusForIngressController(context.Background(), client, tc.ingressController.Name); tc.expectedErr && len(errs) == 0 {
				t.Errorf("expected errors, got no errors")
			} else if !tc.expectedErr && len(errs) != 0 {
				t.Errorf("did not expected errors: %v", errs)
			}

			actualRoutes := routev1.RouteList{}
			if err := client.List(context.Background(), &actualRoutes); err == nil {
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
			} else {
				t.Errorf("error retrieving routes from client: %v", err)
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
					*newRouteWithLabelWithAdmittedStatuses("foo-route", "ns", "shard-label", "foo-ic", "bar-ic"),
					*newRouteWithLabelWithAdmittedStatuses("bar-route", "ns", "shard-label", "baz-ic", "foo-ic", "bar-ic"),
				},
			},
			namespace: corev1.NamespaceList{
				Items: []corev1.Namespace{
					*newNamespace("ns", "shard-label"),
				},
			},
			ingressController: newIngressControllerWithSelectors("foo-ic", "shard-label", "", "shard-label", ""),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithLabelWithAdmittedStatuses("foo-route", "ns", "shard-label", "foo-ic", "bar-ic"),
					*newRouteWithLabelWithAdmittedStatuses("bar-route", "ns", "shard-label", "baz-ic", "foo-ic", "bar-ic"),
				},
			},
		},
		{
			name: "clear route that is no longer admitted by ingress controller by routeSelectors",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithLabelWithAdmittedStatuses("foo-route", "ns", "shard-label", "foo-ic", "bar-ic"),
					*newRouteWithLabelWithAdmittedStatuses("bar-route", "ns", "", "baz-ic", "foo-ic", "bar-ic"),
				},
			},
			namespace: corev1.NamespaceList{
				Items: []corev1.Namespace{
					*newNamespace("ns", ""),
				},
			},
			ingressController: newIngressControllerWithSelectors("foo-ic", "shard-label", "", "", ""),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithLabelWithAdmittedStatuses("foo-route", "ns", "shard-label", "foo-ic", "bar-ic"),
					*newRouteWithLabelWithAdmittedStatuses("bar-route", "ns", "", "baz-ic", "bar-ic"),
				},
			},
		},
		{
			name: "clear route that is no longer admitted by ingress controller by routeSelectors expression",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithLabelWithAdmittedStatuses("foo-route", "ns", "shard-label", "foo-ic", "bar-ic"),
					*newRouteWithLabelWithAdmittedStatuses("bar-route", "ns", "", "baz-ic", "foo-ic", "bar-ic"),
				},
			},
			namespace: corev1.NamespaceList{
				Items: []corev1.Namespace{
					*newNamespace("ns", ""),
				},
			},
			ingressController: newIngressControllerWithSelectors("foo-ic", "", "shard-label", "", ""),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithLabelWithAdmittedStatuses("foo-route", "ns", "shard-label", "foo-ic", "bar-ic"),
					*newRouteWithLabelWithAdmittedStatuses("bar-route", "ns", "", "baz-ic", "bar-ic"),
				},
			},
		},
		{
			name: "clear route that is no longer admitted by ingress controller by namespaceSelectors",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithLabelWithAdmittedStatuses("foo-route", "ns", "", "foo-ic", "bar-ic"),
					*newRouteWithLabelWithAdmittedStatuses("bar-route", "ns2", "", "baz-ic", "foo-ic", "bar-ic"),
				},
			},
			namespace: corev1.NamespaceList{
				Items: []corev1.Namespace{
					*newNamespace("ns", "shard-label"),
					*newNamespace("ns2", ""),
				},
			},
			ingressController: newIngressControllerWithSelectors("foo-ic", "", "", "shard-label", ""),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithLabelWithAdmittedStatuses("foo-route", "ns", "", "foo-ic", "bar-ic"),
					*newRouteWithLabelWithAdmittedStatuses("bar-route", "ns", "", "baz-ic", "bar-ic"),
				},
			},
		},
		{
			name: "clear route that is no longer admitted by ingress controller by namespaceSelectors expression",
			routes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithLabelWithAdmittedStatuses("foo-route", "ns", "", "foo-ic", "bar-ic"),
					*newRouteWithLabelWithAdmittedStatuses("bar-route", "ns2", "", "baz-ic", "foo-ic", "bar-ic"),
				},
			},
			namespace: corev1.NamespaceList{
				Items: []corev1.Namespace{
					*newNamespace("ns", "shard-label"),
					*newNamespace("ns2", ""),
				},
			},
			ingressController: newIngressControllerWithSelectors("foo-ic", "", "", "", "shard-label"),
			expectedRoutes: routev1.RouteList{
				Items: []routev1.Route{
					*newRouteWithLabelWithAdmittedStatuses("foo-route", "ns", "", "foo-ic", "bar-ic"),
					*newRouteWithLabelWithAdmittedStatuses("bar-route", "ns", "", "baz-ic", "bar-ic"),
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
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Load up client with test objects.
			client := unit.NewFakeClient()
			if tc.ingressController != nil {
				if err := client.Create(context.Background(), tc.ingressController); err != nil {
					t.Errorf("error ingress controller route: %v", err)
				}
			}
			for _, route := range tc.routes.Items {
				if err := client.Create(context.Background(), &route); err != nil {
					t.Errorf("error creating route: %v", err)
				}
			}
			for _, ns := range tc.namespace.Items {
				if err := client.Create(context.Background(), &ns); err != nil {
					t.Errorf("error creating ns: %v", err)
				}
			}

			if errs := ClearRoutesNotAdmittedByIngress(context.Background(), client, tc.ingressController); tc.expectedErr && len(errs) == 0 {
				t.Errorf("expected errors, got no errors")
			} else if !tc.expectedErr && len(errs) != 0 {
				t.Errorf("did not expected errors: %v", errs)
			}

			actualRoutes := routev1.RouteList{}
			if err := client.List(context.Background(), &actualRoutes); err == nil {
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
			} else {
				t.Errorf("error retrieving all from client: %v", err)
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
			name:                  "route admitted by default",
			route:                 *newRouteWithAdmittedStatuses("foo-route", "default-ic"),
			ingressControllerName: "default-ic",
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
			name:                  "route admitted by default, not admitted by sharded",
			route:                 *newRouteWithAdmittedStatuses("foo-route", "default-ic"),
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
			ingressControllerName: "default-ic",
			expectedResult:        false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if actualResult := IsRouteStatusAdmitted(tc.route, tc.ingressControllerName); actualResult != tc.expectedResult {
				t.Errorf("expected result %v, got %v", tc.expectedResult, actualResult)
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
			name:  "Is not InvalidSelectorError",
			error: fmt.Errorf("not InvalidSelectorError"),
		},
		{
			name:  "Is InvalidSelectorError",
			error: &InvalidSelectorError{"InvalidSelectorError"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if isInvalidSelectorError := IsInvalidSelectorError(tc.error); tc.expectedInvalidSelectorError && !isInvalidSelectorError {
				t.Errorf("expected InvalidSelectorError, was not InvalidSelectorError")
			}
		})
	}
}

// newIngressControllerWithSelectors returns a new ingresscontroller with the specified name,
// routeSelectors, and namespaceSelectors based on the parameters.
func newIngressControllerWithSelectors(name, routeMatchLabel, routeMatchExpression, namespaceMatchLabel, namespaceMatchExpression string) *operatorv1.IngressController {
	ingresscontroller := operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: operatorcontroller.DefaultOperatorNamespace,
		},
	}
	if len(routeMatchLabel) != 0 {
		ingresscontroller.Spec.RouteSelector = &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"type": routeMatchLabel,
			},
		}
	}
	if len(routeMatchExpression) != 0 {
		ingresscontroller.Spec.RouteSelector = &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      "type",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{routeMatchExpression},
				},
			},
		}
	}
	if len(namespaceMatchLabel) != 0 {
		ingresscontroller.Spec.NamespaceSelector = &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"type": namespaceMatchLabel,
			},
		}
	}
	if len(namespaceMatchExpression) != 0 {
		ingresscontroller.Spec.NamespaceSelector = &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      "type",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{namespaceMatchExpression},
				},
			},
		}
	}
	return &ingresscontroller
}

// newNamespace returns a new namespace with the specified name
// and if label exists, with that label
func newNamespace(name, label string) *corev1.Namespace {
	namespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	if len(label) != 0 {
		namespace.ObjectMeta.Labels = map[string]string{"type": label}
	}
	return &namespace
}

// newRouteWithAdmittedStatuses returns a new route that is admitted by ingress controllers.
func newRouteWithLabelWithAdmittedStatuses(name, namespace, label string, icAdmitted ...string) *routev1.Route {
	route := newRouteWithAdmittedStatuses(name, icAdmitted...)
	route.ObjectMeta.Namespace = namespace
	if len(label) != 0 {
		route.Labels = map[string]string{
			"type": label,
		}
	}
	return route
}

// newRouteWithAdmittedStatuses returns a new route that is admitted by ingress controllers.
func newRouteWithAdmittedStatuses(name string, icAdmitted ...string) *routev1.Route {
	route := routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	if len(icAdmitted) != 0 {
		for _, ic := range icAdmitted {
			route.Status.Ingress = append(route.Status.Ingress, routev1.RouteIngress{
				RouterName: ic,
				Conditions: []routev1.RouteIngressCondition{
					{
						Type:   routev1.RouteAdmitted,
						Status: "True",
					},
				},
			})
		}
	}
	return &route
}
