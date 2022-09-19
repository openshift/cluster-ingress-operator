package routemetrics

import (
	"context"
	"fmt"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
)

const (
	controllerName = "route_metrics_controller"
)

var (
	log = logf.Logger.WithName(controllerName)
)

// New creates the route metrics controller. This is the controller
// that handles all the logic for gathering and exporting
// metrics related to route resources.
func New(mgr manager.Manager, namespace string) (controller.Controller, error) {
	// Create a new cache to watch on Route objects from every namespace.
	newCache, err := cache.New(mgr.GetConfig(), cache.Options{
		Scheme: mgr.GetScheme(),
	})
	if err != nil {
		return nil, err
	}
	// Add the cache to the manager so that the cache is started along with the other runnables.
	mgr.Add(newCache)
	reconciler := &reconciler{
		cache:     newCache,
		Namespace: namespace,
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	// add watch for changes in IngressController
	if err := c.Watch(&source.Kind{Type: &operatorv1.IngressController{}}, &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}
	// add watch for changes in Route
	if err := c.Watch(source.NewKindWithCache(&routev1.Route{}, newCache),
		handler.EnqueueRequestsFromMapFunc(reconciler.routeToIngressController)); err != nil {
		return nil, err
	}
	return c, nil
}

// routeToIngressController creates a reconcile.Request for all the Ingress Controllers related to the Route object.
func (r *reconciler) routeToIngressController(obj client.Object) []reconcile.Request {
	var requests []reconcile.Request
	// Cast the received object into Route object.
	routeObject := obj.(*routev1.Route)

	// Iterate through the related RouteIngresses.
	for _, ri := range routeObject.Status.Ingress {
		// Check if the Route was admitted by the RouteIngress.
		for _, cond := range ri.Conditions {
			if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
				log.Info("queueing ingresscontroller", "name", ri.RouterName)
				// Create a reconcile.Request for the router named in the RouteIngress.
				request := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      ri.RouterName,
						Namespace: r.Namespace,
					},
				}
				requests = append(requests, request)
			}
		}
	}
	return requests
}

// reconciler handles the actual ingresscontroller reconciliation logic in response to events.
type reconciler struct {
	cache     cache.Cache
	Namespace string
}

// Reconcile expects request to refer to a Ingress Controller resource, and will do all the work to gather metrics related to
// the resource.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// Fetch the Ingress Controller object.
	ingressController := &operatorv1.IngressController{}
	if err := r.cache.Get(ctx, request.NamespacedName, ingressController); err != nil {
		if kerrors.IsNotFound(err) {
			// This means the Ingress Controller object was already deleted/finalized.
			log.Info("Ingress Controller not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get Ingress Controller %q: %v", request, err)
	}

	// If the ingresscontroller is deleted, then delete the corresponding RouteMetricsControllerRoutesPerShard metric label
	// and return early.
	if ingressController.DeletionTimestamp != nil {
		// Delete the Shard label corresponding to the Ingress Controller from the RouteMetricsControllerRoutesPerShard metric.
		DeleteRouteMetricsControllerRoutesPerShardMetric(request.Name)
		log.Info("RoutesPerShard metric label corresponding to the Ingress Controller is successfully deleted", "ingresscontroller", ingressController)
		return reconcile.Result{}, nil
	}

	// List all the Namespaces which matches the Namespace LabelSelector.
	namespaceList := corev1.NamespaceList{}
	if ingressController.Spec.NamespaceSelector != nil {
		namespaceLabelSelector := ingressController.Spec.NamespaceSelector.MatchLabels
		if err := r.cache.List(ctx, &namespaceList, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(namespaceLabelSelector),
		}); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to list Namespaces %q: %v", request, err)
		}
	} else {
		// If Namespace LabelSelector is not provided then add Namespace with empty name to indicate all Namespaces.
		namespaceList.Items = append(namespaceList.Items, corev1.Namespace{})
	}

	// Get the Route LabelSelector from the Ingress Controller.
	var routeLabelSelector map[string]string
	if ingressController.Spec.RouteSelector != nil {
		routeLabelSelector = ingressController.Spec.RouteSelector.MatchLabels
	}

	// Variable to store the number of routes admitted by the Shard (Ingress Controller).
	routesAdmitted := 0

	// Iterate through all the Namespaces.
	for _, namespace := range namespaceList.Items {
		// List all the Routes which matches the Route LabelSelector and the Namespace.
		routeList := routev1.RouteList{}
		if err := r.cache.List(ctx, &routeList, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(routeLabelSelector),
			Namespace:     namespace.Name,
		}); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to list Routes for the Shard %q: %v", request, err)
		}

		// Iterate through all the Routes.
		for _, route := range routeList.Items {
			// Check if the Route was admitted by the Ingress Controller.
			if routeAdmitted(route, ingressController.Name) {
				// If the Route was admitted then, the routesAdmitted should be incremented by 1 for the Shard.
				routesAdmitted++
			}
		}
	}

	// Set the value of the metric to the number of routesAdmitted for the corresponding Shard (Ingress Controller).
	SetRouteMetricsControllerRoutesPerShardMetric(request.Name, float64(routesAdmitted))

	return reconcile.Result{}, nil
}

// routeAdmitted returns true if a given route has been admitted by the Ingress Controller.
func routeAdmitted(route routev1.Route, ingressControllerName string) bool {
	// Iterate through the related Ingress Controllers.
	for _, ingress := range route.Status.Ingress {
		// Check if the RouterName matches the name of the Ingress Controller.
		if ingress.RouterName == ingressControllerName {
			// Check if the Route was admitted by the Ingress Controller.
			for _, cond := range ingress.Conditions {
				if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
					return true
				}
			}
			return false
		}
	}
	return false
}
