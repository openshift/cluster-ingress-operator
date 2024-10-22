package ingress

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/exp/slices"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	util "github.com/openshift/cluster-ingress-operator/pkg/util"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test_setDefaultDomain verifies that setDefaultDomain behaves correctly.
func Test_setDefaultDomain(t *testing.T) {
	ingressConfig := &configv1.Ingress{
		Spec: configv1.IngressSpec{
			Domain: "apps.mycluster.com",
		},
	}

	makeIC := func(specDomain, statusDomain string) *operatorv1.IngressController {
		return &operatorv1.IngressController{
			Spec: operatorv1.IngressControllerSpec{
				Domain: specDomain,
			},
			Status: operatorv1.IngressControllerStatus{
				Domain: statusDomain,
			},
		}
	}

	testCases := []struct {
		name           string
		ic             *operatorv1.IngressController
		expectedIC     *operatorv1.IngressController
		expectedResult bool
	}{
		{
			name:           "empty spec, empty status",
			ic:             makeIC("", ""),
			expectedResult: true,
			expectedIC:     makeIC("", ingressConfig.Spec.Domain),
		},
		{
			name:           "empty spec, status has cluster ingress domain",
			ic:             makeIC("", ingressConfig.Spec.Domain),
			expectedResult: false,
			expectedIC:     makeIC("", ingressConfig.Spec.Domain),
		},
		{
			name:           "empty spec, status has custom domain",
			ic:             makeIC("", "otherdomain.com"),
			expectedResult: false,
			expectedIC:     makeIC("", "otherdomain.com"),
		},
		{
			name:           "spec has cluster ingress domain, empty status",
			ic:             makeIC(ingressConfig.Spec.Domain, ""),
			expectedResult: true,
			expectedIC:     makeIC(ingressConfig.Spec.Domain, ingressConfig.Spec.Domain),
		},
		{
			name:           "spec and status have cluster ingress domain",
			ic:             makeIC(ingressConfig.Spec.Domain, ingressConfig.Spec.Domain),
			expectedIC:     makeIC(ingressConfig.Spec.Domain, ingressConfig.Spec.Domain),
			expectedResult: false,
		},
		{
			name:           "spec has cluster ingress domain, status has custom domain",
			ic:             makeIC(ingressConfig.Spec.Domain, "otherdomain.com"),
			expectedIC:     makeIC(ingressConfig.Spec.Domain, "otherdomain.com"),
			expectedResult: false,
		},
		{
			name:           "spec has custom domain, empty status",
			ic:             makeIC("otherdomain.com", ""),
			expectedIC:     makeIC("otherdomain.com", "otherdomain.com"),
			expectedResult: true,
		},
		{
			name:           "spec has custom domain, status has cluster ingress domain",
			ic:             makeIC("otherdomain.com", ingressConfig.Spec.Domain),
			expectedIC:     makeIC("otherdomain.com", ingressConfig.Spec.Domain),
			expectedResult: false,
		},
		{
			name:           "spec and status have custom domain",
			ic:             makeIC("otherdomain.com", "otherdomain.com"),
			expectedIC:     makeIC("otherdomain.com", "otherdomain.com"),
			expectedResult: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ic := tc.ic.DeepCopy()
			if actualResult := setDefaultDomain(ic, ingressConfig); actualResult != tc.expectedResult {
				t.Errorf("expected result %v, got %v", tc.expectedResult, actualResult)
			}
			if !reflect.DeepEqual(ic, tc.expectedIC) {
				t.Errorf("expected ingresscontroller %#v, got %#v", tc.expectedIC, ic)
			}
		})
	}
}

