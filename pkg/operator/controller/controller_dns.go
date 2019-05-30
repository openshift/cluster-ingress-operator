package controller

import (
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"
)

// ensureDNS will create DNS records for the given LB service. If service is
// nil, nothing is done.
func (r *reconciler) ensureDNS(ci *operatorv1.IngressController, service *corev1.Service, dnsConfig *configv1.DNS) error {
	records := desiredDNSRecords(ci, dnsConfig, service)
	for _, record := range records {
		err := r.DNSManager.Ensure(record)
		if err != nil {
			return fmt.Errorf("failed to ensure DNS record %v for %s/%s: %v", record, ci.Namespace, ci.Name, err)
		}
		log.Info("ensured DNS record for ingresscontroller", "namespace", ci.Namespace, "name", ci.Name, "record", record)
	}
	return nil
}

func newAliasRecord(domain, target string, zone configv1.DNSZone) *dns.Record {
	return &dns.Record{
		Zone: zone,
		Type: dns.ALIASRecord,
		Alias: &dns.AliasRecord{
			Domain: domain,
			Target: target,
		},
	}
}

func newARecord(domain, target string, zone configv1.DNSZone) *dns.Record {
	return &dns.Record{
		Zone: zone,
		Type: dns.ARecordType,
		ARecord: &dns.ARecord{
			Domain:  domain,
			Address: target,
		},
	}
}

// desiredDNSRecords will return any necessary DNS records for the given inputs.
// If an ingress domain is in use, records are desired in every specified zone
// present in the cluster DNS configuration.
func desiredDNSRecords(ci *operatorv1.IngressController, dnsConfig *configv1.DNS, service *corev1.Service) []*dns.Record {
	records := []*dns.Record{}

	// If the ingresscontroller has no ingress domain, we cannot configure any
	// DNS records.
	if len(ci.Status.Domain) == 0 {
		return records
	}

	// If the HA type is not cloud, then we don't manage DNS.
	if ci.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return records
	}

	name := fmt.Sprintf("*.%s", ci.Status.Domain)
	zones := []configv1.DNSZone{}
	if dnsConfig.Spec.PrivateZone != nil {
		zones = append(zones, *dnsConfig.Spec.PrivateZone)
	}
	if dnsConfig.Spec.PublicZone != nil {
		zones = append(zones, *dnsConfig.Spec.PublicZone)
	}
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if len(ingress.Hostname) > 0 {
			for _, zone := range zones {
				records = append(records, newAliasRecord(name, ingress.Hostname, zone))
			}
		}
		if len(ingress.IP) > 0 {
			for _, zone := range zones {
				records = append(records, newARecord(name, ingress.IP, zone))
			}
		}
	}

	return records
}
