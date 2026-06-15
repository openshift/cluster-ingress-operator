package managementmode

import (
	"context"
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// CRDComplianceDetail holds per-CRD compliance assessment.
type CRDComplianceDetail struct {
	Name          string
	Present       bool
	BundleVersion string
	Channel       string
	VersionMatch  bool
	ChannelMatch  bool
}

// ComplianceResult is the outcome of assessing cluster Gateway API CRDs.
type ComplianceResult struct {
	Compliant       bool
	Profile         SupportedCRDProfile
	Details         []CRDComplianceDetail
	MissingCRDs     []string
	VersionMismatch []string
	ChannelMismatch []string
	PartialSet      bool
	Message         string
}

// AssessGatewayAPICRDCompliance compares installed Gateway API CRDs against the
// supported profile embedded in this operator release.
func AssessGatewayAPICRDCompliance(ctx context.Context, c client.Reader) (ComplianceResult, error) {
	profile := SupportedProfile()
	result := ComplianceResult{
		Profile: profile,
	}

	presentCount := 0
	for _, name := range profile.CRDNames {
		detail := CRDComplianceDetail{Name: name}
		crd := &apiextensionsv1.CustomResourceDefinition{}
		err := c.Get(ctx, client.ObjectKey{Name: name}, crd)
		if errors.IsNotFound(err) {
			result.MissingCRDs = append(result.MissingCRDs, name)
			result.Details = append(result.Details, detail)
			continue
		}
		if err != nil {
			return result, fmt.Errorf("failed to get CRD %s: %w", name, err)
		}

		detail.Present = true
		presentCount++
		if crd.Annotations != nil {
			detail.BundleVersion = crd.Annotations[AnnotationBundleVersion]
			detail.Channel = crd.Annotations[AnnotationChannel]
		}
		detail.VersionMatch = detail.BundleVersion == profile.BundleVersion
		detail.ChannelMatch = detail.Channel == profile.Channel
		if !detail.VersionMatch {
			result.VersionMismatch = append(result.VersionMismatch, name)
		}
		if !detail.ChannelMatch {
			result.ChannelMismatch = append(result.ChannelMismatch, name)
		}
		result.Details = append(result.Details, detail)
	}

	result.PartialSet = presentCount > 0 && presentCount < len(profile.CRDNames)
	result.Compliant = len(result.MissingCRDs) == 0 &&
		len(result.VersionMismatch) == 0 &&
		len(result.ChannelMismatch) == 0 &&
		!result.PartialSet
	result.Message = FormatComplianceConditionMessage(result)
	return result, nil
}

// FormatComplianceConditionMessage builds a human-readable message for the
// GatewayAPICRDsCompliant status condition.
func FormatComplianceConditionMessage(result ComplianceResult) string {
	if result.Compliant {
		return fmt.Sprintf("Gateway API CRDs match expected bundle-version %q and channel %q",
			result.Profile.BundleVersion, result.Profile.Channel)
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("expected bundle-version %q and channel %q",
		result.Profile.BundleVersion, result.Profile.Channel))
	if result.PartialSet {
		parts = append(parts, fmt.Sprintf("partial Gateway API CRD set (%d/%d present)",
			len(result.Details)-len(result.MissingCRDs), len(result.Profile.CRDNames)))
	}
	if len(result.MissingCRDs) > 0 {
		parts = append(parts, fmt.Sprintf("missing CRDs: %s", strings.Join(result.MissingCRDs, ", ")))
	}
	if len(result.VersionMismatch) > 0 {
		parts = append(parts, fmt.Sprintf("bundle-version mismatch on: %s (expected %q)",
			strings.Join(result.VersionMismatch, ", "), result.Profile.BundleVersion))
	}
	if len(result.ChannelMismatch) > 0 {
		parts = append(parts, fmt.Sprintf("channel mismatch on: %s (expected %q)",
			strings.Join(result.ChannelMismatch, ", "), result.Profile.Channel))
	}
	parts = append(parts, "obtain valid manifests from the gateway-api image in the OpenShift release payload or https://github.com/kubernetes-sigs/gateway-api/releases")
	return strings.Join(parts, "; ")
}

// ListExtraGatewayAPICRDNames returns CRD names in gateway.networking.k8s.io not in the managed set.
func ListExtraGatewayAPICRDNames(ctx context.Context, c client.Reader, managedNames map[string]struct{}) ([]string, error) {
	list := &apiextensionsv1.CustomResourceDefinitionList{}
	if err := c.List(ctx, list); err != nil {
		return nil, err
	}
	var extra []string
	for i := range list.Items {
		crd := &list.Items[i]
		if crd.Spec.Group != gatewayapiv1.GroupName {
			continue
		}
		if _, ok := managedNames[crd.Name]; ok {
			continue
		}
		extra = append(extra, crd.Name)
	}
	return extra, nil
}
