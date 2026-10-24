package status

import (
	"slices"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	corev1 "k8s.io/api/core/v1"
)

// ingressControllersForClusterOperator applies the initial zero-worker startup
// exception only to the default controller's contribution to ClusterOperator
// availability, progress, and versions. It never changes an IngressController
// resource: consumers of those resources still wait for actual router readiness.
// AwaitingNodes records startup history; checking the complete NodeList also
// prevents an unschedulable custom router from masquerading as a zero-worker
// cluster. A failed or unknown node list never enables the exception.
func ingressControllersForClusterOperator(controllers []operatorv1.IngressController, topology configv1.TopologyMode, nodes *corev1.NodeList) []operatorv1.IngressController {
	if topology != configv1.ExternalTopologyMode || nodes == nil || len(nodes.Items) != 0 {
		return controllers
	}
	i := slices.IndexFunc(controllers, func(ic operatorv1.IngressController) bool {
		return ic.Name == manifests.DefaultIngressControllerName && ic.DeletionTimestamp == nil
	})
	if i < 0 {
		return controllers
	}

	updates, ok := zeroWorkerStartupConditions(conditionsByType(controllers[i].Status.Conditions))
	if !ok {
		return controllers
	}

	result := slices.Clone(controllers)
	result[i] = *controllers[i].DeepCopy()
	result[i].Status.Conditions = ingress.MergeConditions(result[i].Status.Conditions, updates...)
	return result
}

// conditionsByType indexes conditions by type for single-lookup access.
func conditionsByType(conditions []operatorv1.OperatorCondition) map[string]operatorv1.OperatorCondition {
	byType := make(map[string]operatorv1.OperatorCondition, len(conditions))
	for _, condition := range conditions {
		byType[condition.Type] = condition
	}
	return byType
}

// zeroWorkerStartupConditions returns the Available (and, when safe, Progressing)
// condition overrides for a default IngressController that is unavailable only
// because it is waiting for the hosted cluster's first guest node. ok is false
// when the exception does not apply: the controller was never in the
// AwaitingNodes startup state, or a real DNS/load-balancer failure is also
// present and must not be hidden by the deployment exception.
func zeroWorkerStartupConditions(conditions map[string]operatorv1.OperatorCondition) (updates []operatorv1.OperatorCondition, ok bool) {
	available := conditions[operatorv1.IngressControllerAvailableConditionType]
	if available.Status != operatorv1.ConditionFalse || available.Reason != ingress.ReasonAwaitingNodes {
		return nil, false
	}
	// AwaitingNodes may coexist with DNS or load-balancer failures. Require
	// both managed dependencies to be either unmanaged or fully ready.
	loadBalancerReady := dependencyReady(conditions, operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.LoadBalancerReadyIngressConditionType)
	dnsReady := dependencyReady(conditions, operatorv1.DNSManagedIngressConditionType, operatorv1.DNSReadyIngressConditionType)
	if !loadBalancerReady || !dnsReady {
		return nil, false
	}

	available.Status = operatorv1.ConditionTrue
	available.Message = "The default router is waiting for nodes during initial zero-worker hosted-cluster startup."
	updates = []operatorv1.OperatorCondition{available}

	// Suppress rollout progress caused by missing nodes, while preserving
	// other rollout reasons and load-balancer provisioning.
	rollout := conditions[ingress.IngressControllerDeploymentRollingOutConditionType]
	rolloutIsExpected := rollout.Status == operatorv1.ConditionFalse ||
		(rollout.Status == operatorv1.ConditionTrue && rollout.Reason == ingress.ReasonDeploymentRollingOut)
	loadBalancerSettled := conditions[operatorv1.LoadBalancerManagedIngressConditionType].Status == operatorv1.ConditionFalse ||
		conditions[ingress.IngressControllerLoadBalancerProgressingConditionType].Status == operatorv1.ConditionFalse
	if rolloutIsExpected && loadBalancerSettled {
		updates = append(updates, operatorv1.OperatorCondition{
			Type:   operatorv1.OperatorStatusTypeProgressing,
			Status: operatorv1.ConditionFalse,
			Reason: ingress.ReasonAwaitingNodes,
		})
	}
	return updates, true
}

// dependencyReady reports whether a managed dependency (DNS, load balancer) is
// either not managed at all or fully ready. An unknown or missing status is
// treated as not ready.
func dependencyReady(conditions map[string]operatorv1.OperatorCondition, managedType, readyType string) bool {
	switch conditions[managedType].Status {
	case operatorv1.ConditionFalse:
		return true
	case operatorv1.ConditionTrue:
		return conditions[readyType].Status == operatorv1.ConditionTrue
	default:
		return false
	}
}
