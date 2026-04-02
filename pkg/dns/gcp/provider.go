package gcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"google.golang.org/api/googleapi"

	configv1 "github.com/openshift/api/config/v1"

	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/util/ipfamily"

	gdnsv1 "google.golang.org/api/dns/v1"
	"google.golang.org/api/option"
)

var (
	_   dns.Provider = &Provider{}
	log              = logf.Logger.WithName("dns")
)

type Provider struct {
	// config is required input.
	config Config
	// dnsService provides DNS API access.
	dnsService *gdnsv1.Service
}

type Config struct {
	Project         string
	UserAgent       string
	CredentialsJSON []byte
	// IPFamily is the cluster's IP family configuration from the
	// Infrastructure CR. When dual-stack, the provider creates both
	// A and AAAA DNS records.
	IPFamily configv1.IPFamilyType
}

func New(config Config) (*Provider, error) {
	dnsService, err := gdnsv1.NewService(context.TODO(), option.WithCredentialsJSON(config.CredentialsJSON), option.WithUserAgent(config.UserAgent))
	if err != nil {
		return nil, err
	}

	provider := &Provider{
		config:     config,
		dnsService: dnsService,
	}

	return provider, nil
}

// ParseZone will parse two different string formatted zones. The first is the short name where only the
// zone id is provided. The second is the long name where the zone and project are both available in the string
// in the format provided by GCP projects/{projectID}/managedZones/{zoneID}.
func ParseZone(defaultProject, zoneID string) (string, string, error) {
	parts := strings.Split(zoneID, "/")
	switch {
	case len(parts) == 1:
		return defaultProject, zoneID, nil
	case len(parts) == 4 && parts[0] == "projects" && parts[2] == "managedZones":
		return parts[1], parts[3], nil
	}

	return "", "", fmt.Errorf("invalid managedZone: %s", zoneID)
}

func (p *Provider) parseZone(zone configv1.DNSZone) (string, string, error) {
	// parse the zone that was provided
	project, zoneID, err := ParseZone(p.config.Project, zone.ID)
	if err != nil {
		return "", "", err
	}
	return project, zoneID, nil
}

func (p *Provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	project, zoneID, err := p.parseZone(zone)
	if err != nil {
		return err
	}

	var additions []*gdnsv1.ResourceRecordSet

	// For A records, check if this is a dual-stack configuration.
	// Dual-stack is detected either by the IPFamily configuration or by
	// the presence of both IPv4 and IPv6 addresses in the targets.
	if record.Spec.RecordType == iov1.ARecordType {
		// Filter targets by IP version
		ipv4Targets := filterIPv4Addresses(record.Spec.Targets)
		ipv6Targets := filterIPv6Addresses(record.Spec.Targets)

		// Determine if this is dual-stack: either IPFamily is set to dual-stack,
		// or we have both IPv4 and IPv6 addresses in the targets.
		isDualStack := ipfamily.IsDualStack(p.config.IPFamily) || (len(ipv4Targets) > 0 && len(ipv6Targets) > 0)

		if isDualStack {
			// Create A record with IPv4 addresses
			if len(ipv4Targets) > 0 {
				aRecord := &gdnsv1.ResourceRecordSet{
					Name:    record.Spec.DNSName,
					Rrdatas: ipv4Targets,
					Type:    "A",
					Ttl:     record.Spec.RecordTTL,
				}
				additions = append(additions, aRecord)
			}

			// Create AAAA record with IPv6 addresses
			if len(ipv6Targets) > 0 {
				aaaaRecord := &gdnsv1.ResourceRecordSet{
					Name:    record.Spec.DNSName,
					Rrdatas: ipv6Targets,
					Type:    "AAAA",
					Ttl:     record.Spec.RecordTTL,
				}
				additions = append(additions, aaaaRecord)
			}
		} else {
			// Not dual-stack, create A record as-is.
			additions = []*gdnsv1.ResourceRecordSet{resourceRecordSet(record)}
		}
	} else {
		// For non-A records (CNAME, etc.), create the record as-is.
		additions = []*gdnsv1.ResourceRecordSet{resourceRecordSet(record)}
	}

	if len(additions) == 0 {
		log.Info("no records to create", "record", record.Spec.DNSName)
		return nil
	}

	change := &gdnsv1.Change{Additions: additions}
	call := p.dnsService.Changes.Create(project, zoneID, change)
	_, err = call.Do()
	// Since we don't yet handle updates, assume that existing records are correct.
	if ae, ok := err.(*googleapi.Error); ok && ae.Code == http.StatusConflict {
		return nil
	}
	return err
}

