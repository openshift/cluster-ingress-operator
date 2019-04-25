package controller

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	IngressClusterOperatorName     = "ingress"
	UnknownReleaseVersionName      = "unknown"
	ingressesEqualConditionMessage = "desired and current number of IngressControllers are equal"
	operatorVersionName            = "operator"
)

// syncOperatorStatus computes the operator's current status and therefrom
// creates or updates the ClusterOperator resource for the operator.
func (r *reconciler) syncOperatorStatus() error {
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: IngressClusterOperatorName}}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: co.Name}, co); err != nil {
		if errors.IsNotFound(err) {
			if err := r.client.Create(context.TODO(), co); err != nil {
				return fmt.Errorf("failed to create clusteroperator %s: %v", co.Name, err)
			}
			log.Info("created clusteroperator", "object", co)
		} else {
			return fmt.Errorf("failed to get clusteroperator %s: %v", co.Name, err)
		}
	}

	ns, ingresses, err := r.getOperatorState()
	if err != nil {
		return fmt.Errorf("failed to get operator state: %v", err)
	}

	oldStatus := co.Status.DeepCopy()
	co.Status.Conditions = computeOperatorStatusConditions(oldStatus.Conditions, ns, ingresses)
	co.Status.RelatedObjects = []configv1.ObjectReference{
		{
			Resource: "namespaces",
			Name:     "openshift-ingress-operator",
		},
		{
			Resource: "namespaces",
			Name:     ns.Name,
		},
	}

	// An available operator resets release version
	for _, condition := range co.Status.Conditions {
		if condition.Type == configv1.OperatorAvailable && condition.Status == configv1.ConditionTrue {
			co.Status.Versions = []configv1.OperandVersion{
				{
					Name:    operatorVersionName,
					Version: r.OperatorReleaseVersion,
				},
				{
					Name:    "ingress-controller",
					Version: r.RouterImage,
				},
			}
		}
	}

	if !operatorStatusesEqual(*oldStatus, co.Status) {
		err = r.client.Status().Update(context.TODO(), co)
		if err != nil {
			return fmt.Errorf("failed to update clusteroperator %s: %v", co.Name, err)
		}
	}

	return nil
}

// getOperatorState gets and returns the resources necessary to compute the
// operator's current state.
func (r *reconciler) getOperatorState() (*corev1.Namespace, []operatorv1.IngressController, error) {
	ns := manifests.RouterNamespace()

	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil, nil
		}

		return nil, nil, fmt.Errorf(
			"error getting Namespace %s: %v", ns.Name, err)
	}

	ingressList := &operatorv1.IngressControllerList{}
	if err := r.client.List(context.TODO(), ingressList, client.InNamespace(r.Namespace)); err != nil {
		return nil, nil, fmt.Errorf("failed to list IngressControllers: %v", err)
	}

	return ns, ingressList.Items, nil
}

// computeOperatorStatusConditions computes the operator's current state.
func computeOperatorStatusConditions(conditions []configv1.ClusterOperatorStatusCondition, ns *corev1.Namespace,
	ingresses []operatorv1.IngressController) []configv1.ClusterOperatorStatusCondition {
	conditions = computeOperatorDegradedCondition(conditions, ns)
	conditions = computeOperatorProgressingCondition(conditions, ingresses)
	conditions = computeOperatorAvailableCondition(conditions, ingresses)

	return conditions
}

// computeOperatorDegradedCondition computes the operator's current Degraded status state.
func computeOperatorDegradedCondition(conditions []configv1.ClusterOperatorStatusCondition, ns *corev1.Namespace) []configv1.ClusterOperatorStatusCondition {
	degradedCondition := &configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorDegraded,
		Status: configv1.ConditionUnknown,
	}
	if ns == nil {
		degradedCondition.Status = configv1.ConditionTrue
		degradedCondition.Reason = "NoNamespace"
		degradedCondition.Message = "operand namespace does not exist"
	} else {
		degradedCondition.Status = configv1.ConditionFalse
		degradedCondition.Message = "operand namespace exists"
	}

	return setOperatorStatusCondition(conditions, degradedCondition)
}

