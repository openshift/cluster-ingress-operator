package gatewayclass

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/chart"
	"github.com/istio-ecosystem/sail-operator/pkg/install"
	"github.com/istio-ecosystem/sail-operator/resources"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "gatewayclass_controller"

	// inferencepoolCrdName is the name of the InferencePool CRD from
	// Gateway API Inference Extension.
	inferencepoolCrdName = "inferencepools.inference.networking.k8s.io"
	// inferencepoolExperimentalCrdName is the name of the experimental
	// (alpha version) InferencePool CRD.
	inferencepoolExperimentalCrdName = "inferencepools.inference.networking.x-k8s.io"

	// subscriptionCatalogOverrideAnnotationKey is the key for an
	// unsupported annotation on the gatewayclass using which a custom
	// catalog source can be specified for the OSSM subscription.  This
	// annotation is only intended for use by OpenShift developers.  Note
	// that this annotation is intended to be used only when initially
	// creating the gatewayclass and subscription; changing the catalog
	// source on an existing subscription will likely have no effect or
	// cause errors.
	subscriptionCatalogOverrideAnnotationKey = "unsupported.do-not-use.openshift.io/ossm-catalog"
	// subscriptionChannelOverrideAnnotationKey is the key for an
	// unsupported annotation on the gatewayclass using which a custom
	// channel can be specified for the OSSM subscription.  This annotation
	// is only intended for use by OpenShift developers.  Note that this
	// annotation is intended to be used only when initially creating the
	// gatewayclass and subscription; changing the channel on an existing
	// subscription will likely have no effect or cause errors.
	subscriptionChannelOverrideAnnotationKey = "unsupported.do-not-use.openshift.io/ossm-channel"
	// subscriptionVersionOverrideAnnotationKey is the key for an
	// unsupported annotation on the gatewayclass using which a custom
	// version of OSSM can be specified.  This annotation is only intended
	// for use by OpenShift developers.  Note that this annotation is
	// intended to be used only when initially creating the gatewayclass and
	// subscription; OLM will not allow downgrades, and upgrades are
	// generally restricted to the next version after the currently
	// installed version.
	subscriptionVersionOverrideAnnotationKey = "unsupported.do-not-use.openshift.io/ossm-version"
	// istioVersionOverrideAnnotationKey is the key for an unsupported
	// annotation on the gatewayclass using which a custom version of Istio
	// can be specified.  This annotation is only intended for use by
	// OpenShift developers.
	istioVersionOverrideAnnotationKey = "unsupported.do-not-use.openshift.io/istio-version"
	// gatewayProxyContainerName is the name of the proxy container
	// in gateway deployments managed by Istio.
	gatewayProxyContainerName = "istio-proxy"
	// sailLibraryFinalizer is added to GatewayClasses using Sail Library installation.
	// When a GatewayClass with this finalizer is deleted:
	// 1. Sail Library mode: Uninstall Istio if this is the last GatewayClass, then remove finalizer
	// 2. Downgrade to OLM: Clean up Sail Library status and finalizer (then OLM takes over Istio)
	sailLibraryFinalizer = "openshift.io/ingress-operator-sail-finalizer"

	// syncAnnotation is set on the GatewayClass when the istiod
	// deployment becomes available. This triggers a GatewayClass watch
	// event in istiod, which re-enqueues any Gateways that were dropped
	// during the startup race between the gateway deployment controller
	// and PushContext initialization.
	// TODO: Remove when https://github.com/istio/istio/issues/61095 is resolved.
	syncAnnotation = "ingress.operator.openshift.io/sync"
)

type extraIstioConfig struct {
	proxyConfig     *configv1.Proxy
	infraConfig     *configv1.Infrastructure
	apiserverConfig *configv1.APIServer
}

var log = logf.Logger.WithName(controllerName)
var gatewayClassController controller.Controller

