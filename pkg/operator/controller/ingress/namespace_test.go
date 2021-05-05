package ingress

import (
	"testing"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	corev1 "k8s.io/api/core/v1"
)

func TestRouterNamespaceChanged(t *testing.T) {
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
			description: "if namespace workload annotation changes",
			mutate: func(ns *corev1.Namespace) {
				ns.Annotations["workload.openshift.io/allowed"] = "foo"
			},
			expect: true,
		},
		{
			description: "if namespace network-policy annotation changes",
			mutate: func(ns *corev1.Namespace) {
				ns.Labels["policy-group.network.openshift.io/ingress"] = "foo"
			},
			expect: true,
		},
		{
			description: "if an unmanaged label and unmanaged annotation change",
			mutate: func(ns *corev1.Namespace) {
				ns.Annotations["unmanaged.annotation.io"] = "foo"
				ns.Labels["unmanaged.label.io"] = "foo"
			},
			expect: false,
		},
	}

	for _, tc := range testCases {
		original := manifests.RouterNamespace()
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		if changed, updated := routerNamespaceChanged(original, mutated); changed != tc.expect {
			t.Errorf("%s, expect routerNamespaceChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := routerNamespaceChanged(mutated, updated); changedAgain {
				t.Errorf("%s, routerNamespaceChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}
