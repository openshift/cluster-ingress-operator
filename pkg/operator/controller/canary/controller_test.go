package canary

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func Test_cycleServicePort(t *testing.T) {
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

func Test_deduplicateErrorStrings(t *testing.T) {
	now := time.Now()
	testCases := []struct {
		Name           string
		ErrorMessages  []timestampedError
		ExpectedResult []string
	}{
		{
			Name:           "Empty input",
			ErrorMessages:  []timestampedError{},
			ExpectedResult: []string{},
		},
		{
			Name: "Multiple errors, no repetition",
			ErrorMessages: []timestampedError{
				{err: fmt.Errorf("foo"), timestamp: now.Add(-10 * time.Second)},
				{err: fmt.Errorf("bar"), timestamp: now.Add(-9 * time.Second)},
				{err: fmt.Errorf("baz"), timestamp: now.Add(-8 * time.Second)},
				{err: fmt.Errorf("quux"), timestamp: now.Add(-7 * time.Second)},
			},
			ExpectedResult: []string{
				"foo",
				"bar",
				"baz",
				"quux",
			},
		},
		{
			Name: "All identical errors",
			ErrorMessages: []timestampedError{
				{err: fmt.Errorf("foo"), timestamp: now.Add(-10 * time.Second)},
				{err: fmt.Errorf("foo"), timestamp: now.Add(-9 * time.Second)},
				{err: fmt.Errorf("foo"), timestamp: now.Add(-8 * time.Second)},
				{err: fmt.Errorf("foo"), timestamp: now.Add(-7 * time.Second)},
			},
			ExpectedResult: []string{
				"foo (x4 over 10s)",
			},
		},
		{
			Name: "Multiple errors, with repetition",
			ErrorMessages: []timestampedError{
				{err: fmt.Errorf("foo"), timestamp: now.Add(-10 * time.Second)},
				{err: fmt.Errorf("bar"), timestamp: now.Add(-9 * time.Second)},
				{err: fmt.Errorf("foo"), timestamp: now.Add(-8 * time.Second)},
				{err: fmt.Errorf("baz"), timestamp: now.Add(-7 * time.Second)},
			},
			ExpectedResult: []string{
				"bar",
				"foo (x2 over 10s)",
				"baz",
			},
		},
		{
			Name: "Many errors, with repetition",
			ErrorMessages: []timestampedError{
				{err: fmt.Errorf("foo"), timestamp: now.Add(-20 * time.Second)},
				{err: fmt.Errorf("bar"), timestamp: now.Add(-19 * time.Second)},
				{err: fmt.Errorf("foo"), timestamp: now.Add(-18 * time.Second)},
				{err: fmt.Errorf("bar"), timestamp: now.Add(-17 * time.Second)},
				{err: fmt.Errorf("baz"), timestamp: now.Add(-16 * time.Second)},
				{err: fmt.Errorf("foo"), timestamp: now.Add(-15 * time.Second)},
				{err: fmt.Errorf("foo"), timestamp: now.Add(-14 * time.Second)},
				{err: fmt.Errorf("quux"), timestamp: now.Add(-13 * time.Second)},
				{err: fmt.Errorf("quux"), timestamp: now.Add(-12 * time.Second)},
				{err: fmt.Errorf("foo"), timestamp: now.Add(-11 * time.Second)},
				{err: fmt.Errorf("foo"), timestamp: now.Add(-10 * time.Second)},
				{err: fmt.Errorf("quux"), timestamp: now.Add(-9 * time.Second)},
			},
			ExpectedResult: []string{
				"bar (x2 over 19s)",
				"baz",
				"foo (x6 over 20s)",
				"quux (x3 over 13s)",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			result := deduplicateErrorStrings(tc.ErrorMessages, now)
			if !cmp.Equal(tc.ExpectedResult, result) {
				t.Errorf("Expected result:\n%s\nbut got:\n%s", strings.Join(tc.ExpectedResult, "\n"), strings.Join(result, "\n"))
			}
		})
	}
}
