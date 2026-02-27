package canary

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	corev1 "k8s.io/api/core/v1"
)

func Test_desiredServiceAccount(t *testing.T) {
	serviceAccount := desiredCanaryServiceAccount()

	assert.Equal(t, serviceAccount.Name, "ingress-canary")

	assert.Equal(t, serviceAccount.Namespace, "openshift-ingress-canary")

	expectedLabels := map[string]string{
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	assert.Equal(t, serviceAccount.Labels, expectedLabels)
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
			expect: false,
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
			expect: false,
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
		{
			description: "if annotations change",
			mutate: func(sa *corev1.ServiceAccount) {
				sa.Annotations = map[string]string{
					"foo": "bar",
				}
			},
			expect: false,
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
