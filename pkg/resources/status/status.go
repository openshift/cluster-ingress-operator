package status

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	corev1 "k8s.io/api/core/v1"
	utilclock "k8s.io/utils/clock"
)

// clock is to enable unit testing
var clock utilclock.Clock = utilclock.RealClock{}

// ComputeDNSStatus computes the status conditions based on the DNSRecord, the current
// configuration of an Ingress Controller, and the Platform. It will return an array of
// conditions that signals if a DNS record for a resource could be published,
// and the reason on success or failure
func ComputeDNSStatus(ic *operatorv1.IngressController, wildcardRecord *iov1.DNSRecord, status *configv1.PlatformStatus, dnsConfig *configv1.DNS, gatewayAPI bool) []operatorv1.OperatorCondition {
	// In case there is no managed DNS zone configured, return a single condition
	// that DNSManaged=False because no zone is configured
	if dnsConfig == nil || (dnsConfig.Spec.PublicZone == nil && dnsConfig.Spec.PrivateZone == nil) {
		// On Gateway API we can have just the single DNSReady and the statement that it is false due to the lack of
		// dns configuration.
		if gatewayAPI {
			return []operatorv1.OperatorCondition{
				{
					Type:    operatorv1.DNSReadyIngressConditionType,
					Status:  operatorv1.ConditionFalse,
					Reason:  "NoDNSZones",
					Message: "No DNS zones are defined in the cluster dns config.",
				},
			}
		}

		return []operatorv1.OperatorCondition{
			{
				Type:    operatorv1.DNSManagedIngressConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "NoDNSZones",
				Message: "No DNS zones are defined in the cluster dns config.",
			},
		}
	}

	var conditions []operatorv1.OperatorCondition

	if !gatewayAPI {
		// The condition below is only possible when an ingress controller is requesting
		// a DNS. In case the ingresscontroller resource is null (eg.: GatewayAPI)
		// this condition will not be verified.
		// Otherwise, it will return a single condition that DNSManaged=False in case
		// the EndpointPublishingStrategy is not "LoadBalancerService"
		if ic != nil && ic.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
			return []operatorv1.OperatorCondition{
				{
					Type:    operatorv1.DNSManagedIngressConditionType,
					Status:  operatorv1.ConditionFalse,
					Reason:  "UnsupportedEndpointPublishingStrategy",
					Message: "The endpoint publishing strategy doesn't support DNS management.",
				},
			}
		}

		// In case this is an ingresscontroller resource, and it contains a DNSManagementPolicy = 'Unmanaged'
		// the "DNSManaged" condition should be false, and in any other case (GatewayAPI, ManagedDNS)
		// return the condition as True
		if ic != nil && ic.Status.EndpointPublishingStrategy.LoadBalancer != nil && ic.Status.EndpointPublishingStrategy.LoadBalancer.DNSManagementPolicy == operatorv1.UnmanagedLoadBalancerDNS {
			conditions = append(conditions, operatorv1.OperatorCondition{
				Type:    operatorv1.DNSManagedIngressConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnmanagedLoadBalancerDNS",
				Message: "The DNS management policy is set to Unmanaged.",
			})
		} else {
			conditions = append(conditions, operatorv1.OperatorCondition{
				Type:    operatorv1.DNSManagedIngressConditionType,
				Status:  operatorv1.ConditionTrue,
				Reason:  "Normal",
				Message: "DNS management is supported and zones are specified in the cluster DNS config.",
			})
		}
	}

	// The switch below is specific for the "DNSReady" condition.
	switch {
	// A null wildcardRecord means that getting the DNSRecord failed by a number of
	// reasons, including it may not be found/created, and in this case DNSReady="False"
	// as no DNSRecord is found
	case wildcardRecord == nil:
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.DNSReadyIngressConditionType,
			Status:  operatorv1.ConditionFalse,
			Reason:  "RecordNotFound",
			Message: "The wildcard record resource was not found.",
		})
	// In case the ManagementPolicy of a DNSRecord = "Unmanaged", this means our
	// controller will not reconcile it, and in this case DNSReady should be Unknown
	case wildcardRecord.Spec.DNSManagementPolicy == iov1.UnmanagedDNS:
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.DNSReadyIngressConditionType,
			Status:  operatorv1.ConditionUnknown,
			Reason:  "UnmanagedDNS",
			Message: "The DNS management policy is set to Unmanaged.",
		})
	// In case the DNSRecord contains no zones, this means that this record couldn't
	// fit into any DNSZone, and in this case DNSReady=False with the Reason of "NoZones"
	case len(wildcardRecord.Status.Zones) == 0:
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.DNSReadyIngressConditionType,
			Status:  operatorv1.ConditionFalse,
			Reason:  "NoZones",
			Message: "The record isn't present in any zones.",
		})
	case len(wildcardRecord.Status.Zones) > 0:
		var failedZones []configv1.DNSZone
		var unknownZones []configv1.DNSZone
		// The loop below will check if any of the zones existing on DNSRecord
		// status (status.zones.condition["Published"].Status) are failed (Status=False)
		// or unknown (Status=Unknown) and in this case, it will reflect on the
		// returned conditions with DNSReady=False and reason being either "FailedZones"
		// or "UnknownZones"
		for _, zone := range wildcardRecord.Status.Zones {
			for _, cond := range zone.Conditions {
				if cond.Type != iov1.DNSRecordPublishedConditionType {
					continue
				}
				if !checkZoneInConfig(dnsConfig, zone.DNSZone) {
					continue
				}
				switch cond.Status {
				case string(operatorv1.ConditionFalse):
					// check to see if the zone is in the dnsConfig.Spec
					// fix:BZ1942657 - relates to status changes when updating DNS PrivateZone config
					failedZones = append(failedZones, zone.DNSZone)
				case string(operatorv1.ConditionUnknown):
					unknownZones = append(unknownZones, zone.DNSZone)
				}
			}
		}
		if len(failedZones) != 0 {
			// TODO: Add failed condition reasons
			conditions = append(conditions, operatorv1.OperatorCondition{
				Type:    operatorv1.DNSReadyIngressConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "FailedZones",
				Message: fmt.Sprintf("The record failed to provision in some zones: %v", failedZones),
			})
		} else if len(unknownZones) != 0 {
			// This condition is an edge case where DNSManaged=True but
			// there was an internal error during publishing record.
			conditions = append(conditions, operatorv1.OperatorCondition{
				Type:    operatorv1.DNSReadyIngressConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnknownZones",
				Message: fmt.Sprintf("Provisioning of the record is in an unknown state in some zones: %v", unknownZones),
			})
		} else {
			// Otherwise if no status.zones.condition["Published"].Status is False
			// add the condition DNSReady=True
			conditions = append(conditions, operatorv1.OperatorCondition{
				Type:    operatorv1.DNSReadyIngressConditionType,
				Status:  operatorv1.ConditionTrue,
				Reason:  "NoFailedZones",
				Message: "The record is provisioned in all reported zones.",
			})
		}
	}

	return conditions
}

