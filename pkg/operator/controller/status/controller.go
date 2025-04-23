package status

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/detector"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	oputil "github.com/openshift/cluster-ingress-operator/pkg/util"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilclock "k8s.io/utils/clock"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	OperatorVersionName          = "operator"
	IngressControllerVersionName = "ingress-controller"
	CanaryImageVersionName       = "canary-server"
	UnknownVersionValue          = "unknown"

	OperatorDegradedSupport configv1.ClusterStatusConditionType = "DegradedSupport"

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
// and uses them to compute the operator status.  It also watches the
// clusteroperators resource so that it reconciles the ingress clusteroperator
// in case something else updates or deletes it.
func New(mgr manager.Manager, config Config, externalStatusSource detector.StatusSource) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		config:               config,
		externalStatusSource: externalStatusSource,
		client:               mgr.GetClient(),
		cache:                operatorCache,
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1.IngressController{}, &handler.EnqueueRequestForObject{})); err != nil {
		return nil, err
	}

	isIngressClusterOperator := func(o client.Object) bool {
		return o.GetName() == operatorcontroller.IngressClusterOperatorName().Name
	}
	toDefaultIngressController := func(ctx context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: config.Namespace,
				Name:      manifests.DefaultIngressControllerName,
			},
		}}
	}
	// The status controller doesn't care which ingresscontroller it
	// is reconciling, so just enqueue a request to reconcile the
	// default ingresscontroller.
	if err := c.Watch(source.Kind[client.Object](operatorCache, &configv1.ClusterOperator{}, handler.EnqueueRequestsFromMapFunc(toDefaultIngressController), predicate.NewPredicateFuncs(isIngressClusterOperator))); err != nil {
		return nil, err
	}

	if config.GatewayAPIEnabled {
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.Subscription{}, handler.EnqueueRequestsFromMapFunc(toDefaultIngressController), predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return e.Object.GetNamespace() == operatorcontroller.OpenshiftOperatorNamespace
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return false
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return e.Object.GetNamespace() == operatorcontroller.OpenshiftOperatorNamespace
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return false
			},
		})); err != nil {
			return nil, err
		}
	}
	c.Watch(source.TypedChannel(externalStatusSource.Channel(), handler.EnqueueRequestsFromMapFunc(toDefaultIngressController)))

	return c, nil
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	// GatewayAPIEnabled indicates that the "GatewayAPI" featuregate is enabled.
	GatewayAPIEnabled      bool
	IngressControllerImage string
	CanaryImage            string
	OperatorReleaseVersion string
	Namespace              string
}

// IngressOperatorStatusExtension holds status extensions of the ingress cluster operator.
type IngressOperatorStatusExtension struct {
	// UnmanagedGatewayAPICRDNames contains a comma separated list of unmanaged GatewayAPI CRDs
	// which are present on the cluster.
	UnmanagedGatewayAPICRDNames string `json:"unmanagedGatewayAPICRDNames,omitempty"`
}

// reconciler handles the actual status reconciliation logic in response to
// events.
type reconciler struct {
	config               Config
	externalStatusSource detector.StatusSource

	client client.Client
	cache  cache.Cache
}