// NewUnmanaged creates and returns a controller that watches gatewayclasses and
// installs and configures Istio.  This is an unmanaged controller, which means
// that the manager does not start it. It also returns a SailUninstaller that
// the gatewayapi controller can use to trigger Sail uninstall on mode transitions.
func NewUnmanaged(mgr manager.Manager, config Config, modeAccessor *operatorcontroller.ModeAccessor) (controller.Controller, operatorcontroller.SailUninstaller, error) {
	operatorCache := mgr.GetCache()

	reconciler := &reconciler{
		config:       config,
		client:       mgr.GetClient(),
		cache:        operatorCache,
		recorder:     mgr.GetEventRecorderFor(controllerName),
		modeAccessor: modeAccessor,
	}
	options := controller.Options{Reconciler: reconciler}
	options.DefaultFromConfig(mgr.GetControllerOptions())
	c, err := controller.NewUnmanaged(controllerName, options)
	if err != nil {
		return nil, nil, err
	}
	// dependentPred filters out watch events when the management mode
	// does not allow dependent controllers to act. This prevents the
	// gatewayclass controller from reacting to operational resource
	// changes while in Unmanaged mode.
	dependentPred := modeAccessor.DependentPredicate()

	isOurGatewayClass := predicate.NewPredicateFuncs(func(o client.Object) bool {
		class := o.(*gatewayapiv1.GatewayClass)
		return class.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName
	})
	notIstioGatewayClass := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() != "istio"
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.GatewayClass{}, reconciler.enqueueRequestForSomeGatewayClass(), isOurGatewayClass, notIstioGatewayClass, dependentPred)); err != nil {
		return nil, nil, err
	}

	isOurInstallPlan := predicate.NewPredicateFuncs(func(o client.Object) bool {
		installPlan := o.(*operatorsv1alpha1.InstallPlan)
		if len(installPlan.Spec.ClusterServiceVersionNames) > 0 {
			if slices.Contains(installPlan.Spec.ClusterServiceVersionNames, config.GatewayAPIOperatorVersion) {
				return true
			}
		}
		return false
	})
	// Check if an InstallPlan is ready for approval. This requires that both the spec.approved field is false and that
	// the status.phase is "RequiresApproval" to make sure OLM is done modifying the InstallPlan before it can be
	// approved.
	isInstallPlanReadyForApproval := predicate.NewPredicateFuncs(func(o client.Object) bool {
		installPlan := o.(*operatorsv1alpha1.InstallPlan)
		return !installPlan.Spec.Approved && installPlan.Status.Phase == operatorsv1alpha1.InstallPlanPhaseRequiresApproval
	})

	// Watch for the InferencePool CRD to determine whether to enable
	// Gateway API Inference Extension (GIE) on the Istio control-plane.
	isInferencepoolCrd := predicate.NewPredicateFuncs(func(o client.Object) bool {
		switch o.GetName() {
		case inferencepoolCrdName, inferencepoolExperimentalCrdName:
			return true
		default:
			return false
		}
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, reconciler.enqueueRequestForSomeGatewayClass(), isInferencepoolCrd, dependentPred)); err != nil {
		return nil, nil, err
	}

	// Watch for Proxy configuration to set the right options on Istio resource
	isClusterProxy := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == "cluster"
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &configv1.Proxy{}, reconciler.enqueueRequestForSomeGatewayClass(), isClusterProxy, dependentPred)); err != nil {
		return nil, nil, err
	}

	if !config.GatewayAPIWithoutOLMEnabled {
		isServiceMeshSubscription := predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetName() == operatorcontroller.ServiceMeshOperatorSubscriptionName().Name
		})
		if err = c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.Subscription{},
			reconciler.enqueueRequestForSomeGatewayClass(), isServiceMeshSubscription, dependentPred)); err != nil {
			return nil, nil, err
		}
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.InstallPlan{}, reconciler.enqueueRequestForSomeGatewayClass(), isOurInstallPlan, isInstallPlanReadyForApproval, dependentPred)); err != nil {
			return nil, nil, err
		}
	} else {
		// Start the Sail Library's background reconciliation loop (runs in a goroutine).
		// Returns a notification channel that signals when library reconciliation completes,
		// allowing us to update GatewayClass status conditions accordingly.
		installer, err := install.New(mgr.GetConfig(), resources.FS, chart.CRDsFS,
			install.WithCRDOwnershipLabel(operatorcontroller.IngressOperatorOwnedAnnotation, "true"),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize sail-operator installation library: %w", err)
		}
		notifyCh, err := installer.Start(config.Context)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to start sail-operator installation library: %w", err)
		}
		reconciler.sailInstaller = installer

		// Reconciliation Triggers with the Sail Library:
		//
		// 1. CIO-initiated reconciliation (GatewayClass controller triggers):
		//    Watches GatewayClass resources, OLM Subscriptions, OLM InstallPlans, and Istio/GIE CRDs.
		//    All events trigger GatewayClass reconciliation, which computes Options and calls
		//    sailInstaller.Apply(). For CRD management events (Subscription/InstallPlan/Istio CRD changes),
		//    we additionally call sailInstaller.Enqueue() to trigger CRD ownership re-evaluation.
		//
		// 2. Sail Library-initiated reconciliation (notification channel):
		//    The Sail Library runs its own reconciliation loop with drift detection for Istio
		//    Helm-managed resources (Deployments, Services, ConfigMaps, etc.). When reconciliation
		//    completes (install, uninstall, drift repair, or error), it signals via notifyCh,
		//    which triggers reconciliation to update GatewayClass status conditions.
		isIstioCRD := predicate.NewPredicateFuncs(func(o client.Object) bool {
			return strings.Contains(o.GetName(), "istio.io")
		})
		if config.OperatorLifecycleManagerEnabled {
			isServiceMeshSubscription := predicate.NewPredicateFuncs(func(o client.Object) bool {
				sub, ok := o.(*operatorsv1alpha1.Subscription)
				if !ok {
					return false
				}
				return sub.Spec != nil && strings.HasPrefix(sub.Spec.Package, "servicemeshoperator")
			})

			if err = c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.Subscription{}, reconciler.enqueueRequestForCRDOwnershipChange(), isServiceMeshSubscription, dependentPred)); err != nil {
				return nil, nil, err
			}
			if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.InstallPlan{}, reconciler.enqueueRequestForCRDOwnershipChange(), isOurInstallPlan, dependentPred)); err != nil {
				return nil, nil, err
			}
		}
		if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, reconciler.enqueueRequestForCRDOwnershipChange(), isIstioCRD, dependentPred)); err != nil {
			return nil, nil, err
		}
		if err := c.Watch(&SailLibrarySource[client.Object]{NotifyCh: notifyCh, RequestsFunc: reconciler.requestsForAllManagedGatewayClasses}); err != nil {
			return nil, nil, err
		}

		// Watch the istiod deployment so that when it becomes available,
		// we can annotate the GatewayClass to trigger a re-enqueue of
		// any Gateways that were dropped during istiod's startup race.
		isIstiodDeployment := predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetNamespace() == config.OperandNamespace && o.GetName() == "istiod-"+operatorcontroller.IstioName("").Name
		})
		if err := c.Watch(source.Kind[client.Object](operatorCache, &appsv1.Deployment{}, reconciler.enqueueRequestForSomeGatewayClass(), isIstiodDeployment, dependentPred)); err != nil {
			return nil, nil, fmt.Errorf("failed to watch istiod deployment: %w", err)
		}
	}

	// Watch the cluster infrastructure config in case the infrastructure
	// topology changes.
	if err := c.Watch(source.Kind[client.Object](operatorCache, &configv1.Infrastructure{}, reconciler.enqueueRequestForSomeGatewayClass(), dependentPred)); err != nil {
		return nil, nil, err
	}

	// Watch the cluster TLSProfile config for changes
	isClusterAPIServerConfig := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == "cluster"
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &configv1.APIServer{}, reconciler.enqueueRequestForSomeGatewayClass(), isClusterAPIServerConfig, dependentPred)); err != nil {
		return nil, nil, err
	}

	// Watch the istiod network policy.
	isIstiodNetworkPolicy := predicate.NewPredicateFuncs(func(o client.Object) bool {
		istiodNetworkPolicyName := operatorcontroller.IstiodNetworkPolicyName()
		return o.GetNamespace() == istiodNetworkPolicyName.Namespace && o.GetName() == istiodNetworkPolicyName.Name
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &networkingv1.NetworkPolicy{}, reconciler.enqueueRequestForSomeGatewayClass(), isIstiodNetworkPolicy, dependentPred)); err != nil {
		return nil, nil, err
	}

	// Watch Ingress CR for mode changes (wake-up watch).
	// When the management mode transitions from Unmanaged to Managed,
	// this triggers re-reconciliation of all managed GatewayClasses.
	// Only register when the management mode gate is enabled because
	// the Ingress CRD only exists on TechPreview/DevPreview clusters.
	if modeAccessor.GateEnabled() {
		ingressToGatewayClasses := func(ctx context.Context, _ client.Object) []reconcile.Request {
			return reconciler.requestsForAllManagedGatewayClasses(ctx, nil)
		}
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1alpha1.Ingress{}, handler.EnqueueRequestsFromMapFunc(ingressToGatewayClasses))); err != nil {
			return nil, nil, fmt.Errorf("failed to watch Ingress for wake-up: %w", err)
		}
	}

	gatewayClassController = c
	return c, reconciler, nil
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	// OperatorNamespace is the namespace in which the operator is deployed.
	OperatorNamespace string
	// OperandNamespace is the namespace in which Istio should be deployed.
	OperandNamespace string
	// GatewayAPIOperatorCatalog is the catalog source to use to install the Gateway API implementation.
	GatewayAPIOperatorCatalog string
	// GatewayAPIOperatorChannel is the release channel of the Gateway API implementation to install.
	GatewayAPIOperatorChannel string
	// GatewayAPIOperatorVersion is the name and release of the Gateway API implementation to install.
	GatewayAPIOperatorVersion string
	// GatewayAPIWithoutOLMEnabled indicates whether the GatewayAPIWithoutOLM feature gate is enabled.
	GatewayAPIWithoutOLMEnabled bool
	// OperatorLifecycleManagerEnabled indicates whether the OperatorLifecycleManager capability is enabled.
	OperatorLifecycleManagerEnabled bool
	// IstioVersion is the version of Istio to install.
	IstioVersion string
	// Context is the context for controller lifecycle.
	Context context.Context
}

