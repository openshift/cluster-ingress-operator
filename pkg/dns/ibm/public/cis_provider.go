package public

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnsrecordsv1"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	common "github.com/openshift/cluster-ingress-operator/pkg/dns/ibm"
	dnsclient "github.com/openshift/cluster-ingress-operator/pkg/dns/ibm/public/client"

	iov1 "github.com/openshift/api/operatoringress/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
)

var (
	_   dns.Provider = &Provider{}
	log              = logf.Logger.WithName("dns")
)

// defaultCISRecordTTL is the default TTL used when a DNS record
// does not specify a valid TTL.
const defaultCISRecordTTL = int64(120)

type Provider struct {
	// dnsServices maps DNS zones to CIS clients.
	dnsServices map[string]dnsclient.DnsClient
}

func NewProvider(config common.Config) (*Provider, error) {
	if len(config.Zones) < 1 {
		return nil, fmt.Errorf("IBM CIS DNS: missing zone data")
	}
	authenticator := &core.IamAuthenticator{
		ApiKey: config.APIKey,
		URL:    config.ServiceEndpointOverrides.GetIAMEndpoint(),
	}
	provider := &Provider{}

	provider.dnsServices = make(map[string]dnsclient.DnsClient)

	for _, zone := range config.Zones {
		options := &dnsrecordsv1.DnsRecordsV1Options{
			Authenticator:  authenticator,
			URL:            config.ServiceEndpointOverrides.GetCISEndpoint(),
			Crn:            &config.InstanceID,
			ZoneIdentifier: &zone,
		}

		dnsService, err := dnsrecordsv1.NewDnsRecordsV1(options)
		if err != nil {
			return nil, fmt.Errorf("failed to create a new IBM CIS DNS instance: %w", err)
		}
		dnsService.EnableRetries(3, 5*time.Second)
		dnsService.Service.SetUserAgent(config.UserAgent)

		provider.dnsServices[zone] = dnsService
	}

	if err := validateDNSServices(provider); err != nil {
		return nil, fmt.Errorf("failed to validate IBM CIS DNS: %w", err)
	}
	log.Info("successfully validated IBM CIS DNS")
	return provider, nil
}

// validateDNSServices validates that provider clients can communicate with
// associated API endpoints by having each client make a get DNS records call.
func validateDNSServices(provider *Provider) error {
	var errs []error
	maxItems := int64(1)
	for _, dnsService := range provider.dnsServices {
		opt := dnsService.NewListAllDnsRecordsOptions()
		opt.PerPage = &maxItems
		if _, _, err := dnsService.ListAllDnsRecords(opt); err != nil {
			errs = append(errs, fmt.Errorf("failed to get dns records: %w", err))
		}
	}
	return kerrors.NewAggregate(errs)
}

func (p *Provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return p.createOrUpdateDNSRecord(record, zone)
}

func (p *Provider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return p.createOrUpdateDNSRecord(record, zone)
}

func (p *Provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	if err := common.ValidateInputDNSData(record, zone); err != nil {
		return fmt.Errorf("delete: invalid dns input data: %w", err)
	}
	dnsService, ok := p.dnsServices[zone.ID]
	if !ok {
		return fmt.Errorf("delete: unknown zone: %v", zone.ID)
	}
	opt := dnsService.NewListAllDnsRecordsOptions()
	opt.SetType(string(record.Spec.RecordType))
	// DNS records may have an ending "." character in the DNS name.  For
	// example, the ingress operator's ingress controller adds a trailing
	// "." when it creates a wildcard DNS record.
	dnsName := strings.TrimSuffix(record.Spec.DNSName, ".")
	opt.SetName(dnsName)
	// While creating DNS records with multiple targets is unsupported, we still
	// iterate through all targets during deletion to be extra cautious.
	for _, target := range record.Spec.Targets {
		opt.SetContent(target)
		result, response, err := dnsService.ListAllDnsRecords(opt)
		if err != nil {
			if response == nil || response.StatusCode != http.StatusNotFound {
				return fmt.Errorf("delete: failed to list the dns record: %w", err)
			}
			continue
		}
		if result == nil || result.Result == nil {
			return fmt.Errorf("delete: ListAllDnsRecords returned nil as result")
		}
		for _, resultData := range result.Result {
			if resultData.ID == nil {
				return fmt.Errorf("delete: record id is nil")
			}
			delOpt := dnsService.NewDeleteDnsRecordOptions(*resultData.ID)
			_, delResponse, err := dnsService.DeleteDnsRecord(delOpt)
			if err != nil {
				if delResponse == nil || delResponse.StatusCode != http.StatusNotFound {
					return fmt.Errorf("delete: failed to delete the dns record: %w", err)
				}
			}
			if delResponse != nil && delResponse.StatusCode != http.StatusNotFound {
				log.Info("deleted DNS record", "record", record.Spec, "zone", zone, "target", target)
			}
		}
	}
	return nil
}

