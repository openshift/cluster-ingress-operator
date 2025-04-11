package gcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/api/googleapi"

	configv1 "github.com/openshift/api/config/v1"

	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

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
	Project                   string
	UserAgent                 string
	CredentialsJSON           []byte
	Endpoints                 []configv1.GCPServiceEndpoint
	GCPCustomEndpointsEnabled bool
}

func New(config Config) (*Provider, error) {
	options := []option.ClientOption{
		option.WithCredentialsJSON(config.CredentialsJSON),
		option.WithUserAgent(config.UserAgent),
	}

	dnsService, err := gdnsv1.NewService(context.TODO(), options...)
	if err != nil {
		return nil, err
	}

	if config.GCPCustomEndpointsEnabled {
		for _, endpoint := range config.Endpoints {
			if endpoint.Name == configv1.GCPServiceEndpointNameDNS {
				// There should be at most 1 endpoint override per service. If there
				// is more than one, only use the first instance.
				dnsService.BasePath = endpoint.URL
				break
			}
		}
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
	change := &gdnsv1.Change{Additions: []*gdnsv1.ResourceRecordSet{resourceRecordSet(record)}}

	project, zoneID, err := p.parseZone(zone)
	if err != nil {
		return err
	}

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
	oldRecord := p.dnsService.ResourceRecordSets.List(project, zoneID).Name(record.Spec.DNSName)
	if err := oldRecord.Pages(ctx, func(page *gdnsv1.ResourceRecordSetsListResponse) error {
		for _, resourceRecordSet := range page.Rrsets {
			log.Info("found old DNS resource record set", "resourceRecordSet", resourceRecordSet)
			change := &gdnsv1.Change{Deletions: []*gdnsv1.ResourceRecordSet{resourceRecordSet}}
			call := p.dnsService.Changes.Create(project, zoneID, change)
			_, err := call.Do()
			if ae, ok := err.(*googleapi.Error); ok && ae.Code == http.StatusNotFound {
				return nil
			}
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if err := p.Ensure(record, zone); err != nil {
		return err
	}
	return nil
}

func (p *Provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	change := &gdnsv1.Change{Deletions: []*gdnsv1.ResourceRecordSet{resourceRecordSet(record)}}
	project, zoneID, err := p.parseZone(zone)
	if err != nil {
		return err
	}
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
