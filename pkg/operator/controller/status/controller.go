package status

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	oputil "github.com/openshift/cluster-ingress-operator/pkg/util"

	corev1 "k8s.io/api/core/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
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

	ingressesEqualConditionMessage = "desired and current number of IngressControllers are equal"

	// gatewaysResourceName is the name of the Gateway API gateways CRD.
	gatewaysResourceName = "gateways.gateway.networking.k8s.io"
	// gatewayclassesResourceName is the name of the Gateway API
	// gatewayclasses CRD.
	gatewayclassesResourceName = "gatewayclasses.gateway.networking.k8s.io"
	// istiosResourceName is the name of the Sail Operator istios CRD.
	istiosResourceName = "istios.sailoperator.io"

	controllerName = "status_controller"
)

var (
	log = logf.Logger.WithName(controllerName)

	// clock is to enable unit testing
	clock utilclock.Clock = utilclock.RealClock{}

	// relatedObjectsCRDs is a set of names of CRDs that we add to
	// relatedObjects if they exist.
	relatedObjectsCRDs = sets.New[string](gatewaysResourceName, gatewayclassesResourceName, istiosResourceName)

	// ossmSubscriptions lists the package names for all OLM subscriptions that are known to conflict with the
	// subscription to OSSM3 created by the Ingress Operator.
	ossmSubscriptions = sets.New[string](
		"sailoperator",
		"servicemeshoperator",
		"servicemeshoperator3",
	)
)

