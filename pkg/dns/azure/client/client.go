package client

import (
	"context"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/pkg/errors"
)

type DNSClient interface {
	Put(ctx context.Context, zone Zone, arec ARecord, metadata map[string]*string) error
	Delete(ctx context.Context, zone Zone, arec ARecord) error
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
	// identity token.  If FederatedTokenFile is specified and
	// AzureWorkloadIdentityEnabled is true, then Azure workload identity is
	// used instead of using a client secret.
	FederatedTokenFile string
	// TenantID is the Azure tenant ID.
	TenantID string
	// AzureWorkloadIdentityEnabled indicates whether the
	// "AzureWorkloadIdentity" feature gate is enabled.
	AzureWorkloadIdentityEnabled bool
}

// ARecord is a DNS A record.
type ARecord struct {
	// Name is the record name.
	Name string

	// Address is the IPv4 address of the A record.
	Address string

	//TTL is the Time To Live property of the A record
	TTL int64

	//Label is the metadata label that needs to be added with the A record.
	Label string
}

type dnsClient struct {
	recordSetClient, privateRecordSetClient DNSClient
}

// New returns an authenticated DNSClient
func New(config Config) (DNSClient, error) {
	credential, err := getAzureCredentials(config)
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

func (c *dnsClient) Put(ctx context.Context, zone Zone, arec ARecord, metadata map[string]*string) error {
	switch zone.Provider {
	case "Microsoft.Network/privateDnsZones":
		return c.privateRecordSetClient.Put(ctx, zone, arec, metadata)
	case "Microsoft.Network/dnszones":
		return c.recordSetClient.Put(ctx, zone, arec, metadata)
	default:
		return errors.Errorf("unsupported Zone provider %s", zone.Provider)
	}
}

func (c *dnsClient) Delete(ctx context.Context, zone Zone, arec ARecord) error {
	switch zone.Provider {
	case "Microsoft.Network/privateDnsZones":
		return c.privateRecordSetClient.Delete(ctx, zone, arec)
	case "Microsoft.Network/dnszones":
		return c.recordSetClient.Delete(ctx, zone, arec)
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
			Cloud: cloudConfig,
		},
	}

	rc, err := armdns.NewRecordSetsClient(config.SubscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	return &recordSetClient{client: rc}, nil
}

func (c *recordSetClient) Put(ctx context.Context, zone Zone, arec ARecord, metadata map[string]*string) error {
	rs := armdns.RecordSet{
		Properties: &armdns.RecordSetProperties{
			TTL: &arec.TTL,
			ARecords: []*armdns.ARecord{
				{IPv4Address: &arec.Address},
			},
			Metadata: metadata,
		},
	}
	_, err := c.client.CreateOrUpdate(ctx, zone.ResourceGroup, zone.Name, arec.Name, armdns.RecordTypeA, rs, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to update dns a record: %s.%s", arec.Name, zone.Name)
	}
	return nil
}

func (c *recordSetClient) Delete(ctx context.Context, zone Zone, arec ARecord) error {
	_, err := c.client.Delete(ctx, zone.ResourceGroup, zone.Name, arec.Name, armdns.RecordTypeA, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to delete dns a record: %s.%s", arec.Name, zone.Name)
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
			Cloud: cloudConfig,
		},
	}

	prc, err := armprivatedns.NewRecordSetsClient(config.SubscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	return &privateRecordSetClient{client: prc}, nil
}

func (c *privateRecordSetClient) Put(ctx context.Context, zone Zone, arec ARecord, metadata map[string]*string) error {
	rs := armprivatedns.RecordSet{
		Properties: &armprivatedns.RecordSetProperties{
			TTL: &arec.TTL,
			ARecords: []*armprivatedns.ARecord{
				{IPv4Address: &arec.Address},
			},
			Metadata: metadata,
		},
	}

	_, err := c.client.CreateOrUpdate(ctx, zone.ResourceGroup, zone.Name, armprivatedns.RecordTypeA, arec.Name, rs, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to update dns a record: %s.%s", arec.Name, zone.Name)
	}
	return nil
}

func (c *privateRecordSetClient) Delete(ctx context.Context, zone Zone, arec ARecord) error {
	_, err := c.client.Delete(ctx, zone.ResourceGroup, zone.Name, armprivatedns.RecordTypeA, arec.Name, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to delete dns a record: %s.%s", arec.Name, zone.Name)
	}
	return nil
}
