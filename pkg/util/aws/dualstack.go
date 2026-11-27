package aws

import (
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/util/ipfamily"
)

// IsDualStack returns true if the given IPFamilyType indicates a dual-stack
// configuration.
// This is a wrapper around the common ipfamily.IsDualStack function for
// backward compatibility.
func IsDualStack(ipFamily configv1.IPFamilyType) bool {
	return ipfamily.IsDualStack(ipFamily)
}
