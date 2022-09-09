package ingress

import (
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
)

// TestSetDefaultDomain verifies that setDefaultDomain behaves correctly.
func TestSetDefaultDomain(t *testing.T) {
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
			ic:             makeIC(ingressConfig.Spec.Domain, "otherdomain.com"),
			expectedIC:     makeIC(ingressConfig.Spec.Domain, "otherdomain.com"),
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
						Scope: operatorv1.ExternalLoadBalancer,
					},
				},
			},
		}
		ingressControllerWithHostNetwork = &operatorv1.IngressController{
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: operatorv1.HostNetworkStrategyType,
					HostNetwork: &operatorv1.HostNetworkStrategy{
						Protocol: operatorv1.TCPProtocol,
					},
				},
			},
		}
		makeInfra = func(platform configv1.PlatformType) *configv1.Infrastructure {
			return &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					Platform: platform,
				},
			}
		}
	)

	testCases := []struct {
		name        string
		infraConfig *configv1.Infrastructure
		expectedIC  *operatorv1.IngressController
	}{
		{
			name:        "AWS",
			infraConfig: makeInfra(configv1.AWSPlatformType),
			expectedIC:  ingressControllerWithLoadBalancer,
		},
		{
			name:        "Azure",
			infraConfig: makeInfra(configv1.AzurePlatformType),
			expectedIC:  ingressControllerWithLoadBalancer,
		},
		{
			name:        "Bare metal",
			infraConfig: makeInfra(configv1.BareMetalPlatformType),
			expectedIC:  ingressControllerWithHostNetwork,
		},
		{
			name:        "Equinix Metal",
			infraConfig: makeInfra(configv1.EquinixMetalPlatformType),
			expectedIC:  ingressControllerWithHostNetwork,
		},
		{
			name:        "GCP",
			infraConfig: makeInfra(configv1.GCPPlatformType),
			expectedIC:  ingressControllerWithLoadBalancer,
		},
		{
			name:        "IBM Cloud",
			infraConfig: makeInfra(configv1.IBMCloudPlatformType),
			expectedIC:  ingressControllerWithLoadBalancer,
		},
		{
			name:        "Libvirt",
			infraConfig: makeInfra(configv1.LibvirtPlatformType),
			expectedIC:  ingressControllerWithHostNetwork,
		},
		{
			name:        "No platform",
			infraConfig: makeInfra(configv1.NonePlatformType),
			expectedIC:  ingressControllerWithHostNetwork,
		},
		{
			name:        "OpenStack",
			infraConfig: makeInfra(configv1.OpenStackPlatformType),
			expectedIC:  ingressControllerWithHostNetwork,
		},
		{
			name:        "RHV",
			infraConfig: makeInfra(configv1.OvirtPlatformType),
			expectedIC:  ingressControllerWithHostNetwork,
		},
		{
			name:        "vSphere",
			infraConfig: makeInfra(configv1.VSpherePlatformType),
			expectedIC:  ingressControllerWithHostNetwork,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ic := &operatorv1.IngressController{}
			infraConfig := tc.infraConfig.DeepCopy()
			if actualResult := setDefaultPublishingStrategy(ic, infraConfig); actualResult != true {
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
		makeIC = func(spec operatorv1.IngressControllerSpec, status operatorv1.IngressControllerStatus) *operatorv1.IngressController {
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
		lb = func(scope operatorv1.LoadBalancerScope) *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: scope,
				},
			}
		}
		elb = func() *operatorv1.EndpointPublishingStrategy {
			eps := lb(operatorv1.ExternalLoadBalancer)
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.AWSLoadBalancerProvider,
				AWS: &operatorv1.AWSLoadBalancerParameters{
					Type: operatorv1.AWSClassicLoadBalancer,
				},
			}
			return eps
		}
		nlb = func() *operatorv1.EndpointPublishingStrategy {
			eps := lb(operatorv1.ExternalLoadBalancer)
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.AWSLoadBalancerProvider,
				AWS: &operatorv1.AWSLoadBalancerParameters{
					Type: operatorv1.AWSNetworkLoadBalancer,
				},
			}
			return eps
		}
		gcpLB = func(clientAccess operatorv1.GCPClientAccess) *operatorv1.EndpointPublishingStrategy {
			eps := lb(operatorv1.ExternalLoadBalancer)
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.GCPLoadBalancerProvider,
				GCP: &operatorv1.GCPLoadBalancerParameters{
					ClientAccess: clientAccess,
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
					Protocol: proto,
				},
			}
		}
		hostNetworkWithNull = func() *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.HostNetworkStrategyType,
			}
		}
	)

	testCases := []struct {
		name           string
		ic             *operatorv1.IngressController
		expectedIC     *operatorv1.IngressController
		expectedResult bool
	}{
		{
			name:           "loadbalancer scope changed from external to internal",
			ic:             makeIC(spec(lb(operatorv1.InternalLoadBalancer)), status(lb(operatorv1.ExternalLoadBalancer))),
			expectedResult: false,
			expectedIC:     makeIC(spec(lb(operatorv1.InternalLoadBalancer)), status(lb(operatorv1.ExternalLoadBalancer))),
		},
		{
			name:           "loadbalancer scope changed from internal to external",
			ic:             makeIC(spec(lb(operatorv1.ExternalLoadBalancer)), status(lb(operatorv1.InternalLoadBalancer))),
			expectedResult: false,
			expectedIC:     makeIC(spec(lb(operatorv1.ExternalLoadBalancer)), status(lb(operatorv1.InternalLoadBalancer))),
		},
		{
			name:           "loadbalancer type changed from ELB to NLB",
			ic:             makeIC(spec(nlb()), status(elb())),
			expectedResult: false,
			expectedIC:     makeIC(spec(nlb()), status(elb())),
		},
		{
			name:           "loadbalancer type changed from NLB to ELB",
			ic:             makeIC(spec(elb()), status(nlb())),
			expectedResult: false,
			expectedIC:     makeIC(spec(elb()), status(nlb())),
		},
		{
			name:           "loadbalancer GCP Global Access changed from unset to global",
			ic:             makeIC(spec(gcpLB(operatorv1.GCPGlobalAccess)), status(lb(operatorv1.ExternalLoadBalancer))),
			expectedResult: true,
			expectedIC:     makeIC(spec(gcpLB(operatorv1.GCPGlobalAccess)), status(gcpLB(operatorv1.GCPGlobalAccess))),
		},
		{
			name:           "loadbalancer GCP Global Access changed from global to local",
			ic:             makeIC(spec(gcpLB(operatorv1.GCPLocalAccess)), status(gcpLB(operatorv1.GCPGlobalAccess))),
			expectedResult: true,
			expectedIC:     makeIC(spec(gcpLB(operatorv1.GCPLocalAccess)), status(gcpLB(operatorv1.GCPLocalAccess))),
		},
		{
			// https://bugzilla.redhat.com/show_bug.cgi?id=1997226
			name:           "nodeport protocol changed to PROXY with null status.endpointPublishingStrategy.nodePort",
			ic:             makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePortWithNull())),
			expectedResult: true,
			expectedIC:     makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePort(operatorv1.ProxyProtocol))),
		},
		{
			name:           "nodeport spec.endpointPublishingStrategy.nodePort set to null",
			ic:             makeIC(spec(nodePortWithNull()), status(nodePort(operatorv1.TCPProtocol))),
			expectedResult: false,
			expectedIC:     makeIC(spec(nodePortWithNull()), status(nodePort(operatorv1.TCPProtocol))),
		},
		{
			name:           "nodeport protocol changed from empty to PROXY",
			ic:             makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePort(""))),
			expectedResult: true,
			expectedIC:     makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePort(operatorv1.ProxyProtocol))),
		},
		{
			name:           "nodeport protocol changed from TCP to PROXY",
			ic:             makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePort(operatorv1.TCPProtocol))),
			expectedResult: true,
			expectedIC:     makeIC(spec(nodePort(operatorv1.ProxyProtocol)), status(nodePort(operatorv1.ProxyProtocol))),
		},
		{
			name:           "nodeport protocol changed from PROXY to TCP",
			ic:             makeIC(spec(nodePort(operatorv1.TCPProtocol)), status(nodePort(operatorv1.ProxyProtocol))),
			expectedResult: true,
			expectedIC:     makeIC(spec(nodePort(operatorv1.TCPProtocol)), status(nodePort(operatorv1.TCPProtocol))),
		},
		{
			// https://bugzilla.redhat.com/show_bug.cgi?id=1997226
			name:           "hostnetwork protocol changed to PROXY with null status.endpointPublishingStrategy.hostNetwork",
			ic:             makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetworkWithNull())),
			expectedResult: true,
			expectedIC:     makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetwork(operatorv1.ProxyProtocol))),
		},
		{
			name:           "hostnetwork protocol changed from empty to PROXY",
			ic:             makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetwork(""))),
			expectedResult: true,
			expectedIC:     makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetwork(operatorv1.ProxyProtocol))),
		},
		{
			name:           "hostnetwork protocol changed from TCP to PROXY",
			ic:             makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetwork(operatorv1.TCPProtocol))),
			expectedResult: true,
			expectedIC:     makeIC(spec(hostNetwork(operatorv1.ProxyProtocol)), status(hostNetwork(operatorv1.ProxyProtocol))),
		},
		{
			name:           "hostnetwork protocol changed from PROXY to TCP",
			ic:             makeIC(spec(hostNetwork(operatorv1.TCPProtocol)), status(hostNetwork(operatorv1.ProxyProtocol))),
			expectedResult: true,
			expectedIC:     makeIC(spec(hostNetwork(operatorv1.TCPProtocol)), status(hostNetwork(operatorv1.TCPProtocol))),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ic := tc.ic.DeepCopy()
			infraConfig := &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					Platform: configv1.NonePlatformType,
				},
			}
			if actualResult := setDefaultPublishingStrategy(ic, infraConfig); actualResult != tc.expectedResult {
				t.Errorf("expected result %v, got %v", tc.expectedResult, actualResult)
			}
			if diff := cmp.Diff(tc.expectedIC, ic); len(diff) != 0 {
				t.Errorf("got expected ingresscontroller: %s", diff)
			}
		})
	}
}

