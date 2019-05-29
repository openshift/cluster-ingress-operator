package controller

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"

	corev1 "k8s.io/api/core/v1"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

var privateZone = configv1.DNSZone{
	ID: "private",
}
var publicZone = configv1.DNSZone{
	ID: "public",
}

var globalConfig *configv1.DNS = &configv1.DNS{
	Spec: configv1.DNSSpec{
		PrivateZone: &privateZone,
		PublicZone:  &publicZone,
	},
}

var privateConfig *configv1.DNS = &configv1.DNS{
	Spec: configv1.DNSSpec{
		PrivateZone: &privateZone,
	},
}

var publicConfig *configv1.DNS = &configv1.DNS{
	Spec: configv1.DNSSpec{
		PublicZone: &publicZone,
	},
}

func TestDesiredDNSRecords(t *testing.T) {
	type ingress struct {
		host string
		ip   string
	}
	type record struct {
		typ    dns.RecordType
		name   string
		target string
		zone   configv1.DNSZone
	}
	makeService := func(ingresses []ingress) *corev1.Service {
		service := &corev1.Service{}
		for _, ingress := range ingresses {
			service.Status.LoadBalancer.Ingress = append(service.Status.LoadBalancer.Ingress, corev1.LoadBalancerIngress{
				IP:       ingress.ip,
				Hostname: ingress.host,
			})
		}
		return service
	}
	makeRecords := func(records []record) []*dns.Record {
		dnsRecords := []*dns.Record{}
		for _, r := range records {
			record := &dns.Record{
				Zone: r.zone,
				Type: r.typ,
			}
			switch record.Type {
			case dns.ALIASRecord:
				record.Alias = &dns.AliasRecord{
					Domain: r.name,
					Target: r.target,
				}
			case dns.ARecordType:
				record.ARecord = &dns.ARecord{
					Domain:  r.name,
					Address: r.target,
				}
			}
			dnsRecords = append(dnsRecords, record)
		}
		return dnsRecords
	}
	tests := []struct {
		description string
		domain      string
		publish     operatorv1.EndpointPublishingStrategyType
		dnsConfig   *configv1.DNS
		ingresses   []ingress
		expect      []record
	}{
		{
			description: "no domain",
			domain:      "",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			dnsConfig:   globalConfig,
			ingresses: []ingress{
				{host: "lb.cloud.example.com", ip: ""},
				{host: "lb.cloud.example.com", ip: "192.0.2.1"},
			},
			expect: []record{},
		},
		{
			description: "not a load balancer",
			domain:      "apps.openshift.example.com",
			publish:     operatorv1.HostNetworkStrategyType,
			dnsConfig:   globalConfig,
			expect:      []record{},
		},
		{
			description: "no zones",
			domain:      "apps.openshift.example.com",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			dnsConfig:   &configv1.DNS{},
			ingresses: []ingress{
				{host: "lb.cloud.example.com"},
			},
			expect: []record{},
		},
		{
			description: "public only ALIAS",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			domain:      "apps.openshift.example.com",
			dnsConfig:   publicConfig,
			ingresses: []ingress{
				{host: "lb.cloud.example.com"},
			},
			expect: []record{
				{typ: dns.ALIASRecord, name: "*.apps.openshift.example.com", target: "lb.cloud.example.com", zone: publicZone},
			},
		},
		{
			description: "private only A",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			domain:      "apps.openshift.example.com",
			dnsConfig:   privateConfig,
			ingresses: []ingress{
				{ip: "192.0.2.1"},
			},
			expect: []record{
				{typ: dns.ARecordType, name: "*.apps.openshift.example.com", target: "192.0.2.1", zone: privateZone},
			},
		},
		{
			description: "global ALIAS",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			domain:      "apps.openshift.example.com",
			dnsConfig:   globalConfig,
			ingresses: []ingress{
				{host: "lb.cloud.example.com"},
			},
			expect: []record{
				{typ: dns.ALIASRecord, name: "*.apps.openshift.example.com", target: "lb.cloud.example.com", zone: publicZone},
				{typ: dns.ALIASRecord, name: "*.apps.openshift.example.com", target: "lb.cloud.example.com", zone: privateZone},
			},
		},
		{
			description: "global A",
			publish:     operatorv1.LoadBalancerServiceStrategyType,
			domain:      "apps.openshift.example.com",
			dnsConfig:   globalConfig,
			ingresses: []ingress{
				{ip: "192.0.2.1"},
			},
			expect: []record{
				{typ: dns.ARecordType, name: "*.apps.openshift.example.com", target: "192.0.2.1", zone: publicZone},
				{typ: dns.ARecordType, name: "*.apps.openshift.example.com", target: "192.0.2.1", zone: privateZone},
			},
		},
	}

	for _, test := range tests {
		t.Logf("testing %s", test.description)
		controller := &operatorv1.IngressController{
			Status: operatorv1.IngressControllerStatus{
				Domain: test.domain,
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: test.publish,
				},
			},
		}
		actual := desiredDNSRecords(controller, test.dnsConfig, makeService(test.ingresses))
		expected := makeRecords(test.expect)
		if !cmp.Equal(actual, expected, cmpopts.EquateEmpty(), cmpopts.SortSlices(cmpRecords)) {
			t.Errorf("expected:")
			for _, r := range expected {
				t.Errorf("\t%s", r)
			}
			t.Errorf("actual:")
			for _, r := range actual {
				t.Errorf("\t%s", r)
			}
		}
	}
}

func cmpRecords(a, b *dns.Record) bool {
	return string(a.Zone.ID) < string(b.Zone.ID)
}
