package dnsrecord

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	awsutil "github.com/openshift/cluster-ingress-operator/pkg/util/aws"
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

// EnsureWildcardDNSRecord creates or updates the wildcard DNS record for the
// given LB service. On dual-stack clusters, both IPv4 and IPv6 targets are
// included in a single record. If service is nil (haveLBS is false), nothing
// is done.
func EnsureWildcardDNSRecord(cl client.Client, name types.NamespacedName, dnsRecordLabels map[string]string, ownerRef metav1.OwnerReference, domain string, endpointPublishingStrategy *operatorv1.EndpointPublishingStrategy, service *corev1.Service, haveLBS bool, platformStatus *configv1.PlatformStatus) (bool, *iov1.DNSRecord, error) {
	if !haveLBS {
		return false, nil, nil
	}

	wantWC, desired := desiredWildcardDNSRecord(name, dnsRecordLabels, ownerRef, domain, endpointPublishingStrategy, service, platformStatus)
	haveWC, current, err := CurrentDNSRecord(cl, name)
	if err != nil {
		return false, nil, err
	}

	switch {
	case wantWC && !haveWC:
		if err := cl.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create dnsrecord %s/%s: %v", desired.Namespace, desired.Name, err)
		}
		log.Info("created dnsrecord", "dnsrecord", desired)
		haveWC, current, err = CurrentDNSRecord(cl, name)
		if err != nil {
			return false, nil, err
		}
	case wantWC && haveWC:
		if updated, err := updateDNSRecord(cl, current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update dnsrecord %s/%s: %v", desired.Namespace, desired.Name, err)
		} else if updated {
			haveWC, current, err = CurrentDNSRecord(cl, name)
			if err != nil {
				return haveWC, current, err
			}
		}
	}

	return haveWC, current, nil
}

// EnsureDNSRecord creates or updates DNS records for the given LB service.
// On dual-stack clusters, both IPv4 and IPv6 targets are included in a single
// record.
func EnsureDNSRecord(cl client.Client, name types.NamespacedName, dnsRecordLabels map[string]string, ownerRef metav1.OwnerReference, domain string, dnsPolicy iov1.DNSManagementPolicy, service *corev1.Service, platformStatus *configv1.PlatformStatus) (bool, *iov1.DNSRecord, error) {
	wantWC, desired := desiredDNSRecord(name, dnsRecordLabels, ownerRef, domain, dnsPolicy, service, platformStatus)
	haveWC, current, err := CurrentDNSRecord(cl, name)
	if err != nil {
		return false, nil, err
	}

	switch {
	case wantWC && !haveWC:
		if err := cl.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create dnsrecord %s/%s: %v", desired.Namespace, desired.Name, err)
		}
		log.Info("Created dnsrecord", "dnsrecord", desired)
		haveWC, current, err = CurrentDNSRecord(cl, name)
		if err != nil {
			return false, nil, err
		}
	case wantWC && haveWC:
		if updated, err := updateDNSRecord(cl, current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update dnsrecord %s/%s: %v", desired.Namespace, desired.Name, err)
		} else if updated {
			log.Info("Updated dnsrecord", "dnsrecord", desired)
			haveWC, current, err = CurrentDNSRecord(cl, name)
			if err != nil {
				return haveWC, current, err
			}
		}
	}

	return haveWC, current, nil
}

// desiredWildcardDNSRecord will return any necessary wildcard DNS records for the
// given service.
func desiredWildcardDNSRecord(name types.NamespacedName, dnsRecordLabels map[string]string, ownerRef metav1.OwnerReference, dnsDomain string, endpointPublishingStrategy *operatorv1.EndpointPublishingStrategy, service *corev1.Service, platformStatus *configv1.PlatformStatus) (bool, *iov1.DNSRecord) {
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

	// Set the DNS management policy on the dnsrecord CR to "Unmanaged" if
	// the ingresscontroller CR specifies "Unmanaged".  For example, the
	// policy can be set to "Unmanaged" if the ingresscontroller's domain
	// isn't a subdomain of the cluster's base domain.
	if endpointPublishingStrategy.LoadBalancer != nil && endpointPublishingStrategy.LoadBalancer.DNSManagementPolicy == operatorv1.UnmanagedLoadBalancerDNS {
		dnsPolicy = iov1.UnmanagedDNS
	}

	return desiredDNSRecord(name, dnsRecordLabels, ownerRef, domain, dnsPolicy, service, platformStatus)
}

