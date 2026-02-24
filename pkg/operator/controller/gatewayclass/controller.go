package gatewayclass

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/install"

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
	// sailLibraryFinalizer is a finalizer key added to GatewayClasses that
	// at some moment were reconciled by sail-library.
	// They signal two things:
	// 1 - For sail-library, if there is no other gatewayclass using it, the reconciliation
	// can be stopped
	// 2 - For olm-install - In case it is called, it means the installation was rolled
	// back and the finalizer must be removed
	sailLibraryFinalizer = "openshift.io/ingress-operator-sail-finalizer"
)

var log = logf.Logger.WithName(controllerName)
var gatewayClassController controller.Controller

// NewUnmanaged creates and returns a controller that watches gatewayclasses and
// installs and configures Istio.  This is an unmanaged controller, which means
// that the manager does not start it.
func NewUnmanaged(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()

	reconciler := &reconciler{
		config:     config,
		client:     mgr.GetClient(),
		cache:      operatorCache,
		kubeConfig: config.KubeConfig,
		recorder:   mgr.GetEventRecorderFor(controllerName),
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
		if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, reconciler.enqueueRequestForSomeGatewayClass(), isInferencepoolCrd)); err != nil {
			return nil, err
		}
	} else {
		// On OLM we need to watch Subscriptions, InstallPlans and CRDs differently.
		// Any change on any subscription, installplan (if ours) or CRD (if Istio or GIE) must:
		// - Trigger a new installer enqueue (part of enqueueRequestForSubscriptionChange)
		// - Trigger a reconciliation for ALL (and not SOME) classes we manage to add status
		isIstioCRD := predicate.NewPredicateFuncs(func(o client.Object) bool {
			return strings.Contains(o.GetName(), "istio.io")
		})

		if err = c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.Subscription{},
			reconciler.enqueueRequestForSubscriptionChange())); err != nil {
			return nil, err
		}
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.InstallPlan{}, reconciler.enqueueRequestForSubscriptionChange(), isOurInstallPlan)); err != nil {
			return nil, err
		}
		if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, reconciler.enqueueRequestForSubscriptionChange(), predicate.Or(isInferencepoolCrd, isIstioCRD))); err != nil {
			return nil, err
		}

		// For Non-OLM install, we start the sail-operator library that
		// does the reconciliation of CRDs and resources.
		// The channel here receives notification from the previously started sail-operator library
		// and triggers new reconciliations
		if config.SailOperatorReconciler != nil && config.SailOperatorReconciler.NotifyCh != nil {
			sailOperatorSource := &SailOperatorSource[client.Object]{
				NotifyCh:     config.SailOperatorReconciler.NotifyCh,
				RequestsFunc: reconciler.allManagedGatewayClasses,
			}
			if err := c.Watch(sailOperatorSource); err != nil {
				return nil, err
			}
		}
	}

	gatewayClassController = c
	return c, nil
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	// KubeConfig is the Kubernetes client configuration used by the Sail Library.
	KubeConfig *rest.Config
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
	// SailOperatorReconciler contains the instance and the notification channel for the reconciler of Istio resources
	SailOperatorReconciler *SailOperatorReconciler
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

type SailOperatorReconciler struct {
	Installer SailLibraryInstaller
	NotifyCh  <-chan struct{}
}

// reconciler reconciles gatewayclasses.
type reconciler struct {
	config Config

	client     client.Client
	cache      cache.Cache
	kubeConfig *rest.Config
	recorder   record.EventRecorder

	startIstioWatch sync.Once
}

func (r *reconciler) enqueueRequestForSubscriptionChange() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			// If this is OLM and we detect the change of subscriptions, we must
			// enforce that Sail Installer also enqueues a new reconciliation
			if r.config.SailOperatorReconciler != nil && r.config.SailOperatorReconciler.Installer != nil {
				// We can call Enqueue as many times as we want, as sail-library should enqueue and filter
				// and not make concurrent operations
				r.config.SailOperatorReconciler.Installer.Enqueue()
			}
			return r.allManagedGatewayClasses(ctx, obj)
		})
}

// enqueueRequestForSomeGatewayClass enqueues a reconciliation request for the
// gatewayclass that has the earliest creation timestamp and that specifies our
// controller name.
func (r *reconciler) enqueueRequestForSomeGatewayClass() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			if r.config.GatewayAPIWithoutOLMEnabled {
				return r.allManagedGatewayClasses(ctx, obj)
			}
			return r.requestsForSomeGatewayClass(ctx, obj)
		},
	)
}

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

func (r *reconciler) allManagedGatewayClasses(ctx context.Context, _ client.Object) []reconcile.Request {
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
	if r.config.GatewayAPIWithoutOLMEnabled && r.config.SailOperatorReconciler != nil && r.config.SailOperatorReconciler.Installer != nil {
		return r.reconcileWithSailLibrary(ctx, request)
	}
	return r.reconcileWithOLM(ctx, request)
}