// Reconcile computes the operator's current status and therefrom creates or
// updates the ClusterOperator resource for the operator.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("Reconciling", "request", request)

	ingressNamespace := manifests.RouterNamespace().Name
	canaryNamespace := manifests.CanaryNamespace().Name

	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: operatorcontroller.IngressClusterOperatorName().Name}}
	if err := r.client.Get(ctx, operatorcontroller.IngressClusterOperatorName(), co); err != nil {
		if errors.IsNotFound(err) {
			initializeClusterOperator(co)
			if err := r.client.Create(ctx, co); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to create clusteroperator %s: %v", co.Name, err)
			}
			log.Info("created clusteroperator", "object", co)
		} else {
			return reconcile.Result{}, fmt.Errorf("failed to get clusteroperator %s: %v", co.Name, err)
		}
	}
	oldStatus := co.Status.DeepCopy()

	state, err := r.getOperatorState(ingressNamespace, canaryNamespace, co)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get operator state: %v", err)
	}

	related := []configv1.ObjectReference{
		{
			Resource: "namespaces",
			Name:     r.config.Namespace,
		},
		{
			Group:     operatorv1.GroupName,
			Resource:  "ingresscontrollers",
			Namespace: r.config.Namespace,
		},
		{
			Group:     iov1.GroupVersion.Group,
			Resource:  "dnsrecords",
			Namespace: r.config.Namespace,
		},
	}
	if state.IngressNamespace != nil {
		related = append(related, configv1.ObjectReference{
			Resource: "namespaces",
			Name:     state.IngressNamespace.Name,
		}, configv1.ObjectReference{
			Group:     iov1.GroupVersion.Group,
			Resource:  "dnsrecords",
			Namespace: state.IngressNamespace.Name,
		})
	}
	if state.CanaryNamespace != nil {
		related = append(related, configv1.ObjectReference{
			Resource: "namespaces",
			Name:     state.CanaryNamespace.Name,
		})
	}
	if r.config.GatewayAPIEnabled {
		related = append(related, configv1.ObjectReference{
			Group:    gatewayapiv1.GroupName,
			Resource: "gatewayclasses",
		})
		if state.haveOSSMSubscription {
			subscriptionName := operatorcontroller.ServiceMeshOperatorSubscriptionName()
			related = append(related, configv1.ObjectReference{
				Group:     operatorsv1alpha1.GroupName,
				Resource:  "subscriptions",
				Namespace: subscriptionName.Namespace,
				Name:      subscriptionName.Name,
			})
			if state.IngressNamespace != nil {
				related = append(related, configv1.ObjectReference{
					Group:     sailv1.GroupVersion.Group,
					Resource:  "istios",
					Namespace: state.IngressNamespace.Name,
				})
				related = append(related, configv1.ObjectReference{
					Group:     gatewayapiv1.GroupName,
					Resource:  "gateways",
					Namespace: state.IngressNamespace.Name,
				})
			}
		}
	}

	co.Status.RelatedObjects = related

	allIngressesAvailable := checkAllIngressesAvailable(state.IngressControllers)

	co.Status.Versions = r.computeOperatorStatusVersions(oldStatus.Versions, allIngressesAvailable)

	co.Status.Conditions = mergeConditions(co.Status.Conditions,
		computeOperatorAvailableCondition(state.IngressControllers),
		computeOperatorProgressingCondition(
			state.IngressControllers,
			allIngressesAvailable,
			oldStatus.Versions,
			co.Status.Versions,
			r.config.OperatorReleaseVersion,
			r.config.IngressControllerImage,
			r.config.CanaryImage,
		),
		computeOperatorDegradedCondition(state),
		computeOperatorUpgradeableCondition(state.IngressControllers),
		computeOperatorEvaluationConditionsDetectedCondition(state.IngressControllers),
		computeExternalStatus(r.externalStatusSource.GetCurrentStatus(string(OperatorDegradedSupport))),
	)

	if !operatorStatusesEqual(*oldStatus, co.Status) {
		if err := r.client.Status().Update(ctx, co); err != nil {
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
		{
			Name:    CanaryImageVersionName,
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
	IngressNamespace   *corev1.Namespace
	CanaryNamespace    *corev1.Namespace
	IngressControllers []operatorv1.IngressController
	DNSRecords         []iov1.DNSRecord

	haveOSSMSubscription        bool
	unmanagedGatewayAPICRDNames string
}

// getOperatorState gets and returns the resources necessary to compute the
// operator's current state.
func (r *reconciler) getOperatorState(ingressNamespace, canaryNamespace string, co *configv1.ClusterOperator) (operatorState, error) {
	state := operatorState{}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ingressNamespace}}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: ingressNamespace}, ns); err != nil {
		if !errors.IsNotFound(err) {
			return state, fmt.Errorf("failed to get namespace %q: %v", ingressNamespace, err)
		}
	} else {
		state.IngressNamespace = ns
	}

	ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: canaryNamespace}}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: canaryNamespace}, ns); err != nil {
		if !errors.IsNotFound(err) {
			return state, fmt.Errorf("failed to get namespace %q: %v", canaryNamespace, err)
		}
	} else {
		state.CanaryNamespace = ns
	}

	ingressList := &operatorv1.IngressControllerList{}
	if err := r.cache.List(context.TODO(), ingressList, client.InNamespace(r.config.Namespace)); err != nil {
		return state, fmt.Errorf("failed to list ingresscontrollers in %q: %v", r.config.Namespace, err)
	} else {
		state.IngressControllers = ingressList.Items
	}

	if r.config.GatewayAPIEnabled {
		var subscription operatorsv1alpha1.Subscription
		subscriptionName := operatorcontroller.ServiceMeshOperatorSubscriptionName()
		if err := r.cache.Get(context.TODO(), subscriptionName, &subscription); err != nil {
			if !errors.IsNotFound(err) {
				return state, fmt.Errorf("failed to get subscription %q: %v", subscriptionName, err)
			}
		} else {
			state.haveOSSMSubscription = true
		}

		if len(co.Status.Extension.Raw) > 0 {
			extension := &IngressOperatorStatusExtension{}
			if err := json.Unmarshal(co.Status.Extension.Raw, extension); err != nil {
				return state, fmt.Errorf("failed to unmarshal status extension of cluster operator %q: %w", co.Name, err)
			}
			state.unmanagedGatewayAPICRDNames = extension.UnmanagedGatewayAPICRDNames
		}
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
			Version: r.config.OperatorReleaseVersion,
		},
		{
			Name:    IngressControllerVersionName,
			Version: r.config.IngressControllerImage,
		},
		{
			Name:    CanaryImageVersionName,
			Version: r.config.CanaryImage,
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
func computeOperatorDegradedCondition(state operatorState) configv1.ClusterOperatorStatusCondition {
	degradedCondition := configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorDegraded,
	}

	for _, fn := range []func(state operatorState) configv1.ClusterOperatorStatusCondition{
		computeIngressControllerDegradedCondition,
		computeGatewayAPICRDsDegradedCondition,
	} {
		degradedCondition = joinConditions(degradedCondition, fn(state))
	}

	return degradedCondition
}