// TestSetDefaultPublishingStrategySetsPlatformDefaults verifies that
// setDefaultPublishingStrategy puts an appropriate default configuration in
// status.endpointPublishingStrategy for each platform when
// spec.endpointPublishingStrategy is nil.
func TestSetDefaultPublishingStrategySetsPlatformDefaults(t *testing.T) {
	var (
		ingressControllerWithLoadBalancer = &operatorv1.IngressController{
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: operatorv1.LoadBalancerServiceStrategyType,
					LoadBalancer: &operatorv1.LoadBalancerStrategy{
						DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						Scope:               operatorv1.ExternalLoadBalancer,
					},
				},
			},
		}
		ingressControllerWithAWSClassicLB = &operatorv1.IngressController{
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: operatorv1.LoadBalancerServiceStrategyType,
					LoadBalancer: &operatorv1.LoadBalancerStrategy{
						DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						Scope:               operatorv1.ExternalLoadBalancer,
						ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
							Type: operatorv1.AWSLoadBalancerProvider,
							AWS: &operatorv1.AWSLoadBalancerParameters{
								Type:                          operatorv1.AWSClassicLoadBalancer,
								ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{},
							},
						},
					},
				},
			},
		}
		ingressControllerWithAWSNLB = &operatorv1.IngressController{
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: operatorv1.LoadBalancerServiceStrategyType,
					LoadBalancer: &operatorv1.LoadBalancerStrategy{
						DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						Scope:               operatorv1.ExternalLoadBalancer,
						ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
							Type: operatorv1.AWSLoadBalancerProvider,
							AWS: &operatorv1.AWSLoadBalancerParameters{
								Type:                          operatorv1.AWSNetworkLoadBalancer,
								NetworkLoadBalancerParameters: &operatorv1.AWSNetworkLoadBalancerParameters{},
							},
						},
					},
				},
			},
		}
		ingressControllerWithLoadBalancerUnmanagedDNS = &operatorv1.IngressController{
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: operatorv1.LoadBalancerServiceStrategyType,
					LoadBalancer: &operatorv1.LoadBalancerStrategy{
						DNSManagementPolicy: operatorv1.UnmanagedLoadBalancerDNS,
						Scope:               operatorv1.ExternalLoadBalancer,
					},
				},
			},
		}
		ingressControllerWithHostNetwork = &operatorv1.IngressController{
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: operatorv1.HostNetworkStrategyType,
					HostNetwork: &operatorv1.HostNetworkStrategy{
						Protocol:  operatorv1.TCPProtocol,
						HTTPPort:  80,
						HTTPSPort: 443,
						StatsPort: 1936,
					},
				},
			},
		}
		makePlatformStatus = func(platform configv1.PlatformType) *configv1.PlatformStatus {
			return &configv1.PlatformStatus{
				Type: platform,
			}
		}

		makeDefaultGCPPlatformStatus = func(platform configv1.PlatformType) *configv1.PlatformStatus {
			return &configv1.PlatformStatus{
				Type: platform,
				GCP: &configv1.GCPPlatformStatus{
					CloudLoadBalancerConfig: &configv1.CloudLoadBalancerConfig{
						DNSType: configv1.PlatformDefaultDNSType,
					},
				},
			}
		}

		makeBYODNSGCPPlatformStatus = func(platform configv1.PlatformType) *configv1.PlatformStatus {
			return &configv1.PlatformStatus{
				Type: platform,
				GCP: &configv1.GCPPlatformStatus{
					CloudLoadBalancerConfig: &configv1.CloudLoadBalancerConfig{
						DNSType:       configv1.ClusterHostedDNSType,
						ClusterHosted: &configv1.CloudLoadBalancerIPs{},
					},
				},
			}
		}

		ingressConfigWithDefaultClassicLB = &configv1.Ingress{
			Spec: configv1.IngressSpec{
				LoadBalancer: configv1.LoadBalancer{
					Platform: configv1.IngressPlatformSpec{
						Type: configv1.AWSPlatformType,
						AWS: &configv1.AWSIngressSpec{
							Type: configv1.Classic,
						},
					},
				},
			},
		}
		ingressConfigWithDefaultNLB = &configv1.Ingress{
			Spec: configv1.IngressSpec{
				LoadBalancer: configv1.LoadBalancer{
					Platform: configv1.IngressPlatformSpec{
						Type: configv1.AWSPlatformType,
						AWS: &configv1.AWSIngressSpec{
							Type: configv1.NLB,
						},
					},
				},
			},
		}
	)

	testCases := []struct {
		name                    string
		platformStatus          *configv1.PlatformStatus
		ingressConfig           *configv1.Ingress
		expectedIC              *operatorv1.IngressController
		domainMatchesBaseDomain bool
	}{
		{
			name:                    "Alibaba",
			platformStatus:          makePlatformStatus(configv1.AlibabaCloudPlatformType),
			expectedIC:              ingressControllerWithLoadBalancer,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "AWS",
			platformStatus:          makePlatformStatus(configv1.AWSPlatformType),
			expectedIC:              ingressControllerWithLoadBalancer,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "AWS with ingress config specifying Classic LB",
			platformStatus:          makePlatformStatus(configv1.AWSPlatformType),
			ingressConfig:           ingressConfigWithDefaultClassicLB,
			expectedIC:              ingressControllerWithAWSClassicLB,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "AWS with ingress config specifying NLB",
			platformStatus:          makePlatformStatus(configv1.AWSPlatformType),
			ingressConfig:           ingressConfigWithDefaultNLB,
			expectedIC:              ingressControllerWithAWSNLB,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "Azure",
			platformStatus:          makePlatformStatus(configv1.AzurePlatformType),
			expectedIC:              ingressControllerWithLoadBalancer,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "Bare metal",
			platformStatus:          makePlatformStatus(configv1.BareMetalPlatformType),
			expectedIC:              ingressControllerWithHostNetwork,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "Equinix Metal",
			platformStatus:          makePlatformStatus(configv1.EquinixMetalPlatformType),
			expectedIC:              ingressControllerWithHostNetwork,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "GCP",
			platformStatus:          makeDefaultGCPPlatformStatus(configv1.GCPPlatformType),
			expectedIC:              ingressControllerWithLoadBalancer,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "GCP With BYO DNS",
			platformStatus:          makeBYODNSGCPPlatformStatus(configv1.GCPPlatformType),
			expectedIC:              ingressControllerWithLoadBalancerUnmanagedDNS,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "IBM Cloud",
			platformStatus:          makePlatformStatus(configv1.IBMCloudPlatformType),
			expectedIC:              ingressControllerWithLoadBalancer,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "Libvirt",
			platformStatus:          makePlatformStatus(configv1.LibvirtPlatformType),
			expectedIC:              ingressControllerWithHostNetwork,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "No platform",
			platformStatus:          makePlatformStatus(configv1.NonePlatformType),
			expectedIC:              ingressControllerWithHostNetwork,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "External platform",
			platformStatus:          makePlatformStatus(configv1.ExternalPlatformType),
			expectedIC:              ingressControllerWithHostNetwork,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "OpenStack",
			platformStatus:          makePlatformStatus(configv1.OpenStackPlatformType),
			expectedIC:              ingressControllerWithHostNetwork,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "Power VS",
			platformStatus:          makePlatformStatus(configv1.PowerVSPlatformType),
			expectedIC:              ingressControllerWithLoadBalancer,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "RHV",
			platformStatus:          makePlatformStatus(configv1.OvirtPlatformType),
			expectedIC:              ingressControllerWithHostNetwork,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "vSphere",
			platformStatus:          makePlatformStatus(configv1.VSpherePlatformType),
			expectedIC:              ingressControllerWithHostNetwork,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "Nutanix",
			platformStatus:          makePlatformStatus(configv1.NutanixPlatformType),
			expectedIC:              ingressControllerWithHostNetwork,
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "AWS Unmanaged DNS",
			platformStatus:          makePlatformStatus(configv1.AWSPlatformType),
			expectedIC:              ingressControllerWithLoadBalancerUnmanagedDNS,
			domainMatchesBaseDomain: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ic := &operatorv1.IngressController{}
			platformStatus := tc.platformStatus.DeepCopy()
			ingressConfig := tc.ingressConfig
			if ingressConfig == nil {
				ingressConfig = &configv1.Ingress{}
			}
			alreadyAdmitted := false
			if actualResult := setDefaultPublishingStrategy(ic, platformStatus, tc.domainMatchesBaseDomain, ingressConfig, alreadyAdmitted); actualResult != true {
				t.Errorf("expected result %v, got %v", true, actualResult)
			}
			if diff := cmp.Diff(tc.expectedIC, ic); len(diff) != 0 {
				t.Errorf("got expected ingresscontroller: %s", diff)
			}
		})
	}
}

