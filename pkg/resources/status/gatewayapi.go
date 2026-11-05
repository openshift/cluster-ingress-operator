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
	hostnameToDNSRecord map[string]*iov1.DNSRecord, attachedListeners ...[]metav1.Condition) {
	// During the fetch of dnsRecord, it can come back as an empty structure instead
	// of a null resource. In this case, we turn it into "null" again to keep
	// the compatibility with ComputeLoadBalancerStatus

	if gwstatus == nil || listenerToHostname == nil || hostnameToDNSRecord == nil {
		return
	}

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

		for _, gwCondition := range gatewayAPIDNSConditions(dnsConfig, dnsRecord, generation) {
			condutils.SetStatusCondition(&gwstatus.Listeners[i].Conditions, gwCondition)
		}
	}

	for _, listener := range gwstatus.Listeners {
		attachedListeners = append(attachedListeners, listener.Conditions)
	}
	AggregateGatewayAPIDNSStatus(&gwstatus.Conditions, generation, attachedListeners...)
}

func gatewayAPIDNSConditions(dnsConfig *configv1.DNS, dnsRecord *iov1.DNSRecord, generation int64) []metav1.Condition {
	if dnsRecord != nil && dnsRecord.Name == "" {
		dnsRecord = nil
	}
	var conditions []metav1.Condition
	for _, condition := range ComputeDNSStatus(nil, dnsRecord, nil, dnsConfig, true) {
		conditions = append(conditions, metav1.Condition{
			Type: condition.Type, Status: metav1.ConditionStatus(condition.Status),
			Reason: condition.Reason, Message: condition.Message, ObservedGeneration: generation,
		})
	}
	return conditions
}

// ComputeListenerSetDNSStatus changes only DNSReady, leaving Istio's status intact.
func ComputeListenerSetDNSStatus(dnsConfig *configv1.DNS, ls *gatewayapiv1.ListenerSet, hostnames map[gatewayapiv1.SectionName]gatewayapiv1.Hostname, records map[string]*iov1.DNSRecord) {
	var listenerConditions [][]metav1.Condition
	for index := range ls.Status.Listeners {
		listener := &ls.Status.Listeners[index]
		if hostname, found := hostnames[listener.Name]; found {
			for _, condition := range gatewayAPIDNSConditions(dnsConfig, records[string(hostname)], ls.Generation) {
				condutils.SetStatusCondition(&listener.Conditions, condition)
			}
		} else {
			condutils.RemoveStatusCondition(&listener.Conditions, operatorv1.DNSReadyIngressConditionType)
		}
		listenerConditions = append(listenerConditions, listener.Conditions)
	}
	AggregateGatewayAPIDNSStatus(&ls.Status.Conditions, ls.Generation, listenerConditions...)
}

// AggregateGatewayAPIDNSStatus includes only listeners with managed DNS status.
func AggregateGatewayAPIDNSStatus(conditions *[]metav1.Condition, generation int64, listeners ...[]metav1.Condition) {
	aggregate := metav1.Condition{Type: operatorv1.DNSReadyIngressConditionType, Status: metav1.ConditionTrue, Reason: "AllListenersReady", Message: "All the listeners have DNS ready", ObservedGeneration: generation}
	hasDNS := false
	for _, listener := range listeners {
		condition := condutils.FindStatusCondition(listener, operatorv1.DNSReadyIngressConditionType)
		if condition == nil {
			continue
		}
		hasDNS = true
		if condition.Status != metav1.ConditionTrue {
			aggregate.Status, aggregate.Reason, aggregate.Message = metav1.ConditionFalse, "SomeListenersNotReady", "One or more listeners have DNS provisioning issues"
		}
	}
	if hasDNS {
		condutils.SetStatusCondition(conditions, aggregate)
	} else {
		condutils.RemoveStatusCondition(conditions, aggregate.Type)
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
