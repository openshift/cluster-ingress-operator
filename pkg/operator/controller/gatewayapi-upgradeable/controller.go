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
func New(mgr manager.Manager) (controller.Controller, error) {
	reconciler := &reconciler{
		cache: mgr.GetCache(), // Directly using mgr.GetCache()
	}

	//Create a new controller with given reconciler
	c, err := controller.New(controllerName, mgr, controller.Options{
		Reconciler: reconciler,
	})
	if err != nil {
		return nil, err
	}

	// Define a predicate filter to watch for specific changes in the ConfigMap
	// Check that ConfigMap's name and namespace match expected values
	adminGatePredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		cm := o.(*corev1.ConfigMap)
		return cm.Name == operatorcontroller.AdminGatesConfigMapName().Name && cm.Namespace == operatorcontroller.AdminGatesConfigMapName().Namespace
	})

	//Map events to the admin gate ConfigMap
	toAdminGateConfigMap := func(_ context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: operatorcontroller.AdminGatesConfigMapName(),
		}}
	}

	// Define the CRD predicate
	crdPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.(*apiextensionsv1.CustomResourceDefinition).Spec.Group == gatewayapiv1.GroupName
	})

	// Set up a single watch for CRD events and map to admin gate ConfigMap
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &apiextensionsv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(toAdminGateConfigMap), crdPredicate)); err != nil {
		return nil, err
	}

	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.ConfigMap{}, &handler.EnqueueRequestForObject{}, adminGatePredicate)); err != nil {
		return nil, err
	}

	return c, nil
}

// reconciler struct holds the client and cache attributes
type reconciler struct {
	client client.Client
	cache  cache.Cache
}

// Reconcile function implements the logic to check conditions and manage the admin gate
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// Check if the admin gate should be added or removed
	adminGateConditionExists, err := r.adminGateConditionExists(ctx)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to determine if admin gate exist: %w", err)
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

// adminGateConditionExists checks if the admin gate condition exists based on both ConfigMap and CRDs
func (r *reconciler) adminGateConditionExists(ctx context.Context) (bool, error) {
	adminGateConfigMap := &corev1.ConfigMap{}
	if err := r.cache.Get(ctx, operatorcontroller.AdminGatesConfigMapName(), adminGateConfigMap); err != nil {
		return false, fmt.Errorf("failed to get configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}

	// Check if the admin key exists and is set to the expected message
	if val, ok := adminGateConfigMap.Data[GatewayAPIAdminKey]; ok && val == GatewayAPIAdminMsg {
		return true, nil // Exists as expected
	}

	// Check for the presence of Gateway API CRDs
	crds := &apiextensionsv1.CustomResourceDefinitionList{}
	if err := r.client.List(ctx, crds); err != nil {
		return false, fmt.Errorf("failed to list CRDs: %w", err)
	}

	for _, crd := range crds.Items {
		if crd.Spec.Group == gatewayapiv1.GroupName {
			return true, nil
		}
	}

	return false, nil // No Gateway API CRDs found
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
