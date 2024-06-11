package routeupgradeable

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
)

const (
	controllerName = "route_upgradeable_controller"

	// RouteConfigAdminGate415Key is the key of the admin-gate that will block upgrades from 4.15 to 4.16.
	RouteConfigAdminGate415Key = "ack-4.15-route-config-not-supported-in-4.16"

	// routeConfigAdminGate415Msg is the message that will be displayed in the admin-gates configmap.
	routeConfigAdminGate415Msg = "A route in this cluster contains a configuration that isn't supported in 4.16. Please review each route's UnservableInFutureVersions status condition for more information."

	// unservableInFutureVersionsIndexFieldName is the name of the index field that will list
	// routes with UnservableInFutureVersions condition.
	unservableInFutureVersionsIndexFieldName = "UnservableInFutureVersionsRoute"
)

var (
	log = logf.Logger.WithName(controllerName)
)

// New creates the route upgradeable controller. This is the controller that handles
// interpreting route status to determine if an admin gate is needed. The admin gate
// will block upgrades until an admin acks it or resolves the unsupported configuration.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		cache:  config.Cache,
		client: mgr.GetClient(),
	}

	c, err := controller.New(controllerName, mgr, controller.Options{
		Reconciler: reconciler,
		RateLimiter: workqueue.NewMaxOfRateLimiter(
			// Rate-limit to 1 update every 5 seconds per
			// reconcile to avoid burning CPU if object
			// updates are frequent.
			workqueue.NewItemExponentialFailureRateLimiter(5*time.Second, 30*time.Second),
			// 10 qps, 100 bucket size, same as DefaultControllerRateLimiter().
			&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
		),
	})
	if err != nil {
		return nil, err
	}

	// Index routes over the UnservableInFutureVersions condition so that we can
	// efficiently find routes with UnservableInFutureVersions=true.
	if err := reconciler.cache.IndexField(context.Background(), &routev1.Route{}, unservableInFutureVersionsIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
		if getRouteUnservableInFutureVersionsStatusCondition(o.(*routev1.Route)) != nil {
			return []string{string(operatorv1.ConditionTrue)}
		}
		return nil
	})); err != nil {
		return nil, fmt.Errorf("failed to create index for routes: %w", err)
	}

	updatedUnservableInFutureVersionsConditionFilter := predicate.Funcs{
		// Although this CreateFunc triggers for each route upon operator startup, we will rely on the
		// configmap watch with the CreateFunc on adminGateFilter to trigger for the admin-gates
		// configmap to ensure the initial state.
		CreateFunc: func(createEvent event.CreateEvent) bool { return false },
		// The only route deletions that matter are routes that have UnservableInFutureVersions.
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
			route := deleteEvent.Object.(*routev1.Route)
			return getRouteUnservableInFutureVersionsStatusCondition(route) != nil
		},
		// Only route updates for UnservableInFutureVersions changes matter.
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRoute := e.ObjectOld.(*routev1.Route)
			newRoute := e.ObjectNew.(*routev1.Route)
			return routeIngressConditionStatusChanged(
				getRouteUnservableInFutureVersionsStatusCondition(newRoute),
				getRouteUnservableInFutureVersionsStatusCondition(oldRoute),
			)
		},
		// Ignore all other events.
		GenericFunc: func(genericEvent event.GenericEvent) bool { return false },
	}

	adminGateFilter := predicate.Funcs{
		// Filter for only when the admin-gates configmap is created.
		// This runs on operator startup, and ensures initial state for the admin-gates configmap.
		CreateFunc: func(e event.CreateEvent) bool {
			configmap := e.Object.(*corev1.ConfigMap)
			return configmap.Name == operatorcontroller.AdminGatesConfigMapName().Name && configmap.Namespace == operatorcontroller.AdminGatesConfigMapName().Namespace
		},
		// Ignore delete events because our reconcile can't do anything if
		// the admin-gates configmap doesn't exist.
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool { return false },
		// Filter for when our specific key is updated for admin-gates configmap.
		UpdateFunc: func(e event.UpdateEvent) bool {
			configmapOld := e.ObjectOld.(*corev1.ConfigMap)
			configmapNew := e.ObjectNew.(*corev1.ConfigMap)
			if configmapOld.Name != operatorcontroller.AdminGatesConfigMapName().Name || configmapOld.Namespace != operatorcontroller.AdminGatesConfigMapName().Namespace {
				return false
			}
			oldVal, oldExists := configmapOld.Data[RouteConfigAdminGate415Key]
			newVal, newExists := configmapNew.Data[RouteConfigAdminGate415Key]
			return oldExists != newExists || oldVal != newVal
		},
		// Ignore all other events.
		GenericFunc: func(genericEvent event.GenericEvent) bool { return false },
	}

	toAdminGateConfigMap := func(_ context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{
			operatorcontroller.AdminGatesConfigMapName(),
		}}
	}

	// Watch for routes using reconciler.cache because this cache
	// includes all namespaces for watching routes.
	if err := c.Watch(source.Kind(reconciler.cache, &routev1.Route{}),
		handler.EnqueueRequestsFromMapFunc(toAdminGateConfigMap),
		updatedUnservableInFutureVersionsConditionFilter); err != nil {
		return nil, err
	}
	// Watch for configmap updates using the operatorCache because this cache includes
	// the openshift-config-managed namespace in which our admin-gate configmap resides.
	if err := c.Watch(source.Kind(operatorCache, &corev1.ConfigMap{}),
		&handler.EnqueueRequestForObject{},
		adminGateFilter); err != nil {
		return nil, err
	}
	return c, nil
}

