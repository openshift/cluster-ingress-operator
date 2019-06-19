package controller

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	iov1 "github.com/openshift/cluster-ingress-operator/pkg/api/v1"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// syncIngressControllerStatus computes the current status of ic and
// updates status upon any changes since last sync.
func (r *reconciler) syncIngressControllerStatus(ic *operatorv1.IngressController, deployment *appsv1.Deployment, service *corev1.Service, operandEvents []corev1.Event, wildcardRecord *iov1.DNSRecord, dnsConfig *configv1.DNS) error {
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return fmt.Errorf("deployment has invalid spec.selector: %v", err)
	}

	updated := ic.DeepCopy()
	updated.Status.AvailableReplicas = deployment.Status.AvailableReplicas
	updated.Status.Selector = selector.String()

	updated.Status.Conditions = []operatorv1.OperatorCondition{}
	updated.Status.Conditions = append(updated.Status.Conditions, computeIngressStatusConditions(updated.Status.Conditions, deployment)...)
	updated.Status.Conditions = append(updated.Status.Conditions, computeLoadBalancerStatus(ic, service, operandEvents)...)
	updated.Status.Conditions = append(updated.Status.Conditions, computeDNSStatus(ic, wildcardRecord, dnsConfig)...)

	for i := range updated.Status.Conditions {
		newCondition := &updated.Status.Conditions[i]
		var oldCondition *operatorv1.OperatorCondition
		for j, possibleOldCondition := range ic.Status.Conditions {
			if possibleOldCondition.Type == newCondition.Type {
				old := ic.Status.Conditions[j]
				oldCondition = &old
				break
			}
		}
		setIngressLastTransitionTime(newCondition, oldCondition)
	}

	if !ingressStatusesEqual(updated.Status, ic.Status) {
		if err := r.client.Status().Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to update ingresscontroller status: %v", err)
		}
	}

	return nil
}

// computeIngressStatusConditions computes the ingress controller's current state.
func computeIngressStatusConditions(oldConditions []operatorv1.OperatorCondition, deployment *appsv1.Deployment) []operatorv1.OperatorCondition {
	oldAvailableCondition := getIngressAvailableCondition(oldConditions)

	return []operatorv1.OperatorCondition{
		computeIngressAvailableCondition(oldAvailableCondition, deployment),
	}
}

// computeIngressAvailableCondition computes the ingress controller's current Available status state.
func computeIngressAvailableCondition(oldAvailableCondition *operatorv1.OperatorCondition, deployment *appsv1.Deployment) operatorv1.OperatorCondition {
	availableCondition := operatorv1.OperatorCondition{
		Type: operatorv1.IngressControllerAvailableConditionType,
	}

	if deployment.Status.AvailableReplicas > 0 {
		availableCondition.Status = operatorv1.ConditionTrue
	} else {
		availableCondition.Status = operatorv1.ConditionFalse
		availableCondition.Reason = "DeploymentUnavailable"
		availableCondition.Message = "no Deployment replicas available"
	}

	return availableCondition
}

// getIngressAvailableCondition fetches ingress controller's available condition from the given conditions.
func getIngressAvailableCondition(conditions []operatorv1.OperatorCondition) *operatorv1.OperatorCondition {
	var availableCondition *operatorv1.OperatorCondition
	for i := range conditions {
		switch conditions[i].Type {
		case operatorv1.IngressControllerAvailableConditionType:
			availableCondition = &conditions[i]
			break
		}
	}

	return availableCondition
}

// setIngressLastTransitionTime sets LastTransitionTime for the given ingress controller condition.
// If the condition has changed, it will assign a new timestamp otherwise keeps the old timestamp.
func setIngressLastTransitionTime(condition, oldCondition *operatorv1.OperatorCondition) {
	if oldCondition != nil && condition.Status == oldCondition.Status &&
		condition.Reason == oldCondition.Reason && condition.Message == oldCondition.Message {
		condition.LastTransitionTime = oldCondition.LastTransitionTime
	} else {
		condition.LastTransitionTime = metav1.Now()
	}
}

// ingressStatusesEqual compares two IngressControllerStatus values.  Returns true
// if the provided values should be considered equal for the purpose of determining
// whether an update is necessary, false otherwise.
func ingressStatusesEqual(a, b operatorv1.IngressControllerStatus) bool {
	conditionCmpOpts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
	}
	if !cmp.Equal(a.Conditions, b.Conditions, conditionCmpOpts...) || a.AvailableReplicas != b.AvailableReplicas ||
		a.Selector != b.Selector {
		return false
	}

	return true
}

