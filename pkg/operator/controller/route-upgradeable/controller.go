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

	"github.com/google/go-cmp/cmp"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
)

const (
	controllerName         = "route_upgradeable_controller"
	routeConfigAck415      = "ack-4.15-route-config-not-supported-in-4.16"
	routeConfigAck415Msg   = "A route in this cluster contains a configuration that isn't supported in 4.16. Please review each route's UnservableInFutureVersions status condition for more information."
	adminGateConfigMapName = "admin-gates"
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
		cache:     config.RouteCache,
		namespace: config.Namespace,
		client:    mgr.GetClient(),
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
	if err := reconciler.cache.IndexField(context.Background(), &routev1.Route{}, "UnservableInFutureVersionsRoute", client.IndexerFunc(func(o client.Object) []string {
		if cond := getRouteUnservableInFutureVersionsStatusCondition(o.(*routev1.Route)); cond != nil {
			return []string{string(operatorv1.ConditionTrue)}
		}
		return []string{}
	})); err != nil {
		return nil, fmt.Errorf("failed to create index for routes: %w", err)
	}

	// Only reconcile when the route's UnservableInFutureVersions status is added, changed, or removed.
	updatedUnservableInFutureVersionsConditionFilter := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRoute, ok := e.ObjectOld.(*routev1.Route)
			if !ok {
				return false
			}
			newRoute, ok := e.ObjectNew.(*routev1.Route)
			if !ok {
				return false
			}
			return cmp.Equal(getRouteUnservableInFutureVersionsStatusCondition(newRoute), getRouteUnservableInFutureVersionsStatusCondition(oldRoute))
		},
	}

	// Only reconcile when the admin gate config map changed or created.
	adminGateFilter := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			configmap, ok := e.ObjectNew.(*corev1.ConfigMap)
			if !ok {
				return false
			}
			return configmap.Name == adminGateConfigMapName && configmap.Namespace == operatorcontroller.GlobalMachineSpecifiedConfigNamespace
		},
		CreateFunc: func(e event.CreateEvent) bool {
			configmap, ok := e.Object.(*corev1.ConfigMap)
			if !ok {
				return false
			}
			return configmap.Name == adminGateConfigMapName && configmap.Namespace == operatorcontroller.GlobalMachineSpecifiedConfigNamespace
		},
	}

	// Watch for routes using reconciler.cache because this cache
	// includes all namespaces for watching routes.
	if err := c.Watch(source.Kind(reconciler.cache, &routev1.Route{}),
		&handler.EnqueueRequestForObject{},
		updatedUnservableInFutureVersionsConditionFilter); err != nil {
		return nil, err
	}
	// Watch for configmaps updates using the operatorCache because this cache includes
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
	client    client.Client
	cache     cache.Cache
	namespace string
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	Namespace  string
	RouteCache cache.Cache // this should be a global cache for all routes
}

// Reconcile expects request to refer to a ConfigMap resource. It will add
// an admin gate if any routes have the UnservableInFutureVersions status and remove the
// admin gate if no routes have the UnservableInFutureVersions status.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// List routes using the "UnservableInFutureVersionsRoute" index which only lists UnservableInFutureVersions routes.
	routeList := &routev1.RouteList{}
	listOpts := client.MatchingFields(map[string]string{
		"UnservableInFutureVersionsRoute": string(operatorv1.ConditionTrue),
	})
	if err := r.cache.List(ctx, routeList, listOpts); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list all routes: %w", err)
	}

	// If any UnservableInFutureVersionsRoute route exists, add admin gate.
	// If no UnservableInFutureVersionsRoute route exists, remove admin gate.
	if len(routeList.Items) > 0 {
		if err := r.addAdminGate(ctx); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to add admin ack: %w", err)
		}
	} else {
		if err := r.removeAdminGate(ctx); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to remove admin ack: %w", err)
		}
	}

	return reconcile.Result{}, nil
}

// getRouteUnservableInFutureVersionsStatusCondition finds and returns a UnservableInFutureVersions=true
// condition on the route. If the UnservableInFutureVersions=true condition doesn't exist, nil is returned.
func getRouteUnservableInFutureVersionsStatusCondition(route *routev1.Route) *routev1.RouteIngressCondition {
	var unserableInFutureVersionsCondition *routev1.RouteIngressCondition
	for _, ingress := range route.Status.Ingress {
		for _, condition := range ingress.Conditions {
			if condition.Type == routev1.RouteUnservableInFutureVersions && condition.Status == corev1.ConditionTrue {
				unserableInFutureVersionsCondition = &condition
				break
			}
		}
	}
	return unserableInFutureVersionsCondition
}

// addAdminGate adds an admin gate to block upgrades.
func (r *reconciler) addAdminGate(ctx context.Context) error {
	adminGateConfigMap := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: adminGateConfigMapName, Namespace: operatorcontroller.GlobalMachineSpecifiedConfigNamespace}, adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to get admin-gates configmap: %w", err)
	}

	log.Info("Updating admin-gates to require admin-ack for unservable in future versions route")

	if adminGateConfigMap.Data == nil {
		adminGateConfigMap.Data = map[string]string{}
	}

	if _, ok := adminGateConfigMap.Data[routeConfigAck415]; !ok {
		adminGateConfigMap.Data[routeConfigAck415] = routeConfigAck415Msg
	} else {
		return nil // Already exists.
	}
	if err := r.client.Update(ctx, adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to update configmap: %w", err)
	}
	return nil
}

// removeAdminGate removes the admin gate to unblock upgrades.
func (r *reconciler) removeAdminGate(ctx context.Context) error {
	adminGateConfigMap := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: adminGateConfigMapName, Namespace: operatorcontroller.GlobalMachineSpecifiedConfigNamespace}, adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to get admin-gates configmap: %w", err)
	}

	if _, ok := adminGateConfigMap.Data[routeConfigAck415]; !ok {
		// Nothing needs to be done if key doesn't exist
		return nil
	}

	log.Info("Updating admin-gates to remove gate for unservable in future versions route")

	delete(adminGateConfigMap.Data, routeConfigAck415)
	if err := r.client.Update(ctx, adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to update configmap: %w", err)
	}
	return nil
}
