package aws

import configv1 "github.com/openshift/api/config/v1"

// IsDualStack returns true if the given IPFamilyType indicates a dual-stack
// configuration.
func IsDualStack(ipFamily configv1.IPFamilyType) bool {
	return ipFamily == configv1.DualStackIPv4Primary || ipFamily == configv1.DualStackIPv6Primary
}
