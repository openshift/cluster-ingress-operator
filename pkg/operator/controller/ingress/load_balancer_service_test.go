package ingress

import (
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDesiredLoadBalancerService(t *testing.T) {
	testCases := []struct {
		description  string
		strategyType operatorv1.EndpointPublishingStrategyType
		lbStrategy   operatorv1.LoadBalancerStrategy
		proxyNeeded  bool
		expect       bool
	}{
		{
			description:  "external classic load balancer with scope",
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			proxyNeeded: true,
			expect:      true,
		},
		{
			description:  "external classic load balancer without scope",
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			proxyNeeded: true,
			expect:      true,
		},
		{
			description:  "external classic load balancer without LoadBalancerStrategy",
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			proxyNeeded:  true,
			expect:       true,
		},
		{
			description:  "internal classic load balancer",
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			proxyNeeded: true,
			expect:      true,
		},
		{
			description:  "external network load balancer without scope",
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSNetworkLoadBalancer,
					},
				},
			},
			proxyNeeded: false,
			expect:      true,
		},
		{
			description:  "external network load balancer with scope",
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
			proxyNeeded: false,
			expect:      true,
		},
		{
			description:  "nodePort service",
			strategyType: operatorv1.NodePortServiceStrategyType,
			proxyNeeded:  false,
			expect:       false,
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
					Type: configv1.AWSPlatformType,
				},
			},
		}

		proxyNeeded, err := IsProxyProtocolNeeded(ic, infraConfig.Status.PlatformStatus)
		switch {
		case err != nil:
			t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ic.Namespace, ic.Name, err)
		case tc.proxyNeeded && !proxyNeeded || !tc.proxyNeeded && proxyNeeded:
			t.Errorf("test %q failed; expected IsProxyProtocolNeeded to return %v, got %v", tc.description, tc.proxyNeeded, proxyNeeded)
		}

		haveSvc, svc, err := desiredLoadBalancerService(ic, deploymentRef, infraConfig.Status.PlatformStatus, proxyNeeded)
		switch {
		case err != nil:
			t.Errorf("test %q failed; unexpected error from desiredLoadBalancerService for endpoint publishing strategy type %v: %v", tc.description, tc.strategyType, err)
		case tc.expect && !haveSvc:
			t.Errorf("test %q failed; expected desiredLoadBalancerService to return a service for endpoint publishing strategy type %v, got nil", tc.description, tc.strategyType)
		case !tc.expect && haveSvc:
			t.Errorf("test %q failed; expected desiredLoadBalancerService to return nil service for endpoint publishing strategy type %v, got %#v", tc.description, tc.strategyType, svc)
		}

		if tc.strategyType == operatorv1.LoadBalancerServiceStrategyType {
			if err := checkServiceHasAnnotation(svc, awsLBHealthCheckIntervalAnnotation, true, awsLBHealthCheckIntervalDefault); err != nil {
				t.Errorf("annotation check for test %q failed: %v", tc.description, err)
			}
			if err := checkServiceHasAnnotation(svc, awsLBHealthCheckTimeoutAnnotation, true, awsLBHealthCheckTimeoutDefault); err != nil {
				t.Errorf("annotation check for test %q failed: %v", tc.description, err)
			}
			if err := checkServiceHasAnnotation(svc, awsLBHealthCheckUnhealthyThresholdAnnotation, true, awsLBHealthCheckUnhealthyThresholdDefault); err != nil {
				t.Errorf("annotation check for test %q failed: %v", tc.description, err)
			}
			if err := checkServiceHasAnnotation(svc, awsLBHealthCheckHealthyThresholdAnnotation, true, awsLBHealthCheckHealthyThresholdDefault); err != nil {
				t.Errorf("annotation check for test %q failed: %v", tc.description, err)
			}
			switch {
			case tc.lbStrategy.ProviderParameters == nil || tc.lbStrategy.ProviderParameters.AWS.Type == operatorv1.AWSClassicLoadBalancer:
				if err := checkServiceHasAnnotation(svc, awsLBProxyProtocolAnnotation, true, "*"); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
			case tc.lbStrategy.ProviderParameters.AWS.Type == operatorv1.AWSNetworkLoadBalancer:
				if err := checkServiceHasAnnotation(svc, AWSLBTypeAnnotation, true, AWSNLBAnnotation); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
			case tc.lbStrategy.Scope == operatorv1.InternalLoadBalancer:
				if err := checkServiceHasAnnotation(svc, AWSLBTypeAnnotation, true, "0.0.0.0/0"); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
			}
		}
	}
}

func checkServiceHasAnnotation(svc *corev1.Service, name string, expectValue bool, expectedValue string) error {
	var (
		actualValue string
		foundValue  bool
	)

	if val, ok := svc.Annotations[name]; ok {
		foundValue = true
		actualValue = val
	}
	switch {
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