// computeIngressControllerDegradedCondition computes the degraded condition for IngressControllers.
func computeIngressControllerDegradedCondition(state operatorState) configv1.ClusterOperatorStatusCondition {
	degradedCondition := configv1.ClusterOperatorStatusCondition{}

	foundDefaultIngressController := false
	for _, ic := range state.IngressControllers {
		if ic.Name != manifests.DefaultIngressControllerName {
			continue
		}
		foundDefaultIngressController = true
		foundDegradedStatusCondition := false
		for _, cond := range ic.Status.Conditions {
			if cond.Type != operatorv1.OperatorStatusTypeDegraded {
				continue
			}
			foundDegradedStatusCondition = true
			switch cond.Status {
			case operatorv1.ConditionFalse:
				degradedCondition.Status = configv1.ConditionFalse
				degradedCondition.Reason = "IngressNotDegraded"
				degradedCondition.Message = fmt.Sprintf("The %q ingress controller reports Degraded=False.", ic.Name)
			case operatorv1.ConditionTrue:
				degradedCondition.Status = configv1.ConditionTrue
				degradedCondition.Reason = "IngressDegraded"
				degradedCondition.Message = fmt.Sprintf("The %q ingress controller reports Degraded=True: %s: %s.", ic.Name, cond.Reason, cond.Message)
			default:
				degradedCondition.Status = configv1.ConditionUnknown
				degradedCondition.Reason = "IngressDegradedStatusUnknown"
				degradedCondition.Message = fmt.Sprintf("The %q ingress controller reports Degraded=%s.", ic.Name, cond.Status)
			}
		}
		if !foundDegradedStatusCondition {
			degradedCondition.Status = configv1.ConditionUnknown
			degradedCondition.Reason = "IngressDoesNotHaveDegradedCondition"
			degradedCondition.Message = fmt.Sprintf("The %q ingress controller is not reporting a Degraded status condition.", ic.Name)
		}
	}
	if !foundDefaultIngressController {
		degradedCondition.Status = configv1.ConditionTrue
		degradedCondition.Reason = "IngressDoesNotExist"
		degradedCondition.Message = fmt.Sprintf("The %q ingress controller does not exist.", manifests.DefaultIngressControllerName)
	}

	return degradedCondition
}

