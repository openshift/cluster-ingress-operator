package canary

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	routev1 "github.com/openshift/api/route/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestDesiredCanaryRoute(t *testing.T) {
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

	if !cmp.Equal(route.Name, expectedRouteName.Name) {
		t.Errorf("expected route name to be %s, but got %s", expectedRouteName.Name, route.Name)
	}

	if !cmp.Equal(route.Namespace, expectedRouteName.Namespace) {
		t.Errorf("expected route namespace to be %s, but got %s", expectedRouteName.Namespace, route.Namespace)
	}

	expectedAnnotations := map[string]string{
		"haproxy.router.openshift.io/balance": "roundrobin",
	}

	if !cmp.Equal(route.Annotations, expectedAnnotations) {
		t.Errorf("expected route annotations to be %s, but got %s", expectedAnnotations, route.Annotations)
	}

	expectedLabels := map[string]string{
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	if !cmp.Equal(route.Labels, expectedLabels) {
		t.Errorf("expected route labels to be %q, but got %q", expectedLabels, route.Labels)
	}

	routeToName := route.Spec.To.Name
	if !cmp.Equal(routeToName, service.Name) {
		t.Errorf("expected route.Spec.To.Name to be %q, but got %q", service.Name, routeToName)
	}

	routeTarget := route.Spec.Port.TargetPort
	validTarget := false
	for _, port := range service.Spec.Ports {
		if cmp.Equal(routeTarget, port.TargetPort) {
			validTarget = true
		}
	}

	if !validTarget {
		t.Errorf("expected %v to be a port in the %v. Route targetPort not in service targetPort list", route.Spec.Port.TargetPort, service.Spec.Ports)
	}

	expectedOwnerRefs := []metav1.OwnerReference{daemonsetRef}
	if !cmp.Equal(route.OwnerReferences, expectedOwnerRefs) {
		t.Errorf("expected service owner references %#v, but got %#v", expectedOwnerRefs, route.OwnerReferences)
	}
}

func TestDesiredCanaryRouteWithInvalidService(t *testing.T) {
	daemonsetRef := metav1.OwnerReference{
		Name: "test",
	}

	_, err := desiredCanaryRoute(nil)
	if err == nil {
		t.Errorf("expected desiredCanaryService to return an error when the parameter service is nil")
	}

	service := desiredCanaryService(daemonsetRef)
	service.Spec.Ports = nil

	_, err = desiredCanaryRoute(service)
	if err == nil {
		t.Errorf("expected desiredCanaryService to return an error when the parameter service has empty spec.ports")
	}
}

func TestCanaryRouteChanged(t *testing.T) {
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
	}

	daemonsetRef := metav1.OwnerReference{
		Name: "test",
	}
	service := desiredCanaryService(daemonsetRef)

	for _, tc := range testCases {
		original, err := desiredCanaryRoute(service)
		if err != nil {
			t.Fatalf("desiredCanaryService returned an error: %v", err)
		}
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		if changed, updated := canaryRouteChanged(original, mutated); changed != tc.expect {
			t.Errorf("%s, expect canaryRouteChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := canaryRouteChanged(mutated, updated); changedAgain {
				t.Errorf("%s, canaryRouteChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}