// SailLibraryInstaller implements the methods of sail library but in a way we can
// also mock and test
type SailLibraryInstaller interface {
	Start(ctx context.Context) (<-chan struct{}, error)
	Apply(opts install.Options) error
	Uninstall(ctx context.Context, namespace, revision string) error
	Status() install.Status
	Enqueue()
}

// reconciler reconciles gatewayclasses.
type reconciler struct {
	config Config

	client   client.Client
	cache    cache.Cache
	recorder record.EventRecorder

	startIstioWatch sync.Once

	// sailInstaller manages Istio control plane lifecycle (install, upgrade, uninstall) via the sail library.
	sailInstaller SailLibraryInstaller

	// sailLifecycleMu serializes "check AllowDependents, then act" between
	// UninstallSail and ensureIstio's Apply call, so a concurrent Uninstall
	// cannot race with an in-flight Apply that read AllowDependents()
	// before the mode transitioned. Whichever critical section runs last
	// observes the true, up-to-date mode state.
	sailLifecycleMu sync.Mutex

	// modeAccessor provides thread-safe access to the resolved Gateway API
	// management mode. When AllowDependents returns false, the reconciler
	// skips Sail/OLM installation to prevent resource creation in Unmanaged mode.
	modeAccessor *operatorcontroller.ModeAccessor
}