// computeGatewayAPICRDsDegradedCondition computes the degraded condition for Gateway API CRDs.
func computeGatewayAPICRDsDegradedCondition(state operatorState) configv1.ClusterOperatorStatusCondition {
	degradedCondition := configv1.ClusterOperatorStatusCondition{}

	if len(state.unmanagedGatewayAPICRDNames) > 0 {
		degradedCondition.Status = configv1.ConditionTrue
		degradedCondition.Reason = "GatewayAPICRDsDegraded"
		degradedCondition.Message = fmt.Sprintf("Unmanaged Gateway API CRDs found: %s.", state.unmanagedGatewayAPICRDNames)
	}

	return degradedCondition
}

// computeOperatorUpgradeableCondition computes the operator's Upgradeable
// status condition.
func computeOperatorUpgradeableCondition(ingresses []operatorv1.IngressController) configv1.ClusterOperatorStatusCondition {
	nonUpgradeableIngresses := make(map[*operatorv1.IngressController]operatorv1.OperatorCondition)
	for i, ingress := range ingresses {
		for j, cond := range ingress.Status.Conditions {
			if cond.Type == operatorv1.OperatorStatusTypeUpgradeable && cond.Status == operatorv1.ConditionFalse {
				nonUpgradeableIngresses[&ingresses[i]] = ingress.Status.Conditions[j]
			}
		}
	}
	if len(nonUpgradeableIngresses) == 0 {
		return configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
			Reason: "IngressControllersUpgradeable",
		}
	}
	message := "Some ingresscontrollers are not upgradeable:"
	// Sort keys so that the result is deterministic.
	keys := make([]*operatorv1.IngressController, 0, len(nonUpgradeableIngresses))
	for ingress := range nonUpgradeableIngresses {
		keys = append(keys, ingress)
	}
	sort.Slice(keys, func(i, j int) bool {
		return oputil.ObjectLess(&keys[i].ObjectMeta, &keys[j].ObjectMeta)
	})
	for _, ingress := range keys {
		cond := nonUpgradeableIngresses[ingress]
		message = fmt.Sprintf("%s ingresscontroller %q is not upgradeable: %s: %s", message, ingress.Name, cond.Reason, cond.Message)
	}
	return configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorUpgradeable,
		Status:  configv1.ConditionFalse,
		Reason:  "IngressControllersNotUpgradeable",
		Message: message,
	}
}