func TestTLSProfileSpecForSecurityProfile(t *testing.T) {
	invalidTLSVersion := configv1.TLSProtocolVersion("abc")
	invalidCiphers := []string{"ECDHE-ECDSA-AES256-GCM-SHA384", "invalid cipher"}
	validCiphers := []string{"ECDHE-ECDSA-AES256-GCM-SHA384"}
	tlsVersion13Ciphers := []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "TLS_CHACHA20_POLY1305_SHA256",
		"TLS_AES_128_CCM_SHA256", "TLS_AES_128_CCM_8_SHA256", "TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384",
		"TLS_CHACHA20_POLY1305_SHA256", "TLS_AES_128_CCM_SHA256", "TLS_AES_128_CCM_8_SHA256"}
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
		// TODO: Update test case to use Modern cipher suites when haproxy is
		//  built with an openssl version that supports tls v1.3.
		{
			description: "modern",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileModernType,
			},
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
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
		ic := &operatorv1.IngressController{
			Spec: operatorv1.IngressControllerSpec{
				TLSSecurityProfile: tc.profile,
			},
		}
		tlsProfileSpec := tlsProfileSpecForSecurityProfile(ic.Spec.TLSSecurityProfile)
		err := validateTLSSecurityProfile(ic)
		if tc.valid && err != nil {
			t.Errorf("%q: unexpected error: %v\nprofile:\n%s", tc.description, err, toYaml(tlsProfileSpec))
			continue
		}
		if !tc.valid && err == nil {
			t.Errorf("%q: expected error for profile:\n%s", tc.description, toYaml(tlsProfileSpec))
			continue
		}
		if tc.expectedSpec != nil && !reflect.DeepEqual(tc.expectedSpec, tlsProfileSpec) {
			t.Errorf("%q: expected profile:\n%s\ngot profile:\n%s", tc.description, toYaml(tc.expectedSpec), toYaml(tlsProfileSpec))
			continue
		}
		t.Logf("%q: got expected values; profile:\n%s\nerror value: %v", tc.description, toYaml(tlsProfileSpec), err)
	}
}