// enqueueRequestForCRDOwnershipChange handles events that may affect CRD ownership.
// This is used for OLM Subscriptions, InstallPlans, and Istio CRDs. When these
// resources change, CRD ownership may transition between OLM and CIO management.
//
// Calls sailInstaller.Enqueue() to trigger the Sail Library to re-evaluate which
// CRDs it can manage, then enqueues reconciliation requests for all GatewayClasses
// so they can update their status based on the new installation state.
func (r *reconciler) enqueueRequestForCRDOwnershipChange() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			// We can call Enqueue as many times as we want, as sail-library should enqueue and filter
			// and not make concurrent operations
			r.sailInstaller.Enqueue()
			return r.requestsForAllManagedGatewayClasses(ctx, obj)
		})
}

// enqueueRequestForSomeGatewayClass enqueues GatewayClass reconciliation.
// Sail Library mode: all classes. OLM mode: oldest class only (to avoid subscription conflicts).
func (r *reconciler) enqueueRequestForSomeGatewayClass() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			if r.config.GatewayAPIWithoutOLMEnabled {
				return r.requestsForAllManagedGatewayClasses(ctx, obj)
			}
			return r.requestsForSomeGatewayClass(ctx, obj)
		},
	)
}

// requestsForSomeGatewayClass returns a reconciliation request for the
// gatewayclass that has the earliest creation timestamp and that specifies our
// controller name.
func (r *reconciler) requestsForSomeGatewayClass(ctx context.Context, _ client.Object) []reconcile.Request {
	requests := []reconcile.Request{}
	var gatewayClasses gatewayapiv1.GatewayClassList
	if err := r.cache.List(ctx, &gatewayClasses); err != nil {
		log.Error(err, "Failed to list gatewayclasses")

		return requests
	}

	var (
		found  bool
		oldest metav1.Time
		name   string
	)
	for i := range gatewayClasses.Items {
		if gatewayClasses.Items[i].Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName {
			continue
		}

		// If we ever added the sail library finalizer, this means this is a rollback so
		// we need to be sure that the OLM process removes the finalizer and the status
		if controllerutil.ContainsFinalizer(&gatewayClasses.Items[i], sailLibraryFinalizer) {
			request := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: "",
					Name:      gatewayClasses.Items[i].Name,
				},
			}
			requests = append(requests, request)
			continue
		}

		ctime := gatewayClasses.Items[i].CreationTimestamp
		if !found || ctime.Before(&oldest) {
			found, oldest, name = true, ctime, gatewayClasses.Items[i].Name
		}
	}

	if found {
		request := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: "", // GatewayClass is cluster-scoped.
				Name:      name,
			},
		}
		requests = append(requests, request)
	}

	return requests
}

