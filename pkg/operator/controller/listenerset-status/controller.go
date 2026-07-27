// The listenerset-status controller watches for ListenerSet resources
// targeting OpenShift-managed Gateways and sets Accepted=False on them.
// Current Istio (1.30.1) does not correctly implement hostname conflict resolution for
// ListenerSets, so CIO disables ListenerSet reconciliation via
// PILOT_IGNORE_RESOURCES. This controller ensures users are informed
// that their ListenerSets will not be reconciled. It also exposes a
// Prometheus gauge to enable alerting when ListenerSets exist on
// managed Gateways.
package listenerset_status

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

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
	controllerName = "listenerset_status_controller"

	// ReasonUnsupportedByController is the reason set on the Accepted
	// condition when a ListenerSet targets an OpenShift-managed Gateway.
	ReasonUnsupportedByController = "UnsupportedByController"
)

var (
	log = logf.Logger.WithName(controllerName)

	listenerSetOnManagedGatewayMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ingress_operator_listenerset_on_managed_gateway",
		Help: "Set to 1 when a ListenerSet targets an OpenShift-managed Gateway. ListenerSets are not yet supported and may cause unexpected traffic behavior on upgrade.",
	})
)

func RegisterMetrics() error {
	if err := prometheus.Register(listenerSetOnManagedGatewayMetric); err != nil {
		return fmt.Errorf("failed to register ListenerSet metric: %w", err)
	}
	return nil
}

func NewUnmanaged(mgr manager.Manager) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	r := &reconciler{
		client: mgr.GetClient(),
		cache:  operatorCache,
	}
	c, err := controller.NewUnmanaged(controllerName, controller.Options{Reconciler: r})
	if err != nil {
		return nil, err
	}

	// Use a single synthetic key so that any ListenerSet or GatewayClass
	// change triggers one global reconcile. The reconciler lists all
	// ListenerSets and managed GatewayClasses to compute the full
	// picture, so per-resource reconciliation would be redundant.
	toReconcile := func(_ context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{Name: "listenerset-check"},
		}}
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.ListenerSet{}, handler.EnqueueRequestsFromMapFunc(toReconcile))); err != nil {
		return nil, fmt.Errorf("failed to watch ListenerSets: %w", err)
	}

	gatewayHasOurController := operatorcontroller.GatewayHasOurController(log, operatorCache, false)
	gatewayPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return gatewayHasOurController(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return gatewayHasOurController(e.ObjectOld) || gatewayHasOurController(e.ObjectNew)
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return gatewayHasOurController(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(toReconcile), gatewayPredicate)); err != nil {
		return nil, fmt.Errorf("failed to watch Gateways: %w", err)
	}

	isOurGatewayClass := func(o client.Object) bool {
		gc, ok := o.(*gatewayapiv1.GatewayClass)
		if !ok {
			return false
		}
		return gc.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName
	}
	gatewayClassPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return isOurGatewayClass(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isOurGatewayClass(e.ObjectOld) || isOurGatewayClass(e.ObjectNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool { return isOurGatewayClass(e.Object) },
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.GatewayClass{}, handler.EnqueueRequestsFromMapFunc(toReconcile), gatewayClassPredicate)); err != nil {
		return nil, fmt.Errorf("failed to watch GatewayClasses: %w", err)
	}

	return c, nil
}

type reconciler struct {
	client client.Client
	cache  cache.Cache
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	gatewayClassList := gatewayapiv1.GatewayClassList{}
	if err := r.cache.List(ctx, &gatewayClassList, client.MatchingFields{
		operatorcontroller.GatewayClassIndexFieldName: operatorcontroller.OpenShiftGatewayClassControllerName,
	}); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list gateway classes: %w", err)
	}
	if len(gatewayClassList.Items) == 0 {
		listenerSetOnManagedGatewayMetric.Set(0)
		return reconcile.Result{}, nil
	}
	ourClassNames := make(map[string]bool, len(gatewayClassList.Items))
	for _, gc := range gatewayClassList.Items {
		ourClassNames[gc.Name] = true
	}

	gatewayList := gatewayapiv1.GatewayList{}
	if err := r.cache.List(ctx, &gatewayList); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list gateways: %w", err)
	}
	type gatewayKey struct{ name, namespace string }
	ourGateways := make(map[gatewayKey]bool)
	for _, gw := range gatewayList.Items {
		if ourClassNames[string(gw.Spec.GatewayClassName)] {
			ourGateways[gatewayKey{gw.Name, gw.Namespace}] = true
		}
	}

	listenerSetList := gatewayapiv1.ListenerSetList{}
	if err := r.cache.List(ctx, &listenerSetList); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list listenersets: %w", err)
	}

	// Set Accepted=False on all ListenerSets targeting our Gateways regardless
	// of whether the Gateway's AllowedListeners would permit attachment.
	// Since PILOT_IGNORE_RESOURCES prevents Istio from setting any status,
	// this is the only signal the user gets that ListenerSets are unsupported.
	found := false
	var errs []error
	for i := range listenerSetList.Items {
		ls := &listenerSetList.Items[i]
		parentNS := ls.Namespace
		if ls.Spec.ParentRef.Namespace != nil {
			parentNS = string(*ls.Spec.ParentRef.Namespace)
		}
		if !ourGateways[gatewayKey{string(ls.Spec.ParentRef.Name), parentNS}] {
			continue
		}
		found = true
		if err := r.setListenerSetNotAccepted(ctx, ls); err != nil {
			errs = append(errs, err)
		}
	}

	if found {
		listenerSetOnManagedGatewayMetric.Set(1)
	} else {
		listenerSetOnManagedGatewayMetric.Set(0)
	}

	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}

func (r *reconciler) setListenerSetNotAccepted(ctx context.Context, ls *gatewayapiv1.ListenerSet) error {
	updated := ls.DeepCopy()
	changed := meta.SetStatusCondition(&updated.Status.Conditions, metav1.Condition{
		Type:               string(gatewayapiv1.ListenerSetConditionAccepted),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: ls.Generation,
		Reason:             ReasonUnsupportedByController,
		Message:            "ListenerSets are not yet supported by the OpenShift Gateway API implementation. This ListenerSet will not be reconciled.",
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
