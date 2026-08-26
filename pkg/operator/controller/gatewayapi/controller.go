package gatewayapi

import (
	"context"
	"fmt"
	"sync"
	"time"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	listenersetstatuscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/listenerset-status"

	"k8s.io/client-go/tools/record"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	controllerName                        = "gatewayapi_controller"
	gatewayAPICRDIndexFieldName           = "gatewayAPICRD"
	unmanagedGatewayAPICRDIndexFieldValue = "unmanaged"
)

var log = logf.Logger.WithName(controllerName)

// New creates and returns a controller that creates Gateway API CRDs when the
// appropriate featuregate is enabled.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		client:       mgr.GetClient(),
		cache:        operatorCache,
		config:       config,
		fieldIndexer: mgr.GetFieldIndexer(),
	}
	c, err := controller.New(controllerName, mgr, controller.Options{
		Reconciler:              reconciler,
		MaxConcurrentReconciles: 1, // Serialize reconciles to prevent mode-flip races
	})
	if err != nil {
		return nil, err
	}
	clusterNamePredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		expectedName := operatorcontroller.FeatureGateClusterConfigName()
		actualName := types.NamespacedName{
			Namespace: o.GetNamespace(),
			Name:      o.GetName(),
		}
		return expectedName == actualName
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &configv1.FeatureGate{}, &handler.EnqueueRequestForObject{}, clusterNamePredicate)); err != nil {
		return nil, fmt.Errorf("failed to watch FeatureGate resources: %w", err)
	}

	toFeatureGate := func(ctx context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: operatorcontroller.FeatureGateClusterConfigName(),
		}}
	}

	infraClusterPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == "cluster"
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &configv1.Infrastructure{}, handler.EnqueueRequestsFromMapFunc(toFeatureGate), infraClusterPredicate)); err != nil {
		return nil, fmt.Errorf("failed to watch Infrastructure resources: %w", err)
	}

	if err := registerIngressWatch(c, operatorCache, config, toFeatureGate); err != nil {
		return nil, err
	}

	isGatewayAPICRD := func(o client.Object) bool {
		crd, ok := o.(*apiextensionsv1.CustomResourceDefinition)
		return ok && crd.Spec.Group == gatewayapiv1.GroupName
	}
	crdPredicate := predicate.NewPredicateFuncs(isGatewayAPICRD)

	// watch for CRDs
	if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(toFeatureGate), crdPredicate)); err != nil {
		return nil, fmt.Errorf("failed to watch CRDs: %w", err)
	}

	// Watch the ValidatingAdmissionPolicy, its binding, and the
	// aggregated ClusterRoles so that external drift on these
	// unmanaged-elsewhere resources is corrected reactively instead of
	// relying on a periodic poll. Together with the Ingress CR and CRD
	// watches above, every resource this controller ensures is now
	// watched, so no blind periodic requeue is needed.
	isGatewayAPIVAP := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == baseAdmissionPolicy.Name
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &admissionregistrationv1.ValidatingAdmissionPolicy{}, handler.EnqueueRequestsFromMapFunc(toFeatureGate), isGatewayAPIVAP)); err != nil {
		return nil, fmt.Errorf("failed to watch ValidatingAdmissionPolicy: %w", err)
	}

	isGatewayAPIVAPBinding := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == desiredAdmissionPolicyBinding.Name
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}, handler.EnqueueRequestsFromMapFunc(toFeatureGate), isGatewayAPIVAPBinding)); err != nil {
		return nil, fmt.Errorf("failed to watch ValidatingAdmissionPolicyBinding: %w", err)
	}

	managedClusterRoleNames := sets.New[string]()
	for _, cr := range managedClusterRoles {
		managedClusterRoleNames.Insert(cr.Name)
	}
	isGatewayAPIClusterRole := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return managedClusterRoleNames.Has(o.GetName())
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &rbacv1.ClusterRole{}, handler.EnqueueRequestsFromMapFunc(toFeatureGate), isGatewayAPIClusterRole)); err != nil {
		return nil, fmt.Errorf("failed to watch ClusterRoles: %w", err)
	}

	// Index unmanaged Gateway API CRDs to enable efficient filtering
	// during list operations.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&apiextensionsv1.CustomResourceDefinition{},
		gatewayAPICRDIndexFieldName,
		client.IndexerFunc(func(o client.Object) []string {
			if isGatewayAPICRD(o) {
				if _, found := managedCRDMap[o.GetName()]; !found {
					return []string{unmanagedGatewayAPICRDIndexFieldValue}
				}
			}
			return []string{}
		})); err != nil {
		return nil, fmt.Errorf("failed to create index for custom resource definitions: %w", err)
	}

	return c, nil
}

