package ingressclass

import (
	"context"

	"github.com/pkg/errors"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	"k8s.io/client-go/tools/record"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	networkingv1 "k8s.io/api/networking/v1"

	"k8s.io/apimachinery/pkg/types"

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
	controllerName = "ingressclass_controller"
)

var (
	log = logf.Logger.WithName(controllerName)
)

// New creates and returns a controller that creates and manages IngressClass
// objects for IngressControllers.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	reconciler := &reconciler{
		config:   config,
		client:   mgr.GetClient(),
		cache:    mgr.GetCache(),
		recorder: mgr.GetEventRecorderFor(controllerName),
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &operatorv1.IngressController{}}, &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &networkingv1.IngressClass{}}, handler.EnqueueRequestsFromMapFunc(reconciler.ingressClassToIngressController), predicate.NewPredicateFuncs(ingressClassHasIngressController)); err != nil {
		return nil, err
	}
	return c, nil
}

// ingressClassHasIngressController returns a value indicating whether the
// provided ingressclass references an ingresscontroller.
func ingressClassHasIngressController(o client.Object) bool {
	class := o.(*networkingv1.IngressClass)
	return class.Spec.Controller == routev1.IngressToRouteIngressClassControllerName &&
		class.Spec.Parameters != nil &&
		class.Spec.Parameters.APIGroup != nil &&
		*class.Spec.Parameters.APIGroup == operatorv1.GroupVersion.String() &&
		class.Spec.Parameters.Kind == "IngressController"
}

// ingressClassToIngressController takes an ingressclass and returns a slice of
// reconcile.Request with a request to reconcile the ingresscontroller that is
// associated with the ingressclass.
func (r *reconciler) ingressClassToIngressController(o client.Object) []reconcile.Request {
	class := o.(*networkingv1.IngressClass)
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: r.config.Namespace,
			Name:      class.Spec.Parameters.Name,
		}},
	}
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	Namespace string
}

// reconciler handles the actual ingressclass reconciliation logic.
type reconciler struct {
	config Config

	client   client.Client
	cache    cache.Cache
	recorder record.EventRecorder
}

// Reconcile expects request to refer to an IngressController in the operator
// namespace and creates or reconciles an IngressClass object for that
// IngressController.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	classes := &networkingv1.IngressClassList{}
	if err := r.cache.List(ctx, classes); err != nil {
		return reconcile.Result{}, errors.Wrap(err, "failed to list ingressclasses")
	}

	if _, _, err := r.ensureIngressClass(request.NamespacedName.Name, classes.Items); err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "failed to ensure ingressclass for ingresscontroller %q", request.NamespacedName)
	}

	return reconcile.Result{}, nil
}
