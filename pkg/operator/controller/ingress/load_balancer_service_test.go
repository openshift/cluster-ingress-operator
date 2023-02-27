package ingress

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestDesiredLoadBalancerService(t *testing.T) {
	testCases := []struct {
		description          string
		platform             configv1.PlatformType
		strategyType         operatorv1.EndpointPublishingStrategyType
		lbStrategy           operatorv1.LoadBalancerStrategy
		proxyNeeded          bool
		expect               bool
		platformStatus       configv1.PlatformStatus
		expectedResourceTags string
	}{
		{
			description:  "external classic load balancer with scope for aws platform",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			proxyNeeded: true,
			expect:      true,
		},
		{
			description:  "external classic load balancer with scope for aws platform and custom user tags",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			proxyNeeded: true,
			expect:      true,
			platformStatus: configv1.PlatformStatus{
				AWS: &configv1.AWSPlatformStatus{
					ResourceTags: []configv1.AWSResourceTag{{
						Key:   "classic-load-balancer-key-with-value",
						Value: "100",
					}, {
						Key:   "classic-load-balancer-key-with-empty-value",
						Value: "",
					}, {
						Value: "classic-load-balancer-value-without-key",
					}},
				},
			},
			expectedResourceTags: "classic-load-balancer-key-with-value=100,classic-load-balancer-key-with-empty-value=",
		},
		{
			description:  "external classic load balancer without scope for aws platform",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			proxyNeeded:  true,
			expect:       true,
		},
		{
			description:  "external classic load balancer without LoadBalancerStrategy for aws platform",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			proxyNeeded:  true,
			expect:       true,
		},
		{
			description:  "internal classic load balancer for aws platform",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			proxyNeeded: true,
			expect:      true,
		},
		{
			description:  "external network load balancer without scope for aws platform",
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
			proxyNeeded: false,
			expect:      true,
		},
		{
			description:  "external network load balancer with scope for aws platform",
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
			proxyNeeded: false,
			expect:      true,
		},
		{
			description:  "external network load balancer with scope for aws platform and custom user tags",
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
			proxyNeeded: false,
			expect:      true,

			platformStatus: configv1.PlatformStatus{
				AWS: &configv1.AWSPlatformStatus{
					ResourceTags: []configv1.AWSResourceTag{{
						Key:   "network-load-balancer-key-with-value",
						Value: "200",
					}, {
						Key:   "network-load-balancer-key-with-empty-value",
						Value: "",
					}, {
						Value: "network-load-balancer-value-without-key",
					}},
				},
			},
			expectedResourceTags: "network-load-balancer-key-with-value=200,network-load-balancer-key-with-empty-value=",
		},
		{
			description:  "nodePort service for aws platform",
			platform:     configv1.AWSPlatformType,
			strategyType: operatorv1.NodePortServiceStrategyType,
			proxyNeeded:  false,
			expect:       false,
		},
		{
			description:  "external load balancer for ibm platform",
			platform:     configv1.IBMCloudPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "internal load balancer for ibm platform",
			platform:     configv1.IBMCloudPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "external load balancer for Power VS platform",
			platform:     configv1.PowerVSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "internal load balancer for Power VS platform",
			platform:     configv1.PowerVSPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "external load balancer for azure platform",
			platform:     configv1.AzurePlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "internal load balancer for azure platform",
			platform:     configv1.AzurePlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "external load balancer for gcp platform",
			platform:     configv1.GCPPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "internal load balancer for gcp platform",
			platform:     configv1.GCPPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "internal load balancer for gcp platform with global ClientAccess",
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
			expect: true,
		},
		{
			description:  "internal load balancer for gcp platform with local ClientAccess",
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
			expect: true,
		},
		{
			description:  "external load balancer for openstack platform",
			platform:     configv1.OpenStackPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "internal load balancer for openstack platform",
			platform:     configv1.OpenStackPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "external load balancer for alibaba platform",
			platform:     configv1.AlibabaCloudPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			},
			expect: true,
		},
		{
			description:  "internal load balancer for alibaba platform",
			platform:     configv1.AlibabaCloudPlatformType,
			strategyType: operatorv1.LoadBalancerServiceStrategyType,
			lbStrategy: operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.InternalLoadBalancer,
			},
			expect: true,
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
				PlatformStatus: &tc.platformStatus,
			},
		}
		infraConfig.Status.PlatformStatus.Type = tc.platform

		proxyNeeded, err := IsProxyProtocolNeeded(ic, infraConfig.Status.PlatformStatus)
		switch {
		case err != nil:
			t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ic.Namespace, ic.Name, err)
		case tc.proxyNeeded && !proxyNeeded || !tc.proxyNeeded && proxyNeeded:
			t.Errorf("test %q failed; expected IsProxyProtocolNeeded to return %v, got %v", tc.description, tc.proxyNeeded, proxyNeeded)
		}

		haveSvc, svc, err := desiredLoadBalancerService(ic, deploymentRef, infraConfig.Status.PlatformStatus)
		switch {
		case err != nil:
			t.Errorf("test %q failed; unexpected error from desiredLoadBalancerService for endpoint publishing strategy type %v: %v", tc.description, tc.strategyType, err)
		case tc.expect && !haveSvc:
			t.Errorf("test %q failed; expected desiredLoadBalancerService to return a service for endpoint publishing strategy type %v, got nil", tc.description, tc.strategyType)
		case !tc.expect && haveSvc:
			t.Errorf("test %q failed; expected desiredLoadBalancerService to return nil service for endpoint publishing strategy type %v, got %#v", tc.description, tc.strategyType, svc)
		}

		isInternal := ic.Status.EndpointPublishingStrategy.LoadBalancer == nil || ic.Status.EndpointPublishingStrategy.LoadBalancer.Scope == operatorv1.InternalLoadBalancer
		platform := infraConfig.Status.PlatformStatus
		switch platform.Type {
		case configv1.AWSPlatformType:
			if isInternal {
				if err := checkServiceHasAnnotation(svc, awsInternalLBAnnotation, true, "true"); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
			}
			if tc.strategyType == operatorv1.LoadBalancerServiceStrategyType {
				if err := checkServiceHasAnnotation(svc, awsLBHealthCheckTimeoutAnnotation, true, awsLBHealthCheckTimeoutDefault); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
				if err := checkServiceHasAnnotation(svc, awsLBHealthCheckUnhealthyThresholdAnnotation, true, awsLBHealthCheckUnhealthyThresholdDefault); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
				if err := checkServiceHasAnnotation(svc, awsLBHealthCheckHealthyThresholdAnnotation, true, awsLBHealthCheckHealthyThresholdDefault); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
				if err := checkServiceHasAnnotation(svc, localWithFallbackAnnotation, true, ""); err != nil {
					t.Errorf("local-with-fallback annotation check for test %q failed: %v", tc.description, err)
				}
				classicLB := tc.lbStrategy.ProviderParameters == nil || tc.lbStrategy.ProviderParameters.AWS.Type == operatorv1.AWSClassicLoadBalancer
				switch {
				case classicLB:
					if len(tc.expectedResourceTags) > 0 {
						if err := checkServiceHasAnnotation(svc, awsLBAdditionalResourceTags, true, tc.expectedResourceTags); err != nil {
							t.Errorf("annotation check for test %q failed: %v, unexpected value", tc.description, err)
						}
					} else {
						if err := checkServiceHasAnnotation(svc, awsLBAdditionalResourceTags, false, ""); err == nil {
							t.Errorf("annotation check for test %q failed; unexpected annotation %s", tc.description, awsLBAdditionalResourceTags)
						}
					}
					if err := checkServiceHasAnnotation(svc, awsLBHealthCheckIntervalAnnotation, true, awsLBHealthCheckIntervalDefault); err != nil {
						t.Errorf("annotation check for test %q failed: %v", tc.description, err)
					}
					if err := checkServiceHasAnnotation(svc, awsLBProxyProtocolAnnotation, true, "*"); err != nil {
						t.Errorf("annotation check for test %q failed: %v", tc.description, err)
					}
				case tc.lbStrategy.ProviderParameters.AWS.Type == operatorv1.AWSNetworkLoadBalancer:
					if len(tc.expectedResourceTags) > 0 {
						if err := checkServiceHasAnnotation(svc, awsLBAdditionalResourceTags, true, tc.expectedResourceTags); err != nil {
							t.Errorf("annotation check for test %q failed: %v, unexpected value", tc.description, err)
						}
					} else {
						if err := checkServiceHasAnnotation(svc, awsLBAdditionalResourceTags, false, ""); err == nil {
							t.Errorf("annotation check for test %q failed; unexpected annotation %s", tc.description, awsLBAdditionalResourceTags)
						}
					}
					if err := checkServiceHasAnnotation(svc, awsLBHealthCheckIntervalAnnotation, true, awsLBHealthCheckIntervalNLB); err != nil {
						t.Errorf("annotation check for test %q failed: %v", tc.description, err)
					}
					if err := checkServiceHasAnnotation(svc, AWSLBTypeAnnotation, true, AWSNLBAnnotation); err != nil {
						t.Errorf("annotation check for test %q failed: %v", tc.description, err)
					}
				case tc.lbStrategy.Scope == operatorv1.InternalLoadBalancer:
					if err := checkServiceHasAnnotation(svc, AWSLBTypeAnnotation, true, "0.0.0.0/0"); err != nil {
						t.Errorf("annotation check for test %q failed: %v", tc.description, err)
					}
				}
			}
		case configv1.IBMCloudPlatformType, configv1.PowerVSPlatformType:
			if isInternal {
				if err := checkServiceHasAnnotation(svc, iksLBScopeAnnotation, true, iksLBScopePrivate); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
				// The ibm public annotation value should not exist for an internal LB.
				if err := checkServiceHasAnnotation(svc, iksLBScopeAnnotation, true, iksLBScopePublic); err == nil {
					t.Errorf("annotation check for test %q failed; unexpected annotation %s: %s", tc.description, iksLBScopeAnnotation, iksLBScopePublic)
				}
			} else {
				if err := checkServiceHasAnnotation(svc, iksLBScopeAnnotation, true, iksLBScopePublic); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
				// The ibm private annotation value should not exist for an external LB.
				if err := checkServiceHasAnnotation(svc, iksLBScopeAnnotation, true, iksLBScopePrivate); err == nil {
					t.Errorf("annotation check for test %q failed; unexpected annotation %s: %s", tc.description, iksLBScopeAnnotation, iksLBScopePrivate)
				}
			}
		case configv1.AzurePlatformType:
			if isInternal {
				if err := checkServiceHasAnnotation(svc, azureInternalLBAnnotation, true, "true"); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
			} else {
				// The azure private annotation should not exist for external LBs.
				if err := checkServiceHasAnnotation(svc, azureInternalLBAnnotation, false, ""); err == nil {
					t.Errorf("annotation check for test %q failed; unexpected annotation %s", tc.description, azureInternalLBAnnotation)
				}
			}
			if err := checkServiceHasAnnotation(svc, localWithFallbackAnnotation, true, ""); err != nil {
				t.Errorf("local-with-fallback annotation check for test %q failed: %v", tc.description, err)
			}
		case configv1.GCPPlatformType:
			if isInternal {
				if err := checkServiceHasAnnotation(svc, gcpLBTypeAnnotation, true, "Internal"); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
			} else {
				// The internal gcp annotation should not exist for an external LB.
				if err := checkServiceHasAnnotation(svc, gcpLBTypeAnnotation, false, ""); err == nil {
					t.Errorf("annotation check for test %q failed; unexpected annotation %s", tc.description, gcpLBTypeAnnotation)
				}
			}
			if err := checkServiceHasAnnotation(svc, localWithFallbackAnnotation, true, ""); err != nil {
				t.Errorf("local-with-fallback annotation check for test %q failed: %v", tc.description, err)
			}
		case configv1.OpenStackPlatformType:
			if isInternal {
				if err := checkServiceHasAnnotation(svc, openstackInternalLBAnnotation, true, "true"); err != nil {
					t.Errorf("annotation check for test %q failed: %v", tc.description, err)
				}
			} else {
				// The internal openstack annotation should not exist for an external LB.
				if err := checkServiceHasAnnotation(svc, openstackInternalLBAnnotation, false, ""); err == nil {
					t.Errorf("annotation check for test %q failed; unexpected annotation %s", tc.description, openstackInternalLBAnnotation)
				}
			}
			if err := checkServiceHasAnnotation(svc, localWithFallbackAnnotation, true, ""); err != nil {
				t.Errorf("local-with-fallback annotation check for test %q failed: %v", tc.description, err)
			}
		}
	}
}

