package ingress

import (
	"reflect"
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestDesiredNodePortService(t *testing.T) {
	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       "router-default",
		UID:        "1",
		Controller: &trueVar,
	}

	testCases := []struct {
		strategyType    operatorv1.EndpointPublishingStrategyType
		wantMetricsPort bool
		expect          bool
		expectService   corev1.Service
	}{
		{
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			expect:       false,
		},
		{
			strategyType: operatorv1.NodePortServiceStrategyType,
			expect:       true,
			expectService: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						localWithFallbackAnnotation: "",
					},
					Namespace: "openshift-ingress",
					Name:      "router-nodeport-default",
					Labels: map[string]string{
						"app":    "router",
						"router": "router-nodeport-default",
						"ingresscontroller.operator.openshift.io/owning-ingresscontroller": "default",
					},
					OwnerReferences: []metav1.OwnerReference{deploymentRef},
				},
				Spec: corev1.ServiceSpec{
					ExternalTrafficPolicy: "Local",
					Ports: []corev1.ServicePort{
						{
							Name:       "http",
							Protocol:   "TCP",
							Port:       int32(80),
							TargetPort: intstr.FromString("http"),
						},
						{
							Name:       "https",
							Protocol:   "TCP",
							Port:       int32(443),
							TargetPort: intstr.FromString("https"),
						},
					},
					Selector: map[string]string{
						"ingresscontroller.operator.openshift.io/deployment-ingresscontroller": "default",
					},
					Type: "NodePort",
				},
			},
		},
		{
			strategyType:    operatorv1.NodePortServiceStrategyType,
			wantMetricsPort: true,
			expect:          true,
			expectService: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						localWithFallbackAnnotation: "",
					},
					Namespace: "openshift-ingress",
					Name:      "router-nodeport-default",
					Labels: map[string]string{
						"app":    "router",
						"router": "router-nodeport-default",
						"ingresscontroller.operator.openshift.io/owning-ingresscontroller": "default",
					},
					OwnerReferences: []metav1.OwnerReference{deploymentRef},
				},
				Spec: corev1.ServiceSpec{
					ExternalTrafficPolicy: "Local",
					Ports: []corev1.ServicePort{
						{
							Name:       "http",
							Protocol:   "TCP",
							Port:       int32(80),
							TargetPort: intstr.FromString("http"),
						},
						{
							Name:       "https",
							Protocol:   "TCP",
							Port:       int32(443),
							TargetPort: intstr.FromString("https"),
						},
						{
							Name:       "metrics",
							Protocol:   "TCP",
							Port:       int32(1936),
							TargetPort: intstr.FromString("metrics"),
						},
					},
					Selector: map[string]string{
						"ingresscontroller.operator.openshift.io/deployment-ingresscontroller": "default",
					},
					Type: "NodePort",
				},
			},
		},
	}

	for _, tc := range testCases {
		ic := &operatorv1.IngressController{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
			},
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: tc.strategyType,
				},
			},
		}
		want, svc, err := desiredNodePortService(ic, deploymentRef, tc.wantMetricsPort)
		if err != nil {
			t.Errorf("unexpected error from desiredNodePortService: %v", err)
		} else if want != tc.expect {
			t.Errorf("expected desiredNodePortService to return %t for endpoint publishing strategy type %v, got %t, with service %#v", tc.expect, tc.strategyType, want, svc)
		} else if tc.expect && !reflect.DeepEqual(svc, &tc.expectService) {
			t.Errorf("expected desiredNodePortService to return %#v, got %#v", &tc.expectService, svc)
		}
	}
}

func TestNodePortServiceChanged(t *testing.T) {
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
			description: "if .spec.externalTrafficPolicy changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyTypeCluster
			},
			expect: true,
		},
		{
			description: "if the local-with-fallback annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["traffic-policy.network.alpha.openshift.io/local-with-fallback"] = "x"
			},
			expect: true,
		},
		{
			description: "if the local-with-fallback annotation is deleted",
			mutate: func(svc *corev1.Service) {
				delete(svc.Annotations, "traffic-policy.network.alpha.openshift.io/local-with-fallback")
			},
			expect: true,
		},
		{
			description: "if .spec.healthCheckNodePort changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.HealthCheckNodePort = int32(34566)
			},
			expect: false,
		},
		{
			description: "if .spec.ports changes",
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
			description: "if .spec.ports[*].nodePort changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.Ports[0].NodePort = int32(33337)
				svc.Spec.Ports[1].NodePort = int32(33338)
			},
			expect: false,
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
	}

	for _, tc := range testCases {
		original := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"traffic-policy.network.alpha.openshift.io/local-with-fallback": "",
				},
				Namespace: "openshift-ingress",
				Name:      "router-original",
				UID:       "1",
			},
			Spec: corev1.ServiceSpec{
				ClusterIP:             "1.2.3.4",
				ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyTypeLocal,
				HealthCheckNodePort:   int32(33333),
				Ports: []corev1.ServicePort{
					{
						Name:       "http",
						NodePort:   int32(33334),
						Port:       int32(80),
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromString("http"),
					},
					{
						Name:       "https",
						NodePort:   int32(33335),
						Port:       int32(443),
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromString("https"),
					},
					{
						Name:       "metrics",
						NodePort:   int32(33336),
						Port:       int32(1936),
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromString("metrics"),
					},
				},
				Selector: map[string]string{
					"foo": "bar",
				},
				Type: corev1.ServiceTypeNodePort,
			},
		}
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		if changed, updated := nodePortServiceChanged(&original, mutated); changed != tc.expect {
			t.Errorf("%s, expect nodePortServiceChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := nodePortServiceChanged(mutated, updated); changedAgain {
				t.Errorf("%s, nodePortServiceChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}
