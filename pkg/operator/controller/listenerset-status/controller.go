// The listenerset-status controller watches for ListenerSet resources
// targeting OpenShift-managed Gateways and sets Accepted=False on them.
// Current Istio (1.30.1) does not correctly implement hostname conflict resolution for
// ListenerSets, so CIO disables ListenerSet reconciliation via
// PILOT_IGNORE_RESOURCES. This controller ensures users are informed
// that their ListenerSets will not be reconciled. It also exposes a
// per-ListenerSet Prometheus gauge to enable alerting when ListenerSets
// exist on managed Gateways.
package listenerset_status

import (
	"context"
	"fmt"

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
	controllerName = "listenerset_status_controller"

	// ReasonUnsupportedByController is the reason set on the Accepted
	// condition when a ListenerSet targets an OpenShift-managed Gateway.
	ReasonUnsupportedByController = "UnsupportedByController"

	// ListenerSetParentGatewayIndex is the field index key for looking up
	// ListenerSets by their parent Gateway name (namespace/name).
	ListenerSetParentGatewayIndex = "spec.parentRef.gateway"
)

var (
	log = logf.Logger.WithName(controllerName)

	listenerSetOnManagedGatewayMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ingress_operator_listenerset_on_managed_gateway",
		Help: "Set to 1 when a ListenerSet targets an OpenShift-managed Gateway. ListenerSets are not yet supported and may cause unexpected traffic behavior on upgrade.",
	}, []string{"listenerset_namespace", "listenerset_name"})
)

func RegisterMetrics() error {
	if err := ctrlruntimemetrics.Registry.Register(listenerSetOnManagedGatewayMetric); err != nil {
		return fmt.Errorf("failed to register ListenerSet metric: %w", err)
	}
	return nil
}

func NewUnmanaged(mgr manager.Manager, modeAccessor *operatorcontroller.ModeAccessor) (controller.Controller, error) {
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

	// Watch ListenerSets directly - each ListenerSet reconciles itself.
	// Only reconcile on create, delete, or parentRef changes.
	listenerSetPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldLS, okOld := e.ObjectOld.(*gatewayapiv1.ListenerSet)
			newLS, okNew := e.ObjectNew.(*gatewayapiv1.ListenerSet)
			if !okOld || !okNew {
				return false
			}
			return string(oldLS.Spec.ParentRef.Name) != string(newLS.Spec.ParentRef.Name) ||
				parentRefNamespace(oldLS) != parentRefNamespace(newLS)
		},
		DeleteFunc: func(e event.DeleteEvent) bool { return true },
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.ListenerSet{}, &handler.EnqueueRequestForObject{}, listenerSetPredicate, modeAccessor.DependentPredicate())); err != nil {
		return nil, fmt.Errorf("failed to watch ListenerSets: %w", err)
	}

	// Watch Gateways - when a Gateway's class changes or it is deleted,
	// enqueue the ListenerSets that target it via the index.
	gatewayHasOurController := operatorcontroller.GatewayHasOurController(log, operatorCache, false)
	gatewayToListenerSets := func(ctx context.Context, o client.Object) []reconcile.Request {
		key := o.GetNamespace() + "/" + o.GetName()
		var listenerSets gatewayapiv1.ListenerSetList
		if err := operatorCache.List(ctx, &listenerSets, client.MatchingFields{ListenerSetParentGatewayIndex: key}); err != nil {
			log.Error(err, "failed to list ListenerSets for Gateway", "gateway", key)
			return nil
		}
		var requests []reconcile.Request
		for _, ls := range listenerSets.Items {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: ls.Namespace, Name: ls.Name},
			})
		}
		return requests
	}
	gatewayPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return gatewayHasOurController(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldGW, okOld := e.ObjectOld.(*gatewayapiv1.Gateway)
			newGW, okNew := e.ObjectNew.(*gatewayapiv1.Gateway)
			if !okOld || !okNew {
				return false
			}
			if oldGW.Spec.GatewayClassName == newGW.Spec.GatewayClassName {
				return false
			}
			return gatewayHasOurController(e.ObjectOld) || gatewayHasOurController(e.ObjectNew)
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return gatewayHasOurController(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(gatewayToListenerSets), gatewayPredicate, modeAccessor.DependentPredicate())); err != nil {
		return nil, fmt.Errorf("failed to watch Gateways: %w", err)
	}

	// Watch GatewayClasses - when our GatewayClass is created or deleted,
	// enqueue all ListenerSets so they can re-evaluate.
	isOurGatewayClass := func(o client.Object) bool {
		gc, ok := o.(*gatewayapiv1.GatewayClass)
		if !ok {
			return false
		}
		return gc.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName
	}
	reconcileAllListenerSets := func(ctx context.Context, _ client.Object) []reconcile.Request {
		var listenerSets gatewayapiv1.ListenerSetList
		if err := operatorCache.List(ctx, &listenerSets); err != nil {
			log.Error(err, "failed to list ListenerSets for GatewayClass change")
			return nil
		}
		var requests []reconcile.Request
		for _, ls := range listenerSets.Items {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: ls.Namespace, Name: ls.Name},
			})
		}
		return requests
	}
	gatewayClassPredicate := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isOurGatewayClass(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return false },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isOurGatewayClass(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.GatewayClass{}, handler.EnqueueRequestsFromMapFunc(reconcileAllListenerSets), gatewayClassPredicate, modeAccessor.DependentPredicate())); err != nil {
		return nil, fmt.Errorf("failed to watch GatewayClasses: %w", err)
	}

	// Watch the Ingress CR for mode changes. Any change to the Ingress
	// CR triggers re-evaluation of all ListenerSets so that dependent
	// controllers can start or stop processing when the management mode
	// changes. Only register when the management mode gate is enabled
	// because the Ingress CRD only exists on TechPreview/DevPreview clusters.
	if modeAccessor != nil && modeAccessor.GateEnabled() {
		ingressToListenerSets := operatorcontroller.IngressWakeUpMapper(operatorCache, func() client.ObjectList { return &gatewayapiv1.ListenerSetList{} })
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1alpha1.Ingress{}, handler.EnqueueRequestsFromMapFunc(ingressToListenerSets))); err != nil {
			return nil, fmt.Errorf("failed to watch Ingress: %w", err)
		}
	}

	return c, nil
}