// TestDesiredLoadBalancerServiceAWSIdleTimeout verifies that
// desiredLoadBalancerService sets the expected annotation iff
// spec.endpointPublishingStrategy.loadBalancer.providerParameters.aws.classicLoadBalancer.connectionIdleTimeout
// is specified.
func TestDesiredLoadBalancerServiceAWSIdleTimeout(t *testing.T) {
	testCases := []struct {
		name                    string
		loadBalancerStrategy    *operatorv1.LoadBalancerStrategy
		expectAnnotationPresent bool
		expectedAnnotationValue string
	}{
		{
			name:                    "nil loadBalancerStrategy",
			loadBalancerStrategy:    nil,
			expectAnnotationPresent: false,
		},
		{
			name: "nil providerParameters",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: nil,
			},
			expectAnnotationPresent: false,
		},
		{
			name: "nil providerParameters.aws",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS:  nil,
				},
			},
			expectAnnotationPresent: false,
		},
		{
			name: "nil providerParameters.aws.classicLoadBalancer",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type:                          operatorv1.AWSClassicLoadBalancer,
						ClassicLoadBalancerParameters: nil,
					},
				},
			},
			expectAnnotationPresent: false,
		},
		{
			name: "nil providerParameters.aws.classicLoadBalancerParameters.connectionIdleTimeout",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type:                          operatorv1.AWSClassicLoadBalancer,
						ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{},
					},
				},
			},
			expectAnnotationPresent: false,
		},
		{
			name: "5 seconds",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSClassicLoadBalancer,
						ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{
							ConnectionIdleTimeout: metav1.Duration{Duration: 5 * time.Second},
						},
					},
				},
			},
			expectAnnotationPresent: true,
			expectedAnnotationValue: "5",
		},
		{
			name: "1 hour",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSClassicLoadBalancer,
						ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{
							ConnectionIdleTimeout: metav1.Duration{Duration: time.Hour},
						},
					},
				},
			},
			expectAnnotationPresent: true,
			expectedAnnotationValue: "3600",
		},
		{
			name: "negative 1 second",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSClassicLoadBalancer,
						ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{
							ConnectionIdleTimeout: metav1.Duration{Duration: -1 * time.Second},
						},
					},
				},
			},
			expectAnnotationPresent: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ic := &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type:         operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: tc.loadBalancerStrategy,
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
			haveSvc, svc, err := desiredLoadBalancerService(ic, deploymentRef, infraConfig.Status.PlatformStatus)
			if err != nil {
				t.Fatal(err)
			}
			if !haveSvc {
				t.Fatal("desiredLoadBalancerService didn't return a service")
			}
			const key = "service.beta.kubernetes.io/aws-load-balancer-connection-idle-timeout"
			switch v, ok := svc.Annotations[key]; {
			case !tc.expectAnnotationPresent && ok:
				t.Errorf("unexpected annotation: %s=%s", key, v)
			case tc.expectAnnotationPresent && !ok:
				t.Errorf("missing expected annotation: %s=%s", key, tc.expectedAnnotationValue)
			case tc.expectAnnotationPresent && v != tc.expectedAnnotationValue:
				t.Errorf("expected annotations %s=%s, found %s=%s", key, tc.expectedAnnotationValue, key, v)
			}
		})

	}
}