// desiredDNSRecord will return any necessary DNS records for the given domain
// and service.
//
// A hostname target always produces a CNAME. For dual-stack clusters, both
// IPv4 and IPv6 addresses are collected into Targets with the primary family
// ordered first. For single-stack, only the first usable address is used.
func desiredDNSRecord(name types.NamespacedName, dnsRecordLabels map[string]string, ownerRef metav1.OwnerReference, domain string, dnsPolicy iov1.DNSManagementPolicy, service *corev1.Service, platformStatus *configv1.PlatformStatus) (bool, *iov1.DNSRecord) {
	// No LB target exists for the domain record to point at.
	if len(service.Status.LoadBalancer.Ingress) == 0 {
		log.Info("No load balancer target for dnsrecord", "dnsrecord", name, "service", service.Name)
		return false, nil
	}

	ipFamily := PlatformIPFamily(platformStatus)

	// Check for a hostname first — that always yields a CNAME.
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if len(ingress.Hostname) > 0 {
			if len(ingress.IP) > 0 {
				log.Error(nil, "Both load balancer hostname and IP are set", "dnsrecord", name, "service", service.Name)
				return false, nil
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
					Targets:             []string{ingress.Hostname},
					RecordType:          iov1.CNAMERecordType,
					RecordTTL:           defaultRecordTTL,
				},
			}
		}
	}

	// Dual-stack: collect all addresses with primary family first.
	if awsutil.IsDualStack(ipFamily) {
		recordType, targets, ok := dualStackTargets(name, service, ipFamily)
		if !ok {
			log.Info("No load balancer target for dnsrecord", "dnsrecord", name, "service", service.Name)
			return false, nil
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
				Targets:             targets,
				RecordType:          recordType,
				RecordTTL:           defaultRecordTTL,
			},
		}
	}

	// Single-stack: use the first usable IP address.
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if len(ingress.IP) == 0 {
			continue
		}
		rt, t := recordForAddress(ingress.IP)
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
				Targets:             []string{t},
				RecordType:          rt,
				RecordTTL:           defaultRecordTTL,
			},
		}
	}

	log.Info("No load balancer target for dnsrecord", "dnsrecord", name, "service", service.Name)
	return false, nil
}

// dualStackTargets collects all LB IP addresses from the service, partitions
// them into IPv4 and IPv6, and returns a combined target list with the primary
// family ordered first. The record type is ARecordType for IPv4-primary and
// AAAARecordType for IPv6-primary.
func dualStackTargets(name types.NamespacedName, service *corev1.Service, ipFamily configv1.IPFamilyType) (iov1.DNSRecordType, []string, bool) {
	var ipv4, ipv6 []string
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if len(ingress.IP) == 0 {
			continue
		}
		rt, t := recordForAddress(ingress.IP)
		if rt == iov1.ARecordType {
			ipv4 = append(ipv4, t)
		} else {
			ipv6 = append(ipv6, t)
		}
	}

	if len(ipv4) == 0 && len(ipv6) == 0 {
		return "", nil, false
	}

	if ipFamily == configv1.DualStackIPv6Primary {
		return iov1.AAAARecordType, append(ipv6, ipv4...), true
	}
	return iov1.ARecordType, append(ipv4, ipv6...), true
}

// PlatformIPFamily returns the IPFamilyType from the platform status.
// It defaults to IPv4 when the platform has no IPFamily field.
func PlatformIPFamily(platformStatus *configv1.PlatformStatus) configv1.IPFamilyType {
	if platformStatus != nil {
		switch {
		case platformStatus.AWS != nil && platformStatus.AWS.IPFamily != "":
			return platformStatus.AWS.IPFamily
		case platformStatus.Azure != nil && platformStatus.Azure.IPFamily != "":
			return platformStatus.Azure.IPFamily
		}
	}
	return configv1.IPv4
}

func isAddressRecordType(t iov1.DNSRecordType) bool {
	return t == iov1.ARecordType || t == iov1.AAAARecordType
}

// recordForAddress returns the record type and the target to publish for a load
// balancer address: AAAA for an IPv6 address, A otherwise. An IPv4-mapped IPv6
// address belongs in an A record, so it is normalized to its IPv4 form rather
// than published in its "::ffff:" spelling, which a DNS provider would reject
// as an A record target. An address that does not parse is left untouched.
func recordForAddress(address string) (iov1.DNSRecordType, string) {
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return iov1.ARecordType, address
	}
	if ip.Is4() || ip.Is4In6() {
		return iov1.ARecordType, ip.Unmap().String()
	}
	return iov1.AAAARecordType, ip.String()
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
