package gatewaynetworkpolicy

import (
	"context"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
	controllerName = "gateway_networkpolicy_controller"
)

var log = logf.Logger.WithName(controllerName)

func NewUnmanaged(mgr manager.Manager, modeAccessor *operatorcontroller.GatewayAPIModeAccessor) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		client:       mgr.GetClient(),
		cache:        operatorCache,
		fieldIndexer: mgr.GetFieldIndexer(),
		modeAccessor: modeAccessor,
	}
	c, err := controller.NewUnmanaged(controllerName, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	isOperandNamespace := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == operatorcontroller.DefaultOperandNamespace
	})

	// watch gateways in ingress operand namespace
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.Gateway{}, &handler.EnqueueRequestForObject{}, isOperandNamespace, modeAccessor.DependentPredicate())); err != nil {
		return nil, err
	}
	// watch network policies in ingress operand namespace
	if err := c.Watch(source.Kind[client.Object](operatorCache, &networkingv1.NetworkPolicy{}, enqueueRequestForOwningGateway(), isOperandNamespace, modeAccessor.DependentPredicate())); err != nil {
		return nil, err
	}

	// Watch the Ingress CR for mode changes. Any change to the Ingress
	// CR triggers re-evaluation of all gateways in the operand namespace
	// so that dependent controllers can start or stop processing when
	// the management mode changes. Only register when the management mode
	// gate is enabled because the Ingress CRD only exists on
	// TechPreview/DevPreview clusters.
	if modeAccessor != nil && modeAccessor.GateEnabled() {
		ingressToGateways := operatorcontroller.IngressWakeUpMapper(operatorCache, func() client.ObjectList { return &gatewayapiv1.GatewayList{} }, client.InNamespace(operatorcontroller.DefaultOperandNamespace))
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1alpha1.Ingress{}, handler.EnqueueRequestsFromMapFunc(ingressToGateways))); err != nil {
			return nil, err
		}
	}

	return c, nil
}

func enqueueRequestForOwningGateway() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, a client.Object) []reconcile.Request {
			labels := a.GetLabels()
			if gatewayName, ok := labels[manifests.OwningGatewayLabel]; ok {
				log.Info("queueing gateway", "gateway", "", "related object", a.GetNamespace()+"/"+a.GetName())
				return []reconcile.Request{{NamespacedName: types.NamespacedName{
					Name:      gatewayName,
					Namespace: operatorcontroller.DefaultOperandNamespace,
				}}}
			}
			return []reconcile.Request{}
		})
}

// reconciler reconciles gateways.
type reconciler struct {
	client       client.Client
	cache        cache.Cache
	recorder     record.EventRecorder
	fieldIndexer client.FieldIndexer
	modeAccessor *operatorcontroller.GatewayAPIModeAccessor
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("Reconciling gateway", "request", request)
	if r.modeAccessor == nil || !r.modeAccessor.AllowDependents() {
		log.Info("Management mode does not allow dependent controllers, skipping reconciliation")
		return reconcile.Result{}, nil
	}

	gateway := gatewayapiv1.Gateway{}
	if err := r.cache.Get(ctx, request.NamespacedName, &gateway); err != nil {
		// Nothing to do on gateway delete
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if _, _, err := r.ensureGatewayNetworkPolicy(ctx, &gateway); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}
