package private

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnssvcsv1"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	common "github.com/openshift/cluster-ingress-operator/pkg/dns/ibm"
	dnsclient "github.com/openshift/cluster-ingress-operator/pkg/dns/ibm/private/client"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	iov1 "github.com/openshift/api/operatoringress/v1"
)

var (
	_   dns.Provider = &Provider{}
	log              = logf.Logger.WithName("dns")

	// validTTLs is a list of TTLs that are permitted by IBM Cloud DNS Services.
	validTTLs = sets.NewInt64(1, 60, 120, 300, 600, 900, 1800, 3600, 7200, 18000, 43200)
)

// defaultDNSSVCSRecordTTL is the default TTL used when a DNS record
// does not specify a valid TTL.
const defaultDNSSVCSRecordTTL = int64(120)

type Provider struct {
	dnsService dnsclient.DnsClient
	config     common.Config
}

func NewProvider(config common.Config) (*Provider, error) {
	if len(config.Zones) < 1 {
		return nil, fmt.Errorf("missing zone data")
	}

	provider := &Provider{}

	authenticator := &core.IamAuthenticator{
		ApiKey: config.APIKey,
		URL:    config.ServiceEndpointOverrides.GetIAMEndpoint(),
	}

	options := &dnssvcsv1.DnsSvcsV1Options{
		Authenticator: authenticator,
		URL:           config.ServiceEndpointOverrides.GetDNSEndpoint(),
	}

	dnsService, err := dnssvcsv1.NewDnsSvcsV1(options)
	if err != nil {
		return nil, fmt.Errorf("failed to create a new IBM Cloud DNS Services instance: %w", err)
	}
	dnsService.EnableRetries(3, 5*time.Second)
	dnsService.Service.SetUserAgent(config.UserAgent)

	provider.dnsService = dnsService
	provider.config.InstanceID = config.InstanceID
	provider.config.Zones = config.Zones

	if err := validateDNSServices(provider); err != nil {
		return nil, fmt.Errorf("failed to validate IBM Cloud DNS Services: %w", err)
	}
	log.Info("successfully validated IBM Cloud DNS Services")

	return provider, nil
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

	listOpt := p.dnsService.NewListResourceRecordsOptions(p.config.InstanceID, zone.ID)
	// DNS records may have an ending "." character in the DNS name.  For
	// example, the ingress operator's ingress controller adds a trailing
	// "." when it creates a wildcard DNS record.
	dnsName := strings.TrimSuffix(record.Spec.DNSName, ".")

	result, response, err := p.dnsService.ListResourceRecords(listOpt)
	if err != nil {
		if response == nil || response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("delete: failed to list the dns record: %w", err)
		}
	}
	if result == nil {
		return fmt.Errorf("delete: ListResourceRecords returned nil as result")
	}

	for _, resourceRecord := range result.ResourceRecords {
		var resourceRecordTarget string
		rData, ok := resourceRecord.Rdata.(map[string]interface{})
		if !ok {
			return fmt.Errorf("delete: failed to get resource data: %v", resourceRecord.Rdata)
		}
		if resourceRecord.Type == nil {
			return fmt.Errorf("delete: failed to get resource type, resourceRecord.Type is nil")
		}
		switch *resourceRecord.Type {
		case string(iov1.CNAMERecordType):
			if value, ok := rData["cname"].(string); ok {
				resourceRecordTarget = value
			} else {
				return fmt.Errorf("delete: resource data has record with unknown rData cname type: %T", rData["cname"])
			}
		case string(iov1.ARecordType):
			if value, ok := rData["ip"].(string); ok {
				resourceRecordTarget = value
			} else {
				return fmt.Errorf("delete: resource data has record with unknown rData ip type:  %T", rData["ip"])
			}
		default:
			return fmt.Errorf("delete: resource data has record with unknown type: %v", *resourceRecord.Type)
		}
		// While creating DNS records with multiple targets is unsupported, we still
		// iterate through all targets during deletion to be extra cautious.
		for _, target := range record.Spec.Targets {
			if *resourceRecord.Name == dnsName {
				if resourceRecordTarget != target {
					log.Info("delete: ignoring record with matching name but unexpected target", "record", record, "target", resourceRecordTarget)
					continue
				}
				if resourceRecord.ID == nil {
					return fmt.Errorf("delete: record id is nil")
				}
				delOpt := p.dnsService.NewDeleteResourceRecordOptions(p.config.InstanceID, zone.ID, *resourceRecord.ID)
				delResponse, err := p.dnsService.DeleteResourceRecord(delOpt)
				if err != nil {
					if delResponse == nil || delResponse.StatusCode != http.StatusNotFound {
						return fmt.Errorf("delete: failed to delete the dns record: %w", err)
					}
				}
				if delResponse != nil && delResponse.StatusCode != http.StatusNotFound {
					log.Info("deleted DNS record", "record", record, "zone", zone, "target", target)
				}
			}
		}
	}

	return nil
}

// validateDNSServices validates that provider clients can communicate with
// associated API endpoints by having each client list zones of the instance.
func validateDNSServices(provider *Provider) error {
	var errs []error

	for _, zoneID := range provider.config.Zones {
		getDnszoneOptions := provider.dnsService.NewGetDnszoneOptions(
			provider.config.InstanceID,
			zoneID)

		_, _, err := provider.dnsService.GetDnszone(getDnszoneOptions)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to get dns zone: %w", err))
		}

		listOpt := provider.dnsService.NewListResourceRecordsOptions(provider.config.InstanceID, zoneID)
		_, _, err = provider.dnsService.ListResourceRecords(listOpt)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to list dns records: %w", err))
		}
	}
	return kerrors.NewAggregate(errs)
}

