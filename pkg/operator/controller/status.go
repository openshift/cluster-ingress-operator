package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/sirupsen/logrus"

	configv1 "github.com/openshift/api/config/v1"
	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/util"
	"github.com/openshift/cluster-ingress-operator/version"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	IngressClusterOperatorName = "ingress"
)

// syncOperatorStatus computes the operator's current status and therefrom
// creates or updates the ClusterOperator resource for the operator.
func (r *reconciler) syncOperatorStatus() error {
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: IngressClusterOperatorName}}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: co.Name}, co)
	isNotFound := errors.IsNotFound(err)
	if err != nil && !isNotFound {
		return fmt.Errorf("failed to get clusteroperator %s: %v", co.Name, err)
	}

	ns, ingresses, deployments, err := r.getOperatorState()
	if err != nil {
		return fmt.Errorf("failed to get operator state: %v", err)
	}

	oldConditions := co.Status.Conditions
	co.Status.Conditions = computeStatusConditions(oldConditions, ns, ingresses, deployments)

	oldRelatedObjects := co.Status.RelatedObjects
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

	oldVersions := co.Status.Versions
	co.Status.Versions = []configv1.OperandVersion{
		{
			Name:    "operator",
			Version: version.OperatorVersion,
		},
		{
			Name:    version.RouterOperandName,
			Version: r.RouterImage,
		},
	}

	if isNotFound {
		if err := r.Client.Create(context.TODO(), co); err != nil {
			return fmt.Errorf("failed to create clusteroperator %s: %v", co.Name, err)
		}
		logrus.Infof("created clusteroperator: %#v", co)
	} else {
		conditionOpts := []cmp.Option{
			cmpopts.IgnoreFields(configv1.ClusterOperatorStatusCondition{}, "LastTransitionTime"),
		}
		conditionsEqual, err := util.ElementsEqual(oldConditions, co.Status.Conditions, conditionOpts)
		if err != nil {
			return fmt.Errorf("failed to compare clusteroperator conditions for %s: %v", co.Name, err)
		}

		relatedObjectsEqual, err := util.ElementsEqual(oldRelatedObjects, co.Status.RelatedObjects, []cmp.Option{})
		if err != nil {
			return fmt.Errorf("failed to compare clusteroperator related objects for %s: %v", co.Name, err)
		}

		versionsEqual, err := util.ElementsEqual(oldVersions, co.Status.Versions, []cmp.Option{})
		if err != nil {
			return fmt.Errorf("failed to compare clusteroperator versions %s: %v", co.Name, err)
		}

		if !conditionsEqual || !relatedObjectsEqual || !versionsEqual {
			err = r.Client.Status().Update(context.TODO(), co)
			if err != nil {
				return fmt.Errorf("failed to update clusteroperator %s: %v", co.Name, err)
			}
		}
	}
	return nil
}

// getOperatorState gets and returns the resources necessary to compute the
// operator's current state.
func (r *reconciler) getOperatorState() (*corev1.Namespace, []ingressv1alpha1.ClusterIngress, []appsv1.Deployment, error) {
	ns, err := r.ManifestFactory.RouterNamespace()
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"error building router namespace: %v", err)
	}

	if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil, nil, nil
		}

		return nil, nil, nil, fmt.Errorf(
			"error getting Namespace %s: %v", ns.Name, err)
	}

	ingressList := &ingressv1alpha1.ClusterIngressList{}
	err = r.Client.List(context.TODO(), &client.ListOptions{Namespace: r.Namespace}, ingressList)
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"failed to list ClusterIngresses: %v", err)
	}

	deploymentList := &appsv1.DeploymentList{}
	err = r.Client.List(context.TODO(), &client.ListOptions{Namespace: ns.Name}, deploymentList)
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"failed to list Deployment: %v", err)
	}

	return ns, ingressList.Items, deploymentList.Items, nil
}

// computeStatusConditions computes the operator's current state.
func computeStatusConditions(conditions []configv1.ClusterOperatorStatusCondition, ns *corev1.Namespace, ingresses []ingressv1alpha1.ClusterIngress, deployments []appsv1.Deployment) []configv1.ClusterOperatorStatusCondition {
	failingCondition := &configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorFailing,
		Status: configv1.ConditionUnknown,
	}
	if ns == nil {
		failingCondition.Status = configv1.ConditionTrue
		failingCondition.Reason = "NoNamespace"
		failingCondition.Message = "router namespace does not exist"
	} else {
		failingCondition.Status = configv1.ConditionFalse
	}
	conditions = setStatusCondition(conditions, failingCondition)

	progressingCondition := &configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorProgressing,
		Status: configv1.ConditionUnknown,
	}
	numIngresses := len(ingresses)
	numDeployments := len(deployments)
	if numIngresses == numDeployments {
		progressingCondition.Status = configv1.ConditionFalse
	} else {
		progressingCondition.Status = configv1.ConditionTrue
		progressingCondition.Reason = "Reconciling"
		progressingCondition.Message = fmt.Sprintf(
			"have %d ingresses, want %d",
			numDeployments, numIngresses)
	}
	conditions = setStatusCondition(conditions, progressingCondition)

	availableCondition := &configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorAvailable,
		Status: configv1.ConditionUnknown,
	}
	deploymentsAvailable := map[string]bool{}
	for _, d := range deployments {
		deploymentsAvailable[d.Name] = d.Status.AvailableReplicas > 0
	}
	unavailable := []string{}
	for _, ingress := range ingresses {
		// TODO Use the manifest to derive the name, or use labels or
		// owner references.
		name := "router-" + ingress.Name
		if available, exists := deploymentsAvailable[name]; !exists {
			msg := fmt.Sprintf("no router for ingress %q",
				ingress.Name)
			unavailable = append(unavailable, msg)
		} else if !available {
			msg := fmt.Sprintf("ingress %q not available",
				ingress.Name)
			unavailable = append(unavailable, msg)
		}
	}
	if len(unavailable) == 0 {
		availableCondition.Status = configv1.ConditionTrue
	} else {
		availableCondition.Status = configv1.ConditionFalse
		availableCondition.Reason = "IngressUnavailable"
		availableCondition.Message = strings.Join(unavailable, "\n")
	}
	conditions = setStatusCondition(conditions, availableCondition)

	return conditions
}

// setStatusCondition returns the result of setting the specified condition in
// the given slice of conditions.
func setStatusCondition(oldConditions []configv1.ClusterOperatorStatusCondition, condition *configv1.ClusterOperatorStatusCondition) []configv1.ClusterOperatorStatusCondition {
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
