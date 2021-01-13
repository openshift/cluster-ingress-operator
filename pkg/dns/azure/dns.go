package azure

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"

	iov1 "github.com/openshift/api/operatoringress/v1"
	dns "github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/dns/azure/client"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
)

var (
	_   dns.Provider = &provider{}
	log              = logf.Logger.WithName("dns")
)

// Config is the necessary input to configure the manager for azure.
type Config struct {
	// Environment is the azure cloud environment.
	Environment string
	// ClientID is an azure service principal appID.
	ClientID string
	// ClientSecret is an azure service principal's credential.
	ClientSecret string
	// TenantID is the azure identity's tenant ID.
	TenantID string
	// SubscriptionID is the azure identity's subscription ID.
	SubscriptionID string
}

type provider struct {
	config       Config
	client       client.DNSClient
	clientConfig client.Config
}

// NewProvider creates a new dns.Provider for Azure. It only supports DNSRecords with
// type A.
func NewProvider(config Config, operatorReleaseVersion string) (dns.Provider, error) {
	env, err := azure.EnvironmentFromName(config.Environment)
	if err != nil {
		return nil, fmt.Errorf("could not determine cloud environment: %w", err)
	}
	c, err := client.New(client.Config{
		Environment:    env,
		SubscriptionID: config.SubscriptionID,
		ClientID:       config.ClientID,
		ClientSecret:   config.ClientSecret,
		TenantID:       config.TenantID,
	}, userAgent(operatorReleaseVersion))
	if err != nil {
		return nil, err
	}
	return &provider{config: config, client: c}, nil
}

func userAgent(operatorReleaseVersion string) string {
	return fmt.Sprintf("%s/%s", "openshift.io ingress-operator", operatorReleaseVersion)
}

func (m *provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	if record.Spec.RecordType != iov1.ARecordType {
		return fmt.Errorf("only A record types are supported")
	}

	targetZone, err := client.ParseZone(zone.ID)
	if err != nil {
		return errors.Wrap(err, "failed to parse zoneID")
	}

	ARecordName, err := getARecordName(record.Spec.DNSName, targetZone.Name)
	if err != nil {
		return err
	}

	// TODO: handle >0 targets
	err = m.client.Put(
		context.TODO(),
		*targetZone,
		client.ARecord{
			Address: record.Spec.Targets[0],
			Name:    ARecordName,
			TTL:     record.Spec.RecordTTL,
		})

	if err == nil {
		log.Info("upserted DNS record", "record", record.Spec, "zone", zone)
	}

	return err
}

func (m *provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	targetZone, err := client.ParseZone(zone.ID)
	if err != nil {
		return errors.Wrap(err, "failed to parse zoneID")
	}

	ARecordName, err := getARecordName(record.Spec.DNSName, targetZone.Name)
	if err != nil {
		return err
	}

	// TODO: handle >0 targets
	err = m.client.Delete(
		context.TODO(),
		*targetZone,
		client.ARecord{
			Address: record.Spec.Targets[0],
			Name:    ARecordName,
			TTL:     record.Spec.RecordTTL,
		})

	if err == nil {
		log.Info("deleted DNS record", "record", record.Spec, "zone", zone)
	}

	return err
}

func (m *provider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return m.Ensure(record, zone)
}

// getARecordName extracts the ARecord subdomain name from the full domain string.
// azure defines the ARecord Name as the subdomain name only.
func getARecordName(recordDomain string, zoneName string) (string, error) {
	return strings.TrimSuffix(strings.TrimSuffix(recordDomain, "."), "."+zoneName), nil
}
