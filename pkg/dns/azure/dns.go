package azure

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/dns/azure/client"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
)

const (
	// OCPClusterIDTagKeyPrefix is the cluster identifying tag key prefix that is added to
	// the Azure resources created by OCP.
	OCPClusterIDTagKeyPrefix = "kubernetes.io_cluster"

	// OCPClusterIDTagValue is the value of the cluster identifying tag that is added to
	// the Azure resources created by OCP.
	OCPClusterIDTagValue = "owned"
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
	// FederatedTokenFile is an azure federated token file.
	FederatedTokenFile string
	// TenantID is the azure identity's tenant ID.
	TenantID string
	// SubscriptionID is the azure identity's subscription ID.
	SubscriptionID string
	// ARMEndpoint specifies a URL to use for resource management in non-sovereign clouds such as Azure Stack.
	// The value is not needed for public Azure, Azure Government as it can be determined by the SDK.
	ARMEndpoint string
	// InfraID is the generated ID that is used to identify cloud resources created by the installer.
	InfraID string
	// Tags is a map of user-defined tags which should be applied to new resources created by the operator.
	Tags map[string]*string
}

type provider struct {
	config       Config
	client       client.DNSClient
	clientConfig client.Config
}

// NewProvider creates a new dns.Provider for Azure. It only supports DNSRecords with
// type A.
func NewProvider(config Config, AzureWorkloadIdentityEnabled bool) (dns.Provider, error) {
	var env azure.Environment
	var err error
	switch {
	case config.ARMEndpoint != "":
		env, err = azure.EnvironmentFromURL(config.ARMEndpoint)
	default:
		env, err = azure.EnvironmentFromName(config.Environment)
	}
	if err != nil {
		return nil, fmt.Errorf("could not determine cloud environment: %w", err)
	}
	c, err := client.New(client.Config{
		Environment:                  env,
		SubscriptionID:               config.SubscriptionID,
		ClientID:                     config.ClientID,
		ClientSecret:                 config.ClientSecret,
		FederatedTokenFile:           config.FederatedTokenFile,
		TenantID:                     config.TenantID,
		AzureWorkloadIdentityEnabled: AzureWorkloadIdentityEnabled,
	})
	if err != nil {
		return nil, err
	}
	return &provider{config: config, client: c}, nil
}

func (m *provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	if record.Spec.RecordType != iov1.ARecordType {
		return fmt.Errorf("only A record types are supported")
	}

	targetZone, err := client.ParseZone(zone.ID)
	if err != nil {
		return errors.Wrap(err, "failed to parse zoneID")
	}

	metadataLabel := m.config.InfraID
	ARecordName, err := getARecordName(record.Spec.DNSName, targetZone.Name)
	if err != nil {
		return err
	}
	ARecord := client.ARecord{
		Address: record.Spec.Targets[0],
		Name:    ARecordName,
		TTL:     record.Spec.RecordTTL,
	}
	if metadataLabel != "" {
		ARecord.Label = fmt.Sprintf("kubernetes.io_cluster.%s", metadataLabel)
	}

	// TODO: handle >0 targets
	err = m.client.Put(context.TODO(), *targetZone, ARecord, m.config.Tags)

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
// Azure defines the ARecord Name as the subdomain name only.
// This function logs a message if recordDomain is not a subdomain of zoneName.
func getARecordName(recordDomain string, zoneName string) (string, error) {
	trimmedDomain := strings.TrimSuffix(recordDomain, ".")
	if !strings.HasSuffix(trimmedDomain, "."+zoneName) {
		log.Info("domain is not a subdomain of zone. The DNS provider may still succeed in updating the record, "+
			"which might be unexpected", "domain", recordDomain, "zone", zoneName)
	}
	return strings.TrimSuffix(trimmedDomain, "."+zoneName), nil
}

// GetTagList returns a list of tags by merging the OCP default tags
// and the user-defined tags present in the Infrastructure.Status
func GetTagList(infraStatus *configv1.InfrastructureStatus) map[string]*string {
	tags := map[string]*string{
		fmt.Sprintf("%s.%v", OCPClusterIDTagKeyPrefix, infraStatus.InfrastructureName): to.StringPtr(OCPClusterIDTagValue),
	}
	if infraStatus.PlatformStatus != nil &&
		infraStatus.PlatformStatus.Azure != nil &&
		infraStatus.PlatformStatus.Azure.ResourceTags != nil {
		for _, tag := range infraStatus.PlatformStatus.Azure.ResourceTags {
			tags[tag.Key] = to.StringPtr(tag.Value)
		}
	}
	return tags
}