// New creates the status controller. This is the controller that handles all
// the logic for creating the ClusterOperator operator and updating its status.
//
// The controller watches IngressController resources in the manager namespace
// and uses them to compute the operator status.  It also watches the
// clusteroperators resource so that it reconciles the ingress clusteroperator
// in case something else updates or deletes it.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	// The status controller needs to be aware of conflicting OLM subscriptions in any namespace. In order to
	// prevent ballooning the main cache to watch all namespaces on all its informers, create a separate cache for
	// tracking OLM subscriptions across all namespaces.
	var subscriptionCache cache.Cache
	var err error
	if subscriptionCache, err = cache.New(mgr.GetConfig(), cache.Options{}); err != nil {
		return nil, err
	}
	mgr.Add(subscriptionCache)
	reconciler := &reconciler{
		config:            config,
		client:            mgr.GetClient(),
		cache:             operatorCache,
		subscriptionCache: subscriptionCache,
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
	isOpenshiftOperatorNamespace := func(o client.Object) bool {
		return o.GetNamespace() == operatorcontroller.OpenshiftOperatorNamespace
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

	// If the "GatewayAPI" and "GatewayAPIController" featuregates are
	// enabled, watch subscriptions so that this controller can update
	// status when the OSSM subscription is created or updated.  Note that
	// the subscriptions resource only exists if the
	// "OperatorLifecycleManager" capability is enabled, so we cannot watch
	// it if the capability is not enabled.  Additionally, the default
	// catalog only exists if the "marketplace" capability is enabled, so we
	// cannot install OSSM without that capability.
	if config.GatewayAPIEnabled && config.GatewayAPIControllerEnabled && config.MarketplaceEnabled && config.OperatorLifecycleManagerEnabled {
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.Subscription{}, handler.EnqueueRequestsFromMapFunc(toDefaultIngressController), predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return e.Object.GetNamespace() == operatorcontroller.OpenshiftOperatorNamespace
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return e.ObjectNew.GetNamespace() == operatorcontroller.OpenshiftOperatorNamespace
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return e.Object.GetNamespace() == operatorcontroller.OpenshiftOperatorNamespace
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return e.Object.GetNamespace() == operatorcontroller.OpenshiftOperatorNamespace
			},
		})); err != nil {
			return nil, err
		}
		if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(toDefaultIngressController), predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return relatedObjectsCRDs.Has(e.Object.GetName())
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return false
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return relatedObjectsCRDs.Has(e.Object.GetName())
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return false
			},
		})); err != nil {
			return nil, err
		}

		isOSSMSubscription := predicate.NewPredicateFuncs(func(o client.Object) bool {
			subscription, ok := o.(*operatorsv1alpha1.Subscription)
			if !ok || subscription.Spec == nil {
				return false
			}
			return ossmSubscriptions.Has(subscription.Spec.Package)
		})
		if err := c.Watch(source.Kind[client.Object](reconciler.subscriptionCache, &operatorsv1alpha1.Subscription{}, handler.EnqueueRequestsFromMapFunc(toDefaultIngressController), isOSSMSubscription)); err != nil {
			return nil, err
		}

		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.ClusterServiceVersion{}, handler.EnqueueRequestsFromMapFunc(toDefaultIngressController), predicate.NewPredicateFuncs(isOpenshiftOperatorNamespace))); err != nil {
			return nil, err
		}
	}

	return c, nil
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	// GatewayAPIEnabled indicates that the "GatewayAPI" featuregate is enabled.
	GatewayAPIEnabled bool
	// GatewayAPIControllerEnabled indicates that the "GatewayAPIController"
	// featuregate is enabled.
	GatewayAPIControllerEnabled bool
	// MarketplaceEnabled indicates whether the "marketplace" capability is
	// enabled.
	MarketplaceEnabled bool
	// OperatorLifecycleManagerEnabled indicates whether the
	// "OperatorLifecycleManager" capability is enabled.
	OperatorLifecycleManagerEnabled bool
	IngressControllerImage          string
	CanaryImage                     string
	OperatorReleaseVersion          string
	Namespace                       string
	GatewayAPIOperatorVersion       string
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
	config Config

	client            client.Client
	cache             cache.Cache
	subscriptionCache cache.Cache
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

	state, err := r.getOperatorState(ctx, ingressNamespace, canaryNamespace, co)
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
	if r.config.GatewayAPIEnabled && r.config.GatewayAPIControllerEnabled {
		if state.haveOSSMSubscription {
			subscriptionName := operatorcontroller.ServiceMeshOperatorSubscriptionName()
			related = append(related, configv1.ObjectReference{
				Group:     operatorsv1alpha1.GroupName,
				Resource:  "subscriptions",
				Namespace: subscriptionName.Namespace,
				Name:      subscriptionName.Name,
			})
		}
		if state.haveIstiosResource {
			related = append(related, configv1.ObjectReference{
				Group:    sailv1.GroupVersion.Group,
				Resource: "istios",
			})
		}
		if state.haveGatewayclassesResource {
			related = append(related, configv1.ObjectReference{
				Group:    gatewayapiv1.GroupName,
				Resource: "gatewayclasses",
			})
		}
		if state.haveGatewaysResource {
			related = append(related, configv1.ObjectReference{
				Group:     gatewayapiv1.GroupName,
				Resource:  "gateways",
				Namespace: "", // Include all namespaces.
			})
		}
	}

	co.Status.RelatedObjects = related

	allIngressesAvailable := checkAllIngressesAvailable(state.IngressControllers)

	co.Status.Versions = r.computeOperatorStatusVersions(oldStatus.Versions, allIngressesAvailable)

	co.Status.Conditions = mergeConditions(co.Status.Conditions,
		computeOperatorAvailableCondition(state.IngressControllers),
		computeOperatorProgressingCondition(
			state,
			r.config,
			allIngressesAvailable,
			oldStatus.Versions,
			co.Status.Versions,
		),
		computeOperatorDegradedCondition(state),
		computeOperatorUpgradeableCondition(state.IngressControllers),
		computeOperatorEvaluationConditionsDetectedCondition(state.IngressControllers),
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

	unmanagedGatewayAPICRDNames string
	// haveOSSMSubscription means that the subscription for OSSM 3 exists.
	haveOSSMSubscription bool
	// haveIstiosResource means that the "istios.sailproject.io" CRD exists.
	haveIstiosResource bool
	// haveGatewaysResource means that the
	// "gateways.gateway.networking.k8s.io" CRD exists.
	haveGatewaysResource bool
	// haveGatewayclassesResource means that the
	// "gatewayclasses.gateway.networking.k8s.io" CRD exists.
	haveGatewayclassesResource bool
	// ossmSubscriptions contains all subscriptions that may conflict with the operator-created ossm subscription.
	ossmSubscriptions []operatorsv1alpha1.Subscription
	// expectedGatewayAPIOperatorVersion reflects the expected OSSM 3 version. It is used in determining if a
	// user-supplied OSSM 3 subscription would cause the operator's installation of OSSM 3 to fail.
	expectedGatewayAPIOperatorVersion string
	// shouldInstallOSSM reflects whether the ingress operator should install OSSM. Currently, this happens when a
	// gateway class with Spec.ControllerName=operatorcontroller.OpenShiftGatewayClassControllerName is created.
	shouldInstallOSSM bool
	// desiredOSSMVersion contains the desired OSSM operator
	// as recorded in the subscription.
	desiredOSSMVersion string
	// installedOSSMVersion contains the currently installed
	// version of the OSSM operator.
	installedOSSMVersion string
	// currentOSSMVersion contains the OSSM operator version
	// which is currently being installed or the next in the upgrade graph.
	currentOSSMVersion string
	// installedOSSMVersionPhase contains the phase of the installed OSSM operator version.
	installedOSSMVersionPhase operatorsv1alpha1.ClusterServiceVersionPhase
}

