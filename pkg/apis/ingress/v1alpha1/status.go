package v1alpha1

import (
	"fmt"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/operator-framework/operator-sdk/pkg/sdk"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// setStatusCondition modifies the ClusterIngress object's status by setting the
// specified condition.
func (ci *ClusterIngress) setStatusCondition(condition *operatorv1alpha1.OperatorCondition) {
	condition.LastTransitionTime = metav1.Now()

	conditions := []operatorv1alpha1.OperatorCondition{}

	found := false
	for _, c := range ci.Status.Conditions {
		if condition.Type == c.Type {
			if condition.Status == c.Status &&
				condition.Reason == c.Reason &&
				condition.Message == c.Message {
				return
			}

			found = true
			conditions = append(conditions, *condition)
		} else {
			conditions = append(conditions, c)
		}
	}
	if !found {
		conditions = append(conditions, *condition)
	}

	ci.Status.Conditions = conditions
}

// SetStatusSyncCondition modifies the ClusterIngress object's status by setting
// its SyncSuccessful condition in accordance with the provided error value.
func (ci *ClusterIngress) SetStatusSyncCondition(err error) {
	condition := &operatorv1alpha1.OperatorCondition{
		Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
		Status: operatorv1alpha1.ConditionTrue,
	}

	if err != nil {
		condition.Status = operatorv1alpha1.ConditionFalse
		condition.Reason = "SyncFailed"
		condition.Message = err.Error()
	}

	ci.setStatusCondition(condition)
}

// SetStatusAvailableCondition modifies the ClusterIngress object's status by
// setting its Available condition to the specified status and message.
func (ci *ClusterIngress) SetStatusAvailableCondition(available bool, message string) {
	condition := &operatorv1alpha1.OperatorCondition{
		Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
		Status: operatorv1alpha1.ConditionTrue,
	}

	if !available {
		condition.Status = operatorv1alpha1.ConditionFalse
		condition.Reason = "ServiceNotReady"
		condition.Message = message
	}

	ci.setStatusCondition(condition)
}

// clusterIngressStatusEqual returns true if and only if the status conditions
// (ignoring LastTransitionTime) and ingresses of the provided ClusterIngress
// objects are equal.
func clusterIngressStatusEqual(oldStatus, newStatus *ClusterIngressStatus) bool {
	if len(newStatus.Conditions) != len(oldStatus.Conditions) {
		return false
	}
	for _, conditionA := range oldStatus.Conditions {
		foundMatchingCondition := false

		for _, conditionB := range newStatus.Conditions {
			// Compare every field except LastTransitionTime.
			if conditionA.Type == conditionB.Type &&
				conditionA.Status == conditionB.Status &&
				conditionA.Reason == conditionB.Reason &&
				conditionA.Message == conditionB.Message {
				foundMatchingCondition = true
				break
			}
		}

		if !foundMatchingCondition {
			return false
		}
	}

	if len(newStatus.LoadBalancer.Ingress) != len(oldStatus.LoadBalancer.Ingress) {
		return false
	}
	for _, ingressA := range oldStatus.LoadBalancer.Ingress {
		foundMatchingIngress := false

		for _, ingressB := range newStatus.LoadBalancer.Ingress {
			if ingressA == ingressB {
				foundMatchingIngress = true
				break
			}
		}

		if !foundMatchingIngress {
			return false
		}
	}

	return true
}

// UpdateStatus updates the receiver ClusterIngress object's status if it
// differs from the provided ClusterIngress object's status.
func (ci *ClusterIngress) UpdateStatus(oldCi *ClusterIngress) error {
	if clusterIngressStatusEqual(&oldCi.Status, &ci.Status) {
		return nil
	}

	if err := sdk.Update(ci); err != nil {
		return fmt.Errorf(
			"failed to update status of ClusterIngress %q: %v",
			ci.Name, err)
	}

	return nil
}
