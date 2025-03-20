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
	gatewayAPIAdminKey = "ack-4.18-gateway-api-management-in-4.19"
	gatewayAPIAdminMsg = "Gateway API CRDs have been detected. OCP fully manages the life-cycle of Gateway API CRDs. External management is unsupported and will be prevented. The cluster administrator is responsible for the safety of existing Gateway API implementations and must acknowledge their responsibilities via the admin gate to proceed with upgrades. See https://docs.redhat.com/en/documentation/openshift_container_platform/4.19/html/release_notes/ocp-4-19-release-notes#ocp-4-19-networking-gateway-api-crd-lifecycle_release-notes for details. Failure to read and understand the documentation for this and the implications can result in outages and data loss."
)

var (
	log = logf.Logger.WithName(controllerName)
)

// The New function initializes the controller and sets up the watch for the ConfigMap.
func New(mgr manager.Manager) (controller.Controller, error) {
	c, err := controller.New(controllerName, mgr, controller.Options{
		Reconciler: &reconciler{
			client: mgr.GetClient(),
			cache:  mgr.GetCache(),
		},
	})
	if err != nil {
		return nil, err
	}

	// Mapping the events to the admin gate ConfigMap.
	toAdminGatesConfigMap := func(_ context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: operatorcontroller.AdminGatesConfigMapName(),
		}}
	}

	// Defining the CRD predicate.
	crdPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		group := o.(*apiextensionsv1.CustomResourceDefinition).Spec.Group
		return group == gatewayapiv1.GroupName || group == "gateway.networking.x-k8s.io"
	})

	// Setting up a watch for CRD events.
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &apiextensionsv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(toAdminGatesConfigMap), crdPredicate)); err != nil {
		return nil, err
	}

	// A predicate filter to watch for specific changes in the ConfigMap.
	// Verify that the ConfigMap's name and namespace match the expected values.
	adminGatePredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == operatorcontroller.AdminGatesConfigMapName().Namespace &&
			o.GetName() == operatorcontroller.AdminGatesConfigMapName().Name
	})

	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.ConfigMap{}, &handler.EnqueueRequestForObject{}, adminGatePredicate)); err != nil {
		return nil, err
	}

	return c, nil
}

// reconciler struct holds the client and cache attributes.
type reconciler struct {
	client client.Client
	cache  cache.Cache
}

// Reconcile function implements the logic to check conditions and manage the admin gate.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	adminGateConditionExists, err := r.adminGateConditionExists(ctx)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to determine if admin gate condition exists: %w", err)
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

// adminGateConditionExists checks if the admin gate condition exists based on both ConfigMap and CRDs.
func (r *reconciler) adminGateConditionExists(ctx context.Context) (bool, error) {
	crds := &apiextensionsv1.CustomResourceDefinitionList{}
	if err := r.cache.List(ctx, crds); err != nil {
		return false, fmt.Errorf("failed to list CRDs: %w", err)
	}

	for _, crd := range crds.Items {
		if crd.Spec.Group == gatewayapiv1.GroupName || crd.Spec.Group == "gateway.networking.x-k8s.io" {
			return true, nil
		}
	}

	return false, nil
}

// The addAdminGate function is responsible for adding the admin gate to the ConfigMap.
func (r *reconciler) addAdminGate(ctx context.Context) error {
	adminGatesConfigMap := &corev1.ConfigMap{}
	if err := r.cache.Get(ctx, operatorcontroller.AdminGatesConfigMapName(), adminGatesConfigMap); err != nil {
		return fmt.Errorf("failed to get configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}

	if adminGatesConfigMap.Data == nil {
		adminGatesConfigMap.Data = map[string]string{}
	}

	// The function checks if the admin key exists and if it is set to the expected message.
	if val, ok := adminGatesConfigMap.Data[gatewayAPIAdminKey]; ok && val == gatewayAPIAdminMsg {
		return nil
	}
	adminGatesConfigMap.Data[gatewayAPIAdminKey] = gatewayAPIAdminMsg

	log.Info("Adding admin gate for Gateway API management")
	if err := r.client.Update(ctx, adminGatesConfigMap); err != nil {
		return fmt.Errorf("failed to update configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}
	return nil
}

// The removeAdminGate function is responsible for removing the admin gate from the ConfigMap.
func (r *reconciler) removeAdminGate(ctx context.Context) error {
	adminGatesConfigMap := &corev1.ConfigMap{}
	if err := r.cache.Get(ctx, operatorcontroller.AdminGatesConfigMapName(), adminGatesConfigMap); err != nil {
		return fmt.Errorf("failed to get configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}

	if _, ok := adminGatesConfigMap.Data[gatewayAPIAdminKey]; !ok {
		return nil
	}

	log.Info("Removing admin gate for Gateway API management")
	delete(adminGatesConfigMap.Data, gatewayAPIAdminKey)
	if err := r.client.Update(ctx, adminGatesConfigMap); err != nil {
		return fmt.Errorf("failed to update configmap %s: %w", operatorcontroller.AdminGatesConfigMapName(), err)
	}
	return nil
}
