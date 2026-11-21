package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

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

	// metadataLookupTimeout bounds the ARM metadata endpoint lookup performed for
	// sovereign/edge clouds so that NewProvider cannot block indefinitely when the
	// ARM endpoint is slow or unreachable.
	metadataLookupTimeout = 30 * time.Second
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
	config Config
	client client.DNSClient
}

// resolveEnvironment maps the configured Azure cloud to an autorest Environment.
// Sovereign/edge clouds (Azure Stack, US Government Secret/IL6) have no built-in
// endpoint table, so their endpoints are resolved from the ARM metadata endpoint
// URL rather than by name. That resolution performs a network call, so it is
// bounded by metadataLookupTimeout.
func resolveEnvironment(config Config) (azure.Environment, error) {
	switch config.Environment {
	case string(configv1.AzureUSSecCloud):
		if err := requireHTTPS(config.ARMEndpoint); err != nil {
			return azure.Environment{}, err
		}
		// autorest's EnvironmentFromURL indexes authentication.audiences[0]
		// without a length check, so a metadata response with no audiences
		// panics inside the SDK rather than returning an error. That panic
		// occurs in the goroutine environmentFromURL spawns, which would crash
		// the process and bypass NewProvider's error handling. Validate the
		// metadata ourselves first and reject the empty-audiences response.
		if err := validateMetadataAudiences(config.ARMEndpoint); err != nil {
			return azure.Environment{}, err
		}
		return environmentFromURL(config.ARMEndpoint)
	case string(configv1.AzureStackCloud):
		return environmentFromURL(config.ARMEndpoint)
	default:
		return azure.EnvironmentFromName(config.Environment)
	}
}

func requireHTTPS(armEndpoint string) error {
	u, err := url.Parse(armEndpoint)
	if err != nil {
		return fmt.Errorf("invalid ARM endpoint %q: %w", armEndpoint, err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("ARM endpoint %q must use https", armEndpoint)
	}
	return nil
}

// metadataEndpoints is the subset of the ARM metadata response
// (<ARMEndpoint>/metadata/endpoints?api-version=1.0) that we validate before
// calling autorest's EnvironmentFromURL.
type metadataEndpoints struct {
	Authentication struct {
		Audiences []string `json:"audiences"`
	} `json:"authentication"`
}

// validateMetadataAudiences fetches the ARM metadata endpoint and returns an
// error if it reports no authentication audiences. autorest's EnvironmentFromURL
// would otherwise panic indexing audiences[0]. The lookup is bounded by
// metadataLookupTimeout and uses a context-aware request so it cannot block
// provider creation indefinitely.
func validateMetadataAudiences(armEndpoint string) error {
	ctx, cancel := context.WithTimeout(context.Background(), metadataLookupTimeout)
	defer cancel()

	metadataURL := fmt.Sprintf("%s/metadata/endpoints?api-version=1.0", strings.TrimSuffix(armEndpoint, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return fmt.Errorf("building ARM metadata request for %q: %w", armEndpoint, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching ARM metadata from %q: %w", armEndpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching ARM metadata from %q: unexpected status %s", armEndpoint, resp.Status)
	}
	var meta metadataEndpoints
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return fmt.Errorf("decoding ARM metadata from %q: %w", armEndpoint, err)
	}
	if len(meta.Authentication.Audiences) == 0 {
		return fmt.Errorf("ARM metadata from %q has no authentication audiences", armEndpoint)
	}
	return nil
}

// environmentFromURL resolves an autorest Environment from the ARM metadata
// endpoint, bounding the lookup with metadataLookupTimeout. autorest's
// EnvironmentFromURL is not context-aware and uses an unbounded HTTP client, so
// it is run in a goroutine and abandoned if the deadline elapses. The result
// channel is buffered so the abandoned goroutine does not leak.
func environmentFromURL(armEndpoint string) (azure.Environment, error) {
	ctx, cancel := context.WithTimeout(context.Background(), metadataLookupTimeout)
	defer cancel()

	type result struct {
		env azure.Environment
		err error
	}
	ch := make(chan result, 1)
	go func() {
		env, err := azure.EnvironmentFromURL(armEndpoint)
		ch <- result{env: env, err: err}
	}()

	select {
	case r := <-ch:
		return r.env, r.err
	case <-ctx.Done():
		return azure.Environment{}, fmt.Errorf("timed out after %s resolving cloud environment from ARM endpoint %q: %w", metadataLookupTimeout, armEndpoint, ctx.Err())
	}
}

// NewProvider creates a new dns.Provider for Azure. It only supports DNSRecords with
// type A.
func NewProvider(config Config) (dns.Provider, error) {
	env, err := resolveEnvironment(config)
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
