package routemetrics

import (
	"context"
	"fmt"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
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
func New(mgr manager.Manager) (controller.Controller, error) {
	// Create a new cluster with a new cache to watch on Route objects from every namespace.
	newCluster, err := cluster.New(mgr.GetConfig(), func(o *cluster.Options) {
		o.Scheme = mgr.GetScheme()
	})
	if err != nil {
		return nil, err
	}
	// Add the cluster to the manager so that the cluster is started along with the other runnables.
	mgr.Add(newCluster)
	reconciler := &reconciler{
		client: mgr.GetClient(),
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
		handler.EnqueueRequestsFromMapFunc(func(client.Object) []reconcile.Request { return nil }),
		predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool { return reconciler.routeCreateEventGatherMetrics(e.Object) },
			DeleteFunc: func(e event.DeleteEvent) bool { return reconciler.routeDeleteEventGatherMetrics(e.Object) },
			UpdateFunc: func(e event.UpdateEvent) bool {
				return reconciler.routeUpdateEventGatherMetrics(e.ObjectOld, e.ObjectNew)
			},
			GenericFunc: func(e event.GenericEvent) bool { return false },
		}); err != nil {
		return nil, err
	}
	return c, nil
}

// routeCreateEventGatherMetrics gather RouteMetricsControllerRouteType and RouteMetricsControllerRoutesPerShard metrics
// based on the Route Create event.
func (r *reconciler) routeCreateEventGatherMetrics(obj client.Object) bool {
	// Cast the received object into Route object.
	routeObject := obj.(*routev1.Route)

	log.Info("route create event", "name", routeObject.Name, "namespace", routeObject.Namespace)

	// Iterate through the related Ingress Controllers.
	for _, ingress := range routeObject.Status.Ingress {
		// Check if the Route has been admitted by the Ingress Controller.
		for _, cond := range ingress.Conditions {
			if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
				// If the Route is admitted then, the RouteMetricsControllerRoutesPerShard should be incremented by 1 for the same Shard.
				IncrementRouteMetricsControllerRoutesPerShardMetric(ingress.RouterName)
			}
		}
	}
	return false
}

// routeDeleteEventGatherMetrics gather RouteMetricsControllerRouteType and RouteMetricsControllerRoutesPerShard metrics
// based on the Route Delete event.
func (r *reconciler) routeDeleteEventGatherMetrics(obj client.Object) bool {
	// Cast the received object into Route object.
	routeObject := obj.(*routev1.Route)

	log.Info("route create event", "name", routeObject.Name, "namespace", routeObject.Namespace)

	// Iterate through the related Ingress Controllers.
	for _, ingress := range routeObject.Status.Ingress {
		// Check if the Route was admitted by the Ingress Controller.
		for _, cond := range ingress.Conditions {
			if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
				// If the Route was admitted then, the RouteMetricsControllerRoutesPerShard should be decremented by 1 for the same Shard.
				DecrementRouteMetricsControllerRoutesPerShardMetric(ingress.RouterName)
			}
		}
	}
	return false
}

// routeUpdateEventGatherMetrics gather RouteMetricsControllerRouteType and RouteMetricsControllerRoutesPerShard metrics
// based on the Route Update event.
func (r *reconciler) routeUpdateEventGatherMetrics(oldObj client.Object, newObj client.Object) bool {
	// Cast the received old object into Route object.
	routeOldObject := oldObj.(*routev1.Route)
	// Cast the received new object into Route object.
	routeNewObject := newObj.(*routev1.Route)

	log.Info("route create event", "name", routeOldObject.Name, "namespace", routeOldObject.Namespace)

	// Create a map of existing Shards the Route belongs to.
	existingShards := make(map[string]bool)
	// Create a map of current Shards the Route belongs to.
	currentShards := make(map[string]bool)

	// Iterate through the related Ingress Controllers of the old Route object.
	for _, ingress := range routeOldObject.Status.Ingress {
		// Check if the Route was admitted by the Ingress Controller.
		for _, cond := range ingress.Conditions {
			if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
				existingShards[ingress.RouterName] = true
			}
		}
	}

	// Iterate through the related Ingress Controllers of the new Route object.
	for _, ingress := range routeNewObject.Status.Ingress {
		// Check if the Route has been admitted by the Ingress Controller.
		for _, cond := range ingress.Conditions {
			// Check if the Route is admitted by the Ingress Controller.
			if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
				currentShards[ingress.RouterName] = true
				if _, alreadyAdmitted := existingShards[ingress.RouterName]; !alreadyAdmitted {
					// If the Route previously did not belong to the Shard and currently does, then the RouteMetricsControllerRoutesPerShard
					// should be incremented by 1 for the same Shard.
					IncrementRouteMetricsControllerRoutesPerShardMetric(ingress.RouterName)
				}
			}
		}
	}

	// Iterate through the existing Shards.
	for shard := range existingShards {
		// Check if the Route is currently admitted by the Shard or not.
		if _, currentlyAdmitted := currentShards[shard]; !currentlyAdmitted {
			// If the Route previously belonged to the Shard but currently does not, then the RouteMetricsControllerRoutesPerShard
			// should be decremented by 1 for the same Shard.
			DecrementRouteMetricsControllerRoutesPerShardMetric(shard)
		}
	}

	return false
}

// reconciler handles the actual ingresscontroller reconciliation logic in response to events.
type reconciler struct {
	client client.Client
}

// Reconcile expects request to refer to a ingresscontroller resource, and will do all the work to gather metrics related to
// the resource.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// Fetch the ingresscontroller object.
	ingress := &operatorv1.IngressController{}
	if err := r.client.Get(ctx, request.NamespacedName, ingress); err != nil {
		if kerrors.IsNotFound(err) {
			// This means the ingresscontroller object was already deleted/finalized.
			log.Info("ingresscontroller not found", "request", request)
			// Delete the Shard label corresponding to the ingresscontroller from the RouteMetricsControllerRoutesPerShard metric.
			DeleteRouteMetricsControllerRoutesPerShardMetric(request.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get ingresscontroller %q: %v", request, err)
	}

	// Add/Update event.

	// Initialize the RouteMetricsControllerRoutesPerShard metric for ingresscontroller.
	InitializeRouteMetricsControllerRoutesPerShardMetric(request.Name)
	return reconcile.Result{}, nil
}
