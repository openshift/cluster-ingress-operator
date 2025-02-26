package gatewayapi_upgradeable

import (
	"context"
	"fmt"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	controllerName     = "gatewayapi_upgradeable_controller"
	GatewayAPIAdminKey = "ack-gateway-api-management"
	GatewayAPIAdminMsg = "You are now acknowledging that you are taking control of the Gateway API CRD Management from here on out. Please review the documentation for implications."
)

var (
	log = logf.Logger.WithName(controllerName)
)

// New function initializes the controller and sets up the watch for the ConfigMap
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		cache:  config.Cache,
		client: mgr.GetClient(),
	}

	//Create a new controller with given reconciler
	c, err := controller.New(controllerName, mgr, controller.Options{
		Reconciler: reconciler,
	})
	if err != nil {
		return nil, err
	}

	// Define a predicate filter to watch for specific changes in the ConfigMap
	adminGateFilter := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			// Watch for creation of the "admin-gates" ConfigMap in "openshift-config-managed" namespace
			configmap := e.Object.(*corev1.ConfigMap)
			return configmap.Name == operatorcontroller.AdminGatesConfigMapName().Name && configmap.Namespace == operatorcontroller.AdminGatesConfigMapName().Namespace
		},
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool { return false }, // Ignore delete events
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Watch for updates to the "admin-gates" ConfigMap to check if the admin key changes
			configmapOld := e.ObjectOld.(*corev1.ConfigMap)
			configmapNew := e.ObjectNew.(*corev1.ConfigMap)
			if configmapOld.Name != operatorcontroller.AdminGatesConfigMapName().Name || configmapOld.Namespace != operatorcontroller.AdminGatesConfigMapName().Namespace {
				return false
			}
			oldVal, oldExists := configmapOld.Data[GatewayAPIAdminKey]
			newVal, newExists := configmapNew.Data[GatewayAPIAdminKey]
			return oldExists != newExists || oldVal != newVal
		},
		GenericFunc: func(genericEvent event.GenericEvent) bool { return false }, // Ignore generic events
	}

	toAdminGateConfigMap := func(_ context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: operatorcontroller.AdminGatesConfigMapName(),
		}}
	}
	// Define the CRD predicate
	crdPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.(*apiextensionsv1.CustomResourceDefinition).Spec.Group == gatewayapiv1.GroupName
	})

	//TODO: too many args to c.watch and not enough to source.Kind
	if err := c.Watch(source.Kind(operatorCache, &corev1.ConfigMap{}),
		handler.EnqueueRequestsFromMapFunc(toAdminGateConfigMap),
		adminGateFilter); err != nil {
		return nil, err
	}

	// Watch for CRD updates using the reconciler cache and apply the CRD predicate
	if err := c.Watch(source.Kind(reconciler.cache, &apiextensionsv1.CustomResourceDefinition{}),
		&handler.EnqueueRequestForObject{},
		crdPredicate); err != nil {
		return nil, err
	}
	return c, nil
}

// reconciler struct holds the client and cache attributes
type reconciler struct {
	client client.Client
	cache  cache.Cache
}

// Config struct holds the cache information
type Config struct {
	//namespace referes to namespace where admin gates controller resides
	Namespace string
	//cache should be a namespace global cache
	Cache cache.Cache
}

// Reconcile function implements the logic to check conditions and manage the admin gate
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)
	adminGateConditionExists, err := r.adminGateConditionExists(ctx)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to determine if UnservableInFutureVersionsRoute routes exist: %w", err)
	}
	if adminGateConditionExists {
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

// adminGateConditionExists is a placeholder for now,
// TODO: adminGatesConditionExists needs to be implemented....
func (r *reconciler) adminGateConditionExists(ctx context.Context, condition string) (bool, error) {

	// Placeholder: Attempt to list ConfigMaps
	// Placeholder: Iterate through ConfigMap list and check for the condition (to be implemented).

	// Placeholder return statement, to be replaced with actual logic.
	return false, nil
}

// addAdminGate function adds the admin gate to the ConfigMap
func (r *reconciler) addAdminGate(ctx context.Context) error {
	adminGateConfigMap := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, operatorcontroller.AdminGatesConfigMapName(), adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to get configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}

	if adminGateConfigMap.Data == nil {
		adminGateConfigMap.Data = map[string]string{}
	}

	// Check if the admin key exists and is set to the expected message
	if val, ok := adminGateConfigMap.Data[GatewayAPIAdminKey]; ok && val == GatewayAPIAdminMsg {
		return nil // Exists as expected
	}
	adminGateConfigMap.Data[GatewayAPIAdminKey] = GatewayAPIAdminMsg

	log.Info("Adding admin gate for Gateway API management")
	if err := r.client.Update(ctx, adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to update configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}
	return nil
}

// removeAdminGate function removes the admin gate from the ConfigMap
func (r *reconciler) removeAdminGate(ctx context.Context) error {
	adminGateConfigMap := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, operatorcontroller.AdminGatesConfigMapName(), adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to get configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}

	// Check if the admin key exists
	if _, ok := adminGateConfigMap.Data[GatewayAPIAdminKey]; !ok {
		return nil // Nothing to do if the key doesn't exist
	}

	log.Info("Removing admin gate for Gateway API management")
	delete(adminGateConfigMap.Data, GatewayAPIAdminKey)
	if err := r.client.Update(ctx, adminGateConfigMap); err != nil {
		return fmt.Errorf("failed to update configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}
	return nil
}
