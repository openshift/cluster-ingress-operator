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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

// Test_reconciler_canaryTolerations verifies that a missing ingress config
// yields no canary tolerations instead of an error, that configured
// tolerations are returned, that the ingress config is not read when canary
// node placement is disabled, that a no-match error (missing CRD) yields no
// tolerations, and that any other error is returned.
func Test_reconciler_canaryTolerations(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := operatorv1.Install(scheme); err != nil {
		t.Fatalf("failed to install operator scheme: %v", err)
	}

	tolerationSeconds := int64(300)
	configured := operatorv1.CanaryToleration{
		Key:               "example.com/gpu",
		Operator:          operatorv1.CanaryTolerationOperatorEqual,
		Value:             "present",
		Effect:            operatorv1.CanaryTolerationEffectNoExecute,
		TolerationSeconds: &tolerationSeconds,
	}
	ingressConfig := &operatorv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: operatorv1.IngressSpec{
			Canary: operatorv1.IngressCanary{
				NodePlacement: operatorv1.CanaryNodePlacement{
					Tolerations: []operatorv1.CanaryToleration{configured},
				},
			},
		},
	}

	testCases := []struct {
		description string
		enabled     bool
		objects     []runtime.Object
		intercept   *interceptor.Funcs
		expect      []operatorv1.CanaryToleration
		expectErr   string
	}{
		{
			description: "missing ingress config yields no tolerations",
			enabled:     true,
			objects:     nil,
			expect:      nil,
		},
		{
			description: "ingress config without canary config yields no tolerations",
			enabled:     true,
			objects: []runtime.Object{
				&operatorv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}},
			},
			expect: nil,
		},
		{
			description: "configured tolerations are returned",
			enabled:     true,
			objects:     []runtime.Object{ingressConfig},
			expect:      []operatorv1.CanaryToleration{configured},
		},
		{
			description: "disabled yields no tolerations without reading the ingress config",
			enabled:     false,
			intercept: &interceptor.Funcs{
				Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					t.Error("expected no ingress config read when canary node placement is disabled")
					return nil
				},
			},
			expect: nil,
		},
		{
			description: "no-match error yields no tolerations",
			enabled:     true,
			intercept: &interceptor.Funcs{
				Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "operator.openshift.io", Kind: "Ingress"}}
				},
			},
			expect: nil,
		},
		{
			description: "other errors are returned",
			enabled:     true,
			intercept: &interceptor.Funcs{
				Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					return fmt.Errorf("boom")
				},
			},
			expectErr: "failed to get ingress config: boom",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tc.objects...)
			if tc.intercept != nil {
				builder = builder.WithInterceptorFuncs(*tc.intercept)
			}
			r := &reconciler{
				config: Config{CanaryNodePlacementEnabled: tc.enabled},
				client: builder.Build(),
			}
			tolerations, err := r.canaryTolerations(context.Background())
			if tc.expectErr != "" {
				if err == nil || err.Error() != tc.expectErr {
					t.Fatalf("expected error %q, got %v", tc.expectErr, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.expect, tolerations); diff != "" {
				t.Errorf("tolerations mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