func TestTLSProfileSpecForIngressController(t *testing.T) {
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
		// TODO: Update test cases to use Modern cipher suites when haproxy is
		//  built with an openssl version that supports tls v1.3.
		{
			description:  "modern, nil -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "nil, modern -> intermediate",
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "modern, empty -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "empty, modern -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
	}

	for _, tc := range testCases {
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
			t.Errorf("%q: expected profile:\n%v\ngot profile:\n%v", tc.description, toYaml(tc.expectedSpec), toYaml(tlsProfileSpec))
		}
	}
}

func TestValidateHTTPHeaderBufferValues(t *testing.T) {
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
		ic := &operatorv1.IngressController{}
		ic.Spec.TuningOptions = tc.tuningOptions
		err := validateHTTPHeaderBufferValues(ic)
		if tc.valid && err != nil {
			t.Errorf("%q: Expected valid HTTPHeaderBuffer to not return a validation error: %v", tc.description, err)
		}

		if !tc.valid && err == nil {
			t.Errorf("%q: Expected invalid HTTPHeaderBuffer to return a validation error", tc.description)
		}
	}
}

// TestIsProxyProtocolNeeded verifies that IsProxyProtocolNeeded returns the
// expected values for various platforms and endpoint publishing strategy
// parameters.
func TestIsProxyProtocolNeeded(t *testing.T) {
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
			description: "private strategy shouldn't use PROXY",
			strategy:    &privateStrategy,
			platform:    &awsPlatform,
			expect:      false,
		},
	}

	for _, tc := range testCases {
		ic := &operatorv1.IngressController{
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: tc.strategy,
			},
		}
		switch actual, err := IsProxyProtocolNeeded(ic, tc.platform); {
		case tc.expectError && err == nil:
			t.Errorf("%q: expected error, got nil", tc.description)
		case !tc.expectError && err != nil:
			t.Errorf("%q: unexpected error: %v", tc.description, err)
		case tc.expect != actual:
			t.Errorf("%q: expected %t, got %t", tc.description, tc.expect, actual)
		}
	}
}
