package canary

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	rbacv1 "k8s.io/api/rbac/v1"
)

func Test_desiredClusterRoleBinding(t *testing.T) {
	clusterRoleBinding := desiredCanaryClusterRoleBinding()

	expectedClusterRoleBindingName := controller.CanaryClusterRoleBindingName()

	if !cmp.Equal(clusterRoleBinding.Name, expectedClusterRoleBindingName.Name) {
		t.Errorf("expected clusterrolebinding name to be %s, but got %s", expectedClusterRoleBindingName.Name, clusterRoleBinding.Name)
	}

	if !cmp.Equal(clusterRoleBinding.Namespace, expectedClusterRoleBindingName.Namespace) {
		t.Errorf("expected clusterrolebinding namespace to be %s, but got %s", expectedClusterRoleBindingName.Namespace, clusterRoleBinding.Namespace)
	}

	expectedLabels := map[string]string{
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	if !cmp.Equal(clusterRoleBinding.Labels, expectedLabels) {
		t.Errorf("expected clusterrolebinding labels to be %q, but got %q", expectedLabels, clusterRoleBinding.Labels)
	}
}

func Test_canaryClusterRoleBindingChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*rbacv1.ClusterRoleBinding)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *rbacv1.ClusterRoleBinding) {},
			expect:      false,
		},
		{
			description: "if subjects change",
			mutate: func(crb *rbacv1.ClusterRoleBinding) {
				crb.Subjects = []rbacv1.Subject{
					{
						Kind:      "test",
						APIGroup:  "foo",
						Name:      "bar",
						Namespace: "foobar",
					},
				}
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			original := desiredCanaryClusterRoleBinding()
			mutated := original.DeepCopy()
			tc.mutate(mutated)
			if changed, updated := canaryClusterRoleBindingChanged(original, mutated); changed != tc.expect {
				t.Errorf("expected canaryClusterRoleBindingChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				if updatedChanged, _ := canaryClusterRoleBindingChanged(original, updated); !updatedChanged {
					t.Error("canaryClusterRoleBindingChanged reported changes but did not make any update")
				}
				if changedAgain, _ := canaryClusterRoleBindingChanged(mutated, updated); changedAgain {
					t.Error("canaryClusterRoleBindingChanged does not behave as a fixed point function")
				}
			}
		})
	}
}
