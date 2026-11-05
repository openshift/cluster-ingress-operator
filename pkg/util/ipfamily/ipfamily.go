// Package ipfamily provides helpers for reading the cluster's declared IP
// family configuration.
//
// The IP family is chosen at install time and recorded in the
// Infrastructure's PlatformStatus. It does not change for the lifetime of a
// running operator, so it -- not the set of addresses that happens to be
// present in a load balancer Service's status on any given reconcile -- is the
// source of truth for which DNS record types a cluster expects.
package ipfamily

import (
	configv1 "github.com/openshift/api/config/v1"
)

// ForPlatform returns the IP family declared for the cluster, or the empty
// string if the platform does not report one. Only AWS and Azure currently
// carry an IPFamily field in their platform status.
func ForPlatform(status *configv1.PlatformStatus) configv1.IPFamilyType {
	if status == nil {
		return ""
	}
	switch status.Type {
	case configv1.AWSPlatformType:
		if status.AWS != nil {
			return status.AWS.IPFamily
		}
	case configv1.AzurePlatformType:
		if status.Azure != nil {
			return status.Azure.IPFamily
		}
	}
	return ""
}

// IsDualStack returns true if the given IP family describes a dual-stack
// cluster, in either primary ordering.
func IsDualStack(ipFamily configv1.IPFamilyType) bool {
	return ipFamily == configv1.DualStackIPv4Primary || ipFamily == configv1.DualStackIPv6Primary
}

// PrimaryIsIPv6 returns true if IPv6 is the primary family. It is only
// meaningful for a dual-stack IP family.
func PrimaryIsIPv6(ipFamily configv1.IPFamilyType) bool {
	return ipFamily == configv1.DualStackIPv6Primary
}