// registerIngressWatch watches the Ingress CR so that management mode
// changes (and status updates such as Present/Compliant) are observed
// reactively. It only registers the watch when the management mode gate
// is enabled, because the Ingress CRD only exists on TechPreview/DevPreview
// clusters and is not installed at all on HyperShift Default clusters.
//
// This must stay gated: c.Watch defers informer creation until the
// manager starts, at which point controller-runtime resolves the CRD's
// GVK via the RESTMapper. If the CRD does not exist, that resolution
// fails with a NoKindMatchError, the cache never syncs, and after the
// sync timeout controller-runtime fatals the whole manager -- taking
// every controller down, not just this one's Gateway API support.
//
// Extracted from New() so the gating decision can be exercised directly
// in unit tests without needing a real or fake API server/RESTMapper to
// reproduce the failure this guards against.
func registerIngressWatch(c controller.Controller, operatorCache cache.Cache, config Config, toFeatureGate handler.MapFunc) error {
	if config.ModeAccessor == nil || !config.ModeAccessor.GateEnabled() {
		return nil
	}
	isClusterIngress := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == "cluster"
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1alpha1.Ingress{}, handler.EnqueueRequestsFromMapFunc(toFeatureGate), isClusterIngress)); err != nil {
		return fmt.Errorf("failed to watch Ingress resources: %w", err)
	}
	return nil
}

// SailUninstaller is a type alias for the shared SailUninstaller interface
// defined in the operatorcontroller package.
type SailUninstaller = operatorcontroller.SailUninstaller

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	// MarketplaceEnabled indicates whether the "marketplace" capability is
	// enabled.
	MarketplaceEnabled bool
	// OperatorLifecycleManagerEnabled indicates whether the
	// "OperatorLifecycleManager" capability is enabled.
	OperatorLifecycleManagerEnabled bool
	// GatewayAPIWithoutOLMEnabled indicates whether the GatewayAPIWithoutOLM
	// feature gate is enabled, allowing Sail Library-based installation.
	GatewayAPIWithoutOLMEnabled bool

	// ModeAccessor is the shared mode accessor that this controller
	// updates after computing CRD management conditions. May be nil
	// when the management mode gate is disabled.
	ModeAccessor *operatorcontroller.GatewayAPIModeAccessor

	// SailUninstaller is called to uninstall the CIO-managed Istio
	// instance when transitioning to Unmanaged mode. May be nil when
	// the Sail Library is not in use.
	SailUninstaller SailUninstaller

	// DependentControllers is a list of controllers that watch Gateway API
	// resources.  The gatewayapi controller starts these controllers once
	// the Gateway API CRDs have been created.
	DependentControllers []controller.Controller
}

// reconciler reconciles gatewayclasses.
type reconciler struct {
	config Config

	client             client.Client
	cache              cache.Cache
	recorder           record.EventRecorder
	fieldIndexer       client.FieldIndexer
	mu                 sync.Mutex
	controllersStarted bool

	// dependentsBlockedLogged tracks whether we have already logged that
	// dependent controllers are blocked, so that steady-state reconciles
	// (e.g. every 30s in Unmanaged mode) don't repeat the same message.
	// Reset once AllowDependents becomes true again.
	dependentsBlockedLogged bool
}

// managementModeEnabled returns true when the GatewayAPIManagementMode
// gate is enabled. It single-sources the flag through ModeAccessor.
func (r *reconciler) managementModeEnabled() bool {
	return r.config.ModeAccessor != nil && r.config.ModeAccessor.GateEnabled()
}

