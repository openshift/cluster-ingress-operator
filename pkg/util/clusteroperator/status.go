package clusteroperator

import (
	osv1 "github.com/openshift/cluster-version-operator/pkg/apis/operatorstatus.openshift.io/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SetStatusCondition returns the result of setting the specified condition in
// the given slice of conditions.
// TODO Replace with cluster-version-operator's SetOperatorStatusCondition
// once update to a version of cluster-version-operator that has it.
// https://github.com/openshift/cluster-version-operator/blob/fe673cb712fa5e27001488fc088ac91bb553353d/lib/resourcemerge/os.go#L36-L54
func SetStatusCondition(oldConditions []osv1.ClusterOperatorStatusCondition, condition *osv1.ClusterOperatorStatusCondition) []osv1.ClusterOperatorStatusCondition {
	condition.LastTransitionTime = metav1.Now()

	newConditions := []osv1.ClusterOperatorStatusCondition{}

	found := false
	for _, c := range oldConditions {
		if condition.Type == c.Type {
			if condition.Status == c.Status &&
				condition.Reason == c.Reason &&
				condition.Message == c.Message {
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

// ConditionsEqual returns true if and only if the provided slices of conditions
// (ignoring LastTransitionTime) are equal.
func ConditionsEqual(oldConditions, newConditions []osv1.ClusterOperatorStatusCondition) bool {
	if len(newConditions) != len(oldConditions) {
		return false
	}

	for _, conditionA := range oldConditions {
		foundMatchingCondition := false

		for _, conditionB := range newConditions {
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

	return true
}
