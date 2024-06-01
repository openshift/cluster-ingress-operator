package canary

import (
	"testing"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	corev1 "k8s.io/api/core/v1"

	projectv1 "github.com/openshift/api/project/v1"
)

func Test_canaryNamespaceChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*corev1.Namespace)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *corev1.Namespace) {},
			expect:      false,
		},
		{
			description: "if namespace node-selector annotation changes",
			mutate: func(ns *corev1.Namespace) {
				ns.Annotations[projectv1.ProjectNodeSelector] = "foo"
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			original := manifests.CanaryNamespace()
			mutated := original.DeepCopy()
			tc.mutate(mutated)
			if changed, updated := canaryNamespaceChanged(original, mutated); changed != tc.expect {
				t.Errorf("expect canaryNamespaceChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				if updatedChanged, _ := canaryNamespaceChanged(original, updated); !updatedChanged {
					t.Error("canaryNamespaceChanged reported changes but did not make any update")
				}
				if changedAgain, _ := canaryNamespaceChanged(mutated, updated); changedAgain {
					t.Error("canaryNamespaceChanged does not behave as a fixed point function")
				}
			}
		})
	}
}
