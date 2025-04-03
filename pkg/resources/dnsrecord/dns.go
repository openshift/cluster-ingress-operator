package dnsrecord

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

var log = logf.Logger.WithName("dnsrecord")

// defaultRecordTTL is the TTL (in seconds) assigned to all new DNS records.
//
// Note that TTL isn't necessarily honored by clouds providers (for example,
// on AWS TTL is not configurable for alias records[1]).
//
// [1] https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/resource-record-sets-choosing-alias-non-alias.html
const defaultRecordTTL int64 = 30

// EnsureWildcardDNSRecord will create wildcard DNS records for the given LB
// service.  If service is nil (haveLBS is false), nothing is done.
func EnsureWildcardDNSRecord(client client.Client, name types.NamespacedName, dnsRecordLabels map[string]string, ownerRef metav1.OwnerReference, domain string, endpointPublishingStrategy *operatorv1.EndpointPublishingStrategy, service *corev1.Service, haveLBS bool) (bool, *iov1.DNSRecord, error) {
	if !haveLBS {
		return false, nil, nil
	}

	wantWC, desired := desiredWildcardDNSRecord(name, dnsRecordLabels, ownerRef, domain, endpointPublishingStrategy, service)
	haveWC, current, err := CurrentDNSRecord(client, name)
	if err != nil {
		return false, nil, err
	}

	switch {
	case wantWC && !haveWC:
		if err := client.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create dnsrecord %s/%s: %v", desired.Namespace, desired.Name, err)
		}
		log.Info("created dnsrecord", "dnsrecord", desired)
		return CurrentDNSRecord(client, name)
	case wantWC && haveWC:
		if updated, err := updateDNSRecord(client, current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update dnsrecord %s/%s: %v", desired.Namespace, desired.Name, err)
		} else if updated {
			return CurrentDNSRecord(client, name)
		}
	}

	return haveWC, current, nil
}

// EnsureDNSRecord will create DNS records for the given LB service.  If service
// is nil (haveLBS is false), nothing is done.
func EnsureDNSRecord(client client.Client, name types.NamespacedName, dnsRecordLabels map[string]string, ownerRef metav1.OwnerReference, domain string, dnsPolicy iov1.DNSManagementPolicy, service *corev1.Service) (bool, *iov1.DNSRecord, error) {
	wantWC, desired := desiredDNSRecord(name, dnsRecordLabels, ownerRef, domain, dnsPolicy, service)
	haveWC, current, err := CurrentDNSRecord(client, name)
	if err != nil {
		return false, nil, err
	}

	switch {
	case wantWC && !haveWC:
		if err := client.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create dnsrecord %s/%s: %v", desired.Namespace, desired.Name, err)
		}
		log.Info("Created dnsrecord", "dnsrecord", desired)
		return CurrentDNSRecord(client, name)
	case wantWC && haveWC:
		if updated, err := updateDNSRecord(client, current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update dnsrecord %s/%s: %v", desired.Namespace, desired.Name, err)
		} else if updated {
			log.Info("Updated dnsrecord", "dnsrecord", desired)
			return CurrentDNSRecord(client, name)
		}
	}
	log.Info("No desired dnsrecord")

	return haveWC, current, nil
}

// desiredWildcardDNSRecord will return any necessary wildcard DNS records for the
// given service.
func desiredWildcardDNSRecord(name types.NamespacedName, dnsRecordLabels map[string]string, ownerRef metav1.OwnerReference, dnsDomain string, endpointPublishingStrategy *operatorv1.EndpointPublishingStrategy, service *corev1.Service) (bool, *iov1.DNSRecord) {
	// If the ingresscontroller has no ingress domain, we cannot configure any
	// DNS records.
	if len(dnsDomain) == 0 {
		return false, nil
	}

	// DNS is only managed for LB publishing.
	if endpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return false, nil
	}

	// Use an absolute name to prevent any ambiguity.
	domain := fmt.Sprintf("*.%s.", dnsDomain)

	dnsPolicy := iov1.ManagedDNS

	// Set the DNS management policy on the dnsrecord to "Unmanaged" if ingresscontroller has "Unmanaged" DNS policy or
	// if the ingresscontroller domain isn't a subdomain of the cluster's base domain.
	if endpointPublishingStrategy.LoadBalancer.DNSManagementPolicy == operatorv1.UnmanagedLoadBalancerDNS {
		dnsPolicy = iov1.UnmanagedDNS
	}

	return desiredDNSRecord(name, dnsRecordLabels, ownerRef, domain, dnsPolicy, service)
}

