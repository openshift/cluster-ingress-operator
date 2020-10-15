package canary

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestDesiredCanaryService(t *testing.T) {
	daemonsetRef := metav1.OwnerReference{
		Name: "test",
	}
	service := desiredCanaryService(daemonsetRef)

	expectedServiceName := types.NamespacedName{
		Namespace: "openshift-ingress-canary",
		Name:      "ingress-canary",
	}

	if !cmp.Equal(service.Name, expectedServiceName.Name) {
		t.Errorf("expected service name to be %s, but got %s", expectedServiceName.Name, service.Name)
	}

	if !cmp.Equal(service.Namespace, expectedServiceName.Namespace) {
		t.Errorf("expected service namespace to be %s, but got %s", expectedServiceName.Namespace, service.Namespace)
	}

	expectedLabels := map[string]string{
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}
	if !cmp.Equal(service.Labels, expectedLabels) {
		t.Errorf("expected service labels to be %q, but got %q", expectedLabels, service.Labels)
	}

	expectedSelector := map[string]string{
		controller.CanaryDaemonSetLabel: canaryControllerName,
	}

	if !cmp.Equal(service.Spec.Selector, expectedSelector) {
		t.Errorf("expected service selector to be %q, but got %q", expectedSelector, service.Spec.Selector)
	}

	expectedOwnerRefs := []metav1.OwnerReference{daemonsetRef}
	if !cmp.Equal(service.OwnerReferences, expectedOwnerRefs) {
		t.Errorf("expected service owner references %#v, but got %#v", expectedOwnerRefs, service.OwnerReferences)
	}
}
