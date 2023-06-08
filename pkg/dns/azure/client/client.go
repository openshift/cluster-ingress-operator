package client

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/profiles/2018-03-01/dns/mgmt/dns"
	"github.com/Azure/azure-sdk-for-go/services/privatedns/mgmt/2018-09-01/privatedns"
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
func New(config Config, userAgentExtension string) (DNSClient, error) {
	rsc, err := newRecordSetClient(config, userAgentExtension)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create recordSetClient")
	}

	prsc, err := newPrivateRecordSetClient(config, userAgentExtension)
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
	client dns.RecordSetsClient
}

func newRecordSetClient(config Config, userAgentExtension string) (*recordSetClient, error) {
	authorizer, err := getAuthorizerForResource(config)
	if err != nil {
		return nil, err
	}

	rc := dns.NewRecordSetsClientWithBaseURI(config.Environment.ResourceManagerEndpoint, config.SubscriptionID)
	rc.AddToUserAgent(userAgentExtension)
	rc.Authorizer = authorizer
	return &recordSetClient{client: rc}, nil
}

func (c *recordSetClient) Put(ctx context.Context, zone Zone, arec ARecord, metadata map[string]*string) error {
	rs := dns.RecordSet{
		RecordSetProperties: &dns.RecordSetProperties{
			TTL: &arec.TTL,
			ARecords: &[]dns.ARecord{
				{Ipv4Address: &arec.Address},
			},
			Metadata: metadata,
		},
	}
	_, err := c.client.CreateOrUpdate(ctx, zone.ResourceGroup, zone.Name, arec.Name, dns.A, rs, "", "")
	if err != nil {
		return errors.Wrapf(err, "failed to update dns a record: %s.%s", arec.Name, zone.Name)
	}
	return nil
}

func (c *recordSetClient) Delete(ctx context.Context, zone Zone, arec ARecord) error {
	_, err := c.client.Get(ctx, zone.ResourceGroup, zone.Name, arec.Name, dns.A)
	if err != nil {
		// TODO: How do we interpret this as a notfound error?
		return nil
	}
	_, err = c.client.Delete(ctx, zone.ResourceGroup, zone.Name, arec.Name, dns.A, "")
	if err != nil {
		return errors.Wrapf(err, "failed to delete dns a record: %s.%s", arec.Name, zone.Name)
	}
	return nil
}

type privateRecordSetClient struct {
	client privatedns.RecordSetsClient
}

func newPrivateRecordSetClient(config Config, userAgentExtension string) (*privateRecordSetClient, error) {
	authorizer, err := getAuthorizerForResource(config)
	if err != nil {
		return nil, err
	}

	prc := privatedns.NewRecordSetsClientWithBaseURI(config.Environment.ResourceManagerEndpoint, config.SubscriptionID)
	prc.AddToUserAgent(userAgentExtension)
	prc.Authorizer = authorizer
	return &privateRecordSetClient{client: prc}, nil
}

func (c *privateRecordSetClient) Put(ctx context.Context, zone Zone, arec ARecord, metadata map[string]*string) error {
	rs := privatedns.RecordSet{
		RecordSetProperties: &privatedns.RecordSetProperties{
			TTL: &arec.TTL,
			ARecords: &[]privatedns.ARecord{
				{Ipv4Address: &arec.Address},
			},
			Metadata: metadata,
		},
	}
	_, err := c.client.CreateOrUpdate(ctx, zone.ResourceGroup, zone.Name, privatedns.A, arec.Name, rs, "", "")
	if err != nil {
		return errors.Wrapf(err, "failed to update dns a record: %s.%s", arec.Name, zone.Name)
	}
	return nil
}

func (c *privateRecordSetClient) Delete(ctx context.Context, zone Zone, arec ARecord) error {
	_, err := c.client.Get(ctx, zone.ResourceGroup, zone.Name, privatedns.A, arec.Name)
	if err != nil {
		// TODO: How do we interpret this as a notfound error?
		return nil
	}
	_, err = c.client.Delete(ctx, zone.ResourceGroup, zone.Name, privatedns.A, arec.Name, "")
	if err != nil {
		return errors.Wrapf(err, "failed to delete dns a record: %s.%s", arec.Name, zone.Name)
	}
	return nil
}
