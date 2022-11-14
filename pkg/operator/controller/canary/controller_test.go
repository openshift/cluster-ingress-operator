package canary

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestCycleServicePort(t *testing.T) {
	tPort1 := intstr.IntOrString{
		StrVal: "80",
	}
	tPort2 := intstr.IntOrString{
		StrVal: "8080",
	}
	tPort3 := intstr.IntOrString{
		StrVal: "8888",
	}
	testCases := []struct {
		description string
		route       *routev1.Route
		service     *corev1.Service
		success     bool
		index       int
	}{
		{
			description: "service with no ports",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Port: &routev1.RoutePort{},
				},
			},
			service: &corev1.Service{
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{},
				},
			},
			success: false,
		},
		{
			description: "route with no ports",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{},
			},
			service: &corev1.Service{
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{},
				},
			},
			success: false,
		},
		{
			description: "service has one port",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Port: &routev1.RoutePort{},
				},
			},
			service: &corev1.Service{
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							TargetPort: tPort1,
						},
					},
				},
			},
			success: false,
		},
		{
			description: "service has two ports",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Port: &routev1.RoutePort{
						TargetPort: tPort1,
					},
				},
			},
			service: &corev1.Service{
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							TargetPort: tPort1,
						},
						{
							TargetPort: tPort2,
						},
					},
				},
			},
			success: true,
			index:   0,
		},
		{
			description: "service has three ports",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Port: &routev1.RoutePort{
						TargetPort: tPort3,
					},
				},
			},
			service: &corev1.Service{
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							TargetPort: tPort1,
						},
						{
							TargetPort: tPort2,
						},
						{
							TargetPort: tPort3,
						},
					},
				},
			},
			success: true,
			index:   2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			route, err := cycleServicePort(tc.service, tc.route)
			if tc.success {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				routeTargetPort := route.Spec.Port.TargetPort
				cycledIndex := (tc.index + 1) % len(tc.service.Spec.Ports)
				expectedPort := tc.service.Spec.Ports[cycledIndex].TargetPort
				if !cmp.Equal(expectedPort, routeTargetPort) {
					t.Errorf("expected route to have port %s, but it has port %s", expectedPort.String(), routeTargetPort.String())
				}
			} else if err == nil {
				t.Error("expected an error")
			}
		})
	}
}
