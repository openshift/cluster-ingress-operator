package canary

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

	nodeSelector := daemonset.Spec.Template.Spec.NodeSelector
	expectedNodeSelector := map[string]string{
		"kubernetes.io/os": "linux",
	}
	if !cmp.Equal(nodeSelector, expectedNodeSelector) {
		t.Errorf("expected daemonset node selector to be %q, but got %q", expectedNodeSelector, nodeSelector)

	}

	tolerations := daemonset.Spec.Template.Spec.Tolerations
	expectedTolerations := []corev1.Toleration{
		{
			Key:      "node-role.kubernetes.io/infra",
			Operator: "Exists",
			Effect:   "NoSchedule",
		},
	}
	if !cmp.Equal(tolerations, expectedTolerations) {
		t.Errorf("expected daemonset tolerations to be %v, but got %v", expectedTolerations, tolerations)
	}
}

func TestCanaryDaemonsetChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*appsv1.DaemonSet)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *appsv1.DaemonSet) {},
			expect:      false,
		},
		{
			description: "if pod template node selector changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.NodeSelector = map[string]string{
					"foo":  "bar",
					"test": "selector",
				}
			},
			expect: true,
		},
		{
			description: "if pod template tolerations changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Tolerations = []corev1.Toleration{
					{
						Key:      "test",
						Operator: "nil",
						Value:    "foo",
						Effect:   "bar",
					},
				}
			},
			expect: true,
		},
		{
			description: "if canary server image changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].Image = "foo.io/test:latest"
			},
			expect: true,
		},
		{
			description: "if canary command changes (removed)",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].Command = []string{}
			},
			expect: true,
		},
		{
			description: "if canary command changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].Command = []string{"foo"}
			},
			expect: true,
		},
		{
			description: "if canary server container name changes",
			mutate: func(ds *appsv1.DaemonSet) {
				ds.Spec.Template.Spec.Containers[0].Name = "bar"
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		original := desiredCanaryDaemonSet("")
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		if changed, updated := canaryDaemonSetChanged(original, mutated); changed != tc.expect {
			t.Errorf("%s, expect canaryDaemonSetChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := canaryDaemonSetChanged(mutated, updated); changedAgain {
				t.Errorf("%s, canaryDaemonSetChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}
