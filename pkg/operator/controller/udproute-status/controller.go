package udproute_status

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/prometheus/client_golang/prometheus"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	informercache "k8s.io/client-go/tools/cache"
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
	controllerName                = "udproute_status_controller"
	ReasonUnsupportedByController = "UnsupportedByController"
	udpRouteParentGatewayIndex    = "spec.parentRefs.gateway"
	UDPRouteCRDName               = "udproutes.gateway.networking.k8s.io"
	unsupportedMessage            = "UDPRoutes are not supported by the OpenShift Gateway API implementation. This UDPRoute will not be reconciled. On a future upgrade, this UDPRoute may become active and could cause unexpected traffic routing."
)

var (
	newGatewayCache                = cache.New
	newGuardController             = controller.NewUnmanaged
	log                            = logf.Logger.WithName(controllerName)
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
	dynamicClient, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}
	// The manager cache is limited to the operator's namespaces, but a UDPRoute
	// parentRef may target a Gateway in any namespace. Use a separate
	// all-namespace cache for Gateway and GatewayClass lookups.
	gatewaysCache, err := newGatewayCache(mgr.GetConfig(), cache.Options{Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper()})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gateway cache: %w", err)
	}
	if err := mgr.Add(gatewaysCache); err != nil {
		return nil, fmt.Errorf("failed to add Gateway cache: %w", err)
	}
	r := &reconciler{
		client: mgr.GetClient(), cache: mgr.GetCache(), gatewayCache: gatewaysCache, modeAccessor: modeAccessor,
		events: make(chan event.GenericEvent, 256),
		newInformer: func(resource schema.GroupVersionResource) informercache.SharedIndexInformer {
			resourceClient := dynamicClient.Resource(resource).Namespace(metav1.NamespaceAll)
			return informercache.NewSharedIndexInformer(&informercache.ListWatch{
				ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
					return resourceClient.List(ctx, options)
				},
				WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
					return resourceClient.Watch(ctx, options)
				},
			}, newUDPRoute(resource.Version), 0, informercache.Indexers{})
		},
	}
	guard, err := newGuardController(controllerName, controller.Options{Reconciler: r, MaxConcurrentReconciles: 1})
	if err != nil {
		return nil, err
	}
	if err := guard.Watch(source.Channel(r.events, &handler.EnqueueRequestForObject{})); err != nil {
		return nil, err
	}
	if err := guard.Watch(source.Kind[client.Object](r.cache, &apiextensionsv1.CustomResourceDefinition{}, &handler.EnqueueRequestForObject{}, predicate.NewPredicateFuncs(func(object client.Object) bool { return object.GetName() == UDPRouteCRDName }))); err != nil {
		return nil, err
	}
	// Gateway events enqueue parent-indexed routes without class I/O. Ownership is
	// checked with the reconciliation context, not an uncancellable cache callback.
	gatewayPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { _, ok := e.Object.(*gatewayapiv1.Gateway); return ok },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldGateway, oldOK := e.ObjectOld.(*gatewayapiv1.Gateway)
			newGateway, newOK := e.ObjectNew.(*gatewayapiv1.Gateway)
			return oldOK && newOK && oldGateway.Spec.GatewayClassName != newGateway.Spec.GatewayClassName
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { _, ok := e.Object.(*gatewayapiv1.Gateway); return ok },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	if err := guard.Watch(source.Kind[client.Object](r.gatewayCache, &gatewayapiv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(r.gatewayToUDPRoutes), gatewayPredicate)); err != nil {
		return nil, err
	}
	classPredicate := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isOurClass(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isOurClass(e.ObjectOld) || isOurClass(e.ObjectNew) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isOurClass(e.Object) },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	if err := guard.Watch(source.Kind[client.Object](r.gatewayCache, &gatewayapiv1.GatewayClass{}, handler.EnqueueRequestsFromMapFunc(r.allUDPRoutes), classPredicate)); err != nil {
		return nil, err
	}
	if modeAccessor != nil && modeAccessor.GateEnabled() {
		if err := guard.Watch(source.Kind[client.Object](r.cache, &operatorv1.Ingress{}, handler.EnqueueRequestsFromMapFunc(r.allUDPRoutes), operatorcontroller.GatewayAPIModeChangePredicate())); err != nil {
			return nil, err
		}
	}
	return guard, nil
}

