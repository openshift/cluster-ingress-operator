package ingress

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDesiredLoadBalancerService(t *testing.T) {
	testCases := []struct {
		strategyType operatorv1.EndpointPublishingStrategyType
		expect       bool
	}{
		{
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			expect:       true,
		},
		{
			strategyType: operatorv1.NodePortServiceStrategyType,
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
					Type: tc.strategyType,
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
				Platform: configv1.AWSPlatformType,
			},
		}

		svc, err := desiredLoadBalancerService(ic, deploymentRef, infraConfig)
		if err != nil {
			t.Errorf("unexpected error from desiredLoadBalancerService for endpoint publishing strategy type %v: %v", tc.strategyType, err)
		} else if tc.expect && svc == nil {
			t.Errorf("expected desiredLoadBalancerService to return a service for endpoint publishing strategy type %v, got nil", tc.strategyType)
		} else if !tc.expect && svc != nil {
			t.Errorf("expected desiredLoadBalancerService to return nil service for endpoint publishing strategy type %v, got %#v", tc.strategyType, svc)
		}
	}
}