func checkServiceHasAnnotation(svc *corev1.Service, name string, expectValue bool, expectedValue string) error {
	var (
		actualValue string
		foundValue  bool
	)

	if val, ok := svc.Annotations[name]; !ok {
		return fmt.Errorf("service annotation %s missing", name)
	} else {
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

// TestShouldUseLocalWithFallback verifies that shouldUseLocalWithFallback
// behaves as expected.
func TestShouldUseLocalWithFallback(t *testing.T) {
	testCases := []struct {
		description string
		local       bool
		override    string
		expect      bool
		expectError bool
	}{
		{
			description: "if using Cluster without an override",
			local:       false,
			expect:      false,
		},
		{
			description: "if using Local without an override",
			local:       true,
			expect:      true,
		},
		{
			description: "if using Local with an override",
			local:       true,
			override:    `{"localWithFallback":"false"}`,
			expect:      false,
		},
		{
			description: "if using Local with a garbage override",
			local:       true,
			override:    `{"localWithFallback":"x"}`,
			expectError: true,
		},
	}
	for _, tc := range testCases {
		var override []byte
		if len(tc.override) != 0 {
			override = []byte(tc.override)
		}
		ic := &operatorv1.IngressController{
			Spec: operatorv1.IngressControllerSpec{
				UnsupportedConfigOverrides: runtime.RawExtension{
					Raw: override,
				},
			},
		}
		policy := corev1.ServiceExternalTrafficPolicyTypeCluster
		if tc.local {
			policy = corev1.ServiceExternalTrafficPolicyTypeLocal
		}
		service := corev1.Service{
			Spec: corev1.ServiceSpec{
				ExternalTrafficPolicy: policy,
			},
		}
		actual, err := shouldUseLocalWithFallback(ic, &service)
		switch {
		case !tc.expectError && err != nil:
			t.Errorf("%q: unexpected error: %v", tc.description, err)
		case tc.expectError && err == nil:
			t.Errorf("%q: expected error, got nil", tc.description)
		case tc.expect != actual:
			t.Errorf("%q: expected %t, got %t", tc.description, tc.expect, actual)
		}
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
			description: "if the local-with-fallback annotation is added",
			mutate: func(svc *corev1.Service) {
				svc.Annotations[localWithFallbackAnnotation] = ""
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
			description: "if .spec.loadBalancerSourceRanges changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.LoadBalancerSourceRanges = []string{"10.0.0.0/8"}
			},
			expect: true,
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
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval annotation is deleted",
			mutate: func(svc *corev1.Service) {
				delete(svc.Annotations, "service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval")
			},
			expect: true,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags"] = "Key3=Value3,Key4=Value4"
			},
			expect: false,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-connection-idle-timeout annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-connection-idle-timeout"] = "120"
			},
			expect: true,
		},
		{
			description: "if the service.kubernetes.io/ibm-load-balancer-cloud-provider-enable-features annotation is added",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.kubernetes.io/ibm-load-balancer-cloud-provider-enable-features"] = "proxy-protocol"
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			original := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"service.beta.kubernetes.io/aws-load-balancer-connection-idle-timeout":  "10",
						"service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval":     "5",
						"service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags": "Key1=Value1,Key2=Value2",
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
				t.Errorf("expected loadBalancerServiceChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				if changedAgain, _ := loadBalancerServiceChanged(mutated, updated); changedAgain {
					t.Error("loadBalancerServiceChanged does not behave as a fixed point function")
				}
			}
		})
	}
}

