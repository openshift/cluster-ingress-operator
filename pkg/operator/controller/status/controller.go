package status

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	iov1 "github.com/openshift/api/operatoringress/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	oputil "github.com/openshift/cluster-ingress-operator/pkg/util"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilclock "k8s.io/apimachinery/pkg/util/clock"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	OperatorVersionName          = "operator"
	IngressControllerVersionName = "ingress-controller"
	UnknownVersionValue          = "unknown"

	ingressesEqualConditionMessage = "desired and current number of IngressControllers are equal"

	controllerName = "status_controller"
)

var log = logf.Logger.WithName(controllerName)

// clock is to enable unit testing
var clock utilclock.Clock = utilclock.RealClock{}

// New creates the status controller. This is the controller that handles all
// the logic for creating the ClusterOperator operator and updating its status.
//
// The controller watches IngressController resources in the manager namespace
// and uses them to compute the operator status.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	reconciler := &reconciler{
		Config: config,
		client: mgr.GetClient(),
		cache:  mgr.GetCache(),
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &operatorv1.IngressController{}}, &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}
	return c, nil
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	IngressControllerImage string
	OperatorReleaseVersion string
	Namespace              string
}

// reconciler handles the actual status reconciliation logic in response to
// events.
type reconciler struct {
	Config

	client client.Client
	cache  cache.Cache
}

// Reconcile computes the operator's current status and therefrom creates or
// updates the ClusterOperator resource for the operator.
func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Info("Reconciling", "request", request)

	nsManifest := manifests.RouterNamespace()

	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: operatorcontroller.IngressClusterOperatorName().Name}}
	if err := r.client.Get(context.TODO(), operatorcontroller.IngressClusterOperatorName(), co); err != nil {
		if errors.IsNotFound(err) {
			initializeClusterOperator(co)
			if err := r.client.Create(context.TODO(), co); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to create clusteroperator %s: %v", co.Name, err)
			}
			log.Info("created clusteroperator", "object", co)
		} else {
			return reconcile.Result{}, fmt.Errorf("failed to get clusteroperator %s: %v", co.Name, err)
		}
	}
	oldStatus := co.Status.DeepCopy()

	state, err := r.getOperatorState(nsManifest.Name)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get operator state: %v", err)
	}

	related := []configv1.ObjectReference{
		{
			Resource: "namespaces",
			Name:     "openshift-ingress-operator",
		},
		{
			Group:     operatorv1.GroupName,
			Resource:  "IngressController",
			Namespace: r.Namespace,
		},
		{
			Group:     iov1.GroupVersion.Group,
			Resource:  "DNSRecord",
			Namespace: r.Namespace,
		},
	}
	if state.Namespace != nil {
		related = append(related, configv1.ObjectReference{
			Resource: "namespaces",
			Name:     state.Namespace.Name,
		})
	}
	co.Status.RelatedObjects = related

	allIngressesAvailable := checkAllIngressesAvailable(state.IngressControllers)

	co.Status.Versions = r.computeOperatorStatusVersions(oldStatus.Versions, allIngressesAvailable)

	co.Status.Conditions = mergeConditions(co.Status.Conditions, computeOperatorAvailableCondition(allIngressesAvailable))
	co.Status.Conditions = mergeConditions(co.Status.Conditions, computeOperatorProgressingCondition(allIngressesAvailable, oldStatus.Versions, co.Status.Versions, r.OperatorReleaseVersion, r.IngressControllerImage))
	co.Status.Conditions = mergeConditions(co.Status.Conditions, computeOperatorDegradedCondition(state.IngressControllers))

	if !operatorStatusesEqual(*oldStatus, co.Status) {
		if err := r.client.Status().Update(context.TODO(), co); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to update clusteroperator %s: %v", co.Name, err)
		}
	}

	return reconcile.Result{}, nil
}