// desiredDNSRecord will return any necessary DNS records for the given domain
// and service.
//
// For now, if the service has more than one .status.loadbalancer.ingress, only
// the first will be used.
//
// TODO: If .status.loadbalancer.ingress is processed once as non-empty and then
// later becomes empty, what should we do? Currently we'll treat it as an intent
// to not have a desired record.
func desiredDNSRecord(name types.NamespacedName, dnsRecordLabels map[string]string, ownerRef metav1.OwnerReference, domain string, dnsPolicy iov1.DNSManagementPolicy, service *corev1.Service) (bool, *iov1.DNSRecord) {
	// No LB target exists for the domain record to point at.
	if len(service.Status.LoadBalancer.Ingress) == 0 {
		log.Info("No load balancer target for dnsrecord", "dnsrecord", name, "service", service.Name)
		return false, nil
	}

	ingress := service.Status.LoadBalancer.Ingress[0]

	// Quick sanity check since we don't know how to handle both being set (is
	// that even a valid state?)
	if len(ingress.Hostname) > 0 && len(ingress.IP) > 0 {
		log.Info("Both load balancer hostname and IP are set", "dnsrecord", name, "service", service.Name)
		return false, nil
	}

	var target string
	var recordType iov1.DNSRecordType

	if len(ingress.Hostname) > 0 {
		recordType = iov1.CNAMERecordType
		target = ingress.Hostname
	} else {
		recordType = iov1.ARecordType
		target = ingress.IP
	}

	return true, &iov1.DNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       name.Namespace,
			Name:            name.Name,
			Labels:          dnsRecordLabels,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
			Finalizers:      []string{manifests.DNSRecordFinalizer},
		},
		Spec: iov1.DNSRecordSpec{
			DNSName:             domain,
			DNSManagementPolicy: dnsPolicy,
			Targets:             []string{target},
			RecordType:          recordType,
			RecordTTL:           defaultRecordTTL,
		},
	}
}

func CurrentDNSRecord(client client.Client, name types.NamespacedName) (bool, *iov1.DNSRecord, error) {
	current := &iov1.DNSRecord{}
	err := client.Get(context.TODO(), name, current)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

func DeleteDNSRecord(client client.Client, name types.NamespacedName) error {
	record := &iov1.DNSRecord{}
	record.Namespace = name.Namespace
	record.Name = name.Name
	if err := client.Delete(context.TODO(), record); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// updateDNSRecord updates a DNSRecord. Returns a boolean indicating whether
// the record was updated, and an error value.
func updateDNSRecord(client client.Client, current, desired *iov1.DNSRecord) (bool, error) {
	changed, updated := dnsRecordChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	log.Info("updated dnsrecord", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// dnsRecordChanged checks if the current DNSRecord spec matches the expected spec and
// if not returns an updated one.
func dnsRecordChanged(current, expected *iov1.DNSRecord) (bool, *iov1.DNSRecord) {
	if cmp.Equal(current.Spec, expected.Spec, cmpopts.EquateEmpty()) {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec
	return true, updated
}

// ManageDNSForDomain returns true if the given domain contains the baseDomain
// of the cluster DNS config. It is only used for AWS and GCP in the beginning, and will be expanded to other clouds
// once we know there are no users depending on this.
// See https://bugzilla.redhat.com/show_bug.cgi?id=2041616
func ManageDNSForDomain(domain string, status *configv1.PlatformStatus, dnsConfig *configv1.DNS) bool {
	if len(domain) == 0 || len(dnsConfig.Spec.BaseDomain) == 0 {
		return false
	}

	mustContain := "." + dnsConfig.Spec.BaseDomain

	// Ignore any trailing dot for comparison.
	if strings.HasSuffix(mustContain, ".") {
		mustContain = mustContain[:len(mustContain)-1]
	}
	if strings.HasSuffix(domain, ".") {
		domain = domain[:len(domain)-1]
	}

	switch status.Type {
	case configv1.AWSPlatformType, configv1.GCPPlatformType:
		return strings.HasSuffix(domain, mustContain)
	default:
		return true
	}
}