// ComputeLoadBalancerStatus returns the set of current
// LoadBalancer-prefixed conditions for the given ingress controller, which are
// used later to determine the ingress controller's Degraded or Available status.
func ComputeLoadBalancerStatus(ic *operatorv1.IngressController, service *corev1.Service, operandEvents []corev1.Event, gwapi bool) []operatorv1.OperatorCondition {
	// Compute the LoadBalancerManagedIngressConditionType condition
	// The condition below is only possible when an ingress controller is requesting
	// a LoadBalancer. In case the ingresscontroller resource is null (eg.: GatewayAPI,
	// that has the LoadBalancer managed by Istio) this condition will not be verified.
	// Otherwise, it will return a single condition that LoadBalancerManaged=False in case
	// the EndpointPublishingStrategy is not "LoadBalancerService"
	if ic != nil && (ic.Status.EndpointPublishingStrategy == nil ||
		ic.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType) {
		return []operatorv1.OperatorCondition{
			{
				Type:    operatorv1.LoadBalancerManagedIngressConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "EndpointPublishingStrategyExcludesManagedLoadBalancer",
				Message: "The configured endpoint publishing strategy does not include a managed load balancer",
			},
		}
	}

	conditions := []operatorv1.OperatorCondition{}

	// Initial condition for any resource that is managed is LoadBalancerManaged=True
	// We skip adding this condition to Gateways from Gateway API to avoid overloading
	// it with a condition that will be always true
	if !gwapi {
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.LoadBalancerManagedIngressConditionType,
			Status:  operatorv1.ConditionTrue,
			Reason:  "WantedByEndpointPublishingStrategy",
			Message: "The endpoint publishing strategy supports a managed load balancer",
		})
	}

	// Compute the LoadBalancerReadyIngressConditionType (LoadBalancerReady) condition
	switch {
	// A null service means that getting the Service failed by some reason like
	//  not be found/created, and in this case LoadBalancerReady="False"
	// as no service is found
	case service == nil:
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.LoadBalancerReadyIngressConditionType,
			Status:  operatorv1.ConditionFalse,
			Reason:  "ServiceNotFound",
			Message: "The LoadBalancer service resource is missing",
		})
	// In case the service is created and has a .status.Loadbalancer.Ingress populated
	// by a service/loadbalancer controller, the LoadBalancerReady can be marked as "True"
	case isProvisioned(service):
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.LoadBalancerReadyIngressConditionType,
			Status:  operatorv1.ConditionTrue,
			Reason:  "LoadBalancerProvisioned",
			Message: "The LoadBalancer service is provisioned",
		})
	// In case the service is created but does not has a .status.Loadbalancer.Ingress populated
	// by a service/loadbalancer controller, the LoadBalancerReady can be marked as "False"
	// defaulting the reason to LoadBalancerPending.
	// As our service controller creates events that can be helpful to give additional
	// context to users, on a best-effort we try to get the events related with this
	// service to provide a better context on the condition
	case isPending(service):
		reason := "LoadBalancerPending"
		message := "The LoadBalancer service is pending"

		// Try and find a more specific reason for for the pending status.
		createFailedReason := "SyncLoadBalancerFailed"
		failedLoadBalancerEvents := getEventsByReason(operandEvents, "service-controller", createFailedReason)
		for _, event := range failedLoadBalancerEvents {
			involved := event.InvolvedObject
			if involved.Kind == "Service" && involved.Namespace == service.Namespace && involved.Name == service.Name && involved.UID == service.UID {
				reason = "SyncLoadBalancerFailed"
				message = fmt.Sprintf("The %s component is reporting SyncLoadBalancerFailed events like: %s\n%s",
					event.Source.Component, event.Message, "The cloud-controller-manager logs may contain more details.")
				break
			}
		}
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.LoadBalancerReadyIngressConditionType,
			Status:  operatorv1.ConditionFalse,
			Reason:  reason,
			Message: message,
		})
	}
	return conditions
}

