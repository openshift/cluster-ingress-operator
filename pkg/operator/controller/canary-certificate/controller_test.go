package canarycertificate

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_canaryCertificateChanged(t *testing.T) {
	trueVar := true
	testCases := []struct {
		description string
		mutate      func(*corev1.Secret)
		expect      bool
	}{
		{
			description: "no change",
			mutate:      func(_ *corev1.Secret) {},
			expect:      false,
		},
		{
			description: "new owner ref added",
			mutate: func(certSecret *corev1.Secret) {
				newOwnerRef := metav1.OwnerReference{
					APIVersion: "apps/v1",
					Kind:       "Service",
					Name:       "random-service",
					UID:        "bazquux",
					Controller: &trueVar,
				}
				certSecret.OwnerReferences = append(certSecret.OwnerReferences, newOwnerRef)
			},
			expect: true,
		},
		{
			description: "cert data changed",
			mutate: func(certSecret *corev1.Secret) {
				certSecret.Data = map[string][]byte{
					"foo": []byte("barbaz"),
				}
			},
			expect: true,
		},
		{
			description: "cert type changed",
			mutate: func(certSecret *corev1.Secret) {
				certSecret.Type = corev1.SecretTypeBasicAuth
			},
			expect: true,
		},
		{
			description: "annotation added",
			mutate: func(certSecret *corev1.Secret) {
				if certSecret.Annotations == nil {
					certSecret.Annotations = map[string]string{}
				}
				certSecret.Annotations["asdf"] = "asdfasdfasdf"
			},
			expect: true,
		},
		{
			description: "label added",
			mutate: func(certSecret *corev1.Secret) {
				if certSecret.Labels == nil {
					certSecret.Labels = map[string]string{}
				}
				certSecret.Labels["newlabel"] = "asdfasdf"
			},
			expect: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			defaultICSecret := &corev1.Secret{
				Data: map[string][]byte{
					"foo": []byte("bar"),
					"baz": []byte("quux"),
				},
				Type: corev1.SecretTypeOpaque,
			}
			ownerRef := metav1.OwnerReference{
				APIVersion: "apps/v1",
				Kind:       "DaemonSet",
				Name:       "ingress-canary",
				UID:        "foobar",
				Controller: &trueVar,
			}
			original := desiredCanaryCertificate(ownerRef, defaultICSecret)
			mutated := original.DeepCopy()
			tc.mutate(mutated)
			if changed, updated := canaryCertificateChanged(original, mutated); changed != tc.expect {
				t.Errorf("expect canaryCertificateChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				if updatedChanged, _ := canaryCertificateChanged(original, updated); !updatedChanged {
					t.Error("canaryCertificateChanged reported changes but did not make any update")
				}
				if changedAgain, _ := canaryCertificateChanged(mutated, updated); changedAgain {
					t.Error("canaryCertificateChanged does not behave as a fixed point function")
				}
			}
		})
	}
}