// TestLoadBalancerServiceAnnotationsChanged verifies that
// loadBalancerServiceAnnotationsChanged behaves correctly.
func TestLoadBalancerServiceAnnotationsChanged(t *testing.T) {
	testCases := []struct {
		description         string
		mutate              func(*corev1.Service)
		currentAnnotations  map[string]string
		expectedAnnotations map[string]string
		managedAnnotations  sets.String
		expect              bool
	}{
		{
			description:         "if current and expected annotations are both empty",
			currentAnnotations:  map[string]string{},
			expectedAnnotations: map[string]string{},
			managedAnnotations:  sets.NewString("foo"),
			expect:              false,
		},
		{
			description:         "if current annotations is nil and expected annotations is empty",
			currentAnnotations:  nil,
			expectedAnnotations: map[string]string{},
			managedAnnotations:  sets.NewString("foo"),
			expect:              false,
		},
		{
			description:         "if current annotations is empty and expected annotations is nil",
			currentAnnotations:  map[string]string{},
			expectedAnnotations: nil,
			managedAnnotations:  sets.NewString("foo"),
			expect:              false,
		},
		{
			description: "if an unmanaged annotation is updated",
			currentAnnotations: map[string]string{
				"foo": "bar",
			},
			expectedAnnotations: map[string]string{
				"foo": "bar",
				"baz": "quux",
			},
			managedAnnotations: sets.NewString("foo"),
			expect:             false,
		},
		{
			description:        "if a managed annotation is set",
			currentAnnotations: map[string]string{},
			expectedAnnotations: map[string]string{
				"foo": "bar",
			},
			managedAnnotations: sets.NewString("foo"),
			expect:             true,
		},
		{
			description: "if a managed annotation is updated",
			currentAnnotations: map[string]string{
				"foo": "bar",
			},
			expectedAnnotations: map[string]string{
				"foo": "baz",
			},
			managedAnnotations: sets.NewString("foo"),
			expect:             true,
		},
		{
			description: "if a managed annotation is deleted",
			currentAnnotations: map[string]string{
				"foo": "bar",
			},
			expectedAnnotations: map[string]string{},
			managedAnnotations:  sets.NewString("foo"),
			expect:              true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			current := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tc.currentAnnotations,
				},
			}
			expected := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tc.expectedAnnotations,
				},
			}
			if changed, updated := loadBalancerServiceAnnotationsChanged(&current, &expected, tc.managedAnnotations); changed != tc.expect {
				t.Errorf("expected loadBalancerServiceAnnotationsChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				if changedAgain, _ := loadBalancerServiceAnnotationsChanged(&expected, updated, tc.managedAnnotations); changedAgain {
					t.Error("loadBalancerServiceAnnotationsChanged does not behave as a fixed point function")
				}
			}
		})
	}
}

