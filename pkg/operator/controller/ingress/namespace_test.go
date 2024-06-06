package ingress

import (
	"testing"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	corev1 "k8s.io/api/core/v1"
)

func Test_routerNamespaceChanged(t *testing.T) {
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
		{
			description: "if a managed label with an empty string value is deleted",
			mutate: func(ns *corev1.Namespace) {
				delete(ns.Labels, "policy-group.network.openshift.io/ingress")
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			desired := manifests.RouterNamespace()
			mutated := desired.DeepCopy()
			tc.mutate(mutated)
			if changed, updated := routerNamespaceChanged(mutated, desired); changed != tc.expect {
				t.Errorf("expect routerNamespaceChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				if updatedChanged, _ := routerNamespaceChanged(mutated, updated); !updatedChanged {
					t.Error("routerNamespaceChanged reported changes but did not make any update")
				}
				if changedAgain, _ := routerNamespaceChanged(desired, updated); changedAgain {
					t.Error("routerNamespaceChanged does not behave as a fixed point function")
				}
			}
		})
	}
}