// requestsForAllManagedGatewayClasses enqueues all GatewayClasses managed by this controller.
// Used when shared installation state changes (Sail Library events, CRD ownership).
func (r *reconciler) requestsForAllManagedGatewayClasses(ctx context.Context, _ client.Object) []reconcile.Request {
	requests := []reconcile.Request{}
	var gatewayClasses gatewayapiv1.GatewayClassList
	if err := r.cache.List(ctx, &gatewayClasses, client.MatchingFields{
		operatorcontroller.GatewayClassIndexFieldName: operatorcontroller.OpenShiftGatewayClassControllerName,
	}); err != nil {
		log.Error(err, "Failed to list gatewayclasses")
		return requests
	}

	for _, class := range gatewayClasses.Items {
		request := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: "", // GatewayClass is cluster-scoped.
				Name:      class.Name,
			},
		}
		requests = append(requests, request)
	}

	return requests
}

// Reconcile expects request to refer to a GatewayClass and creates or
// reconciles an Istio deployment.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if r.config.GatewayAPIWithoutOLMEnabled {
		return r.reconcileWithSailLibrary(ctx, request)
	}
	return r.reconcileWithOLM(ctx, request)
}

// reconcileWithOLM reconciles a GatewayClass using OLM to install OSSM,
// which then manages an Istio CR for the Istio installation.
func (r *reconciler) reconcileWithOLM(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling with OLM", "request", request)
	var errs []error

	var infraConfig configv1.Infrastructure
	if err := r.cache.Get(ctx, types.NamespacedName{Name: "cluster"}, &infraConfig); err != nil {
		return reconcile.Result{}, err
	}

	gatewayclass := &gatewayapiv1.GatewayClass{}
	if err := r.cache.Get(ctx, request.NamespacedName, gatewayclass); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Downgrade scenario: transitioning from Sail Library installation back to OLM-based installation.
	// Clean up Sail Library finalizers and status to allow OLM to take over.
	updatedGatewayClass := gatewayclass.DeepCopy()
	if controllerutil.ContainsFinalizer(updatedGatewayClass, sailLibraryFinalizer) {
		removeSailInstallConditions(&updatedGatewayClass.Status.Conditions)
		if err := r.client.Status().Patch(ctx, updatedGatewayClass, client.MergeFrom(gatewayclass)); err != nil {
			log.Error(err, "error patching the gatewayclass status")
			return reconcile.Result{}, err
		}
		controllerutil.RemoveFinalizer(updatedGatewayClass, sailLibraryFinalizer)
		if err := r.client.Patch(ctx, updatedGatewayClass, client.MergeFrom(gatewayclass)); err != nil {
			log.Error(err, "failed to remove finalizer from gatewayclass")
			return reconcile.Result{}, err
		}

		return reconcile.Result{}, nil // Removing the finalizer should kick a new reconciliation
	}

	// Skip OLM installation when the management mode does not allow
	// dependents. The downgrade (Sail -> OLM) cleanup above still runs
	// so that finalizer removal proceeds even in Unmanaged mode.
	if !r.modeAccessor.AllowDependents() {
		log.Info("Management mode does not allow dependents, skipping OLM installation", "gatewayclass", gatewayclass.Name)
		return reconcile.Result{}, nil
	}

	ossmCatalog := r.config.GatewayAPIOperatorCatalog
	if v, ok := gatewayclass.Annotations[subscriptionCatalogOverrideAnnotationKey]; ok {
		ossmCatalog = v
	}
	ossmChannel := r.config.GatewayAPIOperatorChannel
	if v, ok := gatewayclass.Annotations[subscriptionChannelOverrideAnnotationKey]; ok {
		ossmChannel = v
	}
	ossmVersion := r.config.GatewayAPIOperatorVersion
	if v, ok := gatewayclass.Annotations[subscriptionVersionOverrideAnnotationKey]; ok {
		ossmVersion = v
	}

	_, subscription, err := r.ensureServiceMeshOperatorSubscription(ctx, ossmCatalog, ossmChannel, ossmVersion)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure ServiceMeshOperatorSubscription: %w", err))
	} else if subscription == nil {
		log.Info("No OSSM subscription available; skipping install plan enforcement")
	} else if _, ok := subscription.Annotations[operatorcontroller.IngressOperatorOwnedAnnotation]; !ok {
		log.Info("Found an existing OSSM subscription with another owner; installation skipped",
			"namespace", subscription.Namespace, "name", subscription.Name)
	} else {
		if _, _, err := r.ensureServiceMeshOperatorInstallPlan(ctx, ossmVersion); err != nil {
			errs = append(errs, err)
		}
	}

	istioVersion := r.config.IstioVersion
	if v, ok := gatewayclass.Annotations[istioVersionOverrideAnnotationKey]; ok {
		istioVersion = v
	}
	var gatewayclasses gatewayapiv1.GatewayClassList
	if err := r.cache.List(ctx, &gatewayclasses, client.MatchingFields{operatorcontroller.GatewayClassIndexFieldName: operatorcontroller.OpenShiftGatewayClassControllerName}); err != nil {
		return reconcile.Result{}, err
	}

	if _, _, err := r.ensureIstioOLM(ctx, gatewayclass, istioVersion, gatewayclasses.Items, &infraConfig); err != nil {
		errs = append(errs, err)
	} else {
		// The OSSM operator installs the istios.sailoperator.io CRD.
		// We must create the watch for this resource only after the
		// operator is installed.  We use sync.Once here to start the
		// watch for istios only once.
		r.startIstioWatch.Do(func() {
			isOurIstio := predicate.NewPredicateFuncs(func(o client.Object) bool {
				return o.GetName() == operatorcontroller.IstioName(r.config.OperandNamespace).Name
			})
			if err = gatewayClassController.Watch(source.Kind[client.Object](r.cache, &sailv1.Istio{}, r.enqueueRequestForSomeGatewayClass(), isOurIstio)); err != nil {
				log.Error(err, "failed to watch istios.sailoperator.io", "request", request)
				errs = append(errs, err)
			}
		})
	}
	if _, _, err := r.ensureIstiodNetworkPolicy(ctx); err != nil {
		errs = append(errs, err)
	}

	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}