func TestServiceIngressOwner(t *testing.T) {
	testCases := []struct {
		description string
		service     *corev1.Service
		ingressName string
		expect      bool
	}{
		{
			description: "if owner is set correctly",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						manifests.OwningIngressControllerLabel: "foo",
					},
				},
			},
			ingressName: "foo",
			expect:      true,
		},
		{
			description: "if owner is not set correctly",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						manifests.OwningIngressControllerLabel: "foo",
					},
				},
			},
			ingressName: "bar",
			expect:      false,
		},
		{
			description: "if owner label is not set at all",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{},
			},
			ingressName: "bar",
			expect:      false,
		},
		{
			description: "if service is nil",
			service:     nil,
			ingressName: "bar",
			expect:      false,
		},
	}

	for _, tc := range testCases {
		ic := &operatorv1.IngressController{
			ObjectMeta: metav1.ObjectMeta{
				Name: tc.ingressName,
			},
			Spec:   operatorv1.IngressControllerSpec{},
			Status: operatorv1.IngressControllerStatus{},
		}

		if actual := isServiceOwnedByIngressController(tc.service, ic); actual != tc.expect {
			t.Errorf("expected ownership %t got %t", tc.expect, actual)
		}
	}

}

func TestUpdateLoadBalancerServiceSourceRanges(t *testing.T) {
	// Test all cases in the table presented in <https://github.com/openshift/enhancements/pull/1177>.
	testCases := []struct {
		name                             string
		allowedSourceRanges              []operatorv1.CIDR
		currentAnnotation                string
		currentLoadBalancerSourceRanges  []string
		expectedLoadBalancerSourceRanges []string
		expectAnnotationToBeCleared      bool
		expectChanged                    bool
	}{
		{
			name:                             "only loadBalancerSourceRanges is set",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"foo"},
			expectChanged:                    false,
		},
		{
			name:                             "loadBalancerSourceRanges is different from allowedSourceRanges and annotation is not set",
			allowedSourceRanges:              []operatorv1.CIDR{"bar"},
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"bar"},
			expectChanged:                    true,
		},
		{
			name:                             "only allowedSourceRanges is set",
			allowedSourceRanges:              []operatorv1.CIDR{"bar"},
			expectedLoadBalancerSourceRanges: []string{"bar"},
			expectChanged:                    true,
		},
		{
			name:                             "annotation and loadBalancerSourceRanges are set and allowedSourceRanges is not set",
			currentAnnotation:                "cow",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"foo"},
			expectAnnotationToBeCleared:      false,
			expectChanged:                    false,
		},
		{
			name:                             "annotation, loadBalancerSourceRanges, and allowedSourceRanges are set",
			allowedSourceRanges:              []operatorv1.CIDR{"bar"},
			currentAnnotation:                "cow",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"bar"},
			expectAnnotationToBeCleared:      true,
			expectChanged:                    true,
		},
		{
			name:                             "annotation and loadBalancerSourceRanges are set and identical, allowedSourceRanges is not set",
			currentAnnotation:                "foo",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"foo"},
			expectAnnotationToBeCleared:      false,
			expectChanged:                    false,
		},
		{
			name:                             "annotation and loadBalancerSourceRanges are set and identical, allowedSourceRanges is also set",
			allowedSourceRanges:              []operatorv1.CIDR{"bar"},
			currentAnnotation:                "foo",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"bar"},
			expectAnnotationToBeCleared:      true,
			expectChanged:                    true,
		},
		{
			name:                        "only annotation is set",
			currentAnnotation:           "foo",
			expectAnnotationToBeCleared: false,
			expectChanged:               false,
		},
		{
			name:                             "annotation and allowedSourceRanges are set, loadBalancerSourceRanges is also set",
			allowedSourceRanges:              []operatorv1.CIDR{"bar"},
			currentAnnotation:                "foo",
			expectedLoadBalancerSourceRanges: []string{"bar"},
			expectAnnotationToBeCleared:      true,
			expectChanged:                    true,
		},
		{
			name:                             "allowedSourceRanges and loadBalancerSourceRanges are set and identical, annotation is also set",
			allowedSourceRanges:              []operatorv1.CIDR{"foo"},
			currentAnnotation:                "cow",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"foo"},
			expectAnnotationToBeCleared:      true,
			expectChanged:                    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ic := &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Spec: operatorv1.IngressControllerSpec{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							AllowedSourceRanges: tc.allowedSourceRanges,
						},
					},
				},
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type:         operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{},
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
			wantSvc, desired, err := desiredLoadBalancerService(ic, deploymentRef, infraConfig.Status.PlatformStatus)
			if err != nil {
				t.Fatal(err)
			}
			if !wantSvc {
				t.Fatal("desiredLoadBalancerService didn't return a service")
			}

			current := desired.DeepCopy()
			if len(tc.currentAnnotation) > 0 {
				current.Annotations[corev1.AnnotationLoadBalancerSourceRangesKey] = tc.currentAnnotation
			}
			current.Spec.LoadBalancerSourceRanges = tc.currentLoadBalancerSourceRanges

			changed, svc := loadBalancerServiceChanged(current, desired)
			if changed != tc.expectChanged {
				t.Errorf("expected changed to be %t, got %t", tc.expectChanged, changed)
			}

			if changed {
				if actual := svc.Spec.LoadBalancerSourceRanges; !reflect.DeepEqual(actual, tc.expectedLoadBalancerSourceRanges) {
					t.Errorf("expected LoadBalancerSourceRanges %v, got %v", tc.expectedLoadBalancerSourceRanges, actual)
				}

				if len(tc.currentAnnotation) > 0 {
					if a, exists := svc.Annotations[corev1.AnnotationLoadBalancerSourceRangesKey]; (!exists || len(a) == 0) && !tc.expectAnnotationToBeCleared {
						t.Error("expected service.beta.kubernetes.io/load-balancer-source-ranges annotation not to be cleared")
					} else if exists && len(a) > 0 && tc.expectAnnotationToBeCleared {
						t.Error("expected service.beta.kubernetes.io/load-balancer-source-ranges annotation to be cleared")
					}
				}
			}
		})
	}
}
