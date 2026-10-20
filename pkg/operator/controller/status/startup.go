package status

import (
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
	for i := range controllers {
		ic := &controllers[i]
		if ic.Name != manifests.DefaultIngressControllerName || ic.DeletionTimestamp != nil {
			continue
		}
		conditions := make(map[string]operatorv1.OperatorCondition, len(ic.Status.Conditions))
		for _, condition := range ic.Status.Conditions {
			conditions[condition.Type] = condition
		}
		available := conditions[operatorv1.IngressControllerAvailableConditionType]
		if available.Status != operatorv1.ConditionFalse || available.Reason != ingress.ReasonAwaitingNodes {
			continue
		}
		// AwaitingNodes may coexist with DNS or load-balancer failures. Require
		// known management status and ready managed dependencies so those
		// failures cannot be hidden by the deployment exception.
		dependenciesReady := true
		for _, dependency := range []struct{ managed, ready string }{
			{operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.LoadBalancerReadyIngressConditionType},
			{operatorv1.DNSManagedIngressConditionType, operatorv1.DNSReadyIngressConditionType},
		} {
			managed := conditions[dependency.managed].Status
			if managed != operatorv1.ConditionFalse && (managed != operatorv1.ConditionTrue || conditions[dependency.ready].Status != operatorv1.ConditionTrue) {
				dependenciesReady = false
				break
			}
		}
		if !dependenciesReady {
			continue
		}

		result := append([]operatorv1.IngressController(nil), controllers...)
		result[i] = *ic.DeepCopy()
		available.Status = operatorv1.ConditionTrue
		available.Message = "The default router is waiting for nodes during initial zero-worker hosted-cluster startup."
		updates := []operatorv1.OperatorCondition{available}
		rollout := conditions[ingress.IngressControllerDeploymentRollingOutConditionType]
		lbProgressing := conditions[ingress.IngressControllerLoadBalancerProgressingConditionType]
		if (rollout.Status == operatorv1.ConditionFalse || (rollout.Status == operatorv1.ConditionTrue && rollout.Reason == ingress.ReasonDeploymentRollingOut)) &&
			(conditions[operatorv1.LoadBalancerManagedIngressConditionType].Status == operatorv1.ConditionFalse || lbProgressing.Status == operatorv1.ConditionFalse) {
			updates = append(updates, operatorv1.OperatorCondition{
				Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionFalse,
				Reason: ingress.ReasonAwaitingNodes,
			})
		}
		result[i].Status.Conditions = ingress.MergeConditions(result[i].Status.Conditions, updates...)
		return result
	}
	return controllers
}
