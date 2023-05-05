package ingress

import (
	"testing"

	"github.com/stretchr/testify/assert"

	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// test_desiredInternalIngressControllerService verifies that
// desiredInternalIngressControllerService returns the expected service.
func Test_desiredInternalIngressControllerService(t *testing.T) {
	ic := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
	}
	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       "router-default",
		UID:        "1",
		Controller: &trueVar,
	}

	svc := desiredInternalIngressControllerService(ic, deploymentRef)

	assert.Equal(t, "ClusterIP", string(svc.Spec.Type))
	assert.Equal(t, "Cluster", string(*svc.Spec.InternalTrafficPolicy))
	assert.Equal(t, map[string]string{
		"service.alpha.openshift.io/serving-cert-secret-name": "router-metrics-certs-default",
	}, svc.Annotations)
	assert.Equal(t, []corev1.ServicePort{{
		Name:       "http",
		Protocol:   "TCP",
		Port:       int32(80),
		TargetPort: intstr.FromString("http"),
	}, {
		Name:       "https",
		Protocol:   "TCP",
		Port:       int32(443),
		TargetPort: intstr.FromString("https"),
	}, {
		Name:       "metrics",
		Protocol:   "TCP",
		Port:       int32(1936),
		TargetPort: intstr.FromString("metrics"),
	}}, svc.Spec.Ports)
}

// Test_internalServiceChanged verifies that internalServiceChanged properly
// detects changes and handles updates.
func Test_internalServiceChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*corev1.Service)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *corev1.Service) {},
			expect:      false,
		},
		{
			description: "if .uid changes",
			mutate: func(svc *corev1.Service) {
				svc.UID = "2"
			},
			expect: false,
		},
		{
			description: "if .spec.clusterIP changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.ClusterIP = "2.3.4.5"
				svc.Spec.ClusterIPs = []string{"2.3.4.5"}
			},
			expect: false,
		},
		{
			description: "if .spec.externalIPs changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.ExternalIPs = []string{"3.4.5.6"}
			},
			expect: false,
		},
		{
			description: "if .spec.internalTrafficPolicy changes",
			mutate: func(svc *corev1.Service) {
				v := corev1.ServiceInternalTrafficPolicyLocal
				svc.Spec.InternalTrafficPolicy = &v
			},
			expect: true,
		},
		{
			description: "if the service.alpha.openshift.io/serving-cert-secret-name annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.alpha.openshift.io/serving-cert-secret-name"] = "x"
			},
			expect: true,
		},
		{
			description: "if the service.alpha.openshift.io/serving-cert-secret-name annotation is deleted",
			mutate: func(svc *corev1.Service) {
				delete(svc.Annotations, "service.alpha.openshift.io/serving-cert-secret-name")
			},
			expect: true,
		},
		{
			description: "if .spec.ports changes by adding a new port",
			mutate: func(svc *corev1.Service) {
				newPort := corev1.ServicePort{
					Name:       "foo",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(8080),
					TargetPort: intstr.FromString("foo"),
				}
				svc.Spec.Ports = append(svc.Spec.Ports, newPort)
			},
			expect: true,
		},
		{
			description: "if .spec.ports changes by changing the metrics port's target port from an integer to a string",
			mutate: func(svc *corev1.Service) {
				for i := range svc.Spec.Ports {
					if svc.Spec.Ports[i].Name == "metrics" {
						svc.Spec.Ports[i].TargetPort = intstr.FromString("metrics")
						return
					}
				}
				panic("no metrics port found!")
			},
			expect: true,
		},
		{
			description: "if .spec.selector changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.Selector = nil
			},
			expect: true,
		},
		{
			description: "if .spec.sessionAffinity is defaulted",
			mutate: func(service *corev1.Service) {
				service.Spec.SessionAffinity = corev1.ServiceAffinityNone
			},
			expect: false,
		},
		{
			description: "if .spec.sessionAffinity is set to a non-default value",
			mutate: func(service *corev1.Service) {
				service.Spec.SessionAffinity = corev1.ServiceAffinityClientIP
			},
			expect: true,
		},
		{
			description: "if .spec.type changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.Type = corev1.ServiceTypeLoadBalancer
			},
			expect: true,
		},
		{
			description: "if .spec.ipFamilies is defaulted",
			mutate: func(service *corev1.Service) {
				service.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol}
			},
			expect: false,
		},
		{
			description: "if .spec.ipFamilyPolicy is defaulted",
			mutate: func(service *corev1.Service) {
				v := corev1.IPFamilyPolicySingleStack
				service.Spec.IPFamilyPolicy = &v
			},
			expect: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			original := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"service.alpha.openshift.io/serving-cert-secret-name": "router-metrics-certs-default",
					},
					Namespace: "openshift-ingress",
					Name:      "router-internal-default",
					UID:       "1",
				},
				Spec: corev1.ServiceSpec{
					ClusterIP: "1.2.3.4",
					Ports: []corev1.ServicePort{
						{
							Name:       "http",
							Port:       int32(80),
							Protocol:   corev1.ProtocolTCP,
							TargetPort: intstr.FromString("http"),
						},
						{
							Name:       "https",
							Port:       int32(443),
							Protocol:   corev1.ProtocolTCP,
							TargetPort: intstr.FromString("https"),
						},
						{
							Name:       "metrics",
							Port:       int32(1936),
							Protocol:   corev1.ProtocolTCP,
							TargetPort: intstr.FromInt(1936),
						},
					},
					Selector: map[string]string{
						"foo": "bar",
					},
					Type: corev1.ServiceTypeClusterIP,
				},
			}
			mutated := original.DeepCopy()
			tc.mutate(mutated)
			if changed, updated := internalServiceChanged(&original, mutated); changed != tc.expect {
				t.Errorf("expected internalServiceChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				if changedAgain, _ := internalServiceChanged(mutated, updated); changedAgain {
					t.Error("internalServiceChanged does not behave as a fixed point function")
				}
			}
		})
	}
}