func isOurClass(object client.Object) bool {
	class, ok := object.(*gatewayapiv1.GatewayClass)
	return ok && class.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName
}

type reconciler struct {
	client       client.Client
	cache        cache.Cache
	gatewayCache cache.Cache
	modeAccessor *operatorcontroller.GatewayAPIModeAccessor
	newInformer  func(schema.GroupVersionResource) informercache.SharedIndexInformer
	events       chan event.GenericEvent
	mu           sync.RWMutex
	informer     informercache.SharedIndexInformer
	cancel       context.CancelFunc
	done         chan struct{}
	crdUID       types.UID
	resource     schema.GroupVersionResource
}

func servedUDPResource(crd *apiextensionsv1.CustomResourceDefinition) schema.GroupVersionResource {
	if crd.DeletionTimestamp != nil || crd.Spec.Group != gatewayapiv1.GroupName || crd.Spec.Names.Kind != "UDPRoute" || crd.Spec.Names.Plural != "udproutes" || crd.Spec.Scope != apiextensionsv1.NamespaceScoped {
		return schema.GroupVersionResource{}
	}
	established := false
	for _, condition := range crd.Status.Conditions {
		if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
			established = true
		}
	}
	if !established {
		return schema.GroupVersionResource{}
	}
	for _, wanted := range []string{"v1", "v1alpha2"} {
		for _, version := range crd.Spec.Versions {
			if version.Name == wanted && version.Served {
				return schema.GroupVersionResource{Group: gatewayapiv1.GroupName, Version: wanted, Resource: "udproutes"}
			}
		}
	}
	return schema.GroupVersionResource{}
}

func (r *reconciler) ensureUDPInformer(ctx context.Context) error {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := r.cache.Get(ctx, types.NamespacedName{Name: UDPRouteCRDName}, crd)
	if client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to discover UDPRoute CRD: %w", err)
	}
	resource := servedUDPResource(crd)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.informer != nil && resource == r.resource && crd.UID == r.crdUID {
		select {
		case <-r.done:
		default:
			return nil
		}
	}
	if r.cancel != nil {
		r.cancel()
		<-r.done
		r.cancel = nil
		r.informer = nil
	}
	r.resource = schema.GroupVersionResource{}
	r.crdUID = ""
	udpRouteOnManagedGatewayMetric.Reset()
	if resource.Empty() {
		return nil
	}
	informer := r.newInformer(resource)
	if err := informer.AddIndexers(informercache.Indexers{udpRouteParentGatewayIndex: func(object interface{}) ([]string, error) {
		route, ok := object.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("unexpected UDPRoute index object %T", object)
		}
		return parentGatewayIndexKeys(route)
	}}); err != nil {
		return fmt.Errorf("failed to add UDPRoute field indexer: %w", err)
	}
	// NewUnmanaged leaves ReconciliationTimeout unset, so this context follows
	// controller lifetime. A future timeout requires explicit informer ownership;
	// the stopped-instance check only restarts after another discovery request.
	watchCtx, cancel := context.WithCancel(ctx)
	enqueue := func(object interface{}) {
		if tombstone, ok := object.(informercache.DeletedFinalStateUnknown); ok {
			object = tombstone.Obj
		}
		route, ok := object.(*unstructured.Unstructured)
		if !ok {
			return
		}
		select {
		case r.events <- event.GenericEvent{Object: route.DeepCopy()}:
		case <-watchCtx.Done():
		}
	}
	if _, err := informer.AddEventHandler(informercache.ResourceEventHandlerFuncs{
		AddFunc: enqueue,
		UpdateFunc: func(oldObject, newObject interface{}) {
			oldRoute, oldOK := oldObject.(*unstructured.Unstructured)
			newRoute, newOK := newObject.(*unstructured.Unstructured)
			if oldOK && newOK && udpRouteEventPredicate().Update(event.UpdateEvent{ObjectOld: oldRoute, ObjectNew: newRoute}) {
				enqueue(newObject)
			}
		},
		DeleteFunc: enqueue,
	}); err != nil {
		cancel()
		return fmt.Errorf("failed to watch UDPRoutes: %w", err)
	}
	r.informer, r.cancel, r.crdUID, r.resource = informer, cancel, crd.UID, resource
	r.done = make(chan struct{})
	done := r.done
	go func() { defer close(done); informer.Run(watchCtx.Done()) }()
	return nil
}

