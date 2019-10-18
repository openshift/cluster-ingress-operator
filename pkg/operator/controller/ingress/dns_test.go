package ingress

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"gopkg.in/yaml.v2"

	iov1 "github.com/openshift/api/operatoringress/v1"

	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDesiredWildcardDNSRecord(t *testing.T) {
	tests := []struct {
		description string
		domain      string
		publish     operatorv1.EndpointPublishingStrategyType
		ingresses   []corev1.LoadBalancerIngress
		expect      *iov1.DNSRecordSpec
	}{
		{
			description: "no domain",
			domain:      "",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			ingresses: []corev1.LoadBalancerIngress{
				{Hostname: "lb.cloud.example.com"},
			},
			expect: nil,
		},
		{
			description: "not a load balancer",
			domain:      "apps.openshift.example.com",
			publish:     operatorv1.HostNetworkStrategyType,
			expect:      nil,
		},
		{
			description: "no ingresses",
			domain:      "apps.openshift.example.com",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			ingresses:   []corev1.LoadBalancerIngress{},
			expect:      nil,
		},
		{
			description: "hostname to CNAME record",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			domain:      "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{Hostname: "lb.cloud.example.com"},
			},
			expect: &iov1.DNSRecordSpec{
				DNSName:    "*.apps.openshift.example.com.",
				RecordType: iov1.CNAMERecordType,
				Targets:    []string{"lb.cloud.example.com"},
				RecordTTL:  defaultRecordTTL,
			},
		},
		{
			description: "IP to A record",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			domain:      "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{IP: "192.0.2.1"},
			},
			expect: &iov1.DNSRecordSpec{
				DNSName:    "*.apps.openshift.example.com.",
				RecordType: iov1.ARecordType,
				Targets:    []string{"192.0.2.1"},
				RecordTTL:  defaultRecordTTL,
			},
		},
		{
			description: "IP to A record with more than one IP",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			domain:      "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{IP: "192.0.2.1"}, {IP: "192.0.2.2"},
			},
			expect: &iov1.DNSRecordSpec{
				DNSName:    "*.apps.openshift.example.com.",
				RecordType: iov1.ARecordType,
				Targets:    []string{"192.0.2.1", "192.0.2.2"},
				RecordTTL:  defaultRecordTTL,
			},
		},
		{
			description: "hostname to more than one CNAME record",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			domain:      "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{Hostname: "lb.cloud.example.com"}, {Hostname: "lb.origin.example.com"},
			},
			expect: &iov1.DNSRecordSpec{
				DNSName:    "*.apps.openshift.example.com.",
				RecordType: iov1.CNAMERecordType,
				Targets:    []string{"lb.cloud.example.com", "lb.origin.example.com"},
				RecordTTL:  defaultRecordTTL,
			},
		},
		{
			description: "IP and Hostname both present for the same target",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			domain:      "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{IP: "192.0.2.1", Hostname: "lb.cloud.example.com"}, {IP: "192.0.2.2", Hostname: "lb.origin.example.com"},
			},
			expect: nil,
		},
		{
			description: "IP present for one target, host name present for another target",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			domain:      "apps.openshift.example.com",
			ingresses: []corev1.LoadBalancerIngress{
				{IP: "192.0.2.1"}, {Hostname: "lb.origin.example.com"},
			},
			expect: nil,
		},
	}

	for _, test := range tests {
		t.Logf("testing %s", test.description)
		controller := &operatorv1.IngressController{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
			},
			Status: operatorv1.IngressControllerStatus{
				Domain: test.domain,
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: test.publish,
				},
			},
		}

		service := &corev1.Service{}
		for _, ingress := range test.ingresses {
			service.Status.LoadBalancer.Ingress = append(service.Status.LoadBalancer.Ingress, ingress)
		}

		actual := desiredWildcardRecord(controller, service)
		switch {
		case test.expect != nil && actual != nil:
			if !cmp.Equal(actual.Spec, *test.expect) {
				t.Errorf("expected:\n%s\n\nactual:\n%s", toYaml(test.expect), toYaml(actual.Spec))
			}
		case test.expect == nil && actual != nil:
			t.Errorf("expected nil record, got:\n%s", toYaml(actual))
		case test.expect != nil && actual == nil:
			t.Errorf("expected record but got nil:\n%s", toYaml(test.expect))
		}
	}
}

func toYaml(obj interface{}) string {
	yml, _ := yaml.Marshal(obj)
	return string(yml)
}
