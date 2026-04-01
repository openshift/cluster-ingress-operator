package gatewaynetworkpolicy

import (
	"context"

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

func NewUnmanaged(mgr manager.Manager) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		client:       mgr.GetClient(),
		cache:        operatorCache,
		fieldIndexer: mgr.GetFieldIndexer(),
	}
	c, err := controller.NewUnmanaged(controllerName, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	isOperandNamespace := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == operatorcontroller.DefaultOperandNamespace
	})

	// watch gateways in ingress operand namespace
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.Gateway{}, &handler.EnqueueRequestForObject{}, isOperandNamespace)); err != nil {
		return nil, err
	}
	// watch network policies in ingress operand namespace
	if err := c.Watch(source.Kind[client.Object](operatorCache, &networkingv1.NetworkPolicy{}, enqueueRequestForOwningGateway(), isOperandNamespace)); err != nil {
		return nil, err
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
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("Reconciling gateway", "request", request)

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