func (p *Provider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	ctx := context.Background()

	project, zoneID, err := p.parseZone(zone)
	if err != nil {
		return err
	}

	// Collect all existing records for this DNS name (including both A and AAAA for dual-stack).
	var deletions []*gdnsv1.ResourceRecordSet
	oldRecord := p.dnsService.ResourceRecordSets.List(project, zoneID).Name(record.Spec.DNSName)
	if err := oldRecord.Pages(ctx, func(page *gdnsv1.ResourceRecordSetsListResponse) error {
		for _, resourceRecordSet := range page.Rrsets {
			log.Info("found old DNS resource record set", "resourceRecordSet", resourceRecordSet)
			deletions = append(deletions, resourceRecordSet)
		}
		return nil
	}); err != nil {
		return err
	}

	// Delete all existing records in a single change.
	if len(deletions) > 0 {
		change := &gdnsv1.Change{Deletions: deletions}
		call := p.dnsService.Changes.Create(project, zoneID, change)
		_, err := call.Do()
		if ae, ok := err.(*googleapi.Error); ok && ae.Code == http.StatusNotFound {
			// Continue if not found - the records may have already been deleted.
		} else if err != nil {
			return err
		}
	}

	if err := p.Ensure(record, zone); err != nil {
		return err
	}
	return nil
}

func (p *Provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	project, zoneID, err := p.parseZone(zone)
	if err != nil {
		return err
	}

	var deletions []*gdnsv1.ResourceRecordSet

	// For A records, check if this is a dual-stack configuration.
	// Dual-stack is detected either by the IPFamily configuration or by
	// the presence of both IPv4 and IPv6 addresses in the targets.
	if record.Spec.RecordType == iov1.ARecordType {
		// Filter targets by IP version
		ipv4Targets := filterIPv4Addresses(record.Spec.Targets)
		ipv6Targets := filterIPv6Addresses(record.Spec.Targets)

		// Determine if this is dual-stack: either IPFamily is set to dual-stack,
		// or we have both IPv4 and IPv6 addresses in the targets.
		isDualStack := ipfamily.IsDualStack(p.config.IPFamily) || (len(ipv4Targets) > 0 && len(ipv6Targets) > 0)

		if isDualStack {
			// Delete A record with IPv4 addresses
			if len(ipv4Targets) > 0 {
				aRecord := &gdnsv1.ResourceRecordSet{
					Name:    record.Spec.DNSName,
					Rrdatas: ipv4Targets,
					Type:    "A",
					Ttl:     record.Spec.RecordTTL,
				}
				deletions = append(deletions, aRecord)
			}

			// Delete AAAA record with IPv6 addresses
			if len(ipv6Targets) > 0 {
				aaaaRecord := &gdnsv1.ResourceRecordSet{
					Name:    record.Spec.DNSName,
					Rrdatas: ipv6Targets,
					Type:    "AAAA",
					Ttl:     record.Spec.RecordTTL,
				}
				deletions = append(deletions, aaaaRecord)
			}
		} else {
			// Not dual-stack, delete A record as-is.
			deletions = []*gdnsv1.ResourceRecordSet{resourceRecordSet(record)}
		}
	} else {
		// For non-A records (CNAME, etc.), delete the record as-is.
		deletions = []*gdnsv1.ResourceRecordSet{resourceRecordSet(record)}
	}

	if len(deletions) == 0 {
		log.Info("no records to delete", "record", record.Spec.DNSName)
		return nil
	}

	change := &gdnsv1.Change{Deletions: deletions}
	call := p.dnsService.Changes.Create(project, zoneID, change)
	_, err = call.Do()
	if ae, ok := err.(*googleapi.Error); ok && ae.Code == http.StatusNotFound {
		return nil
	}
	return err
}

func resourceRecordSet(record *iov1.DNSRecord) *gdnsv1.ResourceRecordSet {
	return &gdnsv1.ResourceRecordSet{
		Name:    record.Spec.DNSName,
		Rrdatas: record.Spec.Targets,
		Type:    string(record.Spec.RecordType),
		Ttl:     record.Spec.RecordTTL,
	}
}

// filterIPv4Addresses filters a list of IP addresses to only include IPv4 addresses.
func filterIPv4Addresses(addresses []string) []string {
	var ipv4 []string
	for _, addr := range addresses {
		if ip := net.ParseIP(addr); ip != nil && ip.To4() != nil {
			ipv4 = append(ipv4, addr)
		}
	}
	return ipv4
}

// filterIPv6Addresses filters a list of IP addresses to only include IPv6 addresses.
func filterIPv6Addresses(addresses []string) []string {
	var ipv6 []string
	for _, addr := range addresses {
		if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
			ipv6 = append(ipv6, addr)
		}
	}
	return ipv6
}
