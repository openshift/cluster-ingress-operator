package routemetrics

import (
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
)

// TestRouteAdmitted verifies that routeAdmitted behaves correctly.
func TestRouteAdmitted(t *testing.T) {
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
			if actualResult := routeAdmitted(tc.route, tc.ingressControllerName); actualResult != tc.expectedResult {
				t.Errorf("expected result %v, got %v", tc.expectedResult, actualResult)
			}
		})
	}
}
