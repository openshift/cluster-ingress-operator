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

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	controllerName = "listenerset_status_controller"
)

var (
	log = logf.Logger.WithName(controllerName)

	listenerSetOnManagedGatewayMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ingress_operator_listenerset_on_managed_gateway",
		Help: "Set to 1 when a ListenerSet targets an OpenShift-managed Gateway. ListenerSets are not yet supported and may cause unexpected traffic behavior on upgrade.",
	})
)

func RegisterMetrics() error {
	return prometheus.Register(listenerSetOnManagedGatewayMetric)
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

	toReconcile := func(_ context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{Name: "listenerset-check"},
		}}
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.ListenerSet{}, handler.EnqueueRequestsFromMapFunc(toReconcile))); err != nil {
		return nil, err
	}

	gatewayClassPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		gc, ok := o.(*gatewayapiv1.GatewayClass)
		if !ok {
			return false
		}
		return gc.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.GatewayClass{}, handler.EnqueueRequestsFromMapFunc(toReconcile), gatewayClassPredicate)); err != nil {
		return nil, err
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
	if err := r.client.List(ctx, &gatewayList); err != nil {
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
	if err := r.client.List(ctx, &listenerSetList); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list listenersets: %w", err)
	}

	// Set Accepted=False on all ListenerSets targeting our Gateways regardless
	// of whether the Gateway's AllowedListeners would permit attachment.
	// Since PILOT_IGNORE_RESOURCES prevents Istio from setting any status,
	// this is the only signal the user gets that ListenerSets are unsupported.
	found := false
	for i := range listenerSetList.Items {
		ls := &listenerSetList.Items[i]
		parentNS := ls.Namespace
		if ls.Spec.ParentRef.Namespace != nil {
			parentNS = string(*ls.Spec.ParentRef.Namespace)
		}
		if !ourGateways[gatewayKey{string(ls.Spec.ParentRef.Name), parentNS}] {
			if err := r.clearListenerSetNotAccepted(ctx, ls); err != nil {
				log.Error(err, "failed to clear ListenerSet status", "listenerset", ls.Name, "namespace", ls.Namespace)
			}
			continue
		}
		found = true
		if err := r.setListenerSetNotAccepted(ctx, ls); err != nil {
			log.Error(err, "failed to set ListenerSet status", "listenerset", ls.Name, "namespace", ls.Namespace)
		}
	}

	if found {
		listenerSetOnManagedGatewayMetric.Set(1)
	} else {
		listenerSetOnManagedGatewayMetric.Set(0)
	}

	return reconcile.Result{}, nil
}

func (r *reconciler) setListenerSetNotAccepted(ctx context.Context, ls *gatewayapiv1.ListenerSet) error {
	updated := ls.DeepCopy()
	changed := meta.SetStatusCondition(&updated.Status.Conditions, metav1.Condition{
		Type:               string(gatewayapiv1.ListenerSetConditionAccepted),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: ls.Generation,
		Reason:             "UnsupportedByController",
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

func (r *reconciler) clearListenerSetNotAccepted(ctx context.Context, ls *gatewayapiv1.ListenerSet) error {
	found := meta.FindStatusCondition(ls.Status.Conditions, string(gatewayapiv1.ListenerSetConditionAccepted))
	if found == nil || found.Reason != "UnsupportedByController" {
		return nil
	}
	updated := ls.DeepCopy()
	meta.RemoveStatusCondition(&updated.Status.Conditions, string(gatewayapiv1.ListenerSetConditionAccepted))
	if err := r.client.Status().Patch(ctx, updated, client.MergeFrom(ls)); err != nil {
		return fmt.Errorf("failed to clear ListenerSet %s/%s status: %w", ls.Namespace, ls.Name, err)
	}
	log.Info("cleared ListenerSet Accepted condition", "listenerset", ls.Name, "namespace", ls.Namespace)
	return nil
}
