package common

import (
	"fmt"
	"regexp"

	configv1 "github.com/openshift/api/config/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"

	iov1 "github.com/openshift/api/operatoringress/v1"
)

var (
	// IBMResourceCRNRegexp is a regular expression of IBM resource CRNs
	// See: https://cloud.ibm.com/docs/account?topic=account-crn
	IBMResourceCRNRegexp = regexp.MustCompile(`^crn:v[0-9]:(?P<cloudName>[^:]*):(?P<cloudType>[^:]*):(?P<serviceName>[^:]*):(?P<location>[^:]*):(?P<scope>[^:]*):(?P<guid>[^:]*):(?P<resourceType>[^:]*):(?P<resourceID>[^:]*)$`)
)

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