// getEventsByReason receives a list of events, and returns a filtered list
// containing just the events that were generated by a specific component and reason
func getEventsByReason(events []corev1.Event, component, reason string) []corev1.Event {
	var filtered []corev1.Event
	for i := range events {
		event := events[i]
		if event.Source.Component == component && event.Reason == reason {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

// isProvisioned defines if a service contains a .status.LoadBalancer.Ingress
func isProvisioned(service *corev1.Service) bool {
	ingresses := service.Status.LoadBalancer.Ingress
	return len(ingresses) > 0 && (len(ingresses[0].Hostname) > 0 || len(ingresses[0].IP) > 0)
}

// isPending defines if a service does not contains a .status.LoadBalancer.Ingress
func isPending(service *corev1.Service) bool {
	return !isProvisioned(service)
}

// checkZoneInConfig - private utility to check for a zone in the current config
func checkZoneInConfig(dnsConfig *configv1.DNS, zone configv1.DNSZone) bool {
	return zonesMatch(&zone, dnsConfig.Spec.PublicZone) || zonesMatch(&zone, dnsConfig.Spec.PrivateZone)
}

// zonesMatch returns a Boolean value indicating whether two DNS zones have the
// matching ID or "Name" tag.  If either or both zones are nil, this function
// returns false.
func zonesMatch(a, b *configv1.DNSZone) bool {
	if a == nil || b == nil {
		return false
	}

	if a.ID != "" && b.ID != "" && a.ID == b.ID {
		return true
	}

	if a.Tags["Name"] != "" && b.Tags["Name"] != "" && a.Tags["Name"] == b.Tags["Name"] {
		return true
	}

	return false
}
