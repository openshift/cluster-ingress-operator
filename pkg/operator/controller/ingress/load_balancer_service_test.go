package ingress

import (
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestDesiredLoadBalancerService(t *testing.T) {
	testCases := []struct {
		description             string
		platform                configv1.PlatformType
		strategyType            operatorv1.EndpointPublishingStrategyType
		lbStrategy              operatorv1.LoadBalancerStrategy
		expectAnnotations       map[string]string
		expectAnnotationsAbsent []string
		expectService           bool
	}{
		{
			description:   "AWS, default LB type, default scope",
			platform:      configv1.AWSPlatformType,
			strategyType:  operatorv1.LoadBalancerServiceStrategyType,
			expectService: true,
			expectAnnotations: map[string]string{
				awsLBProxyProtocolAnnotation: "*",
			},
			expectAnnotationsAbsent: []string{
				awsInternalLBAnnotation,
				AWSLBTypeAnnotation,
			},
		},
		{
			description:  "AWS, default LB type, external scope",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expectService: true,
			expectAnnotations: map[string]string{
				awsLBProxyProtocolAnnotation: "*",
			},
			expectAnnotationsAbsent: []string{
				awsInternalLBAnnotation,
				AWSLBTypeAnnotation,
			},
		},
		{
			description:  "AWS, default LB type, internal scope",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
				},
			},
			expectService: true,
			expectAnnotations: map[string]string{
				awsLBProxyProtocolAnnotation: "*",
				awsInternalLBAnnotation:      "true",
			},
			expectAnnotationsAbsent: []string{
				AWSLBTypeAnnotation,
			},
		},
		{
			description:  "AWS, ELB, default scope",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSClassicLoadBalancer,
					},
				},
			},
			expectService: true,
			expectAnnotations: map[string]string{
				awsLBProxyProtocolAnnotation: "*",
			},
			expectAnnotationsAbsent: []string{
				awsInternalLBAnnotation,
				AWSLBTypeAnnotation,
			},
		},
		{
			description:  "AWS, ELB, external scope",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSClassicLoadBalancer,
					},
				},
			},
			expectService: true,
			expectAnnotations: map[string]string{
				awsLBProxyProtocolAnnotation: "*",
			},
			expectAnnotationsAbsent: []string{
				awsInternalLBAnnotation,
				AWSLBTypeAnnotation,
			},
		},
		{
			description:  "AWS, ELB, internal scope",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSClassicLoadBalancer,
					},
				},
			},
			expectService: true,
			expectAnnotations: map[string]string{
				awsLBProxyProtocolAnnotation: "*",
				awsInternalLBAnnotation:      "true",
			},
			expectAnnotationsAbsent: []string{
				AWSLBTypeAnnotation,
			},
		},
		{
			description:  "AWS, NLB, default scope",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSNetworkLoadBalancer,
					},
				},
			},
			expectService: true,
			expectAnnotations: map[string]string{
				AWSLBTypeAnnotation: AWSNLBAnnotation,
			},
			expectAnnotationsAbsent: []string{
				awsLBProxyProtocolAnnotation,
				awsInternalLBAnnotation,
			},
		},
		{
			description:  "AWS, NLB, external scope",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSNetworkLoadBalancer,
					},
				},
			},
			expectService: true,
			expectAnnotations: map[string]string{
				AWSLBTypeAnnotation: AWSNLBAnnotation,
			},
			expectAnnotationsAbsent: []string{
				awsLBProxyProtocolAnnotation,
				awsInternalLBAnnotation,
			},
		},
		{
			description:   "AWS, NodePort",
			platform:      configv1.AWSPlatformType,
			strategyType:  operatorv1.NodePortServiceStrategyType,
			expectService: false,
		},
		{
			description:  "IBM, external scope",
			platform:     configv1.IBMCloudPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expectService: true,
			expectAnnotations: map[string]string{
				iksLBScopeAnnotation: iksLBScopePublic,
			},
		},
		{
			description:  "IBM, internal scope",
			platform:     configv1.IBMCloudPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			expectService: true,
			expectAnnotations: map[string]string{
				iksLBScopeAnnotation: iksLBScopePrivate,
			},
		},
		{
			description:  "Azure, external scope",
			platform:     configv1.AzurePlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expectService: true,
			expectAnnotationsAbsent: []string{
				azureInternalLBAnnotation,
			},
		},
		{
			description:  "Azure, internal scope",
			platform:     configv1.AzurePlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			expectService: true,
			expectAnnotations: map[string]string{
				azureInternalLBAnnotation: "true",
			},
		},
		{
			description:  "GCP, external scope",
			platform:     configv1.GCPPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expectService: true,
			expectAnnotationsAbsent: []string{
				gcpLBTypeAnnotation,
				GCPGlobalAccessAnnotation,
			},
		},
		{
			description:  "GCP, internal scope",
			platform:     configv1.GCPPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			expectService: true,
			expectAnnotations: map[string]string{
				gcpLBTypeAnnotation: "Internal",
			},
			expectAnnotationsAbsent: []string{
				GCPGlobalAccessAnnotation,
			},
		},
		{
			description:  "GCP, internal scope, global ClientAccess",
			platform:     configv1.GCPPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.GCPLoadBalancerProvider,
					GCP: &operatorv1.GCPLoadBalancerParameters{
						ClientAccess: operatorv1.GCPGlobalAccess,
					},
				},
			},
			expectService: true,
			expectAnnotations: map[string]string{
				gcpLBTypeAnnotation:       "Internal",
				GCPGlobalAccessAnnotation: "true",
			},
		},
		{
			description:  "GCP, internal scope, local ClientAccess",
			platform:     configv1.GCPPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.GCPLoadBalancerProvider,
					GCP: &operatorv1.GCPLoadBalancerParameters{
						ClientAccess: operatorv1.GCPLocalAccess,
					},
				},
			},
			expectService: true,
			expectAnnotations: map[string]string{
				gcpLBTypeAnnotation:       "Internal",
				GCPGlobalAccessAnnotation: "false",
			},
		},
		{
			description:  "OpenStack, external scope",
			platform:     configv1.OpenStackPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expectService: true,
			expectAnnotationsAbsent: []string{
				openstackInternalLBAnnotation,
			},
		},
		{
			description:  "OpenStack, internal scope",
			platform:     configv1.OpenStackPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			expectService: true,
			expectAnnotations: map[string]string{
				openstackInternalLBAnnotation: "true",
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
					Type:         tc.strategyType,
					LoadBalancer: &tc.lbStrategy,
				},
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
		infraConfig := &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: tc.platform,
				},
			},
		}

		haveSvc, svc, err := desiredLoadBalancerService(ic, deploymentRef, infraConfig.Status.PlatformStatus)
		switch {
		case err != nil:
			t.Errorf("test %q failed; unexpected error from desiredLoadBalancerService for endpoint publishing strategy type %v: %v", tc.description, tc.strategyType, err)
		case tc.expectService && !haveSvc:
			t.Errorf("test %q failed; expected desiredLoadBalancerService to return a service for endpoint publishing strategy type %v, got nil", tc.description, tc.strategyType)
		case !tc.expectService && haveSvc:
			t.Errorf("test %q failed; expected desiredLoadBalancerService to return nil service for endpoint publishing strategy type %v, got %#v", tc.description, tc.strategyType, svc)
		case haveSvc:
			for expectedAnnotation, expectedValue := range tc.expectAnnotations {
				if err := checkServiceHasAnnotation(svc, expectedAnnotation, true, expectedValue); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
			}
			for _, unexpectedAnnotation := range tc.expectAnnotationsAbsent {
				if err := checkServiceHasAnnotation(svc, unexpectedAnnotation, false, ""); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
			}
		}
	}
}

