package status

import (
	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	corev1 "k8s.io/api/core/v1"
	condutils "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ComputeGatewayAPIDNSStatus will update inplace DNSRecord conditions for GatewayAPI,
// using the same logic of Ingress Controller condition status.
// This function is a wrapper for the IngressController one, but deals with
// metav1.Condition and also inplace replacements as Gateway API is capable of doing
// condition merge
func ComputeGatewayAPIDNSStatus(dnsRecord *iov1.DNSRecord, dnsConfig *configv1.DNS, generation int64, existingConditions *[]metav1.Condition) {
	// During the fetch of dnsRecord, it can come back as an empty structure instead
	// of a null resource. In this case, we turn it into "null" again to keep
	// the compatibility with ComputeLoadBalancerStatus
	if dnsRecord != nil && dnsRecord.Name == "" {
		dnsRecord = nil
	}
	ingressConditions := ComputeDNSStatus(nil, dnsRecord, nil, dnsConfig)
	for _, condition := range ingressConditions {
		gwCondition := metav1.Condition{
			Type:               condition.Type,
			Status:             metav1.ConditionStatus(condition.Status),
			Reason:             condition.Reason,
			Message:            condition.Message,
			ObservedGeneration: generation,
		}
		condutils.SetStatusCondition(existingConditions, gwCondition)
	}
}

// ComputeGatewayAPILoadBalancerStatus will update inplace LoadBalancer conditions
// for GatewayAPI, using the same logic of Ingress Controller condition status.
// This function is a wrapper for the IngressController one, but deals with
// metav1.Condition and also inplace replacements as Gateway API is capable of doing
// condition merge
func ComputeGatewayAPILoadBalancerStatus(service *corev1.Service, operandEvents []corev1.Event, generation int64, existingConditions *[]metav1.Condition) {
	// During the fetch of service, it can come back as an empty structure instead
	// of a null resource. In this case, we turn it into "null" again to keep
	// the compatibility with ComputeLoadBalancerStatus
	if service != nil && service.Name == "" {
		service = nil
	}
	ingressConditions := ComputeLoadBalancerStatus(nil, service, operandEvents)
	for _, condition := range ingressConditions {
		gwCondition := metav1.Condition{
			Type:               condition.Type,
			Status:             metav1.ConditionStatus(condition.Status),
			Reason:             condition.Reason,
			Message:            condition.Message,
			ObservedGeneration: generation,
		}
		condutils.SetStatusCondition(existingConditions, gwCondition)
	}
}