// reconcileWithOLM reconciles a GatewayClass using OLM to install OSSM,
// which then manages an Istio CR for the Istio installation.
func (r *reconciler) reconcileWithOLM(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	sourceGatewayClass := &gatewayapiv1.GatewayClass{}
	if err := r.cache.Get(ctx, request.NamespacedName, sourceGatewayClass); err != nil {
		if !errors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("error getting gatewayclass: %w", err)
		}
		return reconcile.Result{}, nil
	}

	gatewayclass := sourceGatewayClass.DeepCopy()
	// This is not SailOperator class, so remove any finalizer and any status from it
	if controllerutil.RemoveFinalizer(gatewayclass, sailLibraryFinalizer) {
		var errs []error
		err := r.client.Patch(ctx, gatewayclass, client.MergeFrom(sourceGatewayClass))
		if err != nil {
			log.Error(err, "error patching the gatewayclass status")
			errs = append(errs, err)
		}
		removeSailOperatorConditions(&gatewayclass.Status.Conditions)
		if err := r.client.Status().Patch(ctx, gatewayclass, client.MergeFrom(sourceGatewayClass)); err != nil {
			log.Error(err, "error patching the gatewayclass status")
			errs = append(errs, err)

		}

		return reconcile.Result{}, utilerrors.NewAggregate(errs) // Removing the finalizer should kick a new reconciliation
	}

	var errs []error
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
	log.Info("reconciling", "request", request)

	sourceGatewayClass := &gatewayapiv1.GatewayClass{}
	if err := r.cache.Get(ctx, request.NamespacedName, sourceGatewayClass); err != nil {
		if !errors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("error getting gatewayclass: %w", err)
		}
		return reconcile.Result{}, nil
	}

	gatewayclass := sourceGatewayClass.DeepCopy()

	if !gatewayclass.DeletionTimestamp.IsZero() {
		// Check if this is the last remaining GatewayClass. If so:
		// 1 - Stop the library in case this is the last GatewayClass
		// 2 - Delete the finalizer (always, regardless of being the last)
		// 3 - We must always return from here when deletion is in progress
		gatewayClassList := gatewayapiv1.GatewayClassList{}
		if err := r.cache.List(ctx, &gatewayClassList, client.MatchingFields{
			operatorcontroller.GatewayClassIndexFieldName: operatorcontroller.OpenShiftGatewayClassControllerName,
		}); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to list gateway classes: %w", err)
		}

		if len(gatewayClassList.Items) < 2 {
			if err := r.config.SailOperatorReconciler.Installer.Uninstall(ctx, operatorcontroller.DefaultOperandNamespace, operatorcontroller.IstioName("").Name); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to uninstall the operator: %w", err)
			}
		}
		// This is not SailOperator class, so remove any finalizer
		if controllerutil.RemoveFinalizer(gatewayclass, sailLibraryFinalizer) {
			err := r.client.Patch(ctx, gatewayclass, client.MergeFrom(sourceGatewayClass))
			if err != nil {
				log.Error(err, "error patching the gatewayclass status")
			}
			return reconcile.Result{}, err // Removing the finalizer should kick a new reconciliation
		}
		// Finalizer already absent; nothing else to do during deletion.
		return reconcile.Result{}, nil
	}

	if controllerutil.AddFinalizer(gatewayclass, sailLibraryFinalizer) {
		err := r.client.Patch(ctx, gatewayclass, client.MergeFrom(sourceGatewayClass))
		if err != nil {
			log.Error(err, "error patching the gatewayclass status")
		}
		return reconcile.Result{}, err // Return if we added the finalizer, to kick reconciliation again
	}

	// Ensure migration from 4.21 to 4.22 Sail Library.
	if migrationComplete, err := r.ensureOSSMtoSailLibraryMigration(ctx); err != nil {
		return reconcile.Result{}, fmt.Errorf("error validating sail library migration: %w", err)
	} else if !migrationComplete {
		// Migration isn't complete - give OSSM time to clean up.
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}

	var errs []error

	istioVersion := r.config.IstioVersion
	if v, ok := gatewayclass.Annotations[istioVersionOverrideAnnotationKey]; ok {
		istioVersion = v
	}

	if err := r.ensureIstio(ctx, gatewayclass, istioVersion); err != nil {
		log.Error(err, "error ensuring Istio")
		errs = append(errs, err)
	}

	if err := r.client.Status().Patch(ctx, gatewayclass, client.MergeFrom(sourceGatewayClass)); err != nil {
		log.Error(err, "error patching the gatewayclass status")
		errs = append(errs, err)
	}

	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}

// SailOperatorSource bridges a sail operator channel to a MapFunc logic.
// the Sail operator contains a source channel where notification for changes (like drifts)
// can be sent back to our controller, so we trigger a reconciliation of our GatewayClass and its status
type SailOperatorSource[T client.Object] struct {
	NotifyCh     <-chan struct{}
	RequestsFunc func(context.Context, client.Object) []reconcile.Request
}

func (s *SailOperatorSource[T]) Start(ctx context.Context, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-s.NotifyCh:
				if !ok {
					log.Info("sail operator notification channel closed, stopping watch")
					return
				}
				var empty T
				requests := s.RequestsFunc(ctx, empty)
				log.Info("got notification from sail library")
				for _, req := range requests {
					queue.Add(req)
				}
			}
		}
	}()
	return nil
}
