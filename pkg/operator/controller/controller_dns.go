package controller

import (
	"fmt"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"
)

// ensureDNS will create DNS records for the given LB service. If service is
// nil, nothing is done.
func (r *reconciler) ensureDNS(ci *ingressv1alpha1.ClusterIngress, service *corev1.Service, dnsConfig *configv1.DNS) error {
	dnsRecords, err := desiredDNSRecords(ci, service, dnsConfig)
	if err != nil {
		return err
	}
	for _, record := range dnsRecords {
		err := r.DNSManager.Ensure(record)
		if err != nil {
			return fmt.Errorf("failed to ensure DNS record %v for %s/%s: %v", record, ci.Namespace, ci.Name, err)
		}
		log.Info("ensured DNS record for clusteringress", "namespace", ci.Namespace, "name", ci.Name, "record", record)
	}
	return nil
}

// desiredDNSRecords will return any necessary DNS records for the given inputs.
// If service is nil, no records are desired. If an ingress domain is in use,
// records are desired in every specified zone present in the cluster DNS
// configuration.
func desiredDNSRecords(ci *ingressv1alpha1.ClusterIngress, service *corev1.Service, dnsConfig *configv1.DNS) ([]*dns.Record, error) {
	records := []*dns.Record{}

	// If no service exists, no DNS records should exist.
	if service == nil {
		return records, nil
	}

	// TODO: This will need revisited when we stop defaulting .spec.ingressDomain
	// and .spec.highAvailability as both can be nil but used with an effective
	// system-provided default reported on status.
	if ci.Spec.HighAvailability == nil || ci.Spec.HighAvailability.Type != ingressv1alpha1.CloudClusterIngressHA || ci.Spec.IngressDomain == nil {
		return records, nil
	}

	// If no zones are configured, there's nothing to do.
	if dnsConfig.Spec.PrivateZone == nil && dnsConfig.Spec.PublicZone == nil {
		return records, nil
	}

	// If no load balancer has been provisioned, we can't do anything with the
	// configured DNS zones.
	ingress := service.Status.LoadBalancer.Ingress
	if len(ingress) == 0 || len(ingress[0].Hostname) == 0 {
		return nil, fmt.Errorf("no load balancer is assigned to service %s/%s", service.Namespace, service.Name)
	}

	domain := fmt.Sprintf("*.%s", *ci.Spec.IngressDomain)
	makeRecord := func(zone *configv1.DNSZone) *dns.Record {
		return &dns.Record{
			Zone: *zone,
			Type: dns.ALIASRecord,
			Alias: &dns.AliasRecord{
				Domain: domain,
				Target: ingress[0].Hostname,
			},
		}
	}
	if dnsConfig.Spec.PrivateZone != nil {
		records = append(records, makeRecord(dnsConfig.Spec.PrivateZone))
	}
	if dnsConfig.Spec.PublicZone != nil {
		records = append(records, makeRecord(dnsConfig.Spec.PublicZone))
	}
	return records, nil
}
