package routestatus

import (
	"context"
	"fmt"

	routev1 "github.com/openshift/api/route/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "route_status_controller"
)

var (
	log = logf.Logger.WithName(controllerName)
)

// New creates the route status controller. This is the controller that handles reconciling route status and keeping
// it in sync.
func New(mgr manager.Manager, namespace string, cache cache.Cache) (controller.Controller, error) {
	reconciler := &reconciler{
		cache:     cache,
		client:    mgr.GetClient(),
		namespace: namespace,
	}
	c, err := controller.New(controllerName, mgr, controller.Options{
		Reconciler: reconciler,
	})
	if err != nil {
		return nil, err
	}
	// Add watch for changes in Route
	if err := c.Watch(source.NewKindWithCache(&routev1.Route{}, cache), &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}
	// Add watch for changes in Namespace
	if err := c.Watch(source.NewKindWithCache(&corev1.Namespace{}, cache),
		handler.EnqueueRequestsFromMapFunc(reconciler.namespaceToRoutes)); err != nil {
		return nil, err
	}
	return c, nil
}

// namespaceToRoutes creates a reconcile.Request for all the routes in the namespace.
func (r *reconciler) namespaceToRoutes(obj client.Object) []reconcile.Request {
	var requests []reconcile.Request
	// Cast the received object into a Namespace object.
	ns := obj.(*corev1.Namespace)

	routeList := routev1.RouteList{}
	if err := r.cache.List(context.Background(), &routeList, client.InNamespace(ns.Name)); err != nil {
		log.Error(err, "failed to list routes for namespace", "related", obj.GetSelfLink())
		return requests
	}

	for _, route := range routeList.Items {
		log.Info("queueing route", "name", route.Name, "related", obj.GetSelfLink())
		request := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: route.Namespace,
				Name:      route.Name,
			},
		}
		requests = append(requests, request)
	}

	return requests
}

// reconciler handles the actual route reconciliation logic in response to events.
type reconciler struct {
	cache     cache.Cache
	client    client.Client
	namespace string
}

// Reconcile expects request to refer to a Route object, which will clear route status if it is no longer admitted
// by each ingress controller it claims to be.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// Fetch the Route object.
	route := &routev1.Route{}
	if err := r.cache.Get(ctx, request.NamespacedName, route); err != nil {
		if kerrors.IsNotFound(err) {
			// This means the rout object was already deleted/finalized.
			log.Info("route not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get route %q: %w", request, err)
	}

	// Validate and clean up route status for given route if any status is stale.
	if err := clearStaleRouteAdmittedStatus(r.client, route); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}
