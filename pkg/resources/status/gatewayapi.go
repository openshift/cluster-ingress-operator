package status

import (
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	corev1 "k8s.io/api/core/v1"
	condutils "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// ComputeGatewayAPIDNSStatus will update inplace DNSRecord conditions for GatewayAPI,
// using the same logic of Ingress Controller condition status.
// This function is a wrapper for the IngressController one, but deals with
// metav1.Condition and also inplace replacements as Gateway API is capable of doing
// condition merge
// It deals with both DNSReady conditions on the Gateway level and on the listener level
func ComputeGatewayAPIDNSStatus(dnsConfig *configv1.DNS,
	generation int64,
	gwstatus *gatewayapiv1.GatewayStatus,
	listenerToHostname map[gatewayapiv1.SectionName]gatewayapiv1.Hostname,
	hostnameToDNSRecord map[string]*iov1.DNSRecord) {
	// During the fetch of dnsRecord, it can come back as an empty structure instead
	// of a null resource. In this case, we turn it into "null" again to keep
	// the compatibility with ComputeLoadBalancerStatus

	if gwstatus == nil || listenerToHostname == nil || hostnameToDNSRecord == nil {
		return
	}

	gatewayDNSStatus := metav1.Condition{
		Type:               operatorv1.DNSReadyIngressConditionType,
		Status:             metav1.ConditionStatus(operatorv1.ConditionTrue),
		Reason:             "AllListenersReady",
		Message:            "All the listeners have DNS ready",
		ObservedGeneration: generation,
	}

	hasFailedDNS := false
	hasDNSonListener := false

	for i, listener := range gwstatus.Listeners {
		hostname, listenerHasHostname := listenerToHostname[listener.Name]
		// if there is no hostname, we just remove any condition from the listener
		if !listenerHasHostname {
			condutils.RemoveStatusCondition(&gwstatus.Listeners[i].Conditions, operatorv1.DNSReadyIngressConditionType)
			continue
		}

		// We don't check it here because we deal with null records later on ComputeDNSStatus
		dnsRecord := hostnameToDNSRecord[string(hostname)]
		if dnsRecord != nil && dnsRecord.Name == "" {
			dnsRecord = nil
		}

		ingressConditions := ComputeDNSStatus(nil, dnsRecord, nil, dnsConfig, true)
		for _, condition := range ingressConditions {
			// In case at least one DNSReady is not true, we must add the .status.conditions DNSReady
			if condition.Status != operatorv1.ConditionTrue {
				hasFailedDNS = true
			}
			gwCondition := metav1.Condition{
				Type:               condition.Type,
				Status:             metav1.ConditionStatus(condition.Status),
				Reason:             condition.Reason,
				Message:            condition.Message,
				ObservedGeneration: generation,
			}
			// once we set the first DNSReady, we can add a DNSReady, otherwise we
			// must also remove the condition from main gateway .status.conditions
			hasDNSonListener = true
			condutils.SetStatusCondition(&gwstatus.Listeners[i].Conditions, gwCondition)
		}
	}

	if hasDNSonListener {
		if hasFailedDNS {
			gatewayDNSStatus.Status = metav1.ConditionStatus(operatorv1.ConditionFalse)
			gatewayDNSStatus.Reason = "SomeListenersNotReady"
			gatewayDNSStatus.Message = "One or more listeners have DNS provisioning issues"
		}
		condutils.SetStatusCondition(&gwstatus.Conditions, gatewayDNSStatus)
	} else {
		condutils.RemoveStatusCondition(&gwstatus.Conditions, operatorv1.DNSReadyIngressConditionType)
	}

}

// ComputeGatewayAPILoadBalancerStatus will update inplace LoadBalancer conditions
// for GatewayAPI, using the same logic of Ingress Controller condition status.
// This function is a wrapper for the IngressController one, but deals with
// metav1.Condition and also inplace replacements as Gateway API is capable of doing
// condition merge
func ComputeGatewayAPILoadBalancerStatus(service *corev1.Service, operandEvents []corev1.Event, generation int64, existingConditions *[]metav1.Condition) {
	if existingConditions == nil {
		return
	}
	// During the fetch of service, it can come back as an empty structure instead
	// of a null resource. In this case, we turn it into "null" again to keep
	// the compatibility with ComputeLoadBalancerStatus
	if service != nil && service.Name == "" {
		service = nil
	}
	ingressConditions := ComputeLoadBalancerStatus(nil, service, operandEvents, true)
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
