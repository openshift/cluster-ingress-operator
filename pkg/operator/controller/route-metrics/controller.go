package routemetrics

import (
	"context"
	"fmt"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
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
	// Create a copy of the cluster, but with a new cache to watch on Route objects from every namespace.
	newCluster, err := cluster.New(mgr.GetConfig(), func(o *cluster.Options) {
		o.Scheme = mgr.GetScheme()
	})
	if err != nil {
		return nil, err
	}
	// Add the cluster to the manager so that the cluster is started along with the other runnables.
	mgr.Add(newCluster)
	reconciler := &reconciler{
		client:    mgr.GetClient(),
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
	if err := c.Watch(source.NewKindWithCache(&routev1.Route{}, newCluster.GetCache()),
		handler.EnqueueRequestsFromMapFunc(reconciler.routeToIngressController)); err != nil {
		return nil, err
	}
	return c, nil
}

// routeToIngressController .
func (r *reconciler) routeToIngressController(obj client.Object) []reconcile.Request {
	var requests []reconcile.Request
	// Cast the received object into Route object.
	routeObject := obj.(*routev1.Route)

	// Iterate through the related Ingress Controllers.
	for _, ic := range routeObject.Status.Ingress {
		log.Info("queueing ingresscontroller", "name", ic.RouterName)
		// Create a reconcile.Request for the Ingress Controller.
		request := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      ic.RouterName,
				Namespace: r.Namespace,
			},
		}
		requests = append(requests, request)
	}
	return requests
}

// reconciler handles the actual ingresscontroller reconciliation logic in response to events.
type reconciler struct {
	client    client.Client
	Namespace string
}

// Reconcile expects request to refer to a Ingress Controller resource, and will do all the work to gather metrics related to
// the resource.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// Fetch the Ingress Controller object.
	ingressController := &operatorv1.IngressController{}
	if err := r.client.Get(ctx, request.NamespacedName, ingressController); err != nil {
		if kerrors.IsNotFound(err) {
			// This means the Ingress Controller object was already deleted/finalized.
			log.Info("Ingress Controller not found", "request", request)
			// Delete the Shard label corresponding to the Ingress Controller from the RouteMetricsControllerRoutesPerShard metric.
			DeleteRouteMetricsControllerRoutesPerShardMetric(request.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get Ingress Controller %q: %v", request, err)
	}

	// List all the Namespaces which matches the Namespace LabelSelector.
	namespaceList := corev1.NamespaceList{}
	if ingressController.Spec.NamespaceSelector != nil {
		namespaceLabelSelector := ingressController.Spec.NamespaceSelector.MatchLabels
		if err := r.client.List(ctx, &namespaceList, &client.ListOptions{
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

	// Store the previous value of the metric.
	prevMetricValue := GetRouteMetricsControllerRoutesPerShardMetric(request.Name)

	// Initialize the RouteMetricsControllerRoutesPerShard metric for Ingress Controller.
	InitializeRouteMetricsControllerRoutesPerShardMetric(request.Name)

	// Iterate through all the Namespaces.
	for _, namespace := range namespaceList.Items {
		// List all the Routes which matches the Route LabelSelector and the Namespace.
		routeList := routev1.RouteList{}
		if err := r.client.List(ctx, &routeList, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(routeLabelSelector),
			Namespace:     namespace.Name,
		}); err != nil {
			// Set the value of the metric to its previous value and re-queue the request.
			SetRouteMetricsControllerRoutesPerShardMetric(request.Name, prevMetricValue)
			return reconcile.Result{}, fmt.Errorf("failed to list Routes for the Shard %q: %v", request, err)
		}

		// Iterate through all the Routes.
		for _, route := range routeList.Items {
			// Check if the Route was admitted by the Ingress Controller.
			if routeAdmitted(route, ingressController.Name) {
				// If the Route was admitted then, the RouteMetricsControllerRoutesPerShard should be incremented by 1 for the same Shard.
				IncrementRouteMetricsControllerRoutesPerShardMetric(ingressController.Name)
			}
		}
	}

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