// Populate versions and conditions in cluster operator status as CVO expects these fields.
func initializeClusterOperator(co *configv1.ClusterOperator) {
	co.Status.Versions = []configv1.OperandVersion{
		{
			Name:    OperatorVersionName,
			Version: UnknownVersionValue,
		},
		{
			Name:    IngressControllerVersionName,
			Version: UnknownVersionValue,
		},
	}
	co.Status.Conditions = []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionUnknown,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionUnknown,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionUnknown,
		},
	}
}

type operatorState struct {
	Namespace          *corev1.Namespace
	IngressControllers []operatorv1.IngressController
	DNSRecords         []iov1.DNSRecord
}

// getOperatorState gets and returns the resources necessary to compute the
// operator's current state.
func (r *reconciler) getOperatorState(nsName string) (operatorState, error) {
	state := operatorState{}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: nsName}, ns); err != nil {
		if !errors.IsNotFound(err) {
			return state, fmt.Errorf("failed to get namespace %q: %v", nsName, err)
		}
	} else {
		state.Namespace = ns
	}

	ingressList := &operatorv1.IngressControllerList{}
	if err := r.cache.List(context.TODO(), ingressList, client.InNamespace(r.Namespace)); err != nil {
		return state, fmt.Errorf("failed to list ingresscontrollers in %q: %v", r.Namespace, err)
	} else {
		state.IngressControllers = ingressList.Items
	}

	return state, nil
}

// computeOperatorStatusVersions computes the operator's current versions.
func (r *reconciler) computeOperatorStatusVersions(oldVersions []configv1.OperandVersion, allIngressesAvailable bool) []configv1.OperandVersion {
	// We need to report old version until the operator fully transitions to the new version.
	// https://github.com/openshift/cluster-version-operator/blob/master/docs/dev/clusteroperator.md#version-reporting-during-an-upgrade
	if !allIngressesAvailable {
		return oldVersions
	}

	return []configv1.OperandVersion{
		{
			Name:    OperatorVersionName,
			Version: r.OperatorReleaseVersion,
		},
		{
			Name:    IngressControllerVersionName,
			Version: r.IngressControllerImage,
		},
	}
}

// checkAllIngressesAvailable checks if all the ingress controllers are available.
func checkAllIngressesAvailable(ingresses []operatorv1.IngressController) bool {
	for _, ing := range ingresses {
		available := false
		for _, c := range ing.Status.Conditions {
			if c.Type == operatorv1.IngressControllerAvailableConditionType && c.Status == operatorv1.ConditionTrue {
				available = true
				break
			}
		}
		if !available {
			return false
		}
	}

	return len(ingresses) != 0
}

// computeOperatorDegradedCondition computes the operator's current Degraded status state.
func computeOperatorDegradedCondition(ingresses []operatorv1.IngressController) configv1.ClusterOperatorStatusCondition {
	degradedIngresses := make(map[*operatorv1.IngressController]operatorv1.OperatorCondition)
	for i, ingress := range ingresses {
		for j, cond := range ingress.Status.Conditions {
			if cond.Type == operatorv1.OperatorStatusTypeDegraded && cond.Status == operatorv1.ConditionTrue {
				degradedIngresses[&ingresses[i]] = ingress.Status.Conditions[j]
			}
		}
	}
	if len(degradedIngresses) == 0 {
		return configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
			Reason: "NoIngressControllersDegraded",
		}
	}
	message := "Some ingresscontrollers are degraded:"
	// Sort keys so that the result is deterministic.
	keys := make([]*operatorv1.IngressController, 0, len(degradedIngresses))
	for ingress := range degradedIngresses {
		keys = append(keys, ingress)
	}
	sort.Slice(keys, func(i, j int) bool {
		return oputil.ObjectLess(&keys[i].ObjectMeta, &keys[j].ObjectMeta)
	})
	for _, ingress := range keys {
		cond := degradedIngresses[ingress]
		message = fmt.Sprintf("%s ingresscontroller %q is degraded: %s: %s", message, ingress.Name, cond.Reason, cond.Message)
	}
	return configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorDegraded,
		Status:  configv1.ConditionTrue,
		Reason:  "IngressControllersDegraded",
		Message: message,
	}
}