// TestSetDefaultPublishingStrategyHandlesUpdates verifies that
// setDefaultPublishingStrategy correctly handles changes to
// spec.endpointPublishingStrategy.
func TestSetDefaultPublishingStrategyHandlesUpdates(t *testing.T) {
	var (
		managedDNS   = operatorv1.ManagedLoadBalancerDNS
		unmanagedDNS = operatorv1.UnmanagedLoadBalancerDNS
		makeIC       = func(spec operatorv1.IngressControllerSpec, status operatorv1.IngressControllerStatus) *operatorv1.IngressController {
			return &operatorv1.IngressController{Spec: spec, Status: status}
		}
		spec = func(eps *operatorv1.EndpointPublishingStrategy) operatorv1.IngressControllerSpec {
			return operatorv1.IngressControllerSpec{
				EndpointPublishingStrategy: eps,
			}
		}
		status = func(eps *operatorv1.EndpointPublishingStrategy) operatorv1.IngressControllerStatus {
			return operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: eps,
			}
		}
		lbs = func(scope operatorv1.LoadBalancerScope, policy *operatorv1.LoadBalancerDNSManagementPolicy) *operatorv1.LoadBalancerStrategy {
			lbs := &operatorv1.LoadBalancerStrategy{
				Scope: scope,
			}
			if policy != nil {
				lbs.DNSManagementPolicy = *policy
			}
			return lbs
		}
		// providerParameters.type is set, but providerParameters.<platform> is not.
		lbsWithEmptyPlatformParameters = func(providerType operatorv1.LoadBalancerProviderType) *operatorv1.LoadBalancerStrategy {
			lbsWithEmptyPP := lbs(operatorv1.ExternalLoadBalancer, &managedDNS)
			lbsWithEmptyPP.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: providerType,
			}
			return lbsWithEmptyPP
		}
		eps = func(lbs *operatorv1.LoadBalancerStrategy) *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type:         operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: lbs,
			}
		}
		elb = func() *operatorv1.EndpointPublishingStrategy {
			eps := eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.AWSLoadBalancerProvider,
				AWS: &operatorv1.AWSLoadBalancerParameters{
					Type: operatorv1.AWSClassicLoadBalancer,
				},
			}
			return eps
		}
		elbWithNullParameters = func() *operatorv1.EndpointPublishingStrategy {
			eps := eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.AWSLoadBalancerProvider,
				AWS: &operatorv1.AWSLoadBalancerParameters{
					Type:                          operatorv1.AWSClassicLoadBalancer,
					ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{},
				},
			}
			return eps
		}
		elbWithIdleTimeout = func(timeout metav1.Duration) *operatorv1.EndpointPublishingStrategy {
			eps := eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.AWSLoadBalancerProvider,
				AWS: &operatorv1.AWSLoadBalancerParameters{
					Type: operatorv1.AWSClassicLoadBalancer,
					ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{
						ConnectionIdleTimeout: timeout,
					},
				},
			}
			return eps
		}
		nlb = func() *operatorv1.EndpointPublishingStrategy {
			eps := eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.AWSLoadBalancerProvider,
				AWS: &operatorv1.AWSLoadBalancerParameters{
					Type: operatorv1.AWSNetworkLoadBalancer,
				},
			}
			return eps
		}
		nlbWithNullParameters = func() *operatorv1.EndpointPublishingStrategy {
			eps := eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.AWSLoadBalancerProvider,
				AWS: &operatorv1.AWSLoadBalancerParameters{
					Type:                          operatorv1.AWSNetworkLoadBalancer,
					NetworkLoadBalancerParameters: &operatorv1.AWSNetworkLoadBalancerParameters{},
				},
			}
			return eps
		}
		gcpLB = func(clientAccess operatorv1.GCPClientAccess) *operatorv1.EndpointPublishingStrategy {
			eps := eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.GCPLoadBalancerProvider,
				GCP: &operatorv1.GCPLoadBalancerParameters{
					ClientAccess: clientAccess,
				},
			}
			return eps
		}
		ibmLB = func(protocol operatorv1.IngressControllerProtocol) *operatorv1.EndpointPublishingStrategy {
			eps := eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.IBMLoadBalancerProvider,
				IBM: &operatorv1.IBMLoadBalancerParameters{
					Protocol: protocol,
				},
			}
			return eps
		}
		nodePort = func(proto operatorv1.IngressControllerProtocol) *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.NodePortServiceStrategyType,
				NodePort: &operatorv1.NodePortStrategy{
					Protocol: proto,
				},
			}
		}
		nodePortWithNull = func() *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.NodePortServiceStrategyType,
			}
		}
		hostNetwork = func(proto operatorv1.IngressControllerProtocol) *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.HostNetworkStrategyType,
				HostNetwork: &operatorv1.HostNetworkStrategy{
					Protocol:  proto,
					HTTPPort:  80,
					HTTPSPort: 443,
					StatsPort: 1936,
				},
			}
		}
		customHostNetwork = func(httpPort, httpsPort, statsPort int32, protocol operatorv1.IngressControllerProtocol) *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.HostNetworkStrategyType,
				HostNetwork: &operatorv1.HostNetworkStrategy{
					Protocol:  operatorv1.TCPProtocol,
					HTTPPort:  httpPort,
					HTTPSPort: httpsPort,
					StatsPort: statsPort,
				},
			}
		}
		hostNetworkWithNull = func() *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.HostNetworkStrategyType,
			}
		}
		private = func(proto operatorv1.IngressControllerProtocol) *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.PrivateStrategyType,
				Private: &operatorv1.PrivateStrategy{
					Protocol: proto,
				},
			}
		}
		privateWithNull = func() *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.PrivateStrategyType,
			}
		}

		ingressConfigWithDefaultNLB = &configv1.Ingress{
			Spec: configv1.IngressSpec{
				LoadBalancer: configv1.LoadBalancer{
					Platform: configv1.IngressPlatformSpec{
						Type: configv1.AWSPlatformType,
						AWS: &configv1.AWSIngressSpec{
							Type: configv1.NLB,
						},
					},
				},
			},
		}
		ingressConfigWithDefaultClassicLB = &configv1.Ingress{
			Spec: configv1.IngressSpec{
				LoadBalancer: configv1.LoadBalancer{
					Platform: configv1.IngressPlatformSpec{
						Type: configv1.AWSPlatformType,
						AWS: &configv1.AWSIngressSpec{
							Type: configv1.Classic,
						},
					},
				},
			},
		}
	)

	testCases := []struct {
		name                    string
		ic                      *operatorv1.IngressController
		ingressConfig           *configv1.Ingress
		expectedIC              *operatorv1.IngressController
		expectedResult          bool
		domainMatchesBaseDomain bool
	}{
		{
			name:                    "loadbalancer scope changed from external to internal",
			ic:                      makeIC(spec(eps(lbs(operatorv1.InternalLoadBalancer, &managedDNS))), status(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS)))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(eps(lbs(operatorv1.InternalLoadBalancer, &managedDNS))), status(eps(lbs(operatorv1.InternalLoadBalancer, &managedDNS)))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer scope changed from internal to external",
			ic:                      makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))), status(eps(lbs(operatorv1.InternalLoadBalancer, &managedDNS)))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))), status(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS)))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer type set to ELB",
			ic:                      makeIC(spec(elb()), status(elb())),
			expectedResult:          false,
			expectedIC:              makeIC(spec(elb()), status(elbWithNullParameters())),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer type set to NLB",
			ic:                      makeIC(spec(nlb()), status(nlb())),
			expectedResult:          false,
			expectedIC:              makeIC(spec(nlb()), status(nlbWithNullParameters())),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer type changed from ELB to NLB",
			ic:                      makeIC(spec(nlb()), status(elb())),
			expectedResult:          true,
			expectedIC:              makeIC(spec(nlb()), status(nlbWithNullParameters())),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer type changed from NLB to ELB",
			ic:                      makeIC(spec(elb()), status(nlb())),
			expectedResult:          true,
			expectedIC:              makeIC(spec(elb()), status(elbWithNullParameters())),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer type changed from ELB to NLB, with old ELB parameters removal",
			ic:                      makeIC(spec(nlb()), status(elbWithNullParameters())),
			expectedResult:          true,
			expectedIC:              makeIC(spec(nlb()), status(nlbWithNullParameters())),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer type changed from NLB to ELB, with old NLB parameters removal",
			ic:                      makeIC(spec(elb()), status(nlbWithNullParameters())),
			expectedResult:          true,
			expectedIC:              makeIC(spec(elb()), status(elbWithNullParameters())),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer type changed from NLB to unset, with Classic LB as default",
			ic:                      makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))), status(nlb())),
			ingressConfig:           ingressConfigWithDefaultClassicLB,
			expectedResult:          false,
			expectedIC:              makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))), status(nlb())),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer type changed from Classic LB to unset, with NLB as default",
			ic:                      makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))), status(elb())),
			ingressConfig:           ingressConfigWithDefaultNLB,
			expectedResult:          false,
			expectedIC:              makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))), status(elb())),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer type changed from NLB to unset, with NLB as default",
			ic:                      makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))), status(nlb())),
			ingressConfig:           ingressConfigWithDefaultNLB,
			expectedResult:          false,
			expectedIC:              makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))), status(nlb())),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer type with empty platform parameters changed from NLB to unset, with NLB as default (OCPBUGS-43692)",
			ic:                      makeIC(spec(eps(lbsWithEmptyPlatformParameters(operatorv1.AWSLoadBalancerProvider))), status(nlb())),
			ingressConfig:           ingressConfigWithDefaultNLB,
			expectedResult:          false,
			expectedIC:              makeIC(spec(eps(lbsWithEmptyPlatformParameters(operatorv1.AWSLoadBalancerProvider))), status(nlbWithNullParameters())),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer ELB connection idle timeout changed from unset with null provider parameters to 2m",
			ic:                      makeIC(spec(elbWithIdleTimeout(metav1.Duration{Duration: 2 * time.Minute})), status(elbWithNullParameters())),
			expectedResult:          true,
			expectedIC:              makeIC(spec(elbWithIdleTimeout(metav1.Duration{Duration: 2 * time.Minute})), status(elbWithIdleTimeout(metav1.Duration{Duration: 2 * time.Minute}))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer ELB connection idle timeout changed from unset with empty provider parameters to 2m",
			ic:                      makeIC(spec(elbWithIdleTimeout(metav1.Duration{Duration: 2 * time.Minute})), status(elbWithIdleTimeout(metav1.Duration{}))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(elbWithIdleTimeout(metav1.Duration{Duration: 2 * time.Minute})), status(elbWithIdleTimeout(metav1.Duration{Duration: 2 * time.Minute}))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer ELB connection idle timeout changed from unset to -1s",
			ic:                      makeIC(spec(elbWithIdleTimeout(metav1.Duration{Duration: -1 * time.Second})), status(elb())),
			expectedResult:          true,
			expectedIC:              makeIC(spec(elbWithIdleTimeout(metav1.Duration{Duration: -1 * time.Second})), status(elbWithIdleTimeout(metav1.Duration{}))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer ELB connection idle timeout changed from 2m to unset",
			ic:                      makeIC(spec(elb()), status(elbWithIdleTimeout(metav1.Duration{Duration: 2 * time.Minute}))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(elb()), status(elbWithIdleTimeout(metav1.Duration{}))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer GCP Global Access changed from unset to global",
			ic:                      makeIC(spec(gcpLB(operatorv1.GCPGlobalAccess)), status(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS)))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(gcpLB(operatorv1.GCPGlobalAccess)), status(gcpLB(operatorv1.GCPGlobalAccess))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer GCP Global Access changed from global to local",
			ic:                      makeIC(spec(gcpLB(operatorv1.GCPLocalAccess)), status(gcpLB(operatorv1.GCPGlobalAccess))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(gcpLB(operatorv1.GCPLocalAccess)), status(gcpLB(operatorv1.GCPLocalAccess))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer GCP Global Access with empty platform provider parameters changed from global to unset (OCPBUGS-43692)",
			ic:                      makeIC(spec(eps(lbsWithEmptyPlatformParameters(operatorv1.GCPLoadBalancerProvider))), status(gcpLB(operatorv1.GCPGlobalAccess))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(eps(lbsWithEmptyPlatformParameters(operatorv1.GCPLoadBalancerProvider))), status(gcpLB(""))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer IBM Protocol changed from unset to PROXY",
			ic:                      makeIC(spec(ibmLB(operatorv1.ProxyProtocol)), status(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS)))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(ibmLB(operatorv1.ProxyProtocol)), status(ibmLB(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer IBM Protocol changed from TCP to PROXY",
			ic:                      makeIC(spec(ibmLB(operatorv1.ProxyProtocol)), status(ibmLB(operatorv1.TCPProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(ibmLB(operatorv1.ProxyProtocol)), status(ibmLB(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer IBM Protocol changed from PROXY to TCP",
			ic:                      makeIC(spec(ibmLB(operatorv1.TCPProtocol)), status(ibmLB(operatorv1.ProxyProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(ibmLB(operatorv1.TCPProtocol)), status(ibmLB(operatorv1.TCPProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer IBM Protocol with empty platlform parameters changed from PROXY to TCP",
			ic:                      makeIC(spec(eps(lbsWithEmptyPlatformParameters(operatorv1.IBMLoadBalancerProvider))), status(ibmLB(operatorv1.ProxyProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(eps(lbsWithEmptyPlatformParameters(operatorv1.IBMLoadBalancerProvider))), status(ibmLB(""))),
			domainMatchesBaseDomain: true,
		},
		{
			// https://bugzilla.redhat.com/show_bug.cgi?id=1997226
			name:                    "nodeport protocol changed to PROXY with null status.endpointPublishingStrategy.nodePort",
			ic:                      makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePortWithNull())),
			expectedResult:          true,
			expectedIC:              makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePort(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "nodeport spec.endpointPublishingStrategy.nodePort set to null",
			ic:                      makeIC(spec(nodePortWithNull()), status(nodePort(operatorv1.TCPProtocol))),
			expectedResult:          false,
			expectedIC:              makeIC(spec(nodePortWithNull()), status(nodePort(operatorv1.TCPProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "nodeport protocol changed from empty to PROXY",
			ic:                      makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePort(""))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePort(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "nodeport protocol changed from TCP to PROXY",
			ic:                      makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePort(operatorv1.TCPProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePort(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "nodeport protocol changed from PROXY to TCP",
			ic:                      makeIC(spec(nodePort(operatorv1.TCPProtocol)), status(nodePort(operatorv1.ProxyProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(nodePort(operatorv1.TCPProtocol)), status(nodePort(operatorv1.TCPProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			// https://bugzilla.redhat.com/show_bug.cgi?id=1997226
			name:                    "hostnetwork protocol changed to PROXY with null status.endpointPublishingStrategy.hostNetwork",
			ic:                      makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetworkWithNull())),
			expectedResult:          true,
			expectedIC:              makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetwork(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "hostnetwork protocol changed from empty to PROXY",
			ic:                      makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetwork(""))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetwork(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "hostnetwork protocol changed from TCP to PROXY",
			ic:                      makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetwork(operatorv1.TCPProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetwork(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "hostnetwork protocol changed from PROXY to TCP",
			ic:                      makeIC(spec(hostNetwork(operatorv1.TCPProtocol)), status(hostNetwork(operatorv1.ProxyProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(hostNetwork(operatorv1.TCPProtocol)), status(hostNetwork(operatorv1.TCPProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "hostnetwork ports changed",
			ic:                      makeIC(spec(customHostNetwork(8080, 8443, 8136, operatorv1.TCPProtocol)), status(hostNetwork(operatorv1.TCPProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(customHostNetwork(8080, 8443, 8136, operatorv1.TCPProtocol)), status(customHostNetwork(8080, 8443, 8136, operatorv1.TCPProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "hostnetwork ports changed, with status null",
			ic:                      makeIC(spec(customHostNetwork(8080, 8443, 8136, operatorv1.TCPProtocol)), status(hostNetworkWithNull())),
			expectedResult:          true,
			expectedIC:              makeIC(spec(customHostNetwork(8080, 8443, 8136, operatorv1.TCPProtocol)), status(customHostNetwork(8080, 8443, 8136, operatorv1.TCPProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "hostnetwork ports removed",
			ic:                      makeIC(spec(hostNetwork(operatorv1.TCPProtocol)), status(customHostNetwork(8080, 8443, 8136, operatorv1.TCPProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(hostNetwork(operatorv1.TCPProtocol)), status(hostNetwork(operatorv1.TCPProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "hostnetwork ports removed, with spec null",
			ic:                      makeIC(spec(hostNetworkWithNull()), status(customHostNetwork(8080, 8443, 8136, operatorv1.TCPProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(hostNetworkWithNull()), status(hostNetwork(operatorv1.TCPProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "private protocol changed to PROXY with null status.endpointPublishingStrategy.private",
			ic:                      makeIC(spec(private(operatorv1.ProxyProtocol)), status(privateWithNull())),
			expectedResult:          true,
			expectedIC:              makeIC(spec(private(operatorv1.ProxyProtocol)), status(private(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "private spec.endpointPublishingStrategy.private set to null",
			ic:                      makeIC(spec(privateWithNull()), status(private(operatorv1.TCPProtocol))),
			expectedResult:          false,
			expectedIC:              makeIC(spec(privateWithNull()), status(private(operatorv1.TCPProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "private protocol changed from empty to PROXY",
			ic:                      makeIC(spec(private(operatorv1.ProxyProtocol)), status(private(""))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(private(operatorv1.ProxyProtocol)), status(private(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "private protocol changed from TCP to PROXY",
			ic:                      makeIC(spec(private(operatorv1.ProxyProtocol)), status(private(operatorv1.TCPProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(private(operatorv1.ProxyProtocol)), status(private(operatorv1.ProxyProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "private protocol changed from PROXY to TCP",
			ic:                      makeIC(spec(private(operatorv1.TCPProtocol)), status(private(operatorv1.ProxyProtocol))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(private(operatorv1.TCPProtocol)), status(private(operatorv1.TCPProtocol))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer dnsManagementPolicy changed from Managed to Unmanaged",
			ic:                      makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &unmanagedDNS))), status(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS)))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &unmanagedDNS))), status(eps(lbs(operatorv1.ExternalLoadBalancer, &unmanagedDNS)))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "loadbalancer dnsManagementPolicy changed from Unmanaged to Managed",
			ic:                      makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))), status(eps(lbs(operatorv1.ExternalLoadBalancer, &unmanagedDNS)))),
			expectedResult:          true,
			expectedIC:              makeIC(spec(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS))), status(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS)))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "when endpointPublishingStrategy is nil, loadbalancer dnsManagementPolicy defaults to Unmanaged due to domain mismatch with base domain",
			ic:                      makeIC(spec(nil), status(nil)),
			expectedResult:          true,
			expectedIC:              makeIC(spec(nil), status(eps(lbs(operatorv1.ExternalLoadBalancer, &unmanagedDNS)))),
			domainMatchesBaseDomain: false,
		},
		{
			name:                    "when endpointPublishingStrategy is nil and lbType in ingress config is set to Classic",
			ic:                      makeIC(spec(nil), status(nil)),
			ingressConfig:           ingressConfigWithDefaultClassicLB,
			expectedResult:          true,
			expectedIC:              makeIC(spec(nil), status(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS)))),
			domainMatchesBaseDomain: true,
		},
		{
			name:                    "when endpointPublishingStrategy is nil and lbType in ingress config is set to NLB",
			ic:                      makeIC(spec(nil), status(nil)),
			ingressConfig:           ingressConfigWithDefaultNLB,
			expectedResult:          true,
			expectedIC:              makeIC(spec(nil), status(eps(lbs(operatorv1.ExternalLoadBalancer, &managedDNS)))),
			domainMatchesBaseDomain: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ic := tc.ic.DeepCopy()
			platformStatus := &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			}
			ingressConfig := tc.ingressConfig
			if ingressConfig == nil {
				ingressConfig = &configv1.Ingress{}
			}
			alreadyAdmitted := true
			if actualResult := setDefaultPublishingStrategy(ic, platformStatus, tc.domainMatchesBaseDomain, ingressConfig, alreadyAdmitted); actualResult != tc.expectedResult {
				t.Errorf("expected result %v, got %v", tc.expectedResult, actualResult)
			}
			if diff := cmp.Diff(tc.expectedIC, ic); len(diff) != 0 {
				t.Errorf("got expected ingresscontroller: %s", diff)
			}
		})
	}
}

func Test_tlsProfileSpecForSecurityProfile(t *testing.T) {
	invalidTLSVersion := configv1.TLSProtocolVersion("abc")
	invalidCiphers := []string{"ECDHE-ECDSA-AES256-GCM-SHA384", "invalid cipher"}
	validCiphers := []string{"ECDHE-ECDSA-AES256-GCM-SHA384"}
	tlsVersion13Ciphers := []string{
		"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "TLS_CHACHA20_POLY1305_SHA256",
		"TLS_AES_128_CCM_SHA256", "TLS_AES_128_CCM_8_SHA256", "TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384",
		"TLS_CHACHA20_POLY1305_SHA256", "TLS_AES_128_CCM_SHA256", "TLS_AES_128_CCM_8_SHA256",
	}
	tlsVersion1213Ciphers := []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "ECDHE-ECDSA-AES256-GCM-SHA384"}
	testCases := []struct {
		description  string
		profile      *configv1.TLSSecurityProfile
		valid        bool
		expectedSpec *configv1.TLSProfileSpec
	}{
		{
			description:  "default (nil)",
			profile:      nil,
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "default (empty)",
			profile:      &configv1.TLSSecurityProfile{},
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description: "old",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileOldType,
			},
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description: "intermediate",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileIntermediateType,
			},
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description: "modern",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileModernType,
			},
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileModernType],
		},
		{
			description: "custom, nil profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
			},
			valid: false,
		},
		{
			description: "custom, empty profile",
			profile: &configv1.TLSSecurityProfile{
				Type:   configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{},
			},
			valid: false,
		},
		{
			description: "custom, invalid ciphers",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers:       invalidCiphers,
						MinTLSVersion: configv1.VersionTLS10,
					},
				},
			},
			valid: false,
		},
		{
			description: "custom, invalid tls v1.3 only ciphers",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers:       tlsVersion13Ciphers,
						MinTLSVersion: configv1.VersionTLS10,
					},
				},
			},
			valid: false,
		},
		{
			description: "custom, mixed tls v1.2 and v1.3 ciphers",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers:       tlsVersion1213Ciphers,
						MinTLSVersion: configv1.VersionTLS10,
					},
				},
			},
			valid: true,
		},
		{
			description: "custom, invalid minimum security protocol version",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers:       validCiphers,
						MinTLSVersion: invalidTLSVersion,
					},
				},
			},
			valid: false,
		},
		{
			description: "custom, valid minimum security protocol version",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers:       validCiphers,
						MinTLSVersion: configv1.VersionTLS10,
					},
				},
			},
			valid: true,
			expectedSpec: &configv1.TLSProfileSpec{
				Ciphers:       validCiphers,
				MinTLSVersion: configv1.VersionTLS10,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ic := &operatorv1.IngressController{
				Spec: operatorv1.IngressControllerSpec{
					TLSSecurityProfile: tc.profile,
				},
			}
			tlsProfileSpec := tlsProfileSpecForSecurityProfile(ic.Spec.TLSSecurityProfile)
			err := validateTLSSecurityProfile(ic)
			if tc.valid && err != nil {
				t.Fatalf("unexpected error: %v\nprofile:\n%s", err, util.ToYaml(tlsProfileSpec))
			}
			if !tc.valid && err == nil {
				t.Fatalf("expected error for profile:\n%s", util.ToYaml(tlsProfileSpec))
			}
			if tc.expectedSpec != nil && !reflect.DeepEqual(tc.expectedSpec, tlsProfileSpec) {
				t.Fatalf("expected profile:\n%s\ngot profile:\n%s", util.ToYaml(tc.expectedSpec), util.ToYaml(tlsProfileSpec))
			}
			t.Logf("got expected values; profile:\n%s\nerror value: %v", util.ToYaml(tlsProfileSpec), err)
		})
	}
}

func Test_tlsProfileSpecForIngressController(t *testing.T) {
	testCases := []struct {
		description  string
		icProfile    *configv1.TLSSecurityProfile
		apiProfile   *configv1.TLSSecurityProfile
		expectedSpec *configv1.TLSProfileSpec
	}{
		{
			description:  "nil, nil -> intermediate",
			icProfile:    nil,
			apiProfile:   nil,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "nil, empty -> intermediate",
			icProfile:    nil,
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "nil, old -> old",
			icProfile:    nil,
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "nil, invalid -> invalid",
			icProfile:    nil,
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "empty, nil -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   nil,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "empty, empty -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "empty, old -> old",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "empty, invalid -> invalid",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "old, nil -> old",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			apiProfile:   nil,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "old, empty -> old",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "old, modern -> old",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "old, invalid -> old",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "invalid, nil -> invalid",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "invalid, empty -> invalid",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "invalid, old -> invalid",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "invalid, invalid -> invalid",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "modern, nil -> modern",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileModernType],
		},
		{
			description:  "nil, modern -> intermediate",
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileModernType],
		},
		{
			description:  "modern, empty -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileModernType],
		},
		{
			description:  "empty, modern -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileModernType],
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {

			ic := &operatorv1.IngressController{
				Spec: operatorv1.IngressControllerSpec{
					TLSSecurityProfile: tc.icProfile,
				},
			}
			api := &configv1.APIServer{
				Spec: configv1.APIServerSpec{
					TLSSecurityProfile: tc.apiProfile,
				},
			}
			tlsProfileSpec := tlsProfileSpecForIngressController(ic, api)
			if !reflect.DeepEqual(tc.expectedSpec, tlsProfileSpec) {
				t.Errorf("expected profile:\n%v\ngot profile:\n%v", util.ToYaml(tc.expectedSpec), util.ToYaml(tlsProfileSpec))
			}
		})
	}
}

func Test_validateHTTPHeaderBufferValues(t *testing.T) {
	testCases := []struct {
		description   string
		tuningOptions operatorv1.IngressControllerTuningOptions
		valid         bool
	}{
		{
			description:   "no tuningOptions values",
			tuningOptions: operatorv1.IngressControllerTuningOptions{},
			valid:         true,
		},
		{
			description: "valid tuningOptions values",
			tuningOptions: operatorv1.IngressControllerTuningOptions{
				HeaderBufferBytes:           32768,
				HeaderBufferMaxRewriteBytes: 8192,
			},
			valid: true,
		},
		{
			description: "invalid tuningOptions values",
			tuningOptions: operatorv1.IngressControllerTuningOptions{
				HeaderBufferBytes:           8192,
				HeaderBufferMaxRewriteBytes: 32768,
			},
			valid: false,
		},
		{
			description: "invalid tuningOptions values, HeaderBufferMaxRewriteBytes not set",
			tuningOptions: operatorv1.IngressControllerTuningOptions{
				HeaderBufferBytes: 1,
			},
			valid: false,
		},
		{
			description: "invalid tuningOptions values, HeaderBufferBytes not set",
			tuningOptions: operatorv1.IngressControllerTuningOptions{
				HeaderBufferMaxRewriteBytes: 65536,
			},
			valid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ic := &operatorv1.IngressController{}
			ic.Spec.TuningOptions = tc.tuningOptions
			err := validateHTTPHeaderBufferValues(ic)
			if tc.valid && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if !tc.valid && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

// TestValidateClientTLS verifies the validateClientTLS accepts PCRE-compliant
// patterns and rejects invalid patterns.
func Test_validateClientTLS(t *testing.T) {
	testCases := []struct {
		description string
		pattern     string
		expectError bool
	}{
		{
			description: "glob",
			pattern:     "*.openshift.com",
			expectError: true,
		},
		{
			description: "invalid regexp",
			pattern:     "foo.**bar",
			expectError: true,
		},
		{
			description: "valid PCRE pattern",
			pattern:     "foo.*?bar",
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			clientTLS := operatorv1.ClientTLS{
				ClientCertificatePolicy: operatorv1.ClientCertificatePolicyRequired,
				ClientCA: configv1.ConfigMapNameReference{
					Name: "client-tls-ca",
				},
				AllowedSubjectPatterns: []string{tc.pattern},
			}
			ic := &operatorv1.IngressController{
				Spec: operatorv1.IngressControllerSpec{
					ClientTLS: clientTLS,
				},
			}
			switch err := validateClientTLS(ic); {
			case err == nil && tc.expectError:
				t.Error("expected error, got nil")
			case err != nil && !tc.expectError:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// Test_IsProxyProtocolNeeded verifies that IsProxyProtocolNeeded returns the
// expected values for various platforms and endpoint publishing strategy
// parameters.
func Test_IsProxyProtocolNeeded(t *testing.T) {
	var (
		awsPlatform = configv1.PlatformStatus{
			Type: configv1.AWSPlatformType,
		}
		azurePlatform = configv1.PlatformStatus{
			Type: configv1.AzurePlatformType,
		}
		gcpPlatform = configv1.PlatformStatus{
			Type: configv1.GCPPlatformType,
		}
		bareMetalPlatform = configv1.PlatformStatus{
			Type: configv1.BareMetalPlatformType,
		}
		ibmcloudPlatform = configv1.PlatformStatus{
			Type: configv1.IBMCloudPlatformType,
		}

		hostNetworkStrategy = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.HostNetworkStrategyType,
		}
		hostNetworkStrategyWithDefault = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.HostNetworkStrategyType,
			HostNetwork: &operatorv1.HostNetworkStrategy{
				Protocol: operatorv1.DefaultProtocol,
			},
		}
		hostNetworkStrategyWithTCP = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.HostNetworkStrategyType,
			HostNetwork: &operatorv1.HostNetworkStrategy{
				Protocol: operatorv1.TCPProtocol,
			},
		}
		hostNetworkStrategyWithPROXY = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.HostNetworkStrategyType,
			HostNetwork: &operatorv1.HostNetworkStrategy{
				Protocol: operatorv1.ProxyProtocol,
			},
		}
		loadBalancerStrategy = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.LoadBalancerServiceStrategyType,
		}
		loadBalancerStrategyWithNLB = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.LoadBalancerServiceStrategyType,
			LoadBalancer: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSNetworkLoadBalancer,
					},
				},
			},
		}
		loadBalancerStrategyWithIBMCloudPROXY = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.LoadBalancerServiceStrategyType,
			LoadBalancer: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.IBMLoadBalancerProvider,
					IBM: &operatorv1.IBMLoadBalancerParameters{
						Protocol: operatorv1.ProxyProtocol,
					},
				},
			},
		}
		nodePortStrategy = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.NodePortServiceStrategyType,
		}
		nodePortStrategyWithDefault = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.NodePortServiceStrategyType,
			NodePort: &operatorv1.NodePortStrategy{
				Protocol: operatorv1.DefaultProtocol,
			},
		}
		nodePortStrategyWithTCP = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.NodePortServiceStrategyType,
			NodePort: &operatorv1.NodePortStrategy{
				Protocol: operatorv1.TCPProtocol,
			},
		}
		nodePortStrategyWithPROXY = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.NodePortServiceStrategyType,
			NodePort: &operatorv1.NodePortStrategy{
				Protocol: operatorv1.ProxyProtocol,
			},
		}
		privateStrategy = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.PrivateStrategyType,
		}
		privateStrategyWithDefault = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.PrivateStrategyType,
			Private: &operatorv1.PrivateStrategy{
				Protocol: operatorv1.DefaultProtocol,
			},
		}
		privateStrategyWithTCP = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.PrivateStrategyType,
			Private: &operatorv1.PrivateStrategy{
				Protocol: operatorv1.TCPProtocol,
			},
		}
		privateStrategyWithPROXY = operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.PrivateStrategyType,
			Private: &operatorv1.PrivateStrategy{
				Protocol: operatorv1.ProxyProtocol,
			},
		}
	)
	testCases := []struct {
		description string
		strategy    *operatorv1.EndpointPublishingStrategy
		platform    *configv1.PlatformStatus
		expect      bool
		expectError bool
	}{
		{
			description: "nil platformStatus should cause an error",
			strategy:    &loadBalancerStrategy,
			platform:    nil,
			expectError: true,
		},
		{
			description: "hostnetwork strategy shouldn't use PROXY",
			strategy:    &hostNetworkStrategy,
			platform:    &bareMetalPlatform,
			expect:      false,
		},
		{
			description: "hostnetwork strategy specifying default shouldn't use PROXY",
			strategy:    &hostNetworkStrategyWithDefault,
			platform:    &bareMetalPlatform,
			expect:      false,
		},
		{
			description: "hostnetwork strategy specifying TCP shouldn't use PROXY",
			strategy:    &hostNetworkStrategyWithTCP,
			platform:    &bareMetalPlatform,
			expect:      false,
		},
		{
			description: "hostnetwork strategy specifying PROXY should use PROXY",
			strategy:    &hostNetworkStrategyWithPROXY,
			platform:    &bareMetalPlatform,
			expect:      true,
		},
		{
			description: "loadbalancer strategy with ELB should use PROXY",
			strategy:    &loadBalancerStrategy,
			platform:    &awsPlatform,
			expect:      true,
		},
		{
			description: "loadbalancer strategy with NLB shouldn't use PROXY",
			strategy:    &loadBalancerStrategyWithNLB,
			platform:    &awsPlatform,
			expect:      false,
		},
		{
			description: "loadbalancer strategy shouldn't use PROXY on Azure",
			strategy:    &loadBalancerStrategy,
			platform:    &azurePlatform,
			expect:      false,
		},
		{
			description: "loadbalancer strategy shouldn't use PROXY on GCP",
			strategy:    &loadBalancerStrategy,
			platform:    &gcpPlatform,
			expect:      false,
		},
		{
			description: "loadbalancer strategy shouldn't use PROXY on IBMCloud",
			strategy:    &loadBalancerStrategy,
			platform:    &ibmcloudPlatform,
			expect:      false,
		},
		{
			description: "loadbalancer strategy with ProxyProtocol enabled on IBMCloud should use PROXY",
			strategy:    &loadBalancerStrategyWithIBMCloudPROXY,
			platform:    &ibmcloudPlatform,
			expect:      true,
		},
		{
			description: "empty nodeport strategy shouldn't use PROXY",
			strategy:    &nodePortStrategy,
			platform:    &awsPlatform,
			expect:      false,
		},
		{
			description: "nodeport strategy specifying default shouldn't use PROXY",
			strategy:    &nodePortStrategyWithDefault,
			platform:    &awsPlatform,
			expect:      false,
		},
		{
			description: "nodeport strategy specifying TCP shouldn't use PROXY",
			strategy:    &nodePortStrategyWithTCP,
			platform:    &awsPlatform,
			expect:      false,
		},
		{
			description: "nodeport strategy specifying PROXY should use PROXY",
			strategy:    &nodePortStrategyWithPROXY,
			platform:    &awsPlatform,
			expect:      true,
		},
		{
			description: "empty private strategy shouldn't use PROXY",
			strategy:    &privateStrategy,
			platform:    &awsPlatform,
			expect:      false,
		},
		{
			description: "private strategy specifying default shouldn't use PROXY",
			strategy:    &privateStrategyWithDefault,
			platform:    &awsPlatform,
			expect:      false,
		},
		{
			description: "private strategy specifying TCP shouldn't use PROXY",
			strategy:    &privateStrategyWithTCP,
			platform:    &awsPlatform,
			expect:      false,
		},
		{
			description: "private strategy specifying PROXY should use PROXY",
			strategy:    &privateStrategyWithPROXY,
			platform:    &awsPlatform,
			expect:      true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ic := &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: tc.strategy,
				},
			}
			switch actual, err := IsProxyProtocolNeeded(ic, tc.platform); {
			case tc.expectError && err == nil:
				t.Error("expected error, got nil")
			case !tc.expectError && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.expect != actual:
				t.Errorf("expected %t, got %t", tc.expect, actual)
			}
		})
	}
}

func Test_computeUpdatedInfraFromService(t *testing.T) {
	var (
		awsPlatform = configv1.PlatformStatus{
			Type: configv1.AWSPlatformType,
		}
		gcpPlatform = configv1.PlatformStatus{
			Type: configv1.GCPPlatformType,
		}
		gcpPlatformWithDNSType = configv1.PlatformStatus{
			Type: configv1.GCPPlatformType,
			GCP: &configv1.GCPPlatformStatus{
				CloudLoadBalancerConfig: &configv1.CloudLoadBalancerConfig{
					DNSType: configv1.ClusterHostedDNSType,
				},
			},
		}
		ingresses = []corev1.LoadBalancerIngress{
			{IP: "196.78.125.4"},
		}
		IngressLBIP         = configv1.IP("196.78.125.4")
		gcpPlatformWithLBIP = configv1.PlatformStatus{
			Type: configv1.GCPPlatformType,
			GCP: &configv1.GCPPlatformStatus{
				CloudLoadBalancerConfig: &configv1.CloudLoadBalancerConfig{
					DNSType: configv1.ClusterHostedDNSType,
					ClusterHosted: &configv1.CloudLoadBalancerIPs{
						IngressLoadBalancerIPs: []configv1.IP{
							IngressLBIP,
						},
					},
				},
			},
		}
		ingressesWithMultipleIPs = []corev1.LoadBalancerIngress{
			{IP: "196.78.125.4"},
			{IP: "10.10.10.4"},
		}
	)
	testCases := []struct {
		description   string
		platform      *configv1.PlatformStatus
		ingresses     []corev1.LoadBalancerIngress
		expectedInfra configv1.Infrastructure
		expectUpdated bool
		expectError   bool
	}{
		{
			description:   "nil platformStatus should cause an error",
			platform:      nil,
			ingresses:     []corev1.LoadBalancerIngress{},
			expectUpdated: false,
			expectError:   true,
		},
		{
			description:   "unsupported platform should not cause an error",
			platform:      &awsPlatform,
			ingresses:     []corev1.LoadBalancerIngress{},
			expectUpdated: false,
			expectError:   false,
		},
		{
			description:   "gcp platform without DNSType set",
			platform:      &gcpPlatform,
			ingresses:     []corev1.LoadBalancerIngress{},
			expectUpdated: false,
			expectError:   false,
		},
		{
			description:   "gcp platform with DNSType and no LB IP",
			platform:      &gcpPlatformWithDNSType,
			ingresses:     []corev1.LoadBalancerIngress{},
			expectUpdated: false,
			expectError:   false,
		},
		{
			description:   "gcp platform with DNSType and no LB IP in infra config, service has 1 IP",
			platform:      &gcpPlatformWithDNSType,
			ingresses:     ingresses,
			expectUpdated: true,
			expectError:   false,
		},
		{
			description:   "gcp platform with no change to LB IPs",
			platform:      &gcpPlatformWithLBIP,
			ingresses:     ingresses,
			expectUpdated: false,
			expectError:   false,
		},
		{
			description:   "gcp platform with DNSType and LB IP",
			platform:      &gcpPlatformWithLBIP,
			ingresses:     ingressesWithMultipleIPs,
			expectUpdated: true,
			expectError:   false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			infraConfig := &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					PlatformStatus: tc.platform,
				},
			}
			service := &corev1.Service{}
			for _, ingress := range tc.ingresses {
				service.Status.LoadBalancer.Ingress = append(service.Status.LoadBalancer.Ingress, ingress)
			}

			updated, err := computeUpdatedInfraFromService(service, infraConfig)
			switch {
			case tc.expectError && err == nil:
				t.Error("expected error, got nil")
			case !tc.expectError && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.expectUpdated != updated:
				t.Errorf("expected %t, got %t", tc.expectUpdated, updated)
			}
			if updated {
				ingressLBs := service.Status.LoadBalancer.Ingress
				for _, ingress := range ingressLBs {
					if len(ingress.IP) > 0 {
						if !slices.Contains(infraConfig.Status.PlatformStatus.GCP.CloudLoadBalancerConfig.ClusterHosted.IngressLoadBalancerIPs, configv1.IP(ingress.IP)) {
							t.Errorf("expected Infra CR to contain %s", ingress.IP)
						}
					}
				}
			}
		})
	}
}