// createOrUpdateDNSRecord has the common logic for the Ensure and Update methods.
func (p *Provider) createOrUpdateDNSRecord(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	if err := common.ValidateInputDNSData(record, zone); err != nil {
		return fmt.Errorf("createOrUpdateDNSRecord: invalid dns input data: %w", err)
	}

	listOpt := p.dnsService.NewListResourceRecordsOptions(p.config.InstanceID, zone.ID)
	// DNS records may have an ending "." character in the DNS name.  For
	// example, the ingress operator's ingress controller adds a trailing
	// "." when it creates a wildcard DNS record.
	dnsName := strings.TrimSuffix(record.Spec.DNSName, ".")

	if !validTTLs.Has(record.Spec.RecordTTL) {
		log.Info("Warning: TTL must be one of [1 60 120 300 600 900 1800 3600 7200 18000 43200]. RecordTTL set to default", "default DSNSVCS record TTL", defaultDNSSVCSRecordTTL)
		record.Spec.RecordTTL = defaultDNSSVCSRecordTTL
	}
	// We only support one target, warn the user.
	if len(record.Spec.Targets) > 1 {
		log.Info("Warning: Only one DNSRecord target is supported. Additional targets will be ignored.", "targets", record.Spec.Targets)
	}

	listResult, response, err := p.dnsService.ListResourceRecords(listOpt)
	if err != nil {
		if response == nil || response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("createOrUpdateDNSRecord: failed to list the dns record: %w", err)
		}
	}
	if listResult == nil {
		return fmt.Errorf("createOrUpdateDNSRecord: ListResourceRecords returned nil as result")
	}

	target := record.Spec.Targets[0]
	updated := false
	for _, resourceRecord := range listResult.ResourceRecords {
		if *resourceRecord.Name == dnsName {
			if resourceRecord.ID == nil {
				return fmt.Errorf("createOrUpdateDNSRecord: record id is nil")
			}
			updateOpt := p.dnsService.NewUpdateResourceRecordOptions(p.config.InstanceID, zone.ID, *resourceRecord.ID)
			updateOpt.SetName(dnsName)

			if resourceRecord.Type == nil {
				return fmt.Errorf("createOrUpdateDNSRecord: failed to get resource type, resourceRecord.Type is nil")
			}

			// TODO DNS record update should handle the case where we have an A record and want a CNAME record or vice versa
			switch *resourceRecord.Type {
			case string(iov1.CNAMERecordType):
				inputRData, err := p.dnsService.NewResourceRecordUpdateInputRdataRdataCnameRecord(target)
				if err != nil {
					return fmt.Errorf("createOrUpdateDNSRecord: failed to create CNAME inputRData for the dns record: %w", err)
				}
				updateOpt.SetRdata(inputRData)
			case string(iov1.ARecordType):
				inputRData, err := p.dnsService.NewResourceRecordUpdateInputRdataRdataARecord(target)
				if err != nil {
					return fmt.Errorf("createOrUpdateDNSRecord: failed to create A inputRData for the dns record: %w", err)
				}
				updateOpt.SetRdata(inputRData)
			default:
				return fmt.Errorf("createOrUpdateDNSRecord: resource data has record with unknown type: %v", *resourceRecord.Type)
			}
			updateOpt.SetTTL(record.Spec.RecordTTL)
			_, _, err := p.dnsService.UpdateResourceRecord(updateOpt)
			if err != nil {
				return fmt.Errorf("createOrUpdateDNSRecord: failed to update the dns record: %w", err)
			}
			updated = true
			log.Info("updated DNS record", "record", record.Spec, "zone", zone, "target", target)
		}
	}
	if !updated {
		createOpt := p.dnsService.NewCreateResourceRecordOptions(p.config.InstanceID, zone.ID)
		createOpt.SetName(dnsName)
		createOpt.SetType(string(record.Spec.RecordType))

		switch record.Spec.RecordType {
		case iov1.CNAMERecordType:
			inputRData, err := p.dnsService.NewResourceRecordInputRdataRdataCnameRecord(target)
			if err != nil {
				return fmt.Errorf("createOrUpdateDNSRecord: failed to create CNAME inputRData for the dns record: %w", err)
			}
			createOpt.SetRdata(inputRData)
		case iov1.ARecordType:
			inputRData, err := p.dnsService.NewResourceRecordInputRdataRdataARecord(target)
			if err != nil {
				return fmt.Errorf("createOrUpdateDNSRecord: failed to create A inputRData for the dns record: %w", err)
			}
			createOpt.SetRdata(inputRData)
		default:
			return fmt.Errorf("createOrUpdateDNSRecord: resource data has record with unknown type: %v", record.Spec.RecordType)

		}
		createOpt.SetTTL(record.Spec.RecordTTL)
		_, _, err := p.dnsService.CreateResourceRecord(createOpt)
		if err != nil {
			return fmt.Errorf("createOrUpdateDNSRecord: failed to create the dns record: %w", err)
		}
		log.Info("created DNS record", "record", record.Spec, "zone", zone, "target", target)
	}
	return nil
}
