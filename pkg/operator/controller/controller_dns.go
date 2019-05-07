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
	// If no load balancer has been provisioned, we can't do anything with the
	// configured DNS zones.
	ingress := service.Status.LoadBalancer.Ingress
	if len(ingress) == 0 || (len(ingress[0].Hostname) == 0 && len(ingress[0].IP) == 0) {
		return fmt.Errorf("no load balancer is assigned to service %s/%s", service.Namespace, service.Name)
	}

	var dnsRecords []*dns.Record
	if len(ingress[0].Hostname) != 0 {
		aliasRecords, err := desiredDNSAliasRecords(ci, ingress[0].Hostname, dnsConfig)
		if err != nil {
			return err
		}
		dnsRecords = append(dnsRecords, aliasRecords...)
	}

	if len(ingress[0].IP) != 0 {
		aRecords, err := desiredDNSARecords(ci, ingress[0].IP, dnsConfig)
		if err != nil {
			return err
		}
		dnsRecords = append(dnsRecords, aRecords...)
	}

	for _, record := range dnsRecords {
		err := r.DNSManager.Ensure(record)
		if err != nil {
			return fmt.Errorf("failed to ensure DNS record %v for %s/%s: %v", record, ci.Namespace, ci.Name, err)
		}
		log.Info("ensured DNS record for ingresscontroller", "namespace", ci.Namespace, "name", ci.Name, "record", record)
	}
	return nil
}

type makeRecordFunc func(zone *configv1.DNSZone) *dns.Record

func desiredDNSAliasRecords(ci *operatorv1.IngressController, hostname string, dnsConfig *configv1.DNS) ([]*dns.Record, error) {
	domain := fmt.Sprintf("*.%s", ci.Status.Domain)
	makeRecord := func(zone *configv1.DNSZone) *dns.Record {
		return &dns.Record{
			Zone: *zone,
			Type: dns.ALIASRecord,
			Alias: &dns.AliasRecord{
				Domain: domain,
				Target: hostname,
			},
		}
	}
	return desiredDNSRecords(ci, dnsConfig, makeRecord)
}

func desiredDNSARecords(ci *operatorv1.IngressController, ip string, dnsConfig *configv1.DNS) ([]*dns.Record, error) {
	domain := fmt.Sprintf("*.%s", ci.Status.Domain)
	makeRecord := func(zone *configv1.DNSZone) *dns.Record {
		return &dns.Record{
			Zone: *zone,
			Type: dns.ARecordType,
			ARecord: &dns.ARecord{
				Domain:  domain,
				Address: ip,
			},
		}
	}
	return desiredDNSRecords(ci, dnsConfig, makeRecord)
}

// desiredDNSRecords will return any necessary DNS records for the given inputs.
// If an ingress domain is in use, records are desired in every specified zone
// present in the cluster DNS configuration.
func desiredDNSRecords(ci *operatorv1.IngressController, dnsConfig *configv1.DNS, makeRecord makeRecordFunc) ([]*dns.Record, error) {
	records := []*dns.Record{}

	// If the ingresscontroller has no ingress domain, we cannot configure any
	// DNS records.
	if len(ci.Status.Domain) == 0 {
		return records, nil
	}

	// If the HA type is not cloud, then we don't manage DNS.
	if ci.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return records, nil
	}

	// If no zones are configured, there's nothing to do.
	if dnsConfig.Spec.PrivateZone == nil && dnsConfig.Spec.PublicZone == nil {
		return records, nil
	}

	if dnsConfig.Spec.PrivateZone != nil {
		records = append(records, makeRecord(dnsConfig.Spec.PrivateZone))
	}
	if dnsConfig.Spec.PublicZone != nil {
		records = append(records, makeRecord(dnsConfig.Spec.PublicZone))
	}
	return records, nil
}
