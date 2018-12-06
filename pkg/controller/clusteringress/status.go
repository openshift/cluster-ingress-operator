package clusteringress

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"

	configv1 "github.com/openshift/api/config/v1"
	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/util/clusteroperator"
	operatorversion "github.com/openshift/cluster-ingress-operator/version"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// syncOperatorStatus computes the operator's current status and therefrom
// creates or updates the ClusterOperator resource for the operator.
func (h *Reconciler) syncOperatorStatus() error {
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: h.Namespace}}
	err := h.Client.Get(context.TODO(), types.NamespacedName{Name: co.Name}, co)
	isNotFound := errors.IsNotFound(err)
	if err != nil && !isNotFound {
		return fmt.Errorf("failed to get clusteroperator %s: %v", co.Name, err)
	}

	ns, ingresses, deployments, err := h.getOperatorState()
	if err != nil {
		return fmt.Errorf("failed to get operator state: %v", err)
	}

	oldConditions := co.Status.Conditions
	co.Status.Conditions = computeStatusConditions(oldConditions, ns, ingresses, deployments)

	if isNotFound {
		co.Status.Version = operatorversion.Version
		if err := h.Client.Create(context.TODO(), co); err != nil {
			return fmt.Errorf("failed to create clusteroperator %s: %v", co.Name, err)
		}
		logrus.Infof("created clusteroperator: %#v", co)
	} else {
		if !clusteroperator.ConditionsEqual(oldConditions, co.Status.Conditions) {
			err = h.Client.Status().Update(context.TODO(), co)
			if err != nil {
				return fmt.Errorf("failed to update clusteroperator %s: %v", co.Name, err)
			}
		}
	}
	return nil
}

// getOperatorState gets and returns the resources necessary to compute the
// operator's current state.
func (h *Reconciler) getOperatorState() (*corev1.Namespace, []ingressv1alpha1.ClusterIngress, []appsv1.Deployment, error) {
	ns, err := h.ManifestFactory.RouterNamespace()
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"error building router namespace: %v", err)
	}

	if err := h.Client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil, nil, nil
		}

		return nil, nil, nil, fmt.Errorf(
			"error getting Namespace %s: %v", ns.Name, err)
	}

	ingressList := &ingressv1alpha1.ClusterIngressList{}
	err = h.Client.List(context.TODO(), &client.ListOptions{Namespace: h.Namespace}, ingressList)
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"failed to list ClusterIngresses: %v", err)
	}

	deploymentList := &appsv1.DeploymentList{}
	err = h.Client.List(context.TODO(), &client.ListOptions{Namespace: ns.Name}, deploymentList)
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
	conditions = clusteroperator.SetStatusCondition(conditions,
		failingCondition)

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
	conditions = clusteroperator.SetStatusCondition(conditions,
		progressingCondition)

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
	conditions = clusteroperator.SetStatusCondition(conditions,
		availableCondition)

	return conditions
}