// computeOperatorProgressingCondition computes the operator's current Progressing status state.
func computeOperatorProgressingCondition(conditions []configv1.ClusterOperatorStatusCondition, ingresses []operatorv1.IngressController) []configv1.ClusterOperatorStatusCondition {
	// TODO: Update progressingCondition when an ingresscontroller
	//       progressing condition is created. The Operator's condition
	//       should be derived from the ingresscontroller's condition.
	progressingCondition := &configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorProgressing,
		Status: configv1.ConditionUnknown,
	}
	numIngresses := len(ingresses)
	var ingressesAvailable int
	for _, ing := range ingresses {
		for _, c := range ing.Status.Conditions {
			if c.Type == operatorv1.IngressControllerAvailableConditionType && c.Status == operatorv1.ConditionTrue {
				ingressesAvailable++
				break
			}
		}
	}
	if numIngresses == ingressesAvailable {
		progressingCondition.Status = configv1.ConditionFalse
		progressingCondition.Message = ingressesEqualConditionMessage
	} else {
		progressingCondition.Status = configv1.ConditionTrue
		progressingCondition.Reason = "Reconciling"
		progressingCondition.Message = fmt.Sprintf(
			"%d ingress controllers available, want %d",
			ingressesAvailable, numIngresses)
	}

	return setOperatorStatusCondition(conditions, progressingCondition)
}

// computeOperatorAvailableCondition computes the operator's current Available status state.
func computeOperatorAvailableCondition(conditions []configv1.ClusterOperatorStatusCondition,
	ingresses []operatorv1.IngressController) []configv1.ClusterOperatorStatusCondition {
	availableCondition := &configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorAvailable,
		Status: configv1.ConditionUnknown,
	}
	numIngresses := len(ingresses)
	var ingressesAvailable int
	for _, ing := range ingresses {
		for _, c := range ing.Status.Conditions {
			if c.Type == operatorv1.IngressControllerAvailableConditionType && c.Status == operatorv1.ConditionTrue {
				ingressesAvailable++
				break
			}
		}
	}
	if numIngresses == ingressesAvailable {
		availableCondition.Status = configv1.ConditionTrue
		availableCondition.Message = ingressesEqualConditionMessage
	} else {
		availableCondition.Status = configv1.ConditionFalse
		availableCondition.Reason = "IngressUnavailable"
		availableCondition.Message = fmt.Sprintf(
			"%d ingress controllers available, want %d",
			ingressesAvailable, numIngresses)
	}

	return setOperatorStatusCondition(conditions, availableCondition)
}

// setOperatorStatusCondition returns a slice of Operator status conditions as
// a result of setting the specified condition in the given slice of conditions.
func setOperatorStatusCondition(oldConditions []configv1.ClusterOperatorStatusCondition, condition *configv1.ClusterOperatorStatusCondition) []configv1.ClusterOperatorStatusCondition {
	condition.LastTransitionTime = metav1.Now()

	newConditions := []configv1.ClusterOperatorStatusCondition{}

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

// operatorStatusesEqual compares two ClusterOperatorStatus values.  Returns
// true if the provided ClusterOperatorStatus values should be considered equal
// for the purpose of determining whether an update is necessary, false otherwise.
func operatorStatusesEqual(a, b configv1.ClusterOperatorStatus) bool {
	conditionCmpOpts := []cmp.Option{
		cmpopts.IgnoreFields(configv1.ClusterOperatorStatusCondition{}, "LastTransitionTime"),
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b configv1.ClusterOperatorStatusCondition) bool { return a.Type < b.Type }),
	}
	if !cmp.Equal(a.Conditions, b.Conditions, conditionCmpOpts...) {
		return false
	}

	relatedCmpOpts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b configv1.ObjectReference) bool { return a.Name < b.Name }),
	}
	if !cmp.Equal(a.RelatedObjects, b.RelatedObjects, relatedCmpOpts...) {
		return false
	}

	versionsCmpOpts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b configv1.OperandVersion) bool { return a.Name < b.Name }),
	}
	if !cmp.Equal(a.Versions, b.Versions, versionsCmpOpts...) {
		return false
	}

	return true
}
