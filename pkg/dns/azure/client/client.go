package client

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/pkg/errors"
)

// userAgent is the User-Agent identifier for the Cluster Ingress Operator.
// Azure SDK has a 24-character limit for ApplicationID, and spaces are replaced with '/'.
const userAgent = "cluster-ingress-operator"

func telemetryOptions() policy.TelemetryOptions {
	return policy.TelemetryOptions{
		ApplicationID: userAgent,
	}
}

type DNSClient interface {
	Put(ctx context.Context, zone Zone, rs RecordSet, metadata map[string]*string) error
	Delete(ctx context.Context, zone Zone, rs RecordSet) error
}

type Config struct {
	// Environment describes the Azure environment: ChinaCloud,
	// USGovernmentCloud, PublicCloud, or AzureStackCloud.  If empty,
	// AzureStackCloud is assumed.
	Environment azure.Environment
	// SubscriptionID is the subscription id for the Azure identity.
	SubscriptionID string
	// ClientID is an Azure application client id.
	ClientID string
	// ClientSecret is an Azure application client secret.  It is required
	// if Azure workload identity is not used.
	ClientSecret string
	// FederatedTokenFile is the path to a file containing a workload
	// identity token. If no client secret is available, this token file is
	// used for Azure workload identity.
	FederatedTokenFile string
	// TenantID is the Azure tenant ID.
	TenantID string
}

// RecordType distinguishes A from AAAA record sets.
type RecordType string

const (
	RecordTypeA    RecordType = "A"
	RecordTypeAAAA RecordType = "AAAA"
)

// RecordSet is a DNS A or AAAA record set.
type RecordSet struct {
	// Name is the record name.
	Name string

	// Type is the DNS record type (A or AAAA).
	Type RecordType

	// Addresses is the IPv4 or IPv6 address of dns record.
	Addresses []string

	// TTL is the Time To Live property of the A record
	TTL int64

	// Label is the metadata label that needs to be added with the A record.
	Label string
}

type dnsClient struct {
	recordSetClient, privateRecordSetClient DNSClient
}

// New returns an authenticated DNSClient
func New(config Config) (DNSClient, error) {
	credential, err := getAzureCredentials(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get azure credentials")
	}
	rsc, err := newRecordSetClient(config, credential)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create recordSetClient")
	}

	prsc, err := newPrivateRecordSetClient(config, credential)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create privateRecordSetClient")
	}

	return &dnsClient{recordSetClient: rsc, privateRecordSetClient: prsc}, nil
}

func (c *dnsClient) Put(ctx context.Context, zone Zone, rs RecordSet, metadata map[string]*string) error {
	switch zone.Provider {
	case "Microsoft.Network/privateDnsZones":
		return c.privateRecordSetClient.Put(ctx, zone, rs, metadata)
	case "Microsoft.Network/dnszones":
		return c.recordSetClient.Put(ctx, zone, rs, metadata)
	default:
		return errors.Errorf("unsupported Zone provider %s", zone.Provider)
	}
}

func (c *dnsClient) Delete(ctx context.Context, zone Zone, rs RecordSet) error {
	switch zone.Provider {
	case "Microsoft.Network/privateDnsZones":
		return c.privateRecordSetClient.Delete(ctx, zone, rs)
	case "Microsoft.Network/dnszones":
		return c.recordSetClient.Delete(ctx, zone, rs)
	default:
		return errors.Errorf("unsupported Zone provider %s", zone.Provider)
	}
}

type recordSetClient struct {
	client *armdns.RecordSetsClient
}

