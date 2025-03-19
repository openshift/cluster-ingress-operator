package gatewayclass

import (
	"context"
	"fmt"
	"sync"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	"k8s.io/client-go/tools/record"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

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
)

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
	c, err := controller.NewUnmanaged(controllerName, mgr, controller.Options{Reconciler: reconciler})
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
	// Check the approved status of an InstallPlan. The ingress operator only needs to potentially move install plans
	// from Approved=false to Approved=true, so we can filter out all approved plans at the Watch() level.
	isInstallPlanApproved := predicate.NewPredicateFuncs(func(o client.Object) bool {
		installPlan := o.(*operatorsv1alpha1.InstallPlan)
		return installPlan.Spec.Approved
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.InstallPlan{}, reconciler.enqueueRequestForSomeGatewayClass(), isOurInstallPlan, predicate.Not(isInstallPlanApproved))); err != nil {
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
	// GatewayAPIOperatorChannel is the release channel of the Gateway API implementation to install.
	GatewayAPIOperatorChannel string
	// GatewayAPIOperatorVersion is the name and release of the Gateway API implementation to install.
	GatewayAPIOperatorVersion string
}

// reconciler reconciles gatewayclasses.
type reconciler struct {
	config Config

	client   client.Client
	cache    cache.Cache
	recorder record.EventRecorder

	startIstioWatch sync.Once
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

	var gatewayclass gatewayapiv1.GatewayClass
	if err := r.cache.Get(ctx, request.NamespacedName, &gatewayclass); err != nil {
		return reconcile.Result{}, err
	}

	var errs []error
	if _, _, err := r.ensureServiceMeshOperatorSubscription(ctx); err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure ServiceMeshOperatorSubscription: %w", err))
	}
	if _, _, err := r.ensureServiceMeshOperatorInstallPlan(ctx); err != nil {
		errs = append(errs, err)
	}
	if _, _, err := r.ensureIstio(ctx, &gatewayclass); err != nil {
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
