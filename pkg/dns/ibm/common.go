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
	// CISCustomEndpointName is the key used to identify the CIS service in ServiceEndpoints
	CISCustomEndpointName = "cis"
	// DNSCustomEndpointName is the key used to identify the DNS service in ServiceEndpoints
	DNSCustomEndpointName = "dns"
	// IAMCustomEndpointName is the key used to identify the IAM service in ServiceEndpoints
	IAMCustomEndpointName = "iam"
	// default iam server url
	defaultIamTokenServerEndpoint = "https://iam.cloud.ibm.com"
)

var (
	// IBMResourceCRNRegexp is a regular expression of IBM resource CRNs
	// See: https://cloud.ibm.com/docs/account?topic=account-crn
	IBMResourceCRNRegexp = regexp.MustCompile(`^crn:v[0-9]:(?P<cloudName>[^:]*):(?P<cloudType>[^:]*):(?P<serviceName>[^:]*):(?P<location>[^:]*):(?P<scope>[^:]*):(?P<guid>[^:]*):(?P<resourceType>[^:]*):(?P<resourceID>[^:]*)$`)
)

// ServiceEndpoint stores the configuration of a custom url to
// override default IBM Service API endpoints.
type ServiceEndpoint struct {
	// name is the name of the IBM service.
	// For example
	// IAM - https://cloud.ibm.com/apidocs/iam-identity-token-api
	Name string
	// url is fully qualified URI with scheme https, that overrides the default generated
	// endpoint for a client.
	URL string
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
	// ServiceEndpoints is the list of Custom API endpoints to use for Provider clients.
	ServiceEndpoints []ServiceEndpoint
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

// GetServiceEndpointURL return the url of serviceType from endpoints if exist or else returns the default url
func GetServiceEndpointURL(endpoints []ServiceEndpoint, serviceType string) (string, error) {
	for _, ep := range endpoints {
		if ep.Name == serviceType {
			return ep.URL, nil
		}
	}
	switch serviceType {
	case CISCustomEndpointName:
		return dnsrecordsv1.DefaultServiceURL, nil
	case DNSCustomEndpointName:
		return dnssvcsv1.DefaultServiceURL, nil
	case IAMCustomEndpointName:
		return defaultIamTokenServerEndpoint, nil
	}
	return "", fmt.Errorf("unknown serviceType %s", serviceType)
}
