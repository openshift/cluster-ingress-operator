package azure

import (
	"context"
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	dns "github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/dns/azure/client"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/pkg/errors"
)

var (
	_   dns.Manager = &manager{}
	log             = logf.Logger.WithName("dns")
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
	// DNS is public and private DNS zone configuration for the cluster.
	DNS *configv1.DNS
}

type manager struct {
	config       Config
	client       client.DNSClient
	clientConfig client.Config
}

func NewManager(config Config, operatorReleaseVersion string) (dns.Manager, error) {
	c, err := client.New(client.Config{
		Environment:    config.Environment,
		SubscriptionID: config.SubscriptionID,
		ClientID:       config.ClientID,
		ClientSecret:   config.ClientSecret,
		TenantID:       config.TenantID,
	}, userAgent(operatorReleaseVersion))
	if err != nil {
		return nil, err
	}
	return &manager{config: config, client: c}, nil
}

func userAgent(operatorReleaseVersion string) string {
	return fmt.Sprintf("%s/%s", "openshift.io ingress-operator", operatorReleaseVersion)
}

func (m *manager) Ensure(record *dns.Record) error {
	if record.Type != dns.ARecordType {
		return fmt.Errorf("only A record types are supported")
	}

	targetZone, err := client.ParseZone(record.Zone.ID)
	if err != nil {
		return errors.Wrap(err, "failed to parse zoneID")
	}

	ARecordName, err := getARecordName(record.ARecord.Domain, "."+targetZone.Name)
	if err != nil {
		return err
	}

	err = m.client.Put(
		context.TODO(),
		*targetZone,
		client.ARecord{
			Address: record.ARecord.Address,
			Name:    ARecordName,
		})

	if err == nil {
		log.Info("upserted DNS record", "record", record)
	}

	return err
}

func (m *manager) Delete(record *dns.Record) error {
	targetZone, err := client.ParseZone(record.Zone.ID)
	if err != nil {
		return errors.Wrap(err, "failed to parse zoneID")
	}

	ARecordName, err := getARecordName(record.ARecord.Domain, "."+targetZone.Name)
	if err != nil {
		return err
	}

	err = m.client.Delete(
		context.TODO(),
		*targetZone,
		client.ARecord{
			Address: record.ARecord.Address,
			Name:    ARecordName,
		})

	if err == nil {
		log.Info("deleted DNS record", "record", record)
	}

	return err
}

// getARecordName extracts the ARecord subdomain name from the full domain string.
// azure defines the ARecord Name as the subdomain name only.
func getARecordName(recordDomain string, zoneName string) (string, error) {
	return strings.TrimSuffix(recordDomain, zoneName), nil
}