func (r *reconciler) gatewayToUDPRoutes(_ context.Context, object client.Object) []reconcile.Request {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.informer == nil {
		return nil
	}
	objects, err := r.informer.GetIndexer().ByIndex(udpRouteParentGatewayIndex, object.GetNamespace()+"/"+object.GetName())
	if err != nil {
		log.Error(err, "failed to list UDPRoutes for Gateway")
		return nil
	}
	return routeRequests(objects)
}

func (r *reconciler) allUDPRoutes(_ context.Context, _ client.Object) []reconcile.Request {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.informer == nil {
		return nil
	}
	return routeRequests(r.informer.GetIndexer().List())
}

func routeRequests(objects []interface{}) []reconcile.Request {
	requests := make([]reconcile.Request, 0, len(objects))
	for _, object := range objects {
		if route, ok := object.(*unstructured.Unstructured); ok {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(route)})
		}
	}
	return requests
}

func newUDPRoute(version string) *unstructured.Unstructured {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: gatewayapiv1.GroupName, Version: version, Kind: "UDPRoute"})
	return route
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if request.Namespace == "" && request.Name == UDPRouteCRDName {
		return reconcile.Result{}, r.ensureUDPInformer(ctx)
	}
	r.mu.RLock()
	version := r.resource.Version
	r.mu.RUnlock()
	if version == "" {
		udpRouteOnManagedGatewayMetric.DeleteLabelValues(request.Namespace, request.Name)
		return reconcile.Result{}, nil
	}
	if r.modeAccessor == nil || !r.modeAccessor.AllowDependents() {
		udpRouteOnManagedGatewayMetric.DeleteLabelValues(request.Namespace, request.Name)
		return reconcile.Result{}, nil
	}
	route := newUDPRoute(version)
	if err := r.client.Get(ctx, request.NamespacedName, route); err != nil {
		if client.IgnoreNotFound(err) == nil {
			udpRouteOnManagedGatewayMetric.DeleteLabelValues(request.Namespace, request.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get UDPRoute %s: %w", request.NamespacedName, err)
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
	return reconcile.Result{}, r.updateStatus(ctx, route, managedParents)
}

func parentReferences(route *unstructured.Unstructured) ([]gatewayapiv1.ParentReference, error) {
	values, _, err := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if err != nil {
		return nil, err
	}
	parents := make([]gatewayapiv1.ParentReference, 0, len(values))
	for _, value := range values {
		object, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid UDPRoute parentRef %T", value)
		}
		var parent gatewayapiv1.ParentReference
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object, &parent); err != nil {
			return nil, err
		}
		parents = append(parents, parent)
	}
	return parents, nil
}

func (r *reconciler) managedGatewayParents(ctx context.Context, route *unstructured.Unstructured) ([]gatewayapiv1.ParentReference, error) {
	parents, err := parentReferences(route)
	if err != nil {
		return nil, err
	}
	managed := make([]gatewayapiv1.ParentReference, 0, len(parents))
	for _, parent := range parents {
		if !isGatewayParentRef(parent) {
			continue
		}
		gateway := &gatewayapiv1.Gateway{}
		name := parentGatewayName(route.GetNamespace(), parent)
		if err := r.gatewayCache.Get(ctx, name, gateway); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue
			}
			return nil, fmt.Errorf("failed to get Gateway %s: %w", name, err)
		}
		class := &gatewayapiv1.GatewayClass{}
		if err := r.gatewayCache.Get(ctx, types.NamespacedName{Name: string(gateway.Spec.GatewayClassName)}, class); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue
			}
			return nil, fmt.Errorf("failed to get GatewayClass %s: %w", gateway.Spec.GatewayClassName, err)
		}
		if class.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName {
			managed = append(managed, parent)
		}
	}
	return managed, nil
}

