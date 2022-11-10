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
	Project         string
	UserAgent       string
	CredentialsJSON []byte
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

func (p *Provider) parseZone(zone configv1.DNSZone) (string, string, error) {
	id := zone.ID
	project := p.config.Project

	// parse the zone that was provided
	parts := strings.Split(id, "/")
	switch {
	case len(parts) == 1:
		return project, id, nil
	case len(parts) == 4 && parts[0] == "projects" && parts[2] == "managedZones":
		return parts[1], parts[3], nil
	}

	return "", "", fmt.Errorf("invalid managedZone: %s", zone.ID)
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