type reconciler struct {
	client       client.Client
	cache        cache.Cache
	modeAccessor *operatorcontroller.ModeAccessor
}

// Reconcile handles a single ListenerSet. It checks whether the
// ListenerSet targets an OpenShift-managed Gateway and sets
// Accepted=False if so.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "listenerset", request.NamespacedName)
	if r.modeAccessor == nil || !r.modeAccessor.AllowDependents() {
		log.Info("Management mode does not allow dependent controllers, skipping reconciliation")
		return reconcile.Result{}, nil
	}

	ls := &gatewayapiv1.ListenerSet{}
	if err := r.cache.Get(ctx, request.NamespacedName, ls); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// ListenerSet was deleted - clean up metric.
			listenerSetOnManagedGatewayMetric.DeleteLabelValues(request.Namespace, request.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get ListenerSet %s: %w", request.NamespacedName, err)
	}

	parentNS := ls.Namespace
	if ls.Spec.ParentRef.Namespace != nil {
		parentNS = string(*ls.Spec.ParentRef.Namespace)
	}
	parentName := types.NamespacedName{Namespace: parentNS, Name: string(ls.Spec.ParentRef.Name)}

	gw := &gatewayapiv1.Gateway{}
	if err := r.cache.Get(ctx, parentName, gw); err != nil {
		if client.IgnoreNotFound(err) == nil {
			listenerSetOnManagedGatewayMetric.DeleteLabelValues(request.Namespace, request.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get Gateway %s: %w", parentName, err)
	}

	gcName := types.NamespacedName{Name: string(gw.Spec.GatewayClassName)}
	gc := &gatewayapiv1.GatewayClass{}
	if err := r.cache.Get(ctx, gcName, gc); err != nil {
		if client.IgnoreNotFound(err) == nil {
			listenerSetOnManagedGatewayMetric.DeleteLabelValues(request.Namespace, request.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get GatewayClass %s: %w", gcName, err)
	}

	if gc.Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName {
		listenerSetOnManagedGatewayMetric.DeleteLabelValues(request.Namespace, request.Name)
		return reconcile.Result{}, nil
	}

	// ListenerSet targets an OpenShift-managed Gateway.
	listenerSetOnManagedGatewayMetric.WithLabelValues(request.Namespace, request.Name).Set(1)

	if err := r.setListenerSetNotAccepted(ctx, ls); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *reconciler) setListenerSetNotAccepted(ctx context.Context, ls *gatewayapiv1.ListenerSet) error {
	updated := ls.DeepCopy()
	changed := meta.SetStatusCondition(&updated.Status.Conditions, metav1.Condition{
		Type:               string(gatewayapiv1.ListenerSetConditionAccepted),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: ls.Generation,
		Reason:             ReasonUnsupportedByController,
		Message:            "ListenerSets are not yet supported by the OpenShift Gateway API implementation. This ListenerSet will not be reconciled. On a future upgrade, this ListenerSet may become active and could cause unexpected traffic routing.",
	})
	if !changed {
		return nil
	}
	if err := r.client.Status().Patch(ctx, updated, client.MergeFrom(ls)); err != nil {
		return fmt.Errorf("failed to patch ListenerSet %s/%s status: %w", ls.Namespace, ls.Name, err)
	}
	log.Info("set ListenerSet Accepted=False", "listenerset", ls.Name, "namespace", ls.Namespace)
	return nil
}

func parentRefNamespace(ls *gatewayapiv1.ListenerSet) string {
	if ls.Spec.ParentRef.Namespace != nil {
		return string(*ls.Spec.ParentRef.Namespace)
	}
	return ls.Namespace
}