// reconcileWithSailLibrary reconciles a GatewayClass using the Sail Library
// for direct Helm-based installation of Istio.
func (r *reconciler) reconcileWithSailLibrary(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling with sail library", "request", request)

	var infraConfig configv1.Infrastructure
	if err := r.cache.Get(ctx, types.NamespacedName{Name: "cluster"}, &infraConfig); err != nil {
		return reconcile.Result{}, err
	}

	gatewayClass := &gatewayapiv1.GatewayClass{}
	if err := r.cache.Get(ctx, request.NamespacedName, gatewayClass); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if !gatewayClass.DeletionTimestamp.IsZero() {
		return r.ensureGatewayClassDeleted(ctx, gatewayClass)
	}

	// Skip Sail installation when the management mode does not allow
	// dependents. Deletion handling above still runs so that finalizer
	// cleanup proceeds even in Unmanaged mode.
	if !r.modeAccessor.AllowDependents() {
		log.Info("Management mode does not allow dependents, skipping Sail installation", "gatewayclass", gatewayClass.Name)
		return reconcile.Result{}, nil
	}

	updatedGatewayClass := gatewayClass.DeepCopy()
	if controllerutil.AddFinalizer(updatedGatewayClass, sailLibraryFinalizer) {
		if err := r.client.Patch(ctx, updatedGatewayClass, client.MergeFrom(gatewayClass)); err != nil {
			log.Error(err, "failed to add finalizer to gatewayclass")
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil // Finalizer add successful: watch will trigger another reconciliation
	}

	// Ensure migration from OLM to Sail Library.
	if migrationComplete, err := r.ensureOSSMtoSailLibraryMigration(ctx); err != nil {
		return reconcile.Result{}, fmt.Errorf("error validating sail library migration: %w", err)
	} else if !migrationComplete {
		// Migration isn't complete - give OSSM time to clean up.
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}

	var errs []error

	istioVersion := r.config.IstioVersion
	if v, ok := gatewayClass.Annotations[istioVersionOverrideAnnotationKey]; ok {
		istioVersion = v
	}

	var gatewayclasses gatewayapiv1.GatewayClassList
	if err := r.cache.List(ctx, &gatewayclasses, client.MatchingFields{operatorcontroller.GatewayClassIndexFieldName: operatorcontroller.OpenShiftGatewayClassControllerName}); err != nil {
		return reconcile.Result{}, err
	}

	if err := r.ensureIstio(ctx, istioVersion, gatewayclasses.Items, &infraConfig); err != nil {
		log.Error(err, "failed to ensure Istio")
		errs = append(errs, err)
	}

	// Update status for indicating installation success, CRD management, etc.
	status := r.sailInstaller.Status()
	if changed := mapStatusToConditions(status, gatewayClass.Generation, &updatedGatewayClass.Status.Conditions); changed {
		if err := r.client.Status().Patch(ctx, updatedGatewayClass, client.MergeFrom(gatewayClass)); err != nil {
			log.Error(err, "error patching the gatewayclass status")
			errs = append(errs, err)
		}
	}
	if _, _, err := r.ensureIstiodNetworkPolicy(ctx); err != nil {
		errs = append(errs, err)
	}

	// Annotate the GatewayClass when the istiod deployment is available.
	// This triggers a GatewayClass watch event in istiod that re-enqueues
	// any Gateways dropped during the PushContext startup race.
	if status.Installed {
		if result, err := r.ensureGatewayClassSyncAnnotation(ctx, gatewayClass); err != nil {
			errs = append(errs, err)
		} else if result.RequeueAfter > 0 {
			if len(errs) > 0 {
				return result, utilerrors.NewAggregate(errs)
			}
			return result, nil
		}
	}

	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}

// ensureGatewayClassSyncAnnotation checks if the istiod deployment is
// available and, if so, annotates the GatewayClass to trigger a watch
// event in istiod's gateway deployment controller, which re-enqueues
// all Gateways referencing this class. This works around a startup
// race in istiod where the gateway deployment controller can exhaust
// its retry budget before PushContext is initialized, permanently
// dropping Gateways. The annotation value combines the deployment's
// generation and the Available condition's LastTransitionTime so it
// changes on both spec updates (rolling updates) and restarts.
// TODO: Remove when https://github.com/istio/istio/issues/61095 is resolved.
func (r *reconciler) ensureGatewayClassSyncAnnotation(ctx context.Context, gatewayClass *gatewayapiv1.GatewayClass) (reconcile.Result, error) {
	deployName := types.NamespacedName{
		Namespace: r.config.OperandNamespace,
		Name:      "istiod-" + operatorcontroller.IstioName("").Name,
	}
	deploy := &appsv1.Deployment{}
	if err := r.cache.Get(ctx, deployName, deploy); err != nil {
		if errors.IsNotFound(err) {
			// The deployment doesn't exist yet; the deployment
			// watch will trigger a reconcile when it appears.
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get istiod deployment: %w", err)
	}

	// Wait for the deployment controller to observe the latest spec
	// before checking conditions. During a rolling update, Generation
	// increments immediately but Available=True may be stale from the
	// prior rollout.
	if deploy.Status.ObservedGeneration < deploy.Generation {
		// Rollout in progress; the deployment watch will trigger
		// a reconcile when the status catches up.
		return reconcile.Result{}, nil
	}

	// Build an opaque sync value from the deployment's generation and the
	// Available condition's LastTransitionTime. Generation catches rolling
	// updates (spec changes) where istiod stays Available throughout, and
	// LastTransitionTime catches restarts where the pod goes down and back
	// up. Together they ensure the annotation changes whenever a new
	// istiod instance starts, triggering the workaround kick.
	var syncValue string
	for _, c := range deploy.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			syncValue = fmt.Sprintf("%d-%d", deploy.Generation, c.LastTransitionTime.Unix())
			break
		}
	}
	if syncValue == "" {
		// Not yet available; the deployment watch will trigger
		// a reconcile when the condition changes.
		return reconcile.Result{}, nil
	}

	if gatewayClass.Annotations[syncAnnotation] == syncValue {
		return reconcile.Result{}, nil
	}

	updated := gatewayClass.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = make(map[string]string)
	}
	updated.Annotations[syncAnnotation] = syncValue
	if err := r.client.Patch(ctx, updated, client.MergeFrom(gatewayClass)); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to annotate gatewayclass: %w", err)
	}
	log.Info("annotated gatewayclass to trigger istiod gateway re-enqueue", "gatewayclass", gatewayClass.Name)
	return reconcile.Result{}, nil
}