// ingressModeSnapshot captures the desired management mode from a
// single Ingress CR read. All gate-ON decisions within one reconcile
// pass use this snapshot to prevent TOCTOU divergence between VAP
// transition and status/mode-accessor updates.
type ingressModeSnapshot struct {
	// desiredMode is the resolved desired management mode.
	desiredMode operatorv1alpha1.GatewayAPIManagementMode
	// ingress is the Ingress CR object (nil when NotFound).
	ingress *operatorv1alpha1.Ingress
	// found indicates whether the Ingress CR was found on the API
	// server. When false, mode defaults to Managed and status writes
	// are skipped.
	found bool
}

// resolveIngressModeSnapshot performs the single authoritative read of
// the Ingress CR for the gate-ON reconcile path.
//
// Forbidden -> returns error (fail-closed; no ownership actions taken).
// NotFound  -> returns snapshot with desiredMode=Managed and found=false.
// Success   -> returns snapshot with the spec's mode and the live object.
func (r *reconciler) resolveIngressModeSnapshot(ctx context.Context) (ingressModeSnapshot, error) {
	ingress := &operatorv1alpha1.Ingress{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, ingress); err != nil {
		if errors.IsForbidden(err) {
			log.Info("Ingress CR access forbidden, treating ownership as unknown - will requeue", "error", err)
			return ingressModeSnapshot{}, fmt.Errorf("Ingress CR access forbidden, cannot determine management mode: %w", err)
		}
		if errors.IsNotFound(err) {
			log.Info("Ingress CR not found, defaulting to Managed mode")
			return ingressModeSnapshot{
				desiredMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
				ingress:     nil,
				found:       false,
			}, nil
		}
		return ingressModeSnapshot{}, fmt.Errorf("failed to get Ingress CR: %w", err)
	}

	desiredMode := ingress.Spec.GatewayAPI.ManagementMode
	if desiredMode == "" {
		desiredMode = operatorv1alpha1.GatewayAPIManagementModeManaged
	}
	return ingressModeSnapshot{
		desiredMode: desiredMode,
		ingress:     ingress,
		found:       true,
	}, nil
}

// setTransitionState records mode transition progress on the shared
// ModeAccessor and mirrors it into the mode-transition-failed metric, so
// the two are never allowed to drift out of sync.
func (r *reconciler) setTransitionState(state operatorcontroller.TransitionState) {
	r.config.ModeAccessor.SetTransitionState(state)
	updateModeTransitionFailedMetric(state)
}

