package common

import (
	"fmt"
	"regexp"

	"github.com/IBM/networking-go-sdk/dnsrecordsv1"
	"github.com/IBM/networking-go-sdk/dnssvcsv1"
	configv1 "github.com/openshift/api/config/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"

	iov1 "github.com/openshift/api/operatoringress/v1"
)

const (
	// defaultIamTokenServerEndpoint is the default endpoint for the IAM service,
	// originally defined in https://github.com/IBM/go-sdk-core/blob/3466fcd6ba1cb997971b0db8219ca06640463f48/core/iam_authenticator.go#L92
	defaultIamTokenServerEndpoint = "https://iam.cloud.ibm.com"
)

var (
	// IBMResourceCRNRegexp is a regular expression of IBM resource CRNs
	// See: https://cloud.ibm.com/docs/account?topic=account-crn
	IBMResourceCRNRegexp = regexp.MustCompile(`^crn:v[0-9]:(?P<cloudName>[^:]*):(?P<cloudType>[^:]*):(?P<serviceName>[^:]*):(?P<location>[^:]*):(?P<scope>[^:]*):(?P<guid>[^:]*):(?P<resourceType>[^:]*):(?P<resourceID>[^:]*)$`)
)

// ServiceEndpointOverrides can be used to override the service endpoints.
// To determine which URLs to use, client code should always use the Get... functions, which return the default URL when no override is in place for a particular endpoint
// or the overridden URL if it's set for that endpoint. This struct allows overrides to be set/cleared easily during runtime.
type ServiceEndpointOverrides struct {
	// CIS is the URL override for the CIS service endpoint. If this is blank, the default service endpoint is used.
	CIS string
	// DNS is the URL override for the DNS service endpoint. If this is blank, the default service endpoint is used.
	DNS string
	// IAM is the URL override for the IAM service endpoint. If this is blank, the default service endpoint is used.
	IAM string
}

func (s ServiceEndpointOverrides) GetCISEndpoint() string {
	if s.CIS != "" {
		return s.CIS
	}
	return dnsrecordsv1.DefaultServiceURL
}

func (s ServiceEndpointOverrides) GetDNSEndpoint() string {
	if s.DNS != "" {
		return s.DNS
	}
	return dnssvcsv1.DefaultServiceURL
}

func (s ServiceEndpointOverrides) GetIAMEndpoint() string {
	if s.IAM != "" {
		return s.IAM
	}
	return defaultIamTokenServerEndpoint
}

// Config holds common configuration of IBM DNS providers.
type Config struct {
	// APIKey is the IBM Cloud API key that the provider will use.
	APIKey string
	// InstanceID is GUID in case of dns svcs and CRN in case of cis.
	InstanceID string
	// UserAgent is the user-agent identifier that the client will use in
	// HTTP requests to the IBM Cloud API.
	UserAgent string
	// Zones is a list of DNS zones in which the DNS provider manages DNS records.
	Zones []string
	// ServiceEndpointOverrides stores overrides for certain services.
	ServiceEndpointOverrides ServiceEndpointOverrides
}

// ValidateInputDNSData validates the given record and zone.
func ValidateInputDNSData(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	var errs []error
	if record == nil {
		errs = append(errs, fmt.Errorf("validateInputDNSData: dns record is nil"))
	} else {
		if len(record.Spec.DNSName) == 0 {
			errs = append(errs, fmt.Errorf("validateInputDNSData: dns record name is empty"))
		}
		if len(record.Spec.RecordType) == 0 {
			errs = append(errs, fmt.Errorf("validateInputDNSData: dns record type is empty"))
		}
		if len(record.Spec.Targets) == 0 {
			errs = append(errs, fmt.Errorf("validateInputDNSData: dns record content is empty"))
		}
	}
	if len(zone.ID) == 0 {
		errs = append(errs, fmt.Errorf("validateInputDNSData: dns zone id is empty"))
	}
	return kerrors.NewAggregate(errs)
}