// computeOperatorEvaluationConditionsDetectedCondition computes the operator's EvaluationConditionsDetected condition.
func computeOperatorEvaluationConditionsDetectedCondition(ingresses []operatorv1.IngressController) configv1.ClusterOperatorStatusCondition {
	ingressesWithEvaluationCondition := make(map[*operatorv1.IngressController]operatorv1.OperatorCondition)
	for i, ing := range ingresses {
		for j, cond := range ing.Status.Conditions {
			if cond.Type == ingress.IngressControllerEvaluationConditionsDetectedConditionType && cond.Status == operatorv1.ConditionTrue {
				ingressesWithEvaluationCondition[&ingresses[i]] = ing.Status.Conditions[j]
			}
		}
	}
	if len(ingressesWithEvaluationCondition) == 0 {
		return configv1.ClusterOperatorStatusCondition{
			Type:   configv1.EvaluationConditionsDetected,
			Status: configv1.ConditionFalse,
			Reason: "AsExpected",
		}
	}
	message := "Some ingresscontrollers have evaluation conditions:"
	// Sort keys so that the result is deterministic.
	keys := make([]*operatorv1.IngressController, 0, len(ingressesWithEvaluationCondition))
	for ingress := range ingressesWithEvaluationCondition {
		keys = append(keys, ingress)
	}
	sort.Slice(keys, func(i, j int) bool {
		return oputil.ObjectLess(&keys[i].ObjectMeta, &keys[j].ObjectMeta)
	})
	for _, ingress := range keys {
		cond := ingressesWithEvaluationCondition[ingress]
		message = fmt.Sprintf("%s ingresscontroller %q has evaluation condition: %s: %s", message, ingress.Name, cond.Reason, cond.Message)
	}
	return configv1.ClusterOperatorStatusCondition{
		Type:    configv1.EvaluationConditionsDetected,
		Status:  configv1.ConditionTrue,
		Reason:  "IngressControllersHaveEvaluationConditions",
		Message: message,
	}
}

// computeOperatorProgressingCondition computes the operator's current Progressing status state.
func computeOperatorProgressingCondition(ingresscontrollers []operatorv1.IngressController, allIngressesAvailable bool, oldVersions, curVersions []configv1.OperandVersion, operatorReleaseVersion, ingressControllerImage string, canaryImage string) configv1.ClusterOperatorStatusCondition {
	progressingCondition := configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorProgressing,
	}

	progressing := false

	var messages []string

	for _, ic := range ingresscontrollers {
		for _, c := range ic.Status.Conditions {
			if c.Type == operatorv1.OperatorStatusTypeProgressing && c.Status == operatorv1.ConditionTrue {
				msg := fmt.Sprintf("ingresscontroller %q is progressing: %s: %s.", ic.Name, c.Reason, c.Message)
				messages = append(messages, msg)
				progressing = true
			}
		}
	}

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
		case CanaryImageVersionName:
			if opv.Version != canaryImage {
				messages = append(messages, fmt.Sprintf("Moving to canary image version %q.", canaryImage))
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
func computeOperatorAvailableCondition(ingresses []operatorv1.IngressController) configv1.ClusterOperatorStatusCondition {
	availableCondition := configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorAvailable,
	}

	foundDefaultIngressController := false
	for _, ic := range ingresses {
		if ic.Name != manifests.DefaultIngressControllerName {
			continue
		}
		foundDefaultIngressController = true
		foundAvailableStatusCondition := false
		for _, cond := range ic.Status.Conditions {
			if cond.Type != operatorv1.OperatorStatusTypeAvailable {
				continue
			}
			foundAvailableStatusCondition = true
			switch cond.Status {
			case operatorv1.ConditionFalse:
				availableCondition.Status = configv1.ConditionFalse
				availableCondition.Reason = "IngressUnavailable"
				availableCondition.Message = fmt.Sprintf("The %q ingress controller reports Available=False: %s: %s", ic.Name, cond.Reason, cond.Message)
			case operatorv1.ConditionTrue:
				availableCondition.Status = configv1.ConditionTrue
				availableCondition.Reason = "IngressAvailable"
				availableCondition.Message = fmt.Sprintf("The %q ingress controller reports Available=True.", ic.Name)
			default:
				availableCondition.Status = configv1.ConditionUnknown
				availableCondition.Reason = "IngressAvailableStatusUnknown"
				availableCondition.Message = fmt.Sprintf("The %q ingress controller reports Available=%s.", ic.Name, cond.Status)
			}
		}
		if !foundAvailableStatusCondition {
			availableCondition.Status = configv1.ConditionUnknown
			availableCondition.Reason = "IngressDoesNotHaveAvailableCondition"
			availableCondition.Message = fmt.Sprintf("The %q ingress controller is not reporting an Available status condition.", ic.Name)
		}
	}
	if !foundDefaultIngressController {
		availableCondition.Status = configv1.ConditionFalse
		availableCondition.Reason = "IngressDoesNotExist"
		availableCondition.Message = fmt.Sprintf("The %q ingress controller does not exist.", manifests.DefaultIngressControllerName)
	}

	return availableCondition
}

