// The udproute-status controller watches for UDPRoute resources targeting
// OpenShift-managed Gateways and sets Accepted=False for each such parent.
// Istio is configured to ignore UDPRoute resources because UDP routing is not
// supported by the OpenShift Gateway API implementation. This controller makes
// that limitation visible to users and exposes a metric for alerting.
package udproute_status

import (
	"context"
	"fmt"
	"reflect"

	"github.com/prometheus/client_golang/prometheus"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlruntimemetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	controllerName = "udproute_status_controller"

	// ReasonUnsupportedByController is set on the Accepted condition when a
	// UDPRoute targets an OpenShift-managed Gateway.
	ReasonUnsupportedByController = "UnsupportedByController"

	// UDPRouteParentGatewayIndex indexes UDPRoutes by parent Gateway
	// namespace/name.
	UDPRouteParentGatewayIndex = "spec.parentRefs.gateway"

	unsupportedMessage = "UDPRoutes are not supported by the OpenShift Gateway API implementation. This UDPRoute will not be reconciled. On a future upgrade, this UDPRoute may become active and could cause unexpected traffic routing."
)

var (
	log = logf.Logger.WithName(controllerName)

	udpRouteOnManagedGatewayMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ingress_operator_udproute_on_managed_gateway",
		Help: "Set to 1 when a UDPRoute targets an OpenShift-managed Gateway. UDPRoutes are not supported and may cause unexpected traffic behavior on upgrade.",
	}, []string{"udproute_namespace", "udproute_name"})
)

func RegisterMetrics() error {
	if err := ctrlruntimemetrics.Registry.Register(udpRouteOnManagedGatewayMetric); err != nil {
		return fmt.Errorf("failed to register UDPRoute metric: %w", err)
	}
	return nil
}

func NewUnmanaged(mgr manager.Manager, modeAccessor *operatorcontroller.GatewayAPIModeAccessor) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	r := &reconciler{
		client:       mgr.GetClient(),
		cache:        operatorCache,
		modeAccessor: modeAccessor,
	}
	c, err := controller.NewUnmanaged(controllerName, controller.Options{Reconciler: r})
	if err != nil {
		return nil, err
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.UDPRoute{}, &handler.EnqueueRequestForObject{}, udpRouteEventPredicate(), modeAccessor.DependentPredicate())); err != nil {
		return nil, fmt.Errorf("failed to watch UDPRoutes: %w", err)
	}

	gatewayHasOurController := operatorcontroller.GatewayHasOurController(log, operatorCache, false)
	gatewayPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return gatewayHasOurController(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldGateway, okOld := e.ObjectOld.(*gatewayapiv1.Gateway)
			newGateway, okNew := e.ObjectNew.(*gatewayapiv1.Gateway)
			if !okOld || !okNew || oldGateway.Spec.GatewayClassName == newGateway.Spec.GatewayClassName {
				return false
			}
			return gatewayHasOurController(e.ObjectOld) || gatewayHasOurController(e.ObjectNew)
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return gatewayHasOurController(e.Object) },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(gatewayToUDPRoutes(operatorCache)), gatewayPredicate, modeAccessor.DependentPredicate())); err != nil {
		return nil, fmt.Errorf("failed to watch Gateways: %w", err)
	}

	isOurGatewayClass := func(o client.Object) bool {
		gatewayClass, ok := o.(*gatewayapiv1.GatewayClass)
		return ok && gatewayClass.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName
	}
	gatewayClassPredicate := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isOurGatewayClass(e.Object) },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isOurGatewayClass(e.Object) },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.GatewayClass{}, handler.EnqueueRequestsFromMapFunc(allUDPRoutes(operatorCache, "GatewayClass change")), gatewayClassPredicate, modeAccessor.DependentPredicate())); err != nil {
		return nil, fmt.Errorf("failed to watch GatewayClasses: %w", err)
	}

	if modeAccessor != nil && modeAccessor.GateEnabled() {
		ingressToUDPRoutes := operatorcontroller.IngressWakeUpMapper(operatorCache, func() client.ObjectList { return &gatewayapiv1.UDPRouteList{} })
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1alpha1.Ingress{}, handler.EnqueueRequestsFromMapFunc(ingressToUDPRoutes), operatorcontroller.GatewayAPIModeChangePredicate())); err != nil {
			return nil, fmt.Errorf("failed to watch Ingress: %w", err)
		}
	}

	return c, nil
}

func udpRouteEventPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRoute, okOld := e.ObjectOld.(*gatewayapiv1.UDPRoute)
			newRoute, okNew := e.ObjectNew.(*gatewayapiv1.UDPRoute)
			return okOld && okNew && oldRoute.Generation != newRoute.Generation
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func gatewayToUDPRoutes(operatorCache cache.Cache) handler.MapFunc {
	return func(ctx context.Context, o client.Object) []reconcile.Request {
		key := o.GetNamespace() + "/" + o.GetName()
		var routes gatewayapiv1.UDPRouteList
		if err := operatorCache.List(ctx, &routes, client.MatchingFields{UDPRouteParentGatewayIndex: key}); err != nil {
			log.Error(err, "failed to list UDPRoutes for Gateway", "gateway", key)
			return nil
		}
		requests := make([]reconcile.Request, 0, len(routes.Items))
		for _, route := range routes.Items {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: route.Namespace, Name: route.Name}})
		}
		return requests
	}
}

func allUDPRoutes(operatorCache cache.Cache, cause string) handler.MapFunc {
	return func(ctx context.Context, _ client.Object) []reconcile.Request {
		var routes gatewayapiv1.UDPRouteList
		if err := operatorCache.List(ctx, &routes); err != nil {
			log.Error(err, "failed to list UDPRoutes", "cause", cause)
			return nil
		}
		requests := make([]reconcile.Request, 0, len(routes.Items))
		for _, route := range routes.Items {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: route.Namespace, Name: route.Name}})
		}
		return requests
	}
}

type reconciler struct {
	client       client.Client
	cache        cache.Cache
	modeAccessor *operatorcontroller.GatewayAPIModeAccessor
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "udproute", request.NamespacedName)
	route := &gatewayapiv1.UDPRoute{}
	if err := r.cache.Get(ctx, request.NamespacedName, route); err != nil {
		if client.IgnoreNotFound(err) == nil {
			udpRouteOnManagedGatewayMetric.DeleteLabelValues(request.Namespace, request.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get UDPRoute %s: %w", request.NamespacedName, err)
	}
	if r.modeAccessor == nil || !r.modeAccessor.AllowDependents() {
		udpRouteOnManagedGatewayMetric.DeleteLabelValues(request.Namespace, request.Name)
		log.Info("Management mode does not allow dependent controllers, cleaned metric and skipped status reconciliation")
		return reconcile.Result{}, nil
	}

	managedParents, err := r.managedGatewayParents(ctx, route)
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(managedParents) == 0 {
		udpRouteOnManagedGatewayMetric.DeleteLabelValues(request.Namespace, request.Name)
	} else {
		udpRouteOnManagedGatewayMetric.WithLabelValues(request.Namespace, request.Name).Set(1)
	}

	if err := r.updateStatus(ctx, route, managedParents); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *reconciler) managedGatewayParents(ctx context.Context, route *gatewayapiv1.UDPRoute) ([]gatewayapiv1.ParentReference, error) {
	managed := make([]gatewayapiv1.ParentReference, 0, len(route.Spec.ParentRefs))
	for _, parentRef := range route.Spec.ParentRefs {
		if !isGatewayParentRef(parentRef) {
			continue
		}
		parentName := parentGatewayName(route.Namespace, parentRef)
		gateway := &gatewayapiv1.Gateway{}
		if err := r.cache.Get(ctx, parentName, gateway); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue
			}
			return nil, fmt.Errorf("failed to get Gateway %s for UDPRoute %s/%s: %w", parentName, route.Namespace, route.Name, err)
		}
		gatewayClassName := types.NamespacedName{Name: string(gateway.Spec.GatewayClassName)}
		gatewayClass := &gatewayapiv1.GatewayClass{}
		if err := r.cache.Get(ctx, gatewayClassName, gatewayClass); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue
			}
			return nil, fmt.Errorf("failed to get GatewayClass %s for UDPRoute %s/%s: %w", gatewayClassName, route.Namespace, route.Name, err)
		}
		if gatewayClass.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName {
			managed = append(managed, parentRef)
		}
	}
	return managed, nil
}