// computeLoadBalancerStatus returns the complete set of current
// LoadBalancer-prefixed conditions for the given ingress controller.
func computeLoadBalancerStatus(ic *operatorv1.IngressController, service *corev1.Service, operandEvents []corev1.Event) []operatorv1.OperatorCondition {
	if ic.Status.EndpointPublishingStrategy == nil ||
		ic.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return []operatorv1.OperatorCondition{
			{
				Type:    operatorv1.LoadBalancerManagedIngressConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnsupportedEndpointPublishingStrategy",
				Message: fmt.Sprintf("The endpoint publishing strategy does not support a load balancer"),
			},
		}
	}

	conditions := []operatorv1.OperatorCondition{}

	conditions = append(conditions, operatorv1.OperatorCondition{
		Type:    operatorv1.LoadBalancerManagedIngressConditionType,
		Status:  operatorv1.ConditionTrue,
		Reason:  "WantedByEndpointPublishingStrategy",
		Message: "The endpoint publishing strategy supports a managed load balancer",
	})

	switch {
	case service == nil:
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.LoadBalancerReadyIngressConditionType,
			Status:  operatorv1.ConditionFalse,
			Reason:  "ServiceNotFound",
			Message: "The LoadBalancer service resource is missing",
		})
	case isProvisioned(service):
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.LoadBalancerReadyIngressConditionType,
			Status:  operatorv1.ConditionTrue,
			Reason:  "LoadBalancerProvisioned",
			Message: "The LoadBalancer service is provisioned",
		})
	case isPending(service):
		reason := "LoadBalancerPending"
		message := "The LoadBalancer service is pending"

		// Try and find a more specific reason for for the pending status.
		createFailedReason := "CreatingLoadBalancerFailed"
		failedLoadBalancerEvents := getEventsByReason(operandEvents, "service-controller", createFailedReason)
		for _, event := range failedLoadBalancerEvents {
			involved := event.InvolvedObject
			if involved.Kind == "Service" && involved.Namespace == service.Namespace && involved.Name == service.Name {
				reason = "CreatingLoadBalancerFailed"
				message = fmt.Sprintf("The %s component is reporting CreatingLoadBalancerFailed events like: %s\n%s",
					event.Source.Component, event.Message, "The kube-controller-manager logs may contain more details.")
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

func isProvisioned(service *corev1.Service) bool {
	ingresses := service.Status.LoadBalancer.Ingress
	return len(ingresses) > 0 && (len(ingresses[0].Hostname) > 0 || len(ingresses[0].IP) > 0)
}

func isPending(service *corev1.Service) bool {
	return !isProvisioned(service)
}

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

func computeDNSStatus(ic *operatorv1.IngressController, wildcardRecord *iov1.DNSRecord, dnsConfig *configv1.DNS) []operatorv1.OperatorCondition {
	if dnsConfig.Spec.PublicZone == nil && dnsConfig.Spec.PrivateZone == nil {
		return []operatorv1.OperatorCondition{
			{
				Type:    operatorv1.DNSManagedIngressConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "NoDNSZones",
				Message: "No DNS zones are defined in the cluster dns config.",
			},
		}
	}

	if ic.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return []operatorv1.OperatorCondition{
			{
				Type:    operatorv1.DNSManagedIngressConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnsupportedEndpointPublishingStrategy",
				Message: "The endpoint publishing strategy doesn't support DNS management.",
			},
		}
	}

	conditions := []operatorv1.OperatorCondition{
		{
			Type:    operatorv1.DNSManagedIngressConditionType,
			Status:  operatorv1.ConditionTrue,
			Reason:  "Normal",
			Message: "DNS management is supported and zones are specified in the cluster DNS config.",
		},
	}

	switch {
	case wildcardRecord == nil:
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.DNSReadyIngressConditionType,
			Status:  operatorv1.ConditionFalse,
			Reason:  "RecordNotFound",
			Message: "The wildcard record resource was not found.",
		})
	case len(wildcardRecord.Status.Zones) == 0:
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    operatorv1.DNSReadyIngressConditionType,
			Status:  operatorv1.ConditionFalse,
			Reason:  "NoZones",
			Message: "The record isn't present in any zones.",
		})
	case len(wildcardRecord.Status.Zones) > 0:
		var failedZones []configv1.DNSZone
		for _, zone := range wildcardRecord.Status.Zones {
			for _, cond := range zone.Conditions {
				if cond.Type == iov1.DNSRecordFailedConditionType && cond.Status == string(operatorv1.ConditionTrue) {
					failedZones = append(failedZones, zone.DNSZone)
				}
			}
		}
		if len(failedZones) == 0 {
			conditions = append(conditions, operatorv1.OperatorCondition{
				Type:    operatorv1.DNSReadyIngressConditionType,
				Status:  operatorv1.ConditionTrue,
				Reason:  "NoFailedZones",
				Message: "The record is provisioned in all reported zones.",
			})
		} else {
			// TODO: Add failed condition reasons
			conditions = append(conditions, operatorv1.OperatorCondition{
				Type:    operatorv1.DNSReadyIngressConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "FailedZones",
				Message: fmt.Sprintf("The record failed to provision in some zones: %v", failedZones),
			})
		}
	}

	return conditions
}