func newRecordSetClient(config Config, credential azcore.TokenCredential) (*recordSetClient, error) {
	cloudConfig := ParseCloudEnvironment(config)
	options := &arm.ClientOptions{
		ClientOptions: policy.ClientOptions{
			Cloud:     cloudConfig,
			Telemetry: telemetryOptions(),
		},
	}

	rc, err := armdns.NewRecordSetsClient(config.SubscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	return &recordSetClient{client: rc}, nil
}

func (c *recordSetClient) Put(ctx context.Context, zone Zone, rs RecordSet, metadata map[string]*string) error {
	recordType, err := publicRecordType(rs.Type)
	if err != nil {
		return err
	}
	properties := &armdns.RecordSetProperties{
		TTL:      &rs.TTL,
		Metadata: metadata,
	}
	if rs.Label != "" {
		properties.Metadata[rs.Label] = to.StringPtr("owned")
	}
	for i := range rs.Addresses {
		switch rs.Type {
		case RecordTypeA:
			properties.ARecords = append(properties.ARecords, &armdns.ARecord{IPv4Address: &rs.Addresses[i]})
		case RecordTypeAAAA:
			properties.AaaaRecords = append(properties.AaaaRecords, &armdns.AaaaRecord{IPv6Address: &rs.Addresses[i]})
		}
	}
	_, err = c.client.CreateOrUpdate(ctx, zone.ResourceGroup, zone.Name, rs.Name, recordType, armdns.RecordSet{Properties: properties}, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to update dns %s record: %s.%s", rs.Type, rs.Name, zone.Name)
	}
	return nil
}

func (c *recordSetClient) Delete(ctx context.Context, zone Zone, rs RecordSet) error {
	recordType, err := publicRecordType(rs.Type)
	if err != nil {
		return err
	}
	_, err = c.client.Delete(ctx, zone.ResourceGroup, zone.Name, rs.Name, recordType, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to delete dns %s record: %s.%s", rs.Type, rs.Name, zone.Name)
	}
	return nil
}

type privateRecordSetClient struct {
	client *armprivatedns.RecordSetsClient
}

func newPrivateRecordSetClient(config Config, credential azcore.TokenCredential) (*privateRecordSetClient, error) {
	cloudConfig := ParseCloudEnvironment(config)
	options := &arm.ClientOptions{
		ClientOptions: policy.ClientOptions{
			Cloud:     cloudConfig,
			Telemetry: telemetryOptions(),
		},
	}

	prc, err := armprivatedns.NewRecordSetsClient(config.SubscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	return &privateRecordSetClient{client: prc}, nil
}

func (c *privateRecordSetClient) Put(ctx context.Context, zone Zone, rs RecordSet, metadata map[string]*string) error {
	recordType, err := privateRecordType(rs.Type)
	if err != nil {
		return err
	}
	properties := &armprivatedns.RecordSetProperties{
		TTL:      &rs.TTL,
		Metadata: metadata,
	}
	if rs.Label != "" {
		properties.Metadata[rs.Label] = to.StringPtr("owned")
	}
	for i := range rs.Addresses {
		switch rs.Type {
		case RecordTypeA:
			properties.ARecords = append(properties.ARecords, &armprivatedns.ARecord{IPv4Address: &rs.Addresses[i]})
		case RecordTypeAAAA:
			properties.AaaaRecords = append(properties.AaaaRecords, &armprivatedns.AaaaRecord{IPv6Address: &rs.Addresses[i]})
		}
	}

	_, err = c.client.CreateOrUpdate(ctx, zone.ResourceGroup, zone.Name, recordType, rs.Name, armprivatedns.RecordSet{Properties: properties}, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to update dns %s record: %s.%s", recordType, rs.Name, zone.Name)
	}

	return nil
}

func (c *privateRecordSetClient) Delete(ctx context.Context, zone Zone, rs RecordSet) error {
	recordType, err := privateRecordType(rs.Type)
	if err != nil {
		return errors.Wrapf(err, "failed to delete dns %s record: %s.%s", recordType, rs.Name, zone.Name)
	}
	_, err = c.client.Delete(ctx, zone.ResourceGroup, zone.Name, recordType, rs.Name, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to delete dns %s record: %s.%s", recordType, rs.Name, zone.Name)
	}
	return nil
}

// publicRecordType maps a RecordSet type onto the public DNS zone SDK's
// record type.
func publicRecordType(t RecordType) (armdns.RecordType, error) {
	switch t {
	case RecordTypeA:
		return armdns.RecordTypeA, nil
	case RecordTypeAAAA:
		return armdns.RecordTypeAAAA, nil
	default:
		return "", errors.Errorf("unsupported record type %q", t)
	}
}

// privateRecordType maps a RecordSet type onto the private DNS zone SDK's
// record type.
func privateRecordType(t RecordType) (armprivatedns.RecordType, error) {
	switch t {
	case RecordTypeA:
		return armprivatedns.RecordTypeA, nil
	case RecordTypeAAAA:
		return armprivatedns.RecordTypeAAAA, nil
	default:
		return "", errors.Errorf("unsupported record type %q", t)
	}
}
