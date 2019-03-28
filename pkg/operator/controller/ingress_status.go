package controller

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// syncIngressControllerStatus computes the current status of ic and
// updates status upon any changes since last sync.
func (r *reconciler) syncIngressControllerStatus(deployment *appsv1.Deployment, ic *operatorv1.IngressController) error {
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return fmt.Errorf("deployment has invalid spec.selector: %v", err)
	}

	updated := ic.DeepCopy()
	updated.Status.AvailableReplicas = deployment.Status.AvailableReplicas
	updated.Status.Selector = selector.String()
	updated.Status.Conditions = computeIngressStatusConditions(updated.Status.Conditions, deployment)
	if !ingressStatusesEqual(updated.Status, ic.Status) {
		if err := r.client.Status().Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to update ingresscontroller status: %v", err)
		}
	}

	return nil
}

// computeIngressStatusConditions computes the ingress controller's current state.
func computeIngressStatusConditions(conditions []operatorv1.OperatorCondition, deployment *appsv1.Deployment) []operatorv1.OperatorCondition {
	availableCondition := &operatorv1.OperatorCondition{
		Type:   operatorv1.IngressControllerAvailableConditionType,
		Status: operatorv1.ConditionUnknown,
	}
	if deployment.Status.AvailableReplicas > 0 {
		availableCondition.Status = operatorv1.ConditionTrue
	} else {
		availableCondition.Status = operatorv1.ConditionFalse
		availableCondition.Reason = "DeploymentUnavailable"
		availableCondition.Message = "no Deployment replicas available"
	}
	conditions = setIngressStatusCondition(conditions, availableCondition)

	return conditions
}

// setIngressStatusCondition returns the IngressController condition result
// of setting the specified condition in the given slice of conditions.
func setIngressStatusCondition(oldConditions []operatorv1.OperatorCondition, condition *operatorv1.OperatorCondition) []operatorv1.OperatorCondition {
	condition.LastTransitionTime = metav1.Now()

	var newConditions []operatorv1.OperatorCondition

	found := false
	for _, c := range oldConditions {
		if condition.Type == c.Type {
			if condition.Status == c.Status &&
				condition.Reason == c.Reason &&
				condition.Message == c.Message {
				// The ingresscontroller status condition has not changed.
				return oldConditions
			}

			found = true
			newConditions = append(newConditions, *condition)
		} else {
			newConditions = append(newConditions, c)
		}
	}
	if !found {
		newConditions = append(newConditions, *condition)
	}

	return newConditions
}

// ingressStatusesEqual compares two IngressControllerStatus values.  Returns true
// if the provided values should be considered equal for the purpose of determining
// whether an update is necessary, false otherwise.
func ingressStatusesEqual(a, b operatorv1.IngressControllerStatus) bool {
	conditionCmpOpts := []cmp.Option{
		cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime"),
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
	}
	if !cmp.Equal(a.Conditions, b.Conditions, conditionCmpOpts...) || a.AvailableReplicas != b.AvailableReplicas ||
		a.Selector != b.Selector {
		return false
	}

	return true
}
