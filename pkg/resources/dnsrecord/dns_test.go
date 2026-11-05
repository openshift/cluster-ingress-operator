package dnsrecord

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	util "github.com/openshift/cluster-ingress-operator/pkg/util"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func Test_desiredWildcardDNSRecord(t *testing.T) {
	// azureStatus returns an Azure platform status with the given IP family,
	// which is what decides whether a record is dual-stack.
	azureStatus := func(ipFamily configv1.IPFamilyType) *configv1.PlatformStatus {
		return &configv1.PlatformStatus{
			Type:  configv1.AzurePlatformType,
			Azure: &configv1.AzurePlatformStatus{IPFamily: ipFamily},
		}
	}

	tests := []struct {
		description string
		domain      string
		publish     operatorv1.EndpointPublishingStrategy
		ingresses   []corev1.LoadBalancerIngress
		// platformStatus is nil for the single-stack cases, which must
		// behave exactly as they did before dual-stack support.
		platformStatus *configv1.PlatformStatus
		expect         *iov1.DNSRecordSpec
	}{
		{
			description: "null endpointPublishingStrategy.loadBalancer",
			domain:      "lb.cloud.example.com",
			publish: operatorv1.EndpointPublishingStrategy{
				Type:         operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: nil,
			},
			ingresses: []corev1.LoadBalancerIngress{
				{Hostname: "lb.cloud.example.com"},
			},
			expect: &iov1.DNSRecordSpec{
				DNSName:             "*.lb.cloud.example.com.",
				RecordType:          iov1.CNAMERecordType,
				Targets:             []string{"lb.cloud.example.com"},
				RecordTTL:           defaultRecordTTL,
				DNSManagementPolicy: iov1.ManagedDNS,
			},
		},
		{
			description: "no domain",
			domain:      "",
			publish: operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
			ingresses: []corev1.LoadBalancerIngress{
				{Hostname: "lb.cloud.example.com"},
			},
			expect: nil,
		},
		{
			description: "not a load balancer",
			domain:      "apps.openshift.example.com",
			publish: operatorv1.EndpointPublishingStrategy{
				Type:        operatorv1.HostNetworkStrategyType,
				HostNetwork: &operatorv1.HostNetworkStrategy{},
			},
			expect: nil,
		},
		{
			description: "no ingresses",
			domain:      "apps.openshift.example.com",
			publish: operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
			ingresses: []corev1.LoadBalancerIngress{},
			expect:    nil,
		},
		{
			description: "hostname to CNAME record",
			publish: operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
			domain: "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{Hostname: "lb.cloud.example.com"},
			},
			expect: &iov1.DNSRecordSpec{
				DNSName:             "*.apps.openshift.example.com.",
				RecordType:          iov1.CNAMERecordType,
				Targets:             []string{"lb.cloud.example.com"},
				RecordTTL:           defaultRecordTTL,
				DNSManagementPolicy: iov1.ManagedDNS,
			},
		},
		{
			description: "IP to A record",
			publish: operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
			domain: "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{IP: "192.0.2.1"},
			},
			expect: &iov1.DNSRecordSpec{
				DNSName:             "*.apps.openshift.example.com.",
				RecordType:          iov1.ARecordType,
				Targets:             []string{"192.0.2.1"},
				RecordTTL:           defaultRecordTTL,
				DNSManagementPolicy: iov1.ManagedDNS,
			},
		},
		{
			description: "dual-stack IPv4-primary publishes both addresses, IPv4 first",
			publish: operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
			domain: "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{IP: "2603:1030:c02:9::7a7"},
				{IP: "192.0.2.1"},
			},
			platformStatus: azureStatus(configv1.DualStackIPv4Primary),
			expect: &iov1.DNSRecordSpec{
				DNSName:             "*.apps.openshift.example.com.",
				RecordType:          iov1.ARecordType,
				Targets:             []string{"192.0.2.1", "2603:1030:c02:9::7a7"},
				RecordTTL:           defaultRecordTTL,
				DNSManagementPolicy: iov1.ManagedDNS,
			},
		},
		{
			description: "dual-stack IPv6-primary publishes both addresses, IPv6 first",
			publish: operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
			domain: "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{IP: "192.0.2.1"},
				{IP: "2603:1030:c02:9::7a7"},
			},
			platformStatus: azureStatus(configv1.DualStackIPv6Primary),
			expect: &iov1.DNSRecordSpec{
				DNSName:             "*.apps.openshift.example.com.",
				RecordType:          iov1.AAAARecordType,
				Targets:             []string{"2603:1030:c02:9::7a7", "192.0.2.1"},
				RecordTTL:           defaultRecordTTL,
				DNSManagementPolicy: iov1.ManagedDNS,
			},
		},
		{
			// The load balancer has not reported its IPv6 address yet.
			// Publish what is available; the provider reports the record
			// as unpublished until both families are present.
			description: "dual-stack with only the IPv4 address so far",
			publish: operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
			domain: "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{IP: "192.0.2.1"},
			},
			platformStatus: azureStatus(configv1.DualStackIPv4Primary),
			expect: &iov1.DNSRecordSpec{
				DNSName:             "*.apps.openshift.example.com.",
				RecordType:          iov1.ARecordType,
				Targets:             []string{"192.0.2.1"},
				RecordTTL:           defaultRecordTTL,
				DNSManagementPolicy: iov1.ManagedDNS,
			},
		},
		{
			// A hostname serves both families, so a dual-stack cluster
			// still gets a single CNAME.
			description: "dual-stack with a hostname yields a CNAME",
			publish: operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
			domain: "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{Hostname: "lb.cloud.example.com"},
			},
			platformStatus: azureStatus(configv1.DualStackIPv4Primary),
			expect: &iov1.DNSRecordSpec{
				DNSName:             "*.apps.openshift.example.com.",
				RecordType:          iov1.CNAMERecordType,
				Targets:             []string{"lb.cloud.example.com"},
				RecordTTL:           defaultRecordTTL,
				DNSManagementPolicy: iov1.ManagedDNS,
			},
		},
		{
			// A single-stack Azure cluster must behave exactly as before:
			// only the first ingress is used.
			description: "single-stack Azure uses only the first ingress",
			publish: operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
			domain: "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{IP: "192.0.2.1"},
				{IP: "192.0.2.2"},
			},
			platformStatus: azureStatus(configv1.IPv4),
			expect: &iov1.DNSRecordSpec{
				DNSName:             "*.apps.openshift.example.com.",
				RecordType:          iov1.ARecordType,
				Targets:             []string{"192.0.2.1"},
				RecordTTL:           defaultRecordTTL,
				DNSManagementPolicy: iov1.ManagedDNS,
			},
		},
		{
			description: "unmanaged DNS policy",
			publish: operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope:               operatorv1.ExternalLoadBalancer,
					DNSManagementPolicy: operatorv1.UnmanagedLoadBalancerDNS,
				},
			},
			domain: "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{Hostname: "lb.cloud.example.com"},
			},
			expect: &iov1.DNSRecordSpec{
				DNSName:             "*.apps.openshift.example.com.",
				RecordType:          iov1.CNAMERecordType,
				Targets:             []string{"lb.cloud.example.com"},
				RecordTTL:           defaultRecordTTL,
				DNSManagementPolicy: iov1.UnmanagedDNS,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			t.Logf("testing %s", test.description)
			name := types.NamespacedName{
				Namespace: "openshift-ingress-operator",
				Name:      "default-wildcard",
			}
			trueVar := true
			icRef := metav1.OwnerReference{
				APIVersion:         operatorv1.GroupVersion.String(),
				Kind:               "IngressController",
				Name:               "default",
				Controller:         &trueVar,
				BlockOwnerDeletion: &trueVar,
			}
			labels := map[string]string{
				manifests.OwningIngressControllerLabel: "default",
			}
			service := &corev1.Service{}
			for _, ingress := range test.ingresses {
				service.Status.LoadBalancer.Ingress = append(service.Status.LoadBalancer.Ingress, ingress)
			}

			haveWC, actual := desiredWildcardDNSRecord(name, labels, icRef, test.domain, &test.publish, service, test.platformStatus)
			switch {
			case test.expect != nil && haveWC:
				if !cmp.Equal(actual.Spec, *test.expect) {
					t.Errorf("expected:\n%s\n\nactual:\n%s", util.ToYaml(test.expect), util.ToYaml(actual.Spec))
				}
			case test.expect == nil && haveWC:
				t.Errorf("expected nil record, got:\n%s", util.ToYaml(actual))
			case test.expect != nil && !haveWC:
				t.Errorf("expected record but got nil:\n%s", util.ToYaml(test.expect))
			}
		})
	}
}

