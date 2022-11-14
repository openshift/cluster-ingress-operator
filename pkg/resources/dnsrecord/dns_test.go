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

func TestDesiredWildcardDNSRecord(t *testing.T) {
	tests := []struct {
		description string
		domain      string
		publish     operatorv1.EndpointPublishingStrategy
		ingresses   []corev1.LoadBalancerIngress
		expect      *iov1.DNSRecordSpec
	}{
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

			haveWC, actual := desiredWildcardDNSRecord(name, labels, icRef, test.domain, &test.publish, service)
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

func TestManageDNSForDomain(t *testing.T) {
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
			name:         "domain matches the baseDomain on AWS",
			domain:       "apps.openshift.example.com",
			baseDomain:   "openshift.example.com",
			platformType: configv1.AWSPlatformType,
			expected:     true,
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
