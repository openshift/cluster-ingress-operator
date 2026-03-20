package gatewayclass

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
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
	// sailLibraryFinalizer is added to GatewayClasses using Sail Library installation.
	// When a GatewayClass with this finalizer is deleted:
	// 1. Sail Library mode: Uninstall Istio if this is the last GatewayClass, then remove finalizer
	// 2. Downgrade to OLM: Clean up Sail Library status and finalizer (then OLM takes over Istio)
	sailLibraryFinalizer = "openshift.io/ingress-operator-sail-finalizer"

	// Image configuration for Sail Library installations.
	// These are only used for defaulting when CSV image annotations are missing,
	// which should not happen in production clusters with proper OSSM release branches.
	ossmImageRegistry = "registry.redhat.io/openshift-service-mesh"
	istioImageIstiod  = "istio-pilot-rhel9"
	istioImageProxy   = "istio-proxyv2-rhel9"
	istioImageCNI     = "istio-cni-rhel9"
	istioImageZTunnel = "istio-ztunnel-rhel9"
)

type extraIstioConfig struct {
	proxyConfig *configv1.Proxy
}

var log = logf.Logger.WithName(controllerName)
var gatewayClassController controller.Controller

// NewUnmanaged creates and returns a controller that watches gatewayclasses and
// installs and configures Istio.  This is an unmanaged controller, which means
// that the manager does not start it.
func NewUnmanaged(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()

	reconciler := &reconciler{
		config:   config,
		client:   mgr.GetClient(),
		cache:    operatorCache,
		recorder: mgr.GetEventRecorderFor(controllerName),
	}
	options := controller.Options{Reconciler: reconciler}
	options.DefaultFromConfig(mgr.GetControllerOptions())
	c, err := controller.NewUnmanaged(controllerName, options)
	if err != nil {
		return nil, err
	}
	isOurGatewayClass := predicate.NewPredicateFuncs(func(o client.Object) bool {
		class := o.(*gatewayapiv1.GatewayClass)
		return class.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName
	})
	notIstioGatewayClass := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() != "istio"
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.GatewayClass{}, reconciler.enqueueRequestForSomeGatewayClass(), isOurGatewayClass, notIstioGatewayClass)); err != nil {
		return nil, err
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
	if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, reconciler.enqueueRequestForSomeGatewayClass(), isInferencepoolCrd)); err != nil {
		return nil, err
	}

	// Watch for Proxy configuration to set the right options on Istio resource
	isClusterProxy := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == "cluster"
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &configv1.Proxy{}, reconciler.enqueueRequestForSomeGatewayClass(), isClusterProxy)); err != nil {
		return nil, err
	}

	if !config.GatewayAPIWithoutOLMEnabled {
		isServiceMeshSubscription := predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetName() == operatorcontroller.ServiceMeshOperatorSubscriptionName().Name
		})
		if err = c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.Subscription{},
			reconciler.enqueueRequestForSomeGatewayClass(), isServiceMeshSubscription)); err != nil {
			return nil, err
		}
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.InstallPlan{}, reconciler.enqueueRequestForSomeGatewayClass(), isOurInstallPlan, isInstallPlanReadyForApproval)); err != nil {
			return nil, err
		}
	} else {
		// TODO: Remove this when we switch to an OSSM release branch with proper CSV image annotations.
		//       The main branch of sail-operator does not maintain image annotations in the CSV,
		//       causing Istio to fall back to upstream container images. This explicit configuration
		//       ensures Red Hat images are used until the release branch has the annotations properly maintained.
		err := install.SetImageDefaults(resources.FS, ossmImageRegistry, install.ImageNames{
			Istiod:  istioImageIstiod,
			Proxy:   istioImageProxy,
			CNI:     istioImageCNI,
			ZTunnel: istioImageZTunnel,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to set image defaults: %w", err)
		}
		// Start the Sail Library's background reconciliation loop (runs in a goroutine).
		// Returns a notification channel that signals when library reconciliation completes,
		// allowing us to update GatewayClass status conditions accordingly.
		installer, err := install.New(mgr.GetConfig(), resources.FS)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize sail-operator installation library: %w", err)
		}
		notifyCh := installer.Start(config.Context)
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
		isServiceMeshSubscription := predicate.NewPredicateFuncs(func(o client.Object) bool {
			sub, ok := o.(*operatorsv1alpha1.Subscription)
			if !ok {
				return false
			}
			// Check if package name starts with "servicemeshoperator"
			return sub.Spec != nil && strings.HasPrefix(sub.Spec.Package, "servicemeshoperator")
		})

		if err = c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.Subscription{}, reconciler.enqueueRequestForCRDOwnershipChange(), isServiceMeshSubscription)); err != nil {
			return nil, err
		}
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.InstallPlan{}, reconciler.enqueueRequestForCRDOwnershipChange(), isOurInstallPlan)); err != nil {
			return nil, err
		}
		if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, reconciler.enqueueRequestForCRDOwnershipChange(), isIstioCRD)); err != nil {
			return nil, err
		}
		if err := c.Watch(&SailLibrarySource[client.Object]{NotifyCh: notifyCh, RequestsFunc: reconciler.requestsForAllManagedGatewayClasses}); err != nil {
			return nil, err
		}
	}

	gatewayClassController = c
	return c, nil
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
	// IstioVersion is the version of Istio to install.
	IstioVersion string
	// Context is the context for controller lifecycle.
	Context context.Context
}

// SailLibraryInstaller implements the methods of sail library but in a way we can
// also mock and test
type SailLibraryInstaller interface {
	Start(ctx context.Context) <-chan struct{}
	Apply(opts install.Options)
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
	if _, _, err := r.ensureServiceMeshOperatorSubscription(ctx, ossmCatalog, ossmChannel, ossmVersion); err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure ServiceMeshOperatorSubscription: %w", err))
	}
	if _, _, err := r.ensureServiceMeshOperatorInstallPlan(ctx, ossmVersion); err != nil {
		errs = append(errs, err)
	}
	istioVersion := r.config.IstioVersion
	if v, ok := gatewayclass.Annotations[istioVersionOverrideAnnotationKey]; ok {
		istioVersion = v
	}
	if _, _, err := r.ensureIstioOLM(ctx, gatewayclass, istioVersion); err != nil {
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

	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}

// reconcileWithSailLibrary reconciles a GatewayClass using the Sail Library
// for direct Helm-based installation of Istio.
func (r *reconciler) reconcileWithSailLibrary(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling with sail library", "request", request)

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

	if err := r.ensureIstio(ctx, istioVersion); err != nil {
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

	return reconcile.Result{}, utilerrors.NewAggregate(errs)
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
