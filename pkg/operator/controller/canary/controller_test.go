package canary

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func Test_setCanaryStatusConditionConcurrentStatusUpdate(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, operatorv1.Install(scheme))
	ic := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      manifests.DefaultIngressControllerName,
			Namespace: operatorcontroller.DefaultOperatorNamespace,
		},
		Status: operatorv1.IngressControllerStatus{
			Conditions: []operatorv1.OperatorCondition{{
				Type:   operatorv1.OperatorStatusTypeAvailable,
				Status: operatorv1.ConditionFalse,
				Reason: "DeploymentUnavailable",
			}},
		},
	}
	concurrentCondition := operatorv1.OperatorCondition{
		Type:               operatorv1.OperatorStatusTypeAvailable,
		Status:             operatorv1.ConditionTrue,
		Reason:             "DeploymentAvailable",
		Message:            "The ingress deployment became available",
		LastTransitionTime: metav1.NewTime(time.Unix(1, 0)),
	}
	canaryCondition := operatorv1.OperatorCondition{
		Type:    ingress.IngressControllerCanaryCheckSuccessConditionType,
		Status:  operatorv1.ConditionTrue,
		Reason:  "CanaryChecksSucceeding",
		Message: "Canary checks are succeeding",
	}
	var readVersions []string
	var concurrentVersion string
	patchCount, conflictCount := 0, 0
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(ic).WithObjects(ic).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, underlying client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := underlying.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				readVersions = append(readVersions, obj.GetResourceVersion())
				return nil
			},
			SubResourcePatch: func(ctx context.Context, underlying client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				patchCount++
				if patchCount == 1 {
					latest := &operatorv1.IngressController{}
					if err := underlying.Get(ctx, client.ObjectKeyFromObject(ic), latest); err != nil {
						return err
					}
					latest.Status.Conditions = []operatorv1.OperatorCondition{concurrentCondition}
					if err := underlying.Status().Update(ctx, latest); err != nil {
						return err
					}
					concurrentVersion = latest.ResourceVersion
				}
				err := underlying.SubResource(subresource).Patch(ctx, obj, patch, opts...)
				if apierrors.IsConflict(err) {
					conflictCount++
				}
				return err
			},
		}).Build()
	reconciler := &reconciler{config: Config{Namespace: ic.Namespace}, client: fakeClient}
	require.NoError(t, reconciler.setCanaryStatusCondition(canaryCondition))
	assert.Equal(t, 2, patchCount, "expected two patches")
	assert.Equal(t, 1, conflictCount, "expected one native conflict")
	if assert.Len(t, readVersions, 2) {
		assert.NotEqual(t, concurrentVersion, readVersions[0], "expected a stale read")
		assert.Equal(t, concurrentVersion, readVersions[1], "expected a fresh read")
	}
	t.Logf("canary read versions: %v; concurrent version: %s; patches: %d; conflicts: %d", readVersions, concurrentVersion, patchCount, conflictCount)
	updated := &operatorv1.IngressController{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKeyFromObject(ic), updated))
	require.Len(t, updated.Status.Conditions, 2, "expected ingress and canary conditions")
	assert.Empty(t, cmp.Diff(concurrentCondition, updated.Status.Conditions[0]), "concurrent ingress condition was overwritten (-want +got)")
	assert.Empty(t, cmp.Diff(canaryCondition, updated.Status.Conditions[1], cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime")), "unexpected canary condition (-want +got)")
}

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
