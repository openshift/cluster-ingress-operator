package managementmode

import (
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
)

// SupportedCRDProfile describes the Gateway API CRD bundle supported by this CIO release.
type SupportedCRDProfile struct {
	BundleVersion string
	Channel       string
	CRDNames      []string
}

// SupportedProfile returns the CRD profile embedded in this operator release.
func SupportedProfile() SupportedCRDProfile {
	ref := manifests.GatewayClassCRD()
	names := []string{
		manifests.GatewayClassCRD().Name,
		manifests.GatewayCRD().Name,
		manifests.GRPCRouteCRD().Name,
		manifests.HTTPRouteCRD().Name,
		manifests.ReferenceGrantCRD().Name,
		manifests.BackendTLSPolicyCRD().Name,
	}
	bundleVersion := ""
	channel := ""
	if ref.Annotations != nil {
		bundleVersion = ref.Annotations[AnnotationBundleVersion]
		channel = ref.Annotations[AnnotationChannel]
	}
	return SupportedCRDProfile{
		BundleVersion: bundleVersion,
		Channel:       channel,
		CRDNames:      names,
	}
}
