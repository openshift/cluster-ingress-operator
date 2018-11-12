package ingress

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/util/clusteroperator"
	operatorversion "github.com/openshift/cluster-ingress-operator/version"
	osv1 "github.com/openshift/cluster-version-operator/pkg/apis/operatorstatus.openshift.io/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// syncOperatorStatus computes the operator's current status and therefrom
// creates or updates the ClusterOperator resource for the operator.
func (r *IngressReconciler) syncOperatorStatus() {
	co := &osv1.ClusterOperator{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterOperator",
			APIVersion: "operatorstatus.openshift.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.namespace,
			// TODO Use a named constant or get name from config.
			Name: "openshift-ingress",
		},
	}
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: co.Name, Namespace: co.Namespace}, co)
	isNotFound := errors.IsNotFound(err)
	if err != nil && !isNotFound {
		logrus.Errorf("syncOperatorStatus: error getting ClusterOperator %s/%s: %v",
			co.Namespace, co.Name, err)

		return
	}

	ns, ingresses, daemonsets, err := r.getOperatorState()
	if err != nil {
		logrus.Errorf("syncOperatorStatus: getOperatorState: %v", err)

		return
	}

	oldConditions := co.Status.Conditions
	co.Status.Conditions = computeStatusConditions(oldConditions, ns,
		ingresses, daemonsets)

	if isNotFound {
		co.Status.Version = operatorversion.Version

		if err := r.client.Create(context.TODO(), co); err != nil {
			logrus.Errorf("syncOperatorStatus: failed to create ClusterOperator %s/%s: %v",
				co.Namespace, co.Name, err)
		} else {
			logrus.Infof("syncOperatorStatus: created ClusterOperator %s/%s (UID %v)",
				co.Namespace, co.Name, co.UID)
		}

		return
	}

	if clusteroperator.ConditionsEqual(oldConditions, co.Status.Conditions) {
		return
	}

	if _, err := r.cvoClient.OperatorstatusV1().
		ClusterOperators(co.Namespace).
		UpdateStatus(co); err != nil {
		logrus.Errorf("syncOperatorStatus: updating status on %s/%s: %v",
			co.Namespace, co.Name, err)
	}
}

// getOperatorState gets and returns the resources necessary to compute the
// operator's current state.
func (r *IngressReconciler) getOperatorState() (*corev1.Namespace, []ingressv1alpha1.ClusterIngress, []appsv1.DaemonSet, error) {
	ns, err := r.manifestFactory.RouterNamespace()
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"error building router namespace: %v", err)
	}

	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil, nil, nil
		}

		return nil, nil, nil, fmt.Errorf(
			"error getting Namespace %s: %v", ns.Name, err)
	}

	ingressList := &ingressv1alpha1.ClusterIngressList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterIngress",
			APIVersion: "ingress.openshift.io/v1alpha1",
		},
	}
	err = r.client.List(context.TODO(), &client.ListOptions{Namespace: r.namespace}, ingressList)
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"failed to list ClusterIngresses: %v", err)
	}

	daemonsetList := &appsv1.DaemonSetList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
	}
	err = r.client.List(context.TODO(), &client.ListOptions{Namespace: r.namespace}, daemonsetList)
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"failed to list DaemonSets: %v", err)
	}

	return ns, ingressList.Items, daemonsetList.Items, nil
}

// computeStatusConditions computes the operator's current state.
func computeStatusConditions(conditions []osv1.ClusterOperatorStatusCondition, ns *corev1.Namespace, ingresses []ingressv1alpha1.ClusterIngress, daemonsets []appsv1.DaemonSet) []osv1.ClusterOperatorStatusCondition {
	failingCondition := &osv1.ClusterOperatorStatusCondition{
		Type:   osv1.OperatorFailing,
		Status: osv1.ConditionUnknown,
	}
	if ns == nil {
		failingCondition.Status = osv1.ConditionTrue
		failingCondition.Reason = "NoNamespace"
		failingCondition.Message = "router namespace does not exist"
	} else {
		failingCondition.Status = osv1.ConditionFalse
	}
	conditions = clusteroperator.SetStatusCondition(conditions,
		failingCondition)

	progressingCondition := &osv1.ClusterOperatorStatusCondition{
		Type:   osv1.OperatorProgressing,
		Status: osv1.ConditionUnknown,
	}
	numIngresses := len(ingresses)
	numDaemonsets := len(daemonsets)
	if numIngresses == numDaemonsets {
		progressingCondition.Status = osv1.ConditionFalse
	} else {
		progressingCondition.Status = osv1.ConditionTrue
		progressingCondition.Reason = "Reconciling"
		progressingCondition.Message = fmt.Sprintf(
			"have %d ingresses, want %d",
			numDaemonsets, numIngresses)
	}
	conditions = clusteroperator.SetStatusCondition(conditions,
		progressingCondition)

	availableCondition := &osv1.ClusterOperatorStatusCondition{
		Type:   osv1.OperatorAvailable,
		Status: osv1.ConditionUnknown,
	}
	dsAvailable := map[string]bool{}
	for _, ds := range daemonsets {
		dsAvailable[ds.Name] = ds.Status.NumberAvailable > 0
	}
	unavailable := []string{}
	for _, ingress := range ingresses {
		// TODO Use the manifest to derive the name, or use labels or
		// owner references.
		name := "router-" + ingress.Name
		if available, exists := dsAvailable[name]; !exists {
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
		availableCondition.Status = osv1.ConditionTrue
	} else {
		availableCondition.Status = osv1.ConditionFalse
		availableCondition.Reason = "IngressUnavailable"
		availableCondition.Message = strings.Join(unavailable,
			"\n")
	}
	conditions = clusteroperator.SetStatusCondition(conditions,
		availableCondition)

	return conditions
}
