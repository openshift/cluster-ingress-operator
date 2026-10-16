package canary

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

// Test_Reconcile_missingIngressController verifies that a reconcile request for
// an ingress controller that does not exist is a no-op rather than an error.
// Returning an error here would requeue forever, and because the ingress
// controller is fetched before the canary operands are ensured, it would also
// block reconciliation of the canary daemonset, service account, service, and
// route.
func Test_Reconcile_missingIngressController(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, addToScheme := range []func(*runtime.Scheme) error{
		appsv1.AddToScheme,
		corev1.AddToScheme,
		networkingv1.AddToScheme,
		operatorv1.AddToScheme,
	} {
		if err := addToScheme(scheme); err != nil {
			t.Fatal(err)
		}
	}

	r := &reconciler{
		config: Config{
			Namespace:   operatorcontroller.DefaultOperatorNamespace,
			CanaryImage: "test-image",
		},
		client: fake.NewClientBuilder().WithScheme(scheme).Build(),
	}

	request := reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: operatorcontroller.DefaultOperatorNamespace,
		Name:      manifests.DefaultIngressControllerName,
	}}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("expected no error when the ingress controller does not exist, got: %v", err)
	}

	// The canary daemonset must not be created without an ingress controller to
	// derive its tolerations from.
	ds := &appsv1.DaemonSet{}
	err := r.client.Get(context.Background(), operatorcontroller.CanaryDaemonSetName(), ds)
	if !kerrors.IsNotFound(err) {
		t.Errorf("expected the canary daemonset not to exist, got err: %v", err)
	}
}