// Reconcile expects request to refer to a FeatureGate and creates or
// reconciles the Gateway API CRDs.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	if r.managementModeEnabled() {
		// Read the Ingress CR exactly once for the entire gate-ON
		// path so that VAP transition, status, and ShouldManageCRDs
		// decisions all observe the same mode snapshot.
		snapshot, err := r.resolveIngressModeSnapshot(ctx)
		if err != nil {
			// Clear any stale transition state from a prior reconcile
			// so that the status controller does not report a stuck
			// Progressing=True condition.
			r.setTransitionState(operatorcontroller.TransitionState{})
			return reconcile.Result{}, err
		}

		// Only signal a mode transition when the desired mode
		// actually differs from the last successfully applied mode.
		// Without this guard, every periodic requeue (30s) would set
		// InProgress=true, causing ClusterOperator Progressing=True
		// flaps that block upgrades and fire alerts.
		lastApplied := r.config.ModeAccessor.GetLastAppliedMode()
		modeChanged := lastApplied == nil || *lastApplied != snapshot.desiredMode
		if modeChanged {
			r.setTransitionState(operatorcontroller.TransitionState{
				InProgress: true,
				Target:     snapshot.desiredMode,
			})

			if snapshot.desiredMode == operatorv1alpha1.GatewayAPIManagementModeUnmanaged {
				// Close the race window as early as possible: block
				// dependents before Sail uninstall/VAP delete even
				// start, rather than waiting for the full Update()
				// call in reconcileIngressStatus below. See
				// ModeAccessor.BlockDependents for details.
				r.config.ModeAccessor.BlockDependents()
			}
		}

		// Phase 2: delete VAP+binding BEFORE computing Unmanaged
		// status, so the Managed=False/Unmanaged condition is only
		// written after the admission policy is removed.
		// Only run transition operations (Sail uninstall, VAP delete)
		// when the mode actually changed. In steady-state Unmanaged,
		// these operations already completed on a prior reconcile and
		// repeating them every 30s wastes resources.
		if modeChanged {
			if err := r.reconcileAdmissionPolicyTransition(ctx, snapshot); err != nil {
				r.setTransitionState(operatorcontroller.TransitionState{
					InProgress: true,
					Target:     snapshot.desiredMode,
					Error:      err,
				})
				return reconcile.Result{}, err
			}
		}

		// Phase 3: resolve desired mode BEFORE mutating CRDs/RBAC so
		// that Unmanaged or TakeoverBlocked states skip ensure.
		if err := r.reconcileIngressStatus(ctx, snapshot); err != nil {
			r.setTransitionState(operatorcontroller.TransitionState{
				InProgress: true,
				Target:     snapshot.desiredMode,
				Error:      err,
			})
			return reconcile.Result{}, err
		}

		// Only create/update CRDs and their aggregated RBAC when the
		// resolved mode is Managed (not Unmanaged, not TakeoverBlocked).
		if r.config.ModeAccessor.ShouldManageCRDs() {
			if err := r.ensureAdmissionPolicy(ctx); err != nil {
				r.setTransitionState(operatorcontroller.TransitionState{
					InProgress: true,
					Target:     snapshot.desiredMode,
					Error:      err,
				})
				return reconcile.Result{}, err
			}
			if err := r.ensureGatewayAPICRDs(ctx); err != nil {
				r.setTransitionState(operatorcontroller.TransitionState{
					InProgress: true,
					Target:     snapshot.desiredMode,
					Error:      err,
				})
				return reconcile.Result{}, err
			}
			if err := r.ensureGatewayAPIRBAC(ctx); err != nil {
				r.setTransitionState(operatorcontroller.TransitionState{
					InProgress: true,
					Target:     snapshot.desiredMode,
					Error:      err,
				})
				return reconcile.Result{}, err
			}
		}

		// All transition operations completed successfully.
		// Record the applied mode so subsequent steady-state reconciles
		// skip setting InProgress, then clear the transition state.
		r.config.ModeAccessor.SetLastAppliedMode(snapshot.desiredMode)
		r.setTransitionState(operatorcontroller.TransitionState{})
	} else {
		// Gate OFF: preserve legacy always-ensure behavior.
		if err := r.ensureGatewayAPICRDs(ctx); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.ensureGatewayAPIRBAC(ctx); err != nil {
			return reconcile.Result{}, err
		}
	}

	// Always observe unmanaged CRDs for ClusterOperator extension
	// status, regardless of management mode.
	if crdNames, err := r.listUnmanagedGatewayAPICRDs(ctx); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list unmanaged gateway CRDs: %w", err)
	} else if err = r.setUnmanagedGatewayAPICRDNamesStatus(ctx, crdNames); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to update the ingress cluster operator status: %w", err)
	} else {
		if r.managementModeEnabled() {
			updateUnmanagedCRDsMetric(crdNames)
		}
	}

	// The subscriptions resource only exists if the
	// "OperatorLifecycleManager" capability is enabled, and the default
	// catalog only exists if the "marketplace" capability is enabled.  We
	// cannot install OSSM via OLM if the subscriptions resource or default
	// catalog does not exist. However, when the GatewayAPIWithoutOLM feature
	// is enabled, we can install Istio directly using the Sail Library.
	useOLM := r.config.MarketplaceEnabled && r.config.OperatorLifecycleManagerEnabled
	useSailLibrary := r.config.GatewayAPIWithoutOLMEnabled
	if !useOLM && !useSailLibrary {
		return reconcile.Result{}, nil
	}

	// When the management mode gate is enabled, dependent controllers
	// start only when Managed + Present + Compliant are all True.
	if r.managementModeEnabled() && !r.config.ModeAccessor.AllowDependents() {
		r.mu.Lock()
		alreadyLogged := r.dependentsBlockedLogged
		r.dependentsBlockedLogged = true
		r.mu.Unlock()
		if !alreadyLogged {
			log.Info("management mode does not allow dependent controllers yet")
		}
		// No requeue: the Ingress CR, Gateway API CRD, VAP/binding, and
		// ClusterRole watches all map back to this reconciler, so a
		// change to any of them (e.g. mode flipping back to Managed, or
		// CRDs becoming compliant) will trigger a fresh reconcile.
		return reconcile.Result{}, nil
	}
	r.mu.Lock()
	r.dependentsBlockedLogged = false
	r.mu.Unlock()

	if established, err := r.allManagedCRDsEstablished(ctx); err != nil {
		return reconcile.Result{}, err
	} else if !established {
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Update the mode accessor for the legacy (gate-off) path so
	// that AllowDependents reflects CRD establishment.
	if r.config.ModeAccessor != nil && !r.config.ModeAccessor.GateEnabled() {
		r.config.ModeAccessor.SetCRDsEstablished(true)
	}

	if err := r.ensureDependentControllers(ctx); err != nil {
		log.Error(err, "failed to ensure dependent controllers, will retry")
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return reconcile.Result{}, nil
}

// ensureDependentControllers indexes GatewayClass resources and starts
// dependent controllers exactly once. Returns an error if the GatewayClass
// field indexer cannot be created, allowing the caller to retry.
func (r *reconciler) ensureDependentControllers(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.controllersStarted {
		return nil
	}

	// Index gateway classes based on their spec.controllerName
	if err := r.fieldIndexer.IndexField(
		context.Background(),
		&gatewayapiv1.GatewayClass{},
		operatorcontroller.GatewayClassIndexFieldName,
		client.IndexerFunc(func(o client.Object) []string {
			gatewayclass, ok := o.(*gatewayapiv1.GatewayClass)
			if !ok {
				return []string{}
			}
			return []string{string(gatewayclass.Spec.ControllerName)}
		})); err != nil {
		return fmt.Errorf("failed to add field indexer: %w", err)
	}
	// Index ListenerSets by parent Gateway after CRDs are installed.
	if err := r.fieldIndexer.IndexField(
		context.Background(),
		&gatewayapiv1.ListenerSet{},
		listenersetstatuscontroller.ListenerSetParentGatewayIndex,
		client.IndexerFunc(func(o client.Object) []string {
			ls, ok := o.(*gatewayapiv1.ListenerSet)
			if !ok {
				return []string{}
			}
			parentNS := ls.GetNamespace()
			if ls.Spec.ParentRef.Namespace != nil {
				parentNS = string(*ls.Spec.ParentRef.Namespace)
			}
			return []string{parentNS + "/" + string(ls.Spec.ParentRef.Name)}
		})); err != nil {
		return fmt.Errorf("failed to add ListenerSet field indexer: %w", err)
	}

	for i := range r.config.DependentControllers {
		c := &r.config.DependentControllers[i]
		go func() {
			if err := (*c).Start(ctx); err != nil {
				log.Error(err, "cannot start controller")
			}
		}()
	}

	r.controllersStarted = true
	return nil
}

// allManagedCRDsEstablished checks that all managed Gateway API CRDs
// are established and served by the API server before dependent
// controllers attempt to create field indexes on those types.
func (r *reconciler) allManagedCRDsEstablished(ctx context.Context) (bool, error) {
	for _, managed := range managedCRDs {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := r.client.Get(ctx, types.NamespacedName{Name: managed.Name}, &crd); err != nil {
			if errors.IsNotFound(err) {
				log.Info("CRD not yet created, will retry", "name", managed.Name)
				return false, nil
			}
			return false, fmt.Errorf("failed to get CRD %s: %w", managed.Name, err)
		}
		if !isCRDEstablished(&crd) {
			log.Info("CRD not yet established, will retry", "name", managed.Name)
			return false, nil
		}
	}
	return true, nil
}

func isCRDEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, c := range crd.Status.Conditions {
		if c.Type == apiextensionsv1.Established {
			return c.Status == apiextensionsv1.ConditionTrue
		}
	}
	return false
}
