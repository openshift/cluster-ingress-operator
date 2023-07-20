package canary

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	routev1 "github.com/openshift/api/route/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func Test_desiredCanaryRoute(t *testing.T) {
	daemonsetRef := metav1.OwnerReference{
		Name: "test",
	}
	service := desiredCanaryService(daemonsetRef)
	route, err := desiredCanaryRoute(service)

	if err != nil {
		t.Fatalf("desiredCanaryService returned an error: %v", err)
	}

	expectedRouteName := types.NamespacedName{
		Namespace: "openshift-ingress-canary",
		Name:      "canary",
	}

	assert.Equal(t, route.Name, expectedRouteName.Name, "unexpected route name")
	assert.Equal(t, route.Namespace, expectedRouteName.Namespace, "unexpected route namespace")

	expectedAnnotations := map[string]string{
		"haproxy.router.openshift.io/balance": "roundrobin",
	}
	assert.Equal(t, route.Annotations, expectedAnnotations, "unexpected route annotations")

	expectedLabels := map[string]string{
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}
	assert.Equal(t, route.Labels, expectedLabels, "unexpected route labels")

	assert.Equal(t, route.Spec.To.Name, service.Name, "route's spec.to.name does not match service name")

	routeTarget := route.Spec.Port.TargetPort
	validTarget := false
	for _, port := range service.Spec.Ports {
		if cmp.Equal(routeTarget, port.TargetPort) {
			validTarget = true
		}
	}
	assert.True(t, validTarget, "route's target port does not match any of the service's target ports: expected %v to match some port in %v", route.Spec.Port.TargetPort, service.Spec.Ports)

	expectedOwnerRefs := []metav1.OwnerReference{daemonsetRef}
	assert.Equal(t, route.OwnerReferences, expectedOwnerRefs, "unexpected route owner references")
	assert.Equal(t, service.OwnerReferences, expectedOwnerRefs, "unexpected service owner references")

	expectedTLS := &routev1.TLSConfig{
		Termination:                   routev1.TLSTerminationEdge,
		InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
	}
	assert.Equal(t, route.Spec.TLS, expectedTLS, "unexpected route TLS config")
}

func Test_canaryRouteChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*routev1.Route)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *routev1.Route) {},
			expect:      false,
		},
		{
			description: "if route spec.To changes",
			mutate: func(route *routev1.Route) {
				route.Spec.To.Name = "test"
			},
			expect: true,
		},
		{
			description: "if route spec.Port changes",
			mutate: func(route *routev1.Route) {
				route.Spec.Port.TargetPort = intstr.IntOrString{}
			},
			expect: true,
		},
		{
			description: "if route spec.TLS changes",
			mutate: func(route *routev1.Route) {
				route.Spec.TLS = &routev1.TLSConfig{
					Termination: routev1.TLSTerminationPassthrough,
				}
			},
			expect: true,
		},
	}

	daemonsetRef := metav1.OwnerReference{
		Name: "test",
	}
	service := desiredCanaryService(daemonsetRef)

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			original, err := desiredCanaryRoute(service)
			if err != nil {
				t.Fatalf("desiredCanaryService returned an error: %v", err)
			}
			mutated := original.DeepCopy()
			tc.mutate(mutated)
			if changed, updated := canaryRouteChanged(original, mutated); changed != tc.expect {
				t.Errorf("expected canaryRouteChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				if changedAgain, _ := canaryRouteChanged(mutated, updated); changedAgain {
					t.Error("canaryRouteChanged does not behave as a fixed point function")
				}
			}
		})
	}
}
