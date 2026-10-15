package client

import (
	"context"
	"net/http"
	"net/netip"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/pkg/errors"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
)

var log = logf.Logger.WithName("azure-dns-client")

// userAgent is the User-Agent identifier for the Cluster Ingress Operator.
// Azure SDK has a 24-character limit for ApplicationID, and spaces are replaced with '/'.
const userAgent = "cluster-ingress-operator"

func telemetryOptions() policy.TelemetryOptions {
	return policy.TelemetryOptions{
		ApplicationID: userAgent,
	}
}

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
	// identity token. If no client secret is available, this token file is
	// used for Azure workload identity.
	FederatedTokenFile string
	// TenantID is the Azure tenant ID.
	TenantID string
}

// ARecord is a DNS A or AAAA record.
type ARecord struct {
	// Name is the record name.
	Name string

	// Address is the IPv4 or IPv6 address of dns record.
	Address string

	//TTL is the Time To Live property of the A record
	TTL int64

	//Label is the metadata label that needs to be added with the A record.
	Label string
}

// isIPv6 reports whether the record needs to be published as AAAA rather than A.
// Both Put and Delete derive the type from this, so they cannot disagree and
// leave an orphaned record in the zone.
func (a ARecord) isIPv6() bool {
	ip, err := netip.ParseAddr(a.Address)
	// Is6 is also true for an IPv4-mapped address, which belongs in an A
	// record, so it has to be excluded to stay consistent with the record type
	// chosen for the DNSRecord.
	return err == nil && ip.Is6() && !ip.Is4In6()
}

// isNotFound reports whether err is Azure's response for a resource that does
// not exist.
func isNotFound(err error) bool {
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		return respErr.StatusCode == http.StatusNotFound
	}
	return false
}

// ownsRecordSet reports whether an existing record set was written by this
// operator for arec, which is what makes it the previous address family of this
// record rather than a separately managed one. Ownership is keyed on arec.Label,
// the "kubernetes.io_cluster.<infraID>" metadata entry Ensure attaches to
// everything it publishes, and the entry has to carry the same value we would
// write: a zone may tag a record "shared" to mark it as not ours to remove.
//
// desired is the metadata being written for arec, so the comparison is against
// what this operator would produce rather than a copy of the value constant.
// When the label is unknown or absent from desired, nothing can be claimed, and
// the record set is left alone.
func ownsRecordSet(existing, desired map[string]*string, arec ARecord) bool {
	if arec.Label == "" {
		return false
	}

	want, ok := desired[arec.Label]
	if !ok || want == nil {
		return false
	}

	got, ok := existing[arec.Label]
	return ok && got != nil && *got == *want
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

func (c *recordSetClient) Put(ctx context.Context, zone Zone, arec ARecord, metadata map[string]*string) error {
	rs := armdns.RecordSet{
		Properties: &armdns.RecordSetProperties{
			TTL:      &arec.TTL,
			Metadata: metadata,
		},
	}
	recordType, staleType := armdns.RecordTypeA, armdns.RecordTypeAAAA
	if arec.isIPv6() {
		recordType, staleType = armdns.RecordTypeAAAA, armdns.RecordTypeA
		rs.Properties.AaaaRecords = []*armdns.AaaaRecord{
			{IPv6Address: &arec.Address},
		}
	} else {
		rs.Properties.ARecords = []*armdns.ARecord{
			{IPv4Address: &arec.Address},
		}
	}
	_, err := c.client.CreateOrUpdate(ctx, zone.ResourceGroup, zone.Name, arec.Name, recordType, rs, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to update dns %s record: %s.%s", recordType, arec.Name, zone.Name)
	}

	if err := c.deleteStaleRecordSet(ctx, zone, arec, staleType, metadata); err != nil {
		return errors.Wrapf(err, "failed to remove the stale %s record set, which may still resolve: %s.%s", staleType, arec.Name, zone.Name)
	}
	return nil
}

// deleteStaleRecordSet removes the record set this record left behind in the
// other address family. Azure keys a record set by name and type, so publishing
// the new family does not replace the old one, and the name would keep
// resolving to the address it moved away from. It runs after the new record is
// live, so the name is never unresolvable.
//
// Only a record set this operator wrote for this record is removed. A zone may
// legitimately hold an A and an AAAA record under the same name as a managed
// dual-stack pair, and those must survive.
func (c *recordSetClient) deleteStaleRecordSet(ctx context.Context, zone Zone, arec ARecord, staleType armdns.RecordType, metadata map[string]*string) error {
	existing, err := c.client.Get(ctx, zone.ResourceGroup, zone.Name, arec.Name, staleType, nil)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "failed to get stale dns %s record: %s.%s", staleType, arec.Name, zone.Name)
	}

	if existing.Properties == nil || !ownsRecordSet(existing.Properties.Metadata, metadata, arec) {
		return nil
	}

	opts := &armdns.RecordSetsClientDeleteOptions{IfMatch: existing.Etag}
	if _, err := c.client.Delete(ctx, zone.ResourceGroup, zone.Name, arec.Name, staleType, opts); err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == http.StatusPreconditionFailed {
			return errors.Wrapf(err, "stale dns %s record changed after its ownership was checked: %s.%s", staleType, arec.Name, zone.Name)
		}
		return errors.Wrapf(err, "failed to delete stale dns %s record: %s.%s", staleType, arec.Name, zone.Name)
	}
	return nil
}