func (p *Provider) createOrUpdateDNSRecord(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	if err := common.ValidateInputDNSData(record, zone); err != nil {
		return fmt.Errorf("createOrUpdateDNSRecord: invalid dns input data: %w", err)
	}
	dnsService, ok := p.dnsServices[zone.ID]
	if !ok {
		return fmt.Errorf("createOrUpdateDNSRecord: unknown zone: %v", zone.ID)
	}

	// TTL must be between 120 and 2,147,483,647 seconds, or 1 for Automatic.
	if (record.Spec.RecordTTL > 1 && record.Spec.RecordTTL < 120) || record.Spec.RecordTTL == 0 {
		log.Info("Warning: TTL must be between 120 and 2,147,483,647 seconds, or 1 for Automatic. RecordTTL set to default", "default CIS record TTL", defaultCISRecordTTL)
		record.Spec.RecordTTL = defaultCISRecordTTL
	}
	// We only support one target, warn the user.
	if len(record.Spec.Targets) > 1 {
		log.Info("Warning: Only one DNSRecord target is supported. Additional targets will be ignored.", "targets", record.Spec.Targets)
	}

	listOpt := dnsService.NewListAllDnsRecordsOptions()
	listOpt.SetType(string(record.Spec.RecordType))
	// DNS records may have an ending "." character in the DNS name.  For
	// example, the ingress operator's ingress controller adds a trailing
	// "." when it creates a wildcard DNS record.
	dnsName := strings.TrimSuffix(record.Spec.DNSName, ".")
	listOpt.SetName(dnsName)

	result, response, err := dnsService.ListAllDnsRecords(listOpt)
	if err != nil {
		if response == nil || response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("createOrUpdateDNSRecord: failed to list the dns record: %w", err)
		}
	}
	if result == nil || result.Result == nil {
		return fmt.Errorf("createOrUpdateDNSRecord: ListAllDnsRecords returned nil as result")
	}

	target := record.Spec.Targets[0]
	if len(result.Result) == 0 {
		createOpt := dnsService.NewCreateDnsRecordOptions()
		createOpt.SetName(record.Spec.DNSName)
		createOpt.SetType(string(record.Spec.RecordType))
		createOpt.SetContent(target)
		createOpt.SetTTL(record.Spec.RecordTTL)
		_, _, err := dnsService.CreateDnsRecord(createOpt)
		if err != nil {
			return fmt.Errorf("createOrUpdateDNSRecord: failed to create the dns record: %w", err)
		}
		log.Info("created DNS record", "record", record.Spec, "zone", zone, "target", target)
	} else {
		if result.Result[0].ID == nil {
			return fmt.Errorf("createOrUpdateDNSRecord: record id is nil")
		}
		updateOpt := dnsService.NewUpdateDnsRecordOptions(*result.Result[0].ID)
		updateOpt.SetName(record.Spec.DNSName)
		updateOpt.SetType(string(record.Spec.RecordType))
		updateOpt.SetContent(target)
		updateOpt.SetTTL(record.Spec.RecordTTL)
		_, _, err := dnsService.UpdateDnsRecord(updateOpt)
		if err != nil {
			return fmt.Errorf("createOrUpdateDNSRecord: failed to update the dns record: %w", err)
		}
		log.Info("updated DNS record", "record", record.Spec, "zone", zone, "target", target)
	}

	return nil
}
