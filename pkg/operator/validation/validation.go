package validation

import (
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func ValidateIngressController(ic *operatorv1.IngressController) field.ErrorList {
	errs := apivalidation.ValidateObjectMeta(&ic.ObjectMeta, true, apivalidation.NameIsDNS1035Label, nil)

	if ic.Spec.Replicas != nil && *ic.Spec.Replicas < 1 {
		errs = append(errs, field.Invalid(field.NewPath("spec", "replicas"), ic.Spec.Replicas,
			"must be greater than 0"))
	}

	if len(ic.Spec.Domain) != 0 {
		if err := apivalidation.NameIsDNSSubdomain(ic.Spec.Domain, false); len(err) != 0 {
			errs = append(errs, field.Invalid(field.NewPath("spec", "domain"), ic.Spec.Domain,
				"domain must be an RFC 1123 DNS subdomain"))
		}
	}

	if ic.Spec.EndpointPublishingStrategy != nil {
		if ic.Spec.EndpointPublishingStrategy.Type == operatorv1.LoadBalancerServiceStrategyType ||
			ic.Spec.EndpointPublishingStrategy.Type == operatorv1.HostNetworkStrategyType ||
			ic.Spec.EndpointPublishingStrategy.Type == operatorv1.PrivateStrategyType {
			return errs
		} else {
			errs = append(errs, field.Invalid(field.NewPath("spec", "endpointPublishingStrategy", "type"),
				ic.Spec.EndpointPublishingStrategy.Type, fmt.Sprintf("type must be either %s, %s or %s",
					operatorv1.LoadBalancerServiceStrategyType, operatorv1.HostNetworkStrategyType, operatorv1.PrivateStrategyType)))
		}
	}

	return errs
}
