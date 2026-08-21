package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/oauth2/google"
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
	Project         string
	UserAgent       string
	CredentialsJSON []byte
}

func New(config Config) (*Provider, error) {
	ctx := context.TODO()
	// WithAuthCredentialsJSON takes a credentials type argument to allow
	// restricting which credential types are accepted from external sources.
	// In this case, there are no restrictions so we simply pass the type through.
	var f struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(config.CredentialsJSON, &f); err != nil {
		return nil, fmt.Errorf("failed to parse credentials JSON: %w", err)
	}
	creds, err := google.CredentialsFromJSONWithType(ctx, config.CredentialsJSON, google.CredentialsType(f.Type), gdnsv1.CloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("failed to create credentials: %w", err)
	}
	ud, err := creds.GetUniverseDomain()
	if err != nil {
		return nil, fmt.Errorf("failed to get universe domain: %w", err)
	}
	opts := []option.ClientOption{
		option.WithAuthCredentialsJSON(option.CredentialsType(f.Type), config.CredentialsJSON),
		option.WithUserAgent(config.UserAgent),
		option.WithUniverseDomain(ud),
	}
	dnsService, err := gdnsv1.NewService(ctx, opts...)
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
	change := &gdnsv1.Change{Additions: []*gdnsv1.ResourceRecordSet{resourceRecordSet(record)}}

	project, zoneID, err := p.parseZone(zone)
	if err != nil {
		return err
	}

	call := p.dnsService.Changes.Create(project, zoneID, change)
	_, err = call.Do()
	if ae, ok := err.(*googleapi.Error); ok && ae.Code == http.StatusConflict {
		// The record already exists; update it to the desired state.
		return p.Replace(record, zone)
	}
	return err
}

func (p *Provider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	ctx := context.Background()

	project, zoneID, err := p.parseZone(zone)
	if err != nil {
		return err
	}

	// Collect existing record sets that match the DNS name and type so
	// they can be atomically replaced in a single Change request.
	var oldRecords []*gdnsv1.ResourceRecordSet
	listCall := p.dnsService.ResourceRecordSets.List(project, zoneID).Name(record.Spec.DNSName).Type(string(record.Spec.RecordType))
	if err := listCall.Pages(ctx, func(page *gdnsv1.ResourceRecordSetsListResponse) error {
		for _, rrs := range page.Rrsets {
			log.Info("found old DNS resource record set", "resourceRecordSet", rrs)
			oldRecords = append(oldRecords, rrs)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to list resource record sets in zone %s: %w", zoneID, err)
	}

	// Build a single atomic change that deletes old records and adds
	// the new one in one API call, preventing a window where the
	// record does not exist.
	change := &gdnsv1.Change{
		Deletions: oldRecords,
		Additions: []*gdnsv1.ResourceRecordSet{resourceRecordSet(record)},
	}
	call := p.dnsService.Changes.Create(project, zoneID, change)
	if _, err := call.Do(); err != nil {
		if ae, ok := err.(*googleapi.Error); ok && ae.Code == http.StatusNotFound {
			// Old records were already gone; fall back to create.
			return p.Ensure(record, zone)
		}
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