// reconciler handles the actual configmap reconciliation logic in response to events.
type reconciler struct {
	client client.Client
	cache  cache.Cache
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	// Cache should be a namespace global cache since routes
	// are not restricted to any particular namespace.
	Cache cache.Cache
}

// Reconcile expects request to refer to a configmap resource. It will add
// an admin gate if any routes have the UnservableInFutureVersions status and remove the
// admin gate if no routes have the UnservableInFutureVersions status.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	unservableInFutureVersionsRoutesExists, err := r.unservableInFutureVersionsRouteExists(ctx)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to determine if UnservableInFutureVersionsRoute routes exist: %w", err)
	}
	if unservableInFutureVersionsRoutesExists {
		if err := r.addAdminGate(ctx); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to add admin gate: %w", err)
		}
	} else {
		if err := r.removeAdminGate(ctx); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to remove admin gate: %w", err)
		}
	}

	return reconcile.Result{}, nil
}

// unservableInFutureVersionsRouteExists checks if there are any routes with
// the UnservableInFutureVersions condition. It returns true if at least
// one route is found with the UnservableInFutureVersions condition.
func (r *reconciler) unservableInFutureVersionsRouteExists(ctx context.Context) (bool, error) {
	// List routes using the "UnservableInFutureVersionsRoute" index which only lists UnservableInFutureVersions routes.
	routeList := &routev1.RouteList{}
	listOpts := client.MatchingFields(map[string]string{
		unservableInFutureVersionsIndexFieldName: string(operatorv1.ConditionTrue),
	})
	if err := r.cache.List(ctx, routeList, listOpts); err != nil {
		return false, fmt.Errorf("failed to list all routes: %w", err)
	}

	return len(routeList.Items) != 0, nil
}

// getRouteUnservableInFutureVersionsStatusCondition finds and returns a
// RouteIngressCondition with UnservableInFutureVersions=true for any router.
// If the UnservableInFutureVersions=true condition doesn't exist, nil is returned.
func getRouteUnservableInFutureVersionsStatusCondition(route *routev1.Route) *routev1.RouteIngressCondition {
	for i := range route.Status.Ingress {
		for j := range route.Status.Ingress[i].Conditions {
			condition := &route.Status.Ingress[i].Conditions[j]
			if condition.Type == routev1.RouteUnservableInFutureVersions && condition.Status == corev1.ConditionTrue {
				return condition
			}
		}
	}
	return nil
}

// addAdminGate adds an admin gate to block upgrades.
func (r *reconciler) addAdminGate(ctx context.Context) error {
	adminGateConfigMap := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, operatorcontroller.AdminGatesConfigMapName(), adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to get configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}

	if adminGateConfigMap.Data == nil {
		adminGateConfigMap.Data = map[string]string{}
	}

	if val, ok := adminGateConfigMap.Data[RouteConfigAdminGate415Key]; ok && val == routeConfigAdminGate415Msg {
		return nil // Exists as expected.
	}
	adminGateConfigMap.Data[RouteConfigAdminGate415Key] = routeConfigAdminGate415Msg

	log.Info("Adding admin gate for unservable in future versions route")
	if err := r.client.Update(ctx, adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to update configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}
	return nil
}

// removeAdminGate removes the admin gate to unblock upgrades.
func (r *reconciler) removeAdminGate(ctx context.Context) error {
	adminGateConfigMap := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, operatorcontroller.AdminGatesConfigMapName(), adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to get configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}

	if _, ok := adminGateConfigMap.Data[RouteConfigAdminGate415Key]; !ok {
		// Nothing needs to be done if key doesn't exist
		return nil
	}

	log.Info("Removing admin gate for unservable in future versions route")

	delete(adminGateConfigMap.Data, RouteConfigAdminGate415Key)
	if err := r.client.Update(ctx, adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to update configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}
	return nil
}

// routeIngressConditionStatusChanged detects if a RouteIngressCondition's status changed.
func routeIngressConditionStatusChanged(a, b *routev1.RouteIngressCondition) bool {
	// If one is nil, then must both be nil to be unchanged.
	if a == nil || b == nil {
		return a != b
	}
	return a.Status != b.Status
}