// countActiveGatewayClasses returns the number of managed GatewayClasses that
// are not being deleted, excluding the given name.
func countActiveGatewayClasses(list *gatewayapiv1.GatewayClassList, excludeName string) int {
	count := 0
	for i := range list.Items {
		gc := &list.Items[i]
		if gc.Name == excludeName || !gc.DeletionTimestamp.IsZero() {
			continue
		}
		count++
	}
	return count
}

// ensureGatewayClassDeleted handles cleanup when a GatewayClass is being deleted.
// Uninstalls Istio if this is the last managed GatewayClass, then removes the finalizer.
func (r *reconciler) ensureGatewayClassDeleted(ctx context.Context, gatewayClass *gatewayapiv1.GatewayClass) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(gatewayClass, sailLibraryFinalizer) {
		// No finalizer present; nothing to clean up
		return reconcile.Result{}, nil
	}

	// Check if this is the last active GatewayClass - if so, uninstall Istio
	updatedGatewayClass := gatewayClass.DeepCopy()
	gatewayClassList := gatewayapiv1.GatewayClassList{}
	if err := r.cache.List(ctx, &gatewayClassList, client.MatchingFields{
		operatorcontroller.GatewayClassIndexFieldName: operatorcontroller.OpenShiftGatewayClassControllerName,
	}); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list gateway classes: %w", err)
	}
	if countActiveGatewayClasses(&gatewayClassList, gatewayClass.Name) == 0 {
		if err := r.sailInstaller.Uninstall(ctx, r.config.OperandNamespace, operatorcontroller.IstioName("").Name); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to uninstall Istio: %w", err)
		}
	}

	// Remove finalizer to allow Kubernetes to delete the object
	if controllerutil.RemoveFinalizer(updatedGatewayClass, sailLibraryFinalizer) {
		if err := r.client.Patch(ctx, updatedGatewayClass, client.MergeFrom(gatewayClass)); err != nil {
			log.Error(err, "failed to remove finalizer from gatewayclass")
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

// UninstallSail removes the CIO-managed Istio instance. Called by the
// gatewayapi controller when transitioning to Unmanaged mode.
//
// Acquires sailLifecycleMu, shared with ensureIstio's Apply call, so that
// a concurrently in-flight gatewayclass reconcile that read
// AllowDependents()==true before this transition cannot land an Apply()
// after this Uninstall() completes.
func (r *reconciler) UninstallSail(ctx context.Context) error {
	r.sailLifecycleMu.Lock()
	defer r.sailLifecycleMu.Unlock()

	// OLM path: sailInstaller is nil, nothing to uninstall
	if r.sailInstaller == nil {
		return nil
	}
	return r.sailInstaller.Uninstall(ctx, r.config.OperandNamespace, operatorcontroller.IstioName("").Name)
}

// SailLibrarySource bridges a Sail Library channel to a MapFunc logic.
// The Sail Library contains a source channel where notification for changes (like drifts)
// can be sent back to our controller, so we trigger a reconciliation of our GatewayClass and its status.
type SailLibrarySource[T client.Object] struct {
	NotifyCh     <-chan struct{}
	RequestsFunc func(context.Context, client.Object) []reconcile.Request
}

func (s *SailLibrarySource[T]) Start(ctx context.Context, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-s.NotifyCh:
				if !ok {
					log.Info("Sail Library notification channel closed, stopping watch")
					return
				}
				var empty T
				requests := s.RequestsFunc(ctx, empty)
				log.Info("Sail Library reconciliation complete, enqueuing GatewayClass reconciliations",
					"count", len(requests), "gatewayclasses", requests)
				for _, req := range requests {
					queue.Add(req)
				}
			}
		}
	}()
	return nil
}