func computeExternalStatus(status detector.Status) configv1.ClusterOperatorStatusCondition {
	degradedCondition := configv1.ClusterOperatorStatusCondition{
		Type: OperatorDegradedSupport,
	}
	if status.Active {
		degradedCondition.Status = configv1.ConditionTrue
		degradedCondition.Reason = "SupportDegraded"
		degradedCondition.Message = fmt.Sprintf("Unsupportd objects found: %s", status.Details)
	} else {
		degradedCondition.Status = configv1.ConditionFalse
		degradedCondition.Reason = "SupportNotDegraded"
		degradedCondition.Message = "No unsupported usage found."

	}
	return degradedCondition
}

// mergeConditions adds or updates matching conditions, and updates
// the transition time if the status of a condition changed. Returns
// the updated condition array.
func mergeConditions(conditions []configv1.ClusterOperatorStatusCondition, updates ...configv1.ClusterOperatorStatusCondition) []configv1.ClusterOperatorStatusCondition {
	now := metav1.NewTime(clock.Now())
	var additions []configv1.ClusterOperatorStatusCondition
	for i, update := range updates {
		add := true
		for j, cond := range conditions {
			if cond.Type == update.Type {
				add = false
				if update.Status != conditions[j].Status {
					conditions[j].LastTransitionTime = now
				}
				conditions[j].Reason = update.Reason
				conditions[j].Message = update.Message
				conditions[j].Status = update.Status
				break
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

// joinConditions merges two cluster operator conditions into one.
// If both conditions have the same status:
// - Reason becomes: "MultipleComponents[Not]" + condType.
// - Messages are concatenated.
// If the new condition has a higher-priority status, it overrides the current one.
// Status priority (highest to lowest): True, Unknown, False, empty.
func joinConditions(currCond, newCond configv1.ClusterOperatorStatusCondition) configv1.ClusterOperatorStatusCondition {
	switch newCond.Status {
	case configv1.ConditionTrue:
		if currCond.Status == configv1.ConditionTrue {
			currCond.Reason += "And" + newCond.Reason
			currCond.Message += newCond.Message
		} else {
			// Degraded=True status overrides other statuses.
			currCond.Status = newCond.Status
			currCond.Reason = newCond.Reason
			currCond.Message = newCond.Message
		}
	case configv1.ConditionUnknown:
		if currCond.Status == configv1.ConditionUnknown {
			currCond.Reason += "And" + newCond.Reason
			currCond.Message += newCond.Message
		} else if currCond.Status != configv1.ConditionTrue {
			// Degraded=Unknown overrides false and empty statuses.
			currCond.Status = newCond.Status
			currCond.Reason = newCond.Reason
			currCond.Message = newCond.Message
		}
	case configv1.ConditionFalse:
		if currCond.Status == configv1.ConditionFalse {
			currCond.Reason += "And" + newCond.Reason
			currCond.Message += newCond.Message
		} else if currCond.Status == "" {
			// Degraded=False overrides empty status.
			currCond.Status = newCond.Status
			currCond.Reason = newCond.Reason
			currCond.Message = newCond.Message
		}
	}
	return currCond
}
