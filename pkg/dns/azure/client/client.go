package client

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/services/dns/mgmt/2017-10-01/dns"
	"github.com/pkg/errors"
)

type DNSClient interface {
	Put(ctx context.Context, zone Zone, arec ARecord) error
	Delete(ctx context.Context, zone Zone, arec ARecord) error
}

type Config struct {
	Environment    string
	SubscriptionID string
	ClientID       string
	ClientSecret   string
	TenantID       string
}

// ARecord is a DNS A record.
type ARecord struct {
	// Name is the record name.
	Name string

	// Address is the IPv4 address of the A record.
	Address string

	//TTL is the Time To Live property of the A record
	TTL int64
}

type dnsClient struct {
	zones      dns.ZonesClient
	recordSets dns.RecordSetsClient
	config     Config
}

// New returns an authenticated DNSClient
func New(config Config, userAgentExtension string) (DNSClient, error) {
	authorizer, err := getAuthorizerForResource(config)
	if err != nil {
		return nil, err
	}
	zc := dns.NewZonesClient(config.SubscriptionID)
	zc.AddToUserAgent(userAgentExtension)
	zc.Authorizer = authorizer
	rc := dns.NewRecordSetsClient(config.SubscriptionID)
	rc.AddToUserAgent(userAgentExtension)
	rc.Authorizer = authorizer
	return &dnsClient{zones: zc, recordSets: rc, config: config}, nil
}

func (c *dnsClient) Put(ctx context.Context, zone Zone, arec ARecord) error {
	rs := dns.RecordSet{
		RecordSetProperties: &dns.RecordSetProperties{
			TTL: &arec.TTL,
			ARecords: &[]dns.ARecord{
				{Ipv4Address: &arec.Address},
			},
		},
	}
	_, err := c.recordSets.CreateOrUpdate(ctx, zone.ResourceGroup, zone.Name, arec.Name, dns.A, rs, "", "")
	if err != nil {
		return errors.Wrapf(err, "failed to update dns a record: %s.%s", arec.Name, zone.Name)
	}
	return nil
}

func (c *dnsClient) Delete(ctx context.Context, zone Zone, arec ARecord) error {
	_, err := c.recordSets.Delete(ctx, zone.ResourceGroup, zone.Name, arec.Name, dns.A, "")
	if err != nil {
		return errors.Wrapf(err, "failed to delete dns a record: %s.%s", arec.Name, zone.Name)
	}
	return nil
}