func (r *reconciler) updateStatus(ctx context.Context, route *gatewayapiv1.UDPRoute, managedParents []gatewayapiv1.ParentReference) error {
	updated := route.DeepCopy()
	desiredParents := make([]gatewayapiv1.RouteParentStatus, 0, len(route.Status.Parents)+len(managedParents))
	hadOwnedParent := false
	for _, parentStatus := range route.Status.Parents {
		if parentStatus.ControllerName != gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName) {
			desiredParents = append(desiredParents, parentStatus)
		} else {
			hadOwnedParent = true
		}
	}
	if !hadOwnedParent && len(managedParents) == 0 {
		return nil
	}
	for _, parentRef := range managedParents {
		parentStatus := gatewayapiv1.RouteParentStatus{
			ParentRef:      parentRef,
			ControllerName: gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName),
		}
		for _, existing := range route.Status.Parents {
			if existing.ControllerName == parentStatus.ControllerName && reflect.DeepEqual(existing.ParentRef, parentRef) {
				parentStatus.Conditions = append([]metav1.Condition(nil), existing.Conditions...)
				break
			}
		}
		meta.SetStatusCondition(&parentStatus.Conditions, metav1.Condition{
			Type:               string(gatewayapiv1.RouteConditionAccepted),
			Status:             metav1.ConditionFalse,
			ObservedGeneration: route.Generation,
			Reason:             ReasonUnsupportedByController,
			Message:            unsupportedMessage,
		})
		desiredParents = append(desiredParents, parentStatus)
	}
	if reflect.DeepEqual(route.Status.Parents, desiredParents) {
		return nil
	}
	updated.Status.Parents = desiredParents
	if err := r.client.Status().Patch(ctx, updated, client.MergeFromWithOptions(route, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("failed to patch UDPRoute %s/%s status: %w", route.Namespace, route.Name, err)
	}
	log.Info("updated UDPRoute unsupported status", "udproute", route.Name, "namespace", route.Namespace)
	return nil
}

// ParentGatewayIndexKeys returns the unique namespace/name keys for Gateway
// parents referenced by a UDPRoute.
func ParentGatewayIndexKeys(route *gatewayapiv1.UDPRoute) []string {
	keys := make([]string, 0, len(route.Spec.ParentRefs))
	seen := map[string]struct{}{}
	for _, parentRef := range route.Spec.ParentRefs {
		if !isGatewayParentRef(parentRef) {
			continue
		}
		key := parentGatewayName(route.Namespace, parentRef).String()
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func isGatewayParentRef(parentRef gatewayapiv1.ParentReference) bool {
	if parentRef.Group != nil && string(*parentRef.Group) != gatewayapiv1.GroupName {
		return false
	}
	return parentRef.Kind == nil || string(*parentRef.Kind) == "Gateway"
}

func parentGatewayName(routeNamespace string, parentRef gatewayapiv1.ParentReference) types.NamespacedName {
	namespace := routeNamespace
	if parentRef.Namespace != nil {
		namespace = string(*parentRef.Namespace)
	}
	return types.NamespacedName{Namespace: namespace, Name: string(parentRef.Name)}
}