func (r *reconciler) updateStatus(ctx context.Context, route *unstructured.Unstructured, managedParents []gatewayapiv1.ParentReference) error {
	existingParents, _, err := unstructured.NestedSlice(route.Object, "status", "parents")
	if err != nil {
		return err
	}
	desiredParents := make([]interface{}, 0, len(existingParents)+len(managedParents))
	owned := make(map[string]map[string]interface{})
	for _, value := range existingParents {
		parent, ok := value.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid UDPRoute parent status %T", value)
		}
		if parent["controllerName"] != operatorcontroller.OpenShiftGatewayClassControllerName {
			desiredParents = append(desiredParents, value)
		} else {
			ref, _, err := unstructured.NestedMap(parent, "parentRef")
			if err != nil {
				return err
			}
			identity, err := json.Marshal(ref)
			if err != nil {
				return err
			}
			owned[string(identity)] = parent
		}
	}
	if len(owned) == 0 && len(managedParents) == 0 {
		return nil
	}
	for _, ref := range managedParents {
		refObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&ref)
		if err != nil {
			return err
		}
		parent := map[string]interface{}{"parentRef": refObject, "controllerName": operatorcontroller.OpenShiftGatewayClassControllerName}
		identity, err := json.Marshal(refObject)
		if err != nil {
			return err
		}
		if existing, ok := owned[string(identity)]; ok {
			parent = runtime.DeepCopyJSON(existing)
		}
		conditionValues, _, err := unstructured.NestedSlice(parent, "conditions")
		if err != nil {
			return err
		}
		conditions := make([]interface{}, 0, len(conditionValues)+1)
		var accepted metav1.Condition
		for _, value := range conditionValues {
			condition, ok := value.(map[string]interface{})
			if !ok {
				return fmt.Errorf("invalid UDPRoute condition %T", value)
			}
			if condition["type"] != string(gatewayapiv1.RouteConditionAccepted) {
				conditions = append(conditions, value)
				continue
			}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(condition, &accepted); err != nil {
				return err
			}
		}
		ownedConditions := []metav1.Condition{}
		if accepted.Type != "" {
			ownedConditions = append(ownedConditions, accepted)
		}
		meta.SetStatusCondition(&ownedConditions, metav1.Condition{Type: string(gatewayapiv1.RouteConditionAccepted), Status: metav1.ConditionFalse, ObservedGeneration: route.GetGeneration(), Reason: ReasonUnsupportedByController, Message: unsupportedMessage, LastTransitionTime: metav1.NewTime(time.Now())})
		acceptedObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&ownedConditions[0])
		if err != nil {
			return err
		}
		conditions = append(conditions, acceptedObject)
		parent["conditions"] = conditions
		desiredParents = append(desiredParents, parent)
	}
	if reflect.DeepEqual(existingParents, desiredParents) {
		return nil
	}
	updated := route.DeepCopy()
	if err := unstructured.SetNestedSlice(updated.Object, desiredParents, "status", "parents"); err != nil {
		return err
	}
	if err := r.client.Status().Patch(ctx, updated, client.MergeFromWithOptions(route, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("failed to patch UDPRoute %s/%s status: %w", route.GetNamespace(), route.GetName(), err)
	}
	return nil
}

func udpRouteEventPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRoute, oldOK := e.ObjectOld.(*unstructured.Unstructured)
			newRoute, newOK := e.ObjectNew.(*unstructured.Unstructured)
			return oldOK && newOK && (oldRoute.GetGeneration() != newRoute.GetGeneration() || oldRoute.GetUID() != newRoute.GetUID())
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func parentGatewayIndexKeys(route *unstructured.Unstructured) ([]string, error) {
	parents, err := parentReferences(route)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(parents))
	seen := map[string]struct{}{}
	for _, parent := range parents {
		if !isGatewayParentRef(parent) {
			continue
		}
		key := parentGatewayName(route.GetNamespace(), parent).String()
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	return keys, nil
}

func isGatewayParentRef(parent gatewayapiv1.ParentReference) bool {
	return (parent.Group == nil || string(*parent.Group) == gatewayapiv1.GroupName) && (parent.Kind == nil || string(*parent.Kind) == "Gateway")
}

func parentGatewayName(namespace string, parent gatewayapiv1.ParentReference) types.NamespacedName {
	if parent.Namespace != nil {
		namespace = string(*parent.Namespace)
	}
	return types.NamespacedName{Namespace: namespace, Name: string(parent.Name)}
}
