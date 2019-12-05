package gcp

import (
	"context"
	"net/http"

	"google.golang.org/api/googleapi"

	configv1 "github.com/openshift/api/config/v1"

	iov1 "github.com/openshift/cluster-ingress-operator/pkg/api/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"

	gdnsv1 "google.golang.org/api/dns/v1"
	"google.golang.org/api/option"
)

var (
	_ dns.Provider = &Provider{}
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

func (p *Provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	change := &gdnsv1.Change{Additions: []*gdnsv1.ResourceRecordSet{resourceRecordSet(record)}}
	call := p.dnsService.Changes.Create(p.config.Project, zone.ID, change)
	_, err := call.Do()
	// Since we don't yet handle updates, assume that existing records are correct.
	if ae, ok := err.(*googleapi.Error); ok && ae.Code == http.StatusConflict {
		return nil
	}
	return err
}

func (p *Provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	change := &gdnsv1.Change{Deletions: []*gdnsv1.ResourceRecordSet{resourceRecordSet(record)}}
	call := p.dnsService.Changes.Create(p.config.Project, zone.ID, change)
	_, err := call.Do()
	if ae, ok := err.(*googleapi.Error); ok && ae.Code == http.StatusNotFound {
		return nil
	}
	return err
}

func resourceRecordSet(record *iov1.DNSRecord) *gdnsv1.ResourceRecordSet {
	return &gdnsv1.ResourceRecordSet{
		Name:    record.Spec.DNSName,
		Rrdatas: record.Spec.Targets,
		Type:    record.Spec.RecordType,
		Ttl:     record.Spec.RecordTTL,
	}
}

func (p *Provider) StartWatcher(string, <-chan struct{}) error { return nil }