func (c *recordSetClient) Delete(ctx context.Context, zone Zone, arec ARecord) error {
	recordType := armdns.RecordTypeA
	if arec.isIPv6() {
		recordType = armdns.RecordTypeAAAA
	}
	_, err := c.client.Delete(ctx, zone.ResourceGroup, zone.Name, arec.Name, recordType, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to delete dns %s record: %s.%s", recordType, arec.Name, zone.Name)
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

func (c *privateRecordSetClient) Put(ctx context.Context, zone Zone, arec ARecord, metadata map[string]*string) error {
	rs := armprivatedns.RecordSet{
		Properties: &armprivatedns.RecordSetProperties{
			TTL:      &arec.TTL,
			Metadata: metadata,
		},
	}
	recordType, staleType := armprivatedns.RecordTypeA, armprivatedns.RecordTypeAAAA
	if arec.isIPv6() {
		recordType, staleType = armprivatedns.RecordTypeAAAA, armprivatedns.RecordTypeA
		rs.Properties.AaaaRecords = []*armprivatedns.AaaaRecord{
			{IPv6Address: &arec.Address},
		}
	} else {
		rs.Properties.ARecords = []*armprivatedns.ARecord{
			{IPv4Address: &arec.Address},
		}
	}

	_, err := c.client.CreateOrUpdate(ctx, zone.ResourceGroup, zone.Name, recordType, arec.Name, rs, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to update dns %s record: %s.%s", recordType, arec.Name, zone.Name)
	}

	if err := c.deleteStaleRecordSet(ctx, zone, arec, staleType, metadata); err != nil {
		return errors.Wrapf(err, "failed to remove the stale %s record set: %s.%s", staleType, arec.Name, zone.Name)
	}
	return nil
}

// deleteStaleRecordSet is the private-zone counterpart of
// recordSetClient.deleteStaleRecordSet; see that method for the rationale.
func (c *privateRecordSetClient) deleteStaleRecordSet(ctx context.Context, zone Zone, arec ARecord, staleType armprivatedns.RecordType, metadata map[string]*string) error {
	existing, err := c.client.Get(ctx, zone.ResourceGroup, zone.Name, staleType, arec.Name, nil)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "failed to get stale dns %s record: %s.%s", staleType, arec.Name, zone.Name)
	}

	if existing.Properties == nil || !ownsRecordSet(existing.Properties.Metadata, metadata, arec) {
		return nil
	}

	opts := &armprivatedns.RecordSetsClientDeleteOptions{IfMatch: existing.Etag}
	if _, err := c.client.Delete(ctx, zone.ResourceGroup, zone.Name, staleType, arec.Name, opts); err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == http.StatusPreconditionFailed {
			return errors.Wrapf(err, "stale dns %s record changed after its ownership was checked: %s.%s", staleType, arec.Name, zone.Name)
		}
		return errors.Wrapf(err, "failed to delete stale dns %s record: %s.%s", staleType, arec.Name, zone.Name)
	}
	return nil
}

func (c *privateRecordSetClient) Delete(ctx context.Context, zone Zone, arec ARecord) error {
	recordType := armprivatedns.RecordTypeA
	if arec.isIPv6() {
		recordType = armprivatedns.RecordTypeAAAA
	}
	_, err := c.client.Delete(ctx, zone.ResourceGroup, zone.Name, recordType, arec.Name, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to delete dns %s record: %s.%s", recordType, arec.Name, zone.Name)
	}
	return nil
}