func Test_manageDNSForDomain(t *testing.T) {
	tests := []struct {
		name         string
		domain       string
		baseDomain   string
		platformType configv1.PlatformType
		expected     bool
	}{
		{
			name:       "empty domain",
			domain:     "",
			baseDomain: "openshift.example.com",
			expected:   false,
		},
		{
			name:       "empty base domain",
			domain:     "apps.openshift.example.com",
			baseDomain: "",
			expected:   false,
		},
		{
			name:         "domain matches the baseDomain on AWS",
			domain:       "apps.openshift.example.com",
			baseDomain:   "openshift.example.com",
			platformType: configv1.AWSPlatformType,
			expected:     true,
		},
		{
			name:         "domain with trailing dot matches the baseDomain on AWS",
			domain:       "apps.openshift.example.com.",
			baseDomain:   "openshift.example.com",
			platformType: configv1.AWSPlatformType,
			expected:     true,
		},
		{
			name:         "domain matches the baseDomain with trailing dot on AWS",
			domain:       "apps.openshift.example.com",
			baseDomain:   "openshift.example.com.",
			platformType: configv1.AWSPlatformType,
			expected:     true,
		},
		{
			name:         "domain matches the baseDomain, both with trailing dots, on AWS",
			domain:       "apps.openshift.example.com.",
			baseDomain:   "openshift.example.com.",
			platformType: configv1.AWSPlatformType,
			expected:     true,
		},
		{
			name:         "domain with bogus trailing dots does not match baseDomain on AWS",
			domain:       "apps.openshift.example.com..",
			baseDomain:   "openshift.example.com",
			platformType: configv1.AWSPlatformType,
			expected:     false,
		},
		{
			name:         "domain does not match baseDomain with bogus trailing dots on AWS",
			domain:       "apps.openshift.example.com",
			baseDomain:   "openshift.example.com..",
			platformType: configv1.AWSPlatformType,
			expected:     false,
		},
		{
			name:         "domain matches single segment baseDomain on AWS",
			domain:       "openshift.example.com",
			baseDomain:   "example.com",
			platformType: configv1.AWSPlatformType,
			expected:     true,
		},
		{
			name:         "domain does not match the baseDomain on AWS",
			domain:       "test.local",
			baseDomain:   "openshift.example.com",
			platformType: configv1.AWSPlatformType,
			expected:     false,
		},
		{
			name:         "domain does not match prematurely on AWS",
			domain:       "testopenshift.example.com",
			baseDomain:   "openshift.example.com",
			platformType: configv1.AWSPlatformType,
			expected:     false,
		},
		{
			name:         "domain matches the baseDomain on GCP",
			domain:       "apps.openshift.example.com",
			baseDomain:   "openshift.example.com",
			platformType: configv1.GCPPlatformType,
			expected:     true,
		},
		{
			name:         "domain does not match the baseDomain on GCP",
			domain:       "test.local",
			baseDomain:   "openshift.example.com",
			platformType: configv1.GCPPlatformType,
			expected:     false,
		},
		{
			name:         "domain does not match prematurely on GCP",
			domain:       "testopenshift.example.com",
			baseDomain:   "openshift.example.com",
			platformType: configv1.GCPPlatformType,
			expected:     false,
		},
		{
			name:         "domain does not match the baseDomain on unsupported platform",
			domain:       "test.local",
			baseDomain:   "openshift.example.com",
			platformType: configv1.NonePlatformType,
			expected:     true,
		},
		{
			name:         "domain does not match the baseDomain on external platform",
			domain:       "test.local",
			baseDomain:   "openshift.example.com",
			platformType: configv1.ExternalPlatformType,
			expected:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status := configv1.PlatformStatus{
				Type: tc.platformType,
			}

			dnsConfig := configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: tc.baseDomain,
				},
			}
			actual := ManageDNSForDomain(tc.domain, &status, &dnsConfig)
			if actual != tc.expected {
				t.Errorf("%q: expected to be %v, got %v", tc.name, tc.expected, actual)
			}
		})
	}
}
