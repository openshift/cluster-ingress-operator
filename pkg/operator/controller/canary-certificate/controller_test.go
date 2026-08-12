package canarycertificate

import (
	"context"
	"testing"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCanaryCertificateDependencyEvents(t *testing.T) {
	config := Config{
		OperatorNamespace: "openshift-ingress-operator",
		OperandNamespace:  "openshift-ingress",
	}
	defaultIngressControllerName := types.NamespacedName{
		Namespace: config.OperatorNamespace,
		Name:      manifests.DefaultIngressControllerName,
	}

	testCases := []struct {
		description string
		object      client.Object
		expected    types.NamespacedName
		matches     func(client.Object) bool
	}{
		{
			description: "default ingress controller",
			object: &operatorv1.IngressController{ObjectMeta: metav1.ObjectMeta{
				Namespace: defaultIngressControllerName.Namespace,
				Name:      defaultIngressControllerName.Name,
			}},
			expected: defaultIngressControllerName,
			matches: func(object client.Object) bool {
				return isDefaultIngressControllerDependency(object, config.OperatorNamespace)
			},
		},
		{
			description: "canary daemonset",
			object: &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
				Namespace: operatorcontroller.CanaryDaemonSetName().Namespace,
				Name:      operatorcontroller.CanaryDaemonSetName().Name,
			}},
			expected: operatorcontroller.CanaryDaemonSetName(),
			matches:  isCanaryDaemonSetDependency,
		},
		{
			description: "canary certificate",
			object: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Namespace: operatorcontroller.CanaryCertificateName().Namespace,
				Name:      operatorcontroller.CanaryCertificateName().Name,
			}},
			expected: operatorcontroller.CanaryCertificateName(),
			matches:  isCanaryCertificate,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			if !hasNamespacedName(tc.object, tc.expected) {
				t.Fatalf("expected %s/%s to match", tc.expected.Namespace, tc.expected.Name)
			}
			if !tc.matches(tc.object) {
				t.Fatalf("expected dependency predicate to match %s/%s", tc.expected.Namespace, tc.expected.Name)
			}

			requests := toCanaryCertificate(context.Background(), tc.object)
			if len(requests) != 1 || requests[0].NamespacedName != operatorcontroller.CanaryCertificateName() {
				t.Fatalf("expected one request for %v, got %#v", operatorcontroller.CanaryCertificateName(), requests)
			}
		})
	}
}

func TestCanaryCertificateDependencyPredicatesRejectUnrelatedObjects(t *testing.T) {
	operatorNamespace := "openshift-ingress-operator"
	testCases := []struct {
		object  client.Object
		matches func(client.Object) bool
	}{
		{
			object:  &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: operatorcontroller.CanaryDaemonSetName().Namespace, Name: "unrelated"}},
			matches: isCanaryDaemonSetDependency,
		},
		{
			object:  &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "unrelated", Name: operatorcontroller.CanaryCertificateName().Name}},
			matches: isCanaryCertificate,
		},
		{
			object: &operatorv1.IngressController{ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: "unrelated"}},
			matches: func(object client.Object) bool {
				return isDefaultIngressControllerDependency(object, operatorNamespace)
			},
		},
	}
	for _, tc := range testCases {
		if tc.matches(tc.object) {
			t.Fatalf("unexpected dependency match for %s/%s", tc.object.GetNamespace(), tc.object.GetName())
		}
	}
}

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
