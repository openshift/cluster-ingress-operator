package azure

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/dns/azure/client"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	awsutil "github.com/openshift/cluster-ingress-operator/pkg/util/aws"
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
	// IPFamily is the cluster's IP family configuration from the
	// Infrastructure's PlatformStatus. It is fixed for the lifetime of the
	// operator and determines whether a record is expected to have both an
	// A and an AAAA record set.
	IPFamily configv1.IPFamilyType
}

type provider struct {
	config Config
	client client.DNSClient
}

// NewProvider creates a new dns.Provider for Azure. It only supports DNSRecords with
// type A or AAAA.
func NewProvider(config Config) (dns.Provider, error) {
	var env azure.Environment
	var err error
	switch config.Environment {
	case string(configv1.AzureStackCloud):
		env, err = azure.EnvironmentFromURL(config.ARMEndpoint)
	default:
		env, err = azure.EnvironmentFromName(config.Environment)
	}
	if err != nil {
		return nil, fmt.Errorf("could not determine cloud environment: %w", err)
	}
	c, err := client.New(client.Config{
		Environment:        env,
		SubscriptionID:     config.SubscriptionID,
		ClientID:           config.ClientID,
		ClientSecret:       config.ClientSecret,
		FederatedTokenFile: config.FederatedTokenFile,
		TenantID:           config.TenantID,
	})
	if err != nil {
		return nil, err
	}
	return &provider{config: config, client: c}, nil
}

func (m *provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	if record.Spec.RecordType != iov1.ARecordType && record.Spec.RecordType != iov1.AAAARecordType {
		return fmt.Errorf("only A and AAAA record types are supported")
	}

	targetZone, err := client.ParseZone(zone.ID)
	if err != nil {
		return errors.Wrap(err, "failed to parse zoneID")
	}

	ARecordName, err := getARecordName(record.Spec.DNSName, targetZone.Name)
	if err != nil {
		return err
	}
	recordSets, err := recordSetsForTargets(ARecordName, record.Spec.Targets, record.Spec.RecordTTL, m.config.InfraID)
	if err != nil {
		return err
	}
	for _, rs := range recordSets {
		if err := m.client.Put(context.TODO(), *targetZone, rs, m.config.Tags); err != nil {
			return err
		}
	}

	if awsutil.IsDualStack(m.config.IPFamily) && len(recordSets) < 2 {
		return fmt.Errorf("dual-stack cluster requires both an IPv4 and an IPv6 target for %s, got %v", record.Spec.DNSName, record.Spec.Targets)
	}

	log.Info("upserted DNS record", "record", record.Spec, "zone", zone)
	return err
}

func (m *provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	targetZone, err := client.ParseZone(zone.ID)
	if err != nil {
		return errors.Wrap(err, "failed to parse zoneID")
	}

	recordName, err := getARecordName(record.Spec.DNSName, targetZone.Name)
	if err != nil {
		return err
	}

	recordTypes := []client.RecordType{}
	switch {
	case awsutil.IsDualStack(m.config.IPFamily):
		recordTypes = append(recordTypes, client.RecordTypeA, client.RecordTypeAAAA)
	case record.Spec.RecordType == iov1.AAAARecordType:
		recordTypes = append(recordTypes, client.RecordTypeAAAA)
	default:
		recordTypes = append(recordTypes, client.RecordTypeA)
	}

	// TODO: handle >0 targets
	for _, recordType := range recordTypes {
		rs := client.RecordSet{
			Name: recordName,
			Type: recordType,
			TTL:  record.Spec.RecordTTL,
		}
		if err := m.client.Delete(context.TODO(), *targetZone, rs); err != nil {
			return err
		}
	}

	log.Info("deleted DNS record", "record", record.Spec, "zone", zone)
	return nil
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

// recordSetsForTargets partitions targets by address family and returns the
// record sets to publish: at most one A set holding every IPv4 target and one
// AAAA set holding every IPv6 target. The A set is returned first.
//
// An IPv4-mapped IPv6 address belongs in an A record, so it is normalized to
// its IPv4 form rather than written in its "::ffff:" spelling, which Azure
// would reject as an A record address.
func recordSetsForTargets(name string, targets []string, ttl int64, infraID string) ([]client.RecordSet, error) {
	var ipv4, ipv6 []string
	for _, target := range targets {
		ip, err := netip.ParseAddr(target)
		if err != nil {
			return nil, fmt.Errorf("target %q is not a valid IP address: %w", target, err)
		}
		if ip.Is4() || ip.Is4In6() {
			ipv4 = append(ipv4, ip.Unmap().String())
		} else {
			ipv6 = append(ipv6, ip.String())
		}
	}

	defaultRecordSet := client.RecordSet{Name: name, TTL: ttl}

	if infraID != "" {
		defaultRecordSet.Label = fmt.Sprintf("kubernetes.io_cluster.%s", infraID)
	}

	var recordSets []client.RecordSet
	if len(ipv4) > 0 {
		rs := defaultRecordSet
		rs.Type = client.RecordTypeA
		rs.Addresses = ipv4
		recordSets = append(recordSets, rs)
	}
	if len(ipv6) > 0 {
		rs := defaultRecordSet
		rs.Type = client.RecordTypeAAAA
		rs.Addresses = ipv6
		recordSets = append(recordSets, rs)
	}
	if len(recordSets) == 0 {
		return nil, fmt.Errorf("no targets to publish")
	}
	return recordSets, nil
}