// computeOperatorProgressingCondition computes the operator's current Progressing status state.
func computeOperatorProgressingCondition(allIngressesAvailable bool, oldVersions, curVersions []configv1.OperandVersion, operatorReleaseVersion, ingressControllerImage string) configv1.ClusterOperatorStatusCondition {
	// TODO: Update progressingCondition when an ingresscontroller
	//       progressing condition is created. The Operator's condition
	//       should be derived from the ingresscontroller's condition.
	progressingCondition := configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorProgressing,
	}

	progressing := false

	var messages []string
	if !allIngressesAvailable {
		messages = append(messages, "Not all ingress controllers are available.")
		progressing = true
	}

	oldVersionsMap := make(map[string]string)
	for _, opv := range oldVersions {
		oldVersionsMap[opv.Name] = opv.Version
	}

	for _, opv := range curVersions {
		if oldVersion, ok := oldVersionsMap[opv.Name]; ok && oldVersion != opv.Version {
			messages = append(messages, fmt.Sprintf("Upgraded %s to %q.", opv.Name, opv.Version))
		}
		switch opv.Name {
		case OperatorVersionName:
			if opv.Version != operatorReleaseVersion {
				messages = append(messages, fmt.Sprintf("Moving to release version %q.", operatorReleaseVersion))
				progressing = true
			}
		case IngressControllerVersionName:
			if opv.Version != ingressControllerImage {
				messages = append(messages, fmt.Sprintf("Moving to ingress-controller image version %q.", ingressControllerImage))
				progressing = true
			}
		}
	}

	if progressing {
		progressingCondition.Status = configv1.ConditionTrue
		progressingCondition.Reason = "Reconciling"
	} else {
		progressingCondition.Status = configv1.ConditionFalse
		progressingCondition.Reason = "AsExpected"
	}
	progressingCondition.Message = ingressesEqualConditionMessage
	if len(messages) > 0 {
		progressingCondition.Message = strings.Join(messages, "\n")
	}

	return progressingCondition
}

// computeOperatorAvailableCondition computes the operator's current Available status state.
func computeOperatorAvailableCondition(allIngressesAvailable bool) configv1.ClusterOperatorStatusCondition {
	availableCondition := configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorAvailable,
	}

	if allIngressesAvailable {
		availableCondition.Status = configv1.ConditionTrue
		availableCondition.Reason = "AsExpected"
		availableCondition.Message = ingressesEqualConditionMessage
	} else {
		availableCondition.Status = configv1.ConditionFalse
		availableCondition.Reason = "IngressUnavailable"
		availableCondition.Message = "Not all ingress controllers are available."
	}

	return availableCondition
}

// mergeConditions adds or updates matching conditions, and updates
// the transition time if details of a condition have changed. Returns
// the updated condition array.
func mergeConditions(conditions []configv1.ClusterOperatorStatusCondition, updates ...configv1.ClusterOperatorStatusCondition) []configv1.ClusterOperatorStatusCondition {
	now := metav1.NewTime(clock.Now())
	var additions []configv1.ClusterOperatorStatusCondition
	for i, update := range updates {
		add := true
		for j, cond := range conditions {
			if cond.Type == update.Type {
				add = false
				if conditionChanged(cond, update) {
					conditions[j].Status = update.Status
					conditions[j].Reason = update.Reason
					conditions[j].Message = update.Message
					conditions[j].LastTransitionTime = now
					break
				}
			}
		}
		if add {
			updates[i].LastTransitionTime = now
			additions = append(additions, updates[i])
		}
	}
	conditions = append(conditions, additions...)
	return conditions
}

func conditionChanged(a, b configv1.ClusterOperatorStatusCondition) bool {
	return a.Status != b.Status || a.Reason != b.Reason || a.Message != b.Message
}

// operatorStatusesEqual compares two ClusterOperatorStatus values.  Returns
// true if the provided ClusterOperatorStatus values should be considered equal
// for the purpose of determining whether an update is necessary, false otherwise.
func operatorStatusesEqual(a, b configv1.ClusterOperatorStatus) bool {
	conditionCmpOpts := []cmp.Option{
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
