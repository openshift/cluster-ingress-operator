package gatewayclass

import (
	"context"
	"fmt"
	"sync"

	configv1 "github.com/openshift/api/config/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	"k8s.io/client-go/tools/record"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
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

	// gatewayclassControllerIndexFieldName is the name of the index for gatewayclass by controller name.
	gatewayclassControllerIndexFieldName = "spec.controllerName"
)

var log = logf.Logger.WithName(controllerName)
var gatewayClassController controller.Controller

// NewUnmanaged creates and returns a controller that watches gatewayclasses and
// installs and configures Istio.  This is an unmanaged controller, which means
// that the manager does not start it.
func NewUnmanaged(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		config:       config,
		client:       mgr.GetClient(),
		cache:        operatorCache,
		fieldIndexer: mgr.GetFieldIndexer(),
		recorder:     mgr.GetEventRecorderFor(controllerName),
	}
	c, err := controller.NewUnmanaged(controllerName, controller.Options{Reconciler: reconciler})
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

	isServiceMeshSubscription := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == operatorcontroller.ServiceMeshOperatorSubscriptionName().Name
	})
	if err = c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.Subscription{},
		reconciler.enqueueRequestForSomeGatewayClass(), isServiceMeshSubscription)); err != nil {
		return nil, err
	}

	isOurInstallPlan := predicate.NewPredicateFuncs(func(o client.Object) bool {
		installPlan := o.(*operatorsv1alpha1.InstallPlan)
		if len(installPlan.Spec.ClusterServiceVersionNames) > 0 {
			for _, csv := range installPlan.Spec.ClusterServiceVersionNames {
				if csv == config.GatewayAPIOperatorVersion {
					return true
				}
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
	if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.InstallPlan{}, reconciler.enqueueRequestForSomeGatewayClass(), isOurInstallPlan, isInstallPlanReadyForApproval)); err != nil {
		return nil, err
	}

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

	// Watch the cluster infrastructure config in case the infrastructure
	// topology changes.
	if err := c.Watch(source.Kind[client.Object](operatorCache, &configv1.Infrastructure{}, reconciler.enqueueRequestForSomeGatewayClass())); err != nil {
		return nil, err
	}

	gatewayClassController = c
	return c, nil
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	// OperatorNamespace is the namespace in which the operator should
	// create the Istio CR.
	OperatorNamespace string
	// OperandNamespace is the namespace in which Istio should be deployed.
	OperandNamespace string
	// GatewayAPIOperatorCatalog is the catalog source to use to install the Gateway API implementation.
	GatewayAPIOperatorCatalog string
	// GatewayAPIOperatorChannel is the release channel of the Gateway API implementation to install.
	GatewayAPIOperatorChannel string
	// GatewayAPIOperatorVersion is the name and release of the Gateway API implementation to install.
	GatewayAPIOperatorVersion string
	// IstioVersion is the version of Istio to configure on the Istio CR.
	IstioVersion string
}

// reconciler reconciles gatewayclasses.
type reconciler struct {
	config Config

	client       client.Client
	cache        cache.Cache
	fieldIndexer client.FieldIndexer
	recorder     record.EventRecorder

	startIstioWatch sync.Once
	// startGatewayclassControllerIndex ensures we create the gatewayclasses index at
	// most once.
	startGatewayclassControllerIndex sync.Once
}

// enqueueRequestForSomeGatewayClass enqueues a reconciliation request for the
// gatewayclass that has the earliest creation timestamp and that specifies our
// controller name.
func (r *reconciler) enqueueRequestForSomeGatewayClass() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, _ client.Object) []reconcile.Request {
			requests := []reconcile.Request{}

			var gatewayClasses gatewayapiv1.GatewayClassList
			if err := r.cache.List(context.Background(), &gatewayClasses); err != nil {
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
		},
	)
}

// Reconcile expects request to refer to a GatewayClass and creates or
// reconciles an Istio deployment.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	var infraConfig configv1.Infrastructure
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, &infraConfig); err != nil {
		return reconcile.Result{}, err
	}

	var gatewayclass gatewayapiv1.GatewayClass
	if err := r.cache.Get(ctx, request.NamespacedName, &gatewayclass); err != nil {
		return reconcile.Result{}, err
	}

	var errs []error

	// Create the Index gatewayclasses once on first reconciliation.  The
	// index cannot be created in NewUnmanaged as the CRD might not exist
	// when NewUnmanaged is called, and so we create the index here.
	r.startGatewayclassControllerIndex.Do(func() {
		gatewayclassControllerIndexFn := client.IndexerFunc(func(o client.Object) []string {
			gatewayclass, ok := o.(*gatewayapiv1.GatewayClass)
			if !ok {
				return []string{}
			}

			return []string{string(gatewayclass.Spec.ControllerName)}
		})
		if err := r.fieldIndexer.IndexField(context.Background(), &gatewayapiv1.GatewayClass{}, gatewayclassControllerIndexFieldName, gatewayclassControllerIndexFn); err != nil {
			log.Error(err, "failed to create index for gatewayclasses", "request", request)
			errs = append(errs, err)
		}
	})

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

	if _, _, err := r.ensureIstio(ctx, &gatewayclass, istioVersion, &infraConfig); err != nil {
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