// getOperatorState gets and returns the resources necessary to compute the
// operator's current state.
func (r *reconciler) getOperatorState(ctx context.Context, ingressNamespace, canaryNamespace string, co *configv1.ClusterOperator) (operatorState, error) {
	state := operatorState{}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ingressNamespace}}
	if err := r.client.Get(ctx, types.NamespacedName{Name: ingressNamespace}, ns); err != nil {
		if !errors.IsNotFound(err) {
			return state, fmt.Errorf("failed to get namespace %q: %v", ingressNamespace, err)
		}
	} else {
		state.IngressNamespace = ns
	}

	ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: canaryNamespace}}
	if err := r.client.Get(ctx, types.NamespacedName{Name: canaryNamespace}, ns); err != nil {
		if !errors.IsNotFound(err) {
			return state, fmt.Errorf("failed to get namespace %q: %v", canaryNamespace, err)
		}
	} else {
		state.CanaryNamespace = ns
	}

	ingressList := &operatorv1.IngressControllerList{}
	if err := r.cache.List(ctx, ingressList, client.InNamespace(r.config.Namespace)); err != nil {
		return state, fmt.Errorf("failed to list ingresscontrollers in %q: %v", r.config.Namespace, err)
	} else {
		state.IngressControllers = ingressList.Items
	}

	if r.config.GatewayAPIEnabled {
		if len(co.Status.Extension.Raw) > 0 {
			extension := &IngressOperatorStatusExtension{}
			if err := json.Unmarshal(co.Status.Extension.Raw, extension); err != nil {
				return state, fmt.Errorf("failed to unmarshal status extension of cluster operator %q: %w", co.Name, err)
			}
			state.unmanagedGatewayAPICRDNames = extension.UnmanagedGatewayAPICRDNames
		}

		if r.config.GatewayAPIControllerEnabled && r.config.MarketplaceEnabled && r.config.OperatorLifecycleManagerEnabled {
			var subscription operatorsv1alpha1.Subscription
			subscriptionName := operatorcontroller.ServiceMeshOperatorSubscriptionName()
			if err := r.cache.Get(ctx, subscriptionName, &subscription); err != nil {
				if !errors.IsNotFound(err) {
					return state, fmt.Errorf("failed to get subscription %q: %v", subscriptionName, err)
				}
			} else {
				state.haveOSSMSubscription = true

				// To compute the OSSM operator's progressing status,
				// we need to inspect the CSV level to determine whether
				// the installedCSV of the subscription succeeded.
				var installedCSV operatorsv1alpha1.ClusterServiceVersion
				installedCSVName := types.NamespacedName{
					Name:      subscription.Status.InstalledCSV,
					Namespace: operatorcontroller.OpenshiftOperatorNamespace,
				}
				if err := r.cache.Get(ctx, installedCSVName, &installedCSV); err != nil {
					if !errors.IsNotFound(err) {
						return state, fmt.Errorf("failed to get installed CSV %q: %v", installedCSVName.Name, err)
					}
				} else {
					state.installedOSSMVersionPhase = installedCSV.Status.Phase
				}
				// StartingCSV is set by the gatewayclass controller to record
				// the desired operator version, even if this field is noop for OLM
				// after the operator was installed.
				state.desiredOSSMVersion = subscription.Spec.StartingCSV
				state.installedOSSMVersion = subscription.Status.InstalledCSV
				state.currentOSSMVersion = subscription.Status.CurrentCSV
			}

			var (
				crd                                  apiextensionsv1.CustomResourceDefinition
				gatewaysResourceNamespacedName       = types.NamespacedName{Name: gatewaysResourceName}
				gatewayclassesResourceNamespacedName = types.NamespacedName{Name: gatewayclassesResourceName}
				istiosResourceNamespacedName         = types.NamespacedName{Name: istiosResourceName}
			)

			if err := r.cache.Get(ctx, gatewaysResourceNamespacedName, &crd); err != nil {
				if !errors.IsNotFound(err) {
					return state, fmt.Errorf("failed to get CRD %q: %v", gatewaysResourceName, err)
				}
			} else {
				state.haveGatewaysResource = true
			}
			if err := r.cache.Get(ctx, gatewayclassesResourceNamespacedName, &crd); err != nil {
				if !errors.IsNotFound(err) {
					return state, fmt.Errorf("failed to get CRD %q: %v", gatewayclassesResourceName, err)
				}
			} else {
				state.haveGatewayclassesResource = true
			}
			if err := r.cache.Get(ctx, istiosResourceNamespacedName, &crd); err != nil {
				if !errors.IsNotFound(err) {
					return state, fmt.Errorf("failed to get CRD %q: %v", istiosResourceName, err)
				}
			} else {
				state.haveIstiosResource = true
			}

			state.expectedGatewayAPIOperatorVersion = r.config.GatewayAPIOperatorVersion
			subscriptionList := operatorsv1alpha1.SubscriptionList{}
			if err := r.subscriptionCache.List(ctx, &subscriptionList); err != nil {
				return state, fmt.Errorf("failed to get subscriptions: %w", err)
			}
			for _, subscription := range subscriptionList.Items {
				if subscription.Spec != nil && ossmSubscriptions.Has(subscription.Spec.Package) {
					state.ossmSubscriptions = append(state.ossmSubscriptions, subscription)
				}
			}

			gatewayClassList := gatewayapiv1.GatewayClassList{}
			if err := r.cache.List(ctx, &gatewayClassList, client.MatchingFields{
				operatorcontroller.GatewayClassIndexFieldName: operatorcontroller.OpenShiftGatewayClassControllerName,
			}); err != nil {
				return state, fmt.Errorf("failed to list gateway classes: %w", err)
			}
			// If one or more gateway classes have ControllerName=operatorcontroller.OpenShiftGatewayClassControllerName,
			// the ingress operator should try to install OSSM.
			state.shouldInstallOSSM = (len(gatewayClassList.Items) > 0)
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
		computeGatewayAPIInstallDegradedCondition,
		computeOSSMOperatorDegradedCondition,
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

// computeGatewayAPIInstallDegradedCondition computes the degraded condition for the Gateway API OSSM subscription. It
// checks for known conflicting subscriptions, as well as already existing subscription(s) to OSSM3, and reports
// degraded when any of those subscriptions would prevent the installation of an appropriate Istio control plane.
func computeGatewayAPIInstallDegradedCondition(state operatorState) configv1.ClusterOperatorStatusCondition {
	degradedCondition := configv1.ClusterOperatorStatusCondition{}

	// If OSSM doesn't need to be installed, or there are no possible conflicting subscriptions, return degraded=false.
	if !state.shouldInstallOSSM || len(state.ossmSubscriptions) == 0 {
		return degradedCondition
	}

	conflicts := []string{}
	warnings := []string{}
	for _, subscription := range state.ossmSubscriptions {
		if subscription.Spec.Package == "servicemeshoperator3" {
			if _, found := subscription.Annotations[operatorcontroller.IngressOperatorOwnedAnnotation]; found {
				// The subscription that the ingress operator creates naturally does not conflict with itself.
				continue
			}
			if subscription.Status.InstalledCSV == "" {
				// The subscription hasn't finished its install. We will get another reconcile request once the
				// installation is complete, so we can ignore this for now.
				continue
			}
			versionDiff, err := compareVersionNums(subscription.Status.InstalledCSV, state.expectedGatewayAPIOperatorVersion)
			switch {
			case err != nil:
				warnings = append(warnings, fmt.Sprintf("failed to compare installed OSSM version to expected: %v", err))
			case versionDiff < 0:
				// Installed version is newer than expected. Gateway API install may still work if the correct Istio
				// version is supported. Warn the user that the installed OSSM version may be incompatible.
				warnings = append(warnings, fmt.Sprintf("Found version %s, but operator-managed Gateway API expects version %s. Operator-managed Gateway API may not work as intended.", subscription.Status.InstalledCSV, state.expectedGatewayAPIOperatorVersion))
			case versionDiff > 0:
				// Installed version is older than expected. Gateway API install will not work, since the correct Istio
				// version won't be supported.
				conflicts = append(conflicts, fmt.Sprintf("Installed version %s does not support operator-managed Gateway API. Install version %s or uninstall %s/%s to enable functionality.", subscription.Status.InstalledCSV, state.expectedGatewayAPIOperatorVersion, subscription.Namespace, subscription.Name))
			case versionDiff == 0:
				// Installed version is exactly as expected. Nothing to do.
			}
		} else {
			conflicts = append(conflicts, fmt.Sprintf("Package %s from subscription %s/%s prevents enabling operator-managed Gateway API. Uninstall %s/%s to enable functionality.", subscription.Spec.Package, subscription.Namespace, subscription.Name, subscription.Namespace, subscription.Name))
		}
	}
	if len(conflicts) > 0 {
		degradedCondition.Status = configv1.ConditionTrue
		degradedCondition.Reason = "GatewayAPIInstallConflict"
		degradedCondition.Message = strings.Join(conflicts, "\n")
	} else if len(warnings) > 0 {
		// Warnings are not enough to set degraded=true, but should still be included in the status message to warn
		// users to possible issues. Leave status=false and reason unset, but put warning messages in the message field.
		degradedCondition.Status = configv1.ConditionFalse
		degradedCondition.Reason = "GatewayAPIInstallWarnings"
		degradedCondition.Message = strings.Join(warnings, "\n")
	}

	return degradedCondition
}

// computeOSSMOperatorDegradedCondition computes the degraded condition for OSSM operator.
func computeOSSMOperatorDegradedCondition(state operatorState) configv1.ClusterOperatorStatusCondition {
	degradedCondition := configv1.ClusterOperatorStatusCondition{}

	if state.haveOSSMSubscription {
		if state.installedOSSMVersionPhase == operatorsv1alpha1.CSVPhaseFailed {
			degradedCondition.Status = configv1.ConditionTrue
			degradedCondition.Reason = "OSSMOperatorDegraded"
			degradedCondition.Message = fmt.Sprintf("OSSM operator failed to install version %q", state.installedOSSMVersion)
		}
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
func computeOperatorProgressingCondition(state operatorState, config Config, allIngressesAvailable bool, oldVersions, curVersions []configv1.OperandVersion) configv1.ClusterOperatorStatusCondition {
	progressingCondition := configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorProgressing,
	}

	icCondition := computeIngressControllersProgressingCondition(state.IngressControllers, allIngressesAvailable, oldVersions, curVersions, config.OperatorReleaseVersion, config.IngressControllerImage, config.CanaryImage)
	ossmCondition := computeOSSMOperatorProgressingCondition(state)

	return joinConditions(joinConditions(progressingCondition, icCondition), ossmCondition)
}

// computeIngressControllersProgressingCondition computes the IngressControllers' current Progressing status state.
func computeIngressControllersProgressingCondition(ingresscontrollers []operatorv1.IngressController, allIngressesAvailable bool, oldVersions, curVersions []configv1.OperandVersion, operatorReleaseVersion, ingressControllerImage string, canaryImage string) configv1.ClusterOperatorStatusCondition {
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

// computeOSSMOperatorProgressingCondition computes the OSSM operator's current Progressing status state.
func computeOSSMOperatorProgressingCondition(state operatorState) configv1.ClusterOperatorStatusCondition {
	progressingCondition := configv1.ClusterOperatorStatusCondition{}

	// Skip updating the progressing status if OSSM operator subscription doesn't exist.
	if !state.haveOSSMSubscription {
		return progressingCondition
	}

	progressing, failed := false, false

	// Set the progressing to true if the desired version is different
	// from the currently installed.
	if state.desiredOSSMVersion != state.installedOSSMVersion {
		// Note that the progressing state remains "true" if the desired version is
		// beyond the latest available in the upgrade graph.
		// There is no reliable way of knowing that the end of the upgrade graph
		// is reached and no next version is available. None of OLM resources
		// can provide such information.
		progressing = true
	} else {
		// The desired version was reached. However the CSV may still be
		// in "Replacing" or "Installing" state, stop setting progressing
		// when the installed CSV succeeded.
		if state.installedOSSMVersionPhase != operatorsv1alpha1.CSVPhaseSucceeded {
			progressing = true
		}
	}

	// Set Progressing=False if the installation failed.
	if state.installedOSSMVersionPhase == operatorsv1alpha1.CSVPhaseFailed {
		failed = true
		progressing = false
	}

	if progressing {
		progressingCondition.Status = configv1.ConditionTrue
		progressingCondition.Reason = "OSSMOperatorUpgrading"
		progressingCondition.Message = fmt.Sprintf("OSSM operator is upgrading to version %q", state.desiredOSSMVersion)
		if len(state.installedOSSMVersion) > 0 && len(state.currentOSSMVersion) > 0 {
			progressingCondition.Message += fmt.Sprintf(" (installed(-ing):%q,next:%q)", state.installedOSSMVersion, state.currentOSSMVersion)
		}
	} else {
		progressingCondition.Status = configv1.ConditionFalse
		if !failed {
			progressingCondition.Reason = "OSSMOperatorUpToDate"
			progressingCondition.Message = fmt.Sprintf("OSSM operator is running version %q", state.desiredOSSMVersion)
		} else {
			progressingCondition.Reason = "OSSMOperatorInstallFailed"
			progressingCondition.Message = fmt.Sprintf("OSSM operator failed to install version %q", state.installedOSSMVersion)
		}
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
// - Reasons are concatenated using "And" as a delimiter.
// - Messages are concatenated.
// If the new condition has a higher-priority status, it overrides the current one.
// Status priority (highest to lowest): True, Unknown, False, empty.
func joinConditions(currCond, newCond configv1.ClusterOperatorStatusCondition) configv1.ClusterOperatorStatusCondition {
	switch newCond.Status {
	case configv1.ConditionTrue:
		if currCond.Status == configv1.ConditionTrue {
			currCond.Reason += "And" + newCond.Reason
			currCond.Message += "\n" + newCond.Message
		} else {
			// Degraded=True status overrides other statuses.
			currCond.Status = newCond.Status
			currCond.Reason = newCond.Reason
			currCond.Message = newCond.Message
		}
	case configv1.ConditionUnknown:
		if currCond.Status == configv1.ConditionUnknown {
			currCond.Reason += "And" + newCond.Reason
			currCond.Message += "\n" + newCond.Message
		} else if currCond.Status != configv1.ConditionTrue {
			// Degraded=Unknown overrides false and empty statuses.
			currCond.Status = newCond.Status
			currCond.Reason = newCond.Reason
			currCond.Message = newCond.Message
		}
	case configv1.ConditionFalse:
		if currCond.Status == configv1.ConditionFalse {
			currCond.Reason += "And" + newCond.Reason
			currCond.Message += "\n" + newCond.Message
		} else if currCond.Status == "" {
			// Degraded=False overrides empty status.
			currCond.Status = newCond.Status
			currCond.Reason = newCond.Reason
			currCond.Message = newCond.Message
		}
	}
	return currCond
}

// compareVersionNums compares two strings of format "<name>.v#.#.#". It returns a negative number if 'a' has a higher
// version number, a positive number if 'b' has a higher version number, or 0 if the version numbers are identical. If
// it is unable to parse either version number, or if the names from the two version strings differ, an error is
// returned and the comparison should be considered invalid.
func compareVersionNums(a, b string) (int, error) {
	var aName, bName string
	var aX, aY, aZ, bX, bY, bZ int
	aSplit := strings.Split(a, ".")
	if len(aSplit) != 4 {
		return 0, fmt.Errorf("%q does not match expected format", a)
	}
	aName = aSplit[0]
	aX, _ = strconv.Atoi(aSplit[1][1:]) // X has format "v%d", so [1:] cuts out the "v"
	aY, _ = strconv.Atoi(aSplit[2])
	aZ, _ = strconv.Atoi(aSplit[3])
	bSplit := strings.Split(b, ".")
	if len(bSplit) != 4 {
		return 0, fmt.Errorf("%q does not match expected format", b)
	}
	bName = bSplit[0]
	bX, _ = strconv.Atoi(bSplit[1][1:]) // X has format "v%d", so [1:] cuts out the "v"
	bY, _ = strconv.Atoi(bSplit[2])
	bZ, _ = strconv.Atoi(bSplit[3])
	if aName != bName {
		return 0, fmt.Errorf("%q and %q are different packages. cannot compare version numbers", a, b)
	}
	if aX != bX {
		return bX - aX, nil
	}
	if aY != bY {
		return bY - aY, nil
	}
	return bZ - aZ, nil
}