func checkServiceHasAnnotation(svc *corev1.Service, name string, expectValue bool, expectedValue string) error {
	switch actualValue, foundValue := svc.Annotations[name]; {
	case !expectValue && foundValue:
		return fmt.Errorf("service annotation %s has unexpected setting: %q", name, actualValue)
	case expectValue && !foundValue:
		return fmt.Errorf("service is missing annotation %v", name)
	case expectValue && expectedValue != actualValue:
		return fmt.Errorf("service has unexpected %s annotation setting: expected %q, got %q", name, expectedValue, actualValue)
	default:
		return nil
	}
}

func TestLoadBalancerServiceChanged(t *testing.T) {
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
			expect: false,
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
			expect: false,
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
			expect: false,
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
			expect: false,
		},
		{
			description: "if .spec.type changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.Type = corev1.ServiceTypeNodePort
			},
			expect: false,
		},
		{
			description: "if the service.beta.kubernetes.io/load-balancer-source-ranges annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/load-balancer-source-ranges"] = "10.0.0.0/8"
			},
			expect: false,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval"] = "10"
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		original := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval": "5",
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
				},
				Selector: map[string]string{
					"foo": "bar",
				},
				Type: corev1.ServiceTypeLoadBalancer,
			},
		}
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		if changed, updated := loadBalancerServiceChanged(&original, mutated); changed != tc.expect {
			t.Errorf("%s, expect loadBalancerServiceChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := loadBalancerServiceChanged(mutated, updated); changedAgain {
				t.Errorf("%s, loadBalancerServiceChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}

func TestLoadBalancerServiceExternallyModified(t *testing.T) {
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
			},
			expect: false,
		},
		{
			description: "if .spec.externalIPs changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.ExternalIPs = []string{"3.4.5.6"}
			},
			expect: true,
		},
		{
			description: "if .spec.externalTrafficPolicy changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyTypeCluster
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
				svc.Spec.Type = corev1.ServiceTypeNodePort
			},
			expect: true,
		},
		{
			description: "if the service.beta.kubernetes.io/load-balancer-source-ranges annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/load-balancer-source-ranges"] = "10.0.0.0/8"
			},
			expect: true,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval"] = "10"
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		platformStatus := &configv1.PlatformStatus{
			Type: configv1.AWSPlatformType,
		}
		original := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval": "5",
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
				},
				Selector: map[string]string{
					"foo": "bar",
				},
				Type: corev1.ServiceTypeLoadBalancer,
			},
		}
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		if changed, updated := loadBalancerServiceExternallyModified(&original, mutated, platformStatus); changed != tc.expect {
			t.Errorf("%s, expect loadBalancerServiceChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := loadBalancerServiceExternallyModified(mutated, updated, platformStatus); changedAgain {
				t.Errorf("%s, loadBalancerServiceChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}
