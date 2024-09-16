package canary

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func Test_desiredCanaryService(t *testing.T) {
	daemonsetRef := metav1.OwnerReference{
		Name: "test",
	}
	service := desiredCanaryService(daemonsetRef)

	expectedServiceName := types.NamespacedName{
		Namespace: "openshift-ingress-canary",
		Name:      "ingress-canary",
	}

	if !cmp.Equal(service.Name, expectedServiceName.Name) {
		t.Errorf("expected service name to be %s, but got %s", expectedServiceName.Name, service.Name)
	}

	if !cmp.Equal(service.Namespace, expectedServiceName.Namespace) {
		t.Errorf("expected service namespace to be %s, but got %s", expectedServiceName.Namespace, service.Namespace)
	}

	expectedLabels := map[string]string{
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}
	if !cmp.Equal(service.Labels, expectedLabels) {
		t.Errorf("expected service labels to be %q, but got %q", expectedLabels, service.Labels)
	}

	expectedSelector := map[string]string{
		controller.CanaryDaemonSetLabel: canaryControllerName,
	}

	if !cmp.Equal(service.Spec.Selector, expectedSelector) {
		t.Errorf("expected service selector to be %q, but got %q", expectedSelector, service.Spec.Selector)
	}

	expectedOwnerRefs := []metav1.OwnerReference{daemonsetRef}
	if !cmp.Equal(service.OwnerReferences, expectedOwnerRefs) {
		t.Errorf("expected service owner references %#v, but got %#v", expectedOwnerRefs, service.OwnerReferences)
	}

	expectedAnnotations := map[string]string{}
	if !cmp.Equal(service.Annotations, expectedAnnotations, cmpopts.EquateEmpty()) {
		t.Errorf("expected service annotations to be %q, but got %q", expectedAnnotations, service.Annotations)
	}

	expectedPorts := []corev1.ServicePort{}
	for _, port := range CanaryPorts {
		servicePort := corev1.ServicePort{
			Name:       fmt.Sprintf("%d-tcp", port),
			Port:       port,
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstr.FromInt32(port),
		}
		expectedPorts = append(expectedPorts, servicePort)
	}
	if !cmp.Equal(service.Spec.Ports, expectedPorts) {
		t.Errorf("expected service ports to be %#v, but got %#v", expectedPorts, service.Spec.Ports)
	}
}

func Test_canaryServiceChanged(t *testing.T) {
	testcases := []struct {
		description string
		mutate      func(service *corev1.Service)
		expected    bool
	}{
		{
			description: "no change",
			mutate:      func(service *corev1.Service) {},
			expected:    false,
		},
		{
			description: "changed annotation",
			mutate: func(service *corev1.Service) {
				if service.Annotations == nil {
					service.Annotations = map[string]string{}
				}
				service.Annotations["foo"] = "bar"
			},
			expected: true,
		},
		{
			description: "changed ports",
			mutate: func(service *corev1.Service) {
				service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{
					Name:       "",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(100),
					TargetPort: intstr.FromInt(100),
				})
			},
			expected: true,
		},
	}
	for _, tc := range testcases {
		t.Run(tc.description, func(t *testing.T) {
			trueVar := true
			original := desiredCanaryService(metav1.OwnerReference{
				APIVersion: "apps/v1",
				Kind:       "daemonset",
				Name:       "foo",
				UID:        "bar",
				Controller: &trueVar,
			})
			mutated := original.DeepCopy()
			tc.mutate(mutated)
			if changed, updated := canaryServiceChanged(original, mutated); changed != tc.expected {
				t.Errorf("expected canaryServiceChanged to be %v, got %v", tc.expected, changed)
			} else if changed {
				if changedAgain, _ := canaryServiceChanged(mutated, updated); changedAgain {
					t.Error("canaryServiceChanged does not behave as a fixed-point function")
				}
			}
		})
	}
}
