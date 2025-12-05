package canary

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
)

func Test_desiredServiceAccount(t *testing.T) {
	serviceAccount := desiredCanaryServiceAccount()

	expectedServiceAccountName := controller.CanaryServiceAccountName()

	if !cmp.Equal(serviceAccount.Name, expectedServiceAccountName.Name) {
		t.Errorf("expected service account name to be %s, but got %s", expectedServiceAccountName.Name, serviceAccount.Name)
	}

	if !cmp.Equal(serviceAccount.Namespace, expectedServiceAccountName.Namespace) {
		t.Errorf("expected service account namespace to be %s, but got %s", expectedServiceAccountName.Namespace, serviceAccount.Namespace)
	}

	expectedLabels := map[string]string{
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	if !cmp.Equal(serviceAccount.Labels, expectedLabels) {
		t.Errorf("expected service account labels to be %q, but got %q", expectedLabels, serviceAccount.Labels)
	}
}

func Test_canaryServiceAccountChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*corev1.ServiceAccount)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *corev1.ServiceAccount) {},
			expect:      false,
		},
		{
			description: "if secrets change",
			mutate: func(sa *corev1.ServiceAccount) {
				sa.Secrets = []corev1.ObjectReference{
					{
						Kind:      "foo",
						FieldPath: "foo/bar",
					},
				}
			},
			expect: true,
		},
		{
			description: "if image pull secrets change",
			mutate: func(sa *corev1.ServiceAccount) {
				sa.ImagePullSecrets = []corev1.LocalObjectReference{
					{
						Name: "foo",
					},
				}
			},
			expect: true,
		},
		{
			description: "if auto mount service account token changes",
			mutate: func(sa *corev1.ServiceAccount) {
				val := true
				if sa.AutomountServiceAccountToken == nil {
					sa.AutomountServiceAccountToken = &val
				} else {
					val = *sa.AutomountServiceAccountToken
					invertVal := !val
					sa.AutomountServiceAccountToken = &invertVal
				}
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			original := desiredCanaryServiceAccount()
			mutated := original.DeepCopy()
			tc.mutate(mutated)
			if changed, updated := canaryServiceAccountChanged(original, mutated); changed != tc.expect {
				t.Errorf("expected canaryServiceAccountChanged to be %t, but got %t", tc.expect, changed)
			} else if changed {
				if updatedChanged, _ := canaryServiceAccountChanged(original, updated); !updatedChanged {
					t.Error("canaryServiceAccountChanged reported changes but did not make any update")
				}
				if changedAgain, _ := canaryServiceAccountChanged(mutated, updated); changedAgain {
					t.Error("canaryServiceAccountChanged does not behave as a fixed point function")
				}
			}
		})
	}
}
