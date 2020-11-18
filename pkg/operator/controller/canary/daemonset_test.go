package canary

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDesiredCanaryDaemonSet(t *testing.T) {
	canaryImage := "openshift/hello-openshift:latest"

	daemonset := desiredCanaryDaemonSet(canaryImage)

	expectedDaemonSetName := controller.CanaryDaemonSetName()

	if !cmp.Equal(daemonset.Name, expectedDaemonSetName.Name) {
		t.Errorf("expected daemonset name to be %s, but got %s", expectedDaemonSetName.Name, daemonset.Name)
	}

	if !cmp.Equal(daemonset.Namespace, expectedDaemonSetName.Namespace) {
		t.Errorf("expected daemonset namespace to be %s, but got %s", expectedDaemonSetName.Namespace, daemonset.Namespace)
	}

	expectedLabels := map[string]string{
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	if !cmp.Equal(daemonset.Labels, expectedLabels) {
		t.Errorf("expected daemonset labels to be %q, but got %q", expectedLabels, daemonset.Labels)
	}

	labelSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			controller.CanaryDaemonSetLabel: canaryControllerName,
		},
	}

	if !cmp.Equal(daemonset.Spec.Selector, labelSelector) {
		t.Errorf("expected daemonset selector to be %q, but got %q", labelSelector, daemonset.Spec.Selector)
	}

	if !cmp.Equal(daemonset.Spec.Template.Labels, labelSelector.MatchLabels) {
		t.Errorf("expected daemonset template labels to be %q, but got %q", labelSelector.MatchLabels, daemonset.Spec.Template.Labels)
	}

	containers := daemonset.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Errorf("expected daemonset to have 1 container, but found %d", len(containers))
	}

	if !cmp.Equal(containers[0].Image, canaryImage) {
		t.Errorf("expected daemonset container image to be %q, but got %q", canaryImage, containers[0].Image)
	}
}
