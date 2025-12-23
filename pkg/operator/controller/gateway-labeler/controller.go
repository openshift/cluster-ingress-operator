package gateway_labeler

import (
	"context"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
	controllerName = "gateway_labeler_controller"
)

var log = logf.Logger.WithName(controllerName)

// NewUnmanaged creates and returns a controller that adds the "istio.io/rev"
// label to gateways so that Istio reconciles them.  This is an unmanaged
// controller, which means that the manager does not start it.
//
// The "istio.io/rev" label MUST be present on gateways in order for an
// Istio control-plane deployed via the OSSM Operator to consider that
// resource managed.
func NewUnmanaged(mgr manager.Manager) (controller.Controller, error) {
	// Create a new cache for gateways so it can watch all namespaces.
	// (Using the operator cache for gateways in all namespaces would cause
	// it to cache other resources in all namespaces.)
	gatewaysCache, err := cache.New(mgr.GetConfig(), cache.Options{
		Scheme: mgr.GetScheme(),
	})
	if err != nil {
		return nil, err
	}
	// Make sure the manager starts the cache with the other runnables.
	mgr.Add(gatewaysCache)

	reconciler := &reconciler{
		cache:    gatewaysCache,
		client:   mgr.GetClient(),
		recorder: mgr.GetEventRecorderFor(controllerName),
	}
	options := controller.Options{Reconciler: reconciler}
	options.DefaultFromConfig(mgr.GetControllerOptions())
	c, err := controller.NewUnmanaged(controllerName, options)
	if err != nil {
		return nil, err
	}

	// Watch gatewayclasses so that when one is created with our controller
	// name or is updated to change its controller name to our controller
	// name, then we update labels on gateways that reference that
	// gatewayclass.
	key, value := operatorcontroller.IstioRevLabelKey, operatorcontroller.IstioName("").Name
	gatewayClassToGateways := func(ctx context.Context, o client.Object) []reconcile.Request {
		requests := []reconcile.Request{}

		// Using an index might be more efficient than listing all
		// gateways and then filtering.  However, setting up an index is
		// complicated by the fact that the Gateway API CRDs might not
		// exist when this controller is initialized.

		var gateways gatewayapiv1.GatewayList
		if err := reconciler.cache.List(ctx, &gateways); err != nil {
			log.Error(err, "Failed to list gateways for gatewayclass", "gatewayclass", o.GetName())
			return requests
		}

		for i := range gateways.Items {
			if gateways.Items[i].Labels[key] == value {
				continue
			}
			if string(gateways.Items[i].Spec.GatewayClassName) != o.GetName() {
				continue
			}
			request := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: gateways.Items[i].Namespace,
					Name:      gateways.Items[i].Name,
				},
			}
			requests = append(requests, request)
		}

		return requests
	}
	gatewayClassPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			gatewayClass := e.Object.(*gatewayapiv1.GatewayClass)

			if gatewayClass.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName {
				return true
			}

			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Don't reconcile gateways when the gatewayclass is
			// deleted.  If Istio created resources for a gateway,
			// then we want Istio to continue managing those
			// resources.  The user can remove the label or delete
			// the gateway and related resources if desired.
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			old := e.ObjectOld.(*gatewayapiv1.GatewayClass)
			new := e.ObjectNew.(*gatewayapiv1.GatewayClass)

			switch {
			case old.Spec.ControllerName == new.Spec.ControllerName:
				// Creation and updates from a different
				// controller Name are the only triggers needed
				// to ensure all managed gateways have the
				// proper labels, so triggering on any update
				// where the controller name has not changed
				// would be redundant.
				return false
			case new.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName:
				// Reconcile gateways that point to a
				// gatewayclass that is updated to have our
				// controller name.
				return true
			case old.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName:
				// Don't reconcile gateways if the controller
				// name is changed *from* our controller name.
				// If Istio created resources for the gateway,
				// then we want Istio to continue managing those
				// resources.  The user can remove the label or
				// delete the gateway and related resources if
				// desired.  Note that we do not clean up any of
				// the labels we place at this time.
				return false
			default:
				return false
			}
		},
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}
	if err := c.Watch(source.Kind[client.Object](gatewaysCache, &gatewayapiv1.GatewayClass{}, handler.EnqueueRequestsFromMapFunc(gatewayClassToGateways), gatewayClassPredicate)); err != nil {
		return nil, err
	}

	// Watch gateways so that we update labels when a gateway is updated or
	// something modifies or removes the label.
	gatewayHasOurController := func(o client.Object) bool {
		gateway := o.(*gatewayapiv1.Gateway)

		if gateway.Labels[key] == value {
			return false
		}

		gatewayClassName := types.NamespacedName{
			Namespace: "", // Gatewayclasses are cluster-scoped.
			Name:      string(gateway.Spec.GatewayClassName),
		}
		var gatewayClass gatewayapiv1.GatewayClass
		if err := reconciler.cache.Get(context.Background(), gatewayClassName, &gatewayClass); err != nil {
			log.Error(err, "failed to get gatewayclass for gateway", "gateway", gateway.Name, "namespace", gateway.Namespace, "gatewayclass", gatewayClassName.Name)
			return false
		}

		if gatewayClass.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName {
			return true
		}

		return false
	}
	gatewayPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return gatewayHasOurController(e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return gatewayHasOurController(e.ObjectNew)
		},
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}
	if err := c.Watch(source.Kind[client.Object](gatewaysCache, &gatewayapiv1.Gateway{}, &handler.EnqueueRequestForObject{}, gatewayPredicate)); err != nil {
		return nil, err
	}

	return c, nil
}

// reconciler reconciles gateways, adding the istio.io/rev label.
type reconciler struct {
	cache    cache.Cache
	client   client.Client
	recorder record.EventRecorder
}

// Reconcile expects request to refer to a gateway and adds the istio.io/rev
// label to it.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("Reconciling gateway", "request", request)

	var gateway gatewayapiv1.Gateway
	if err := r.cache.Get(ctx, request.NamespacedName, &gateway); err != nil {
		return reconcile.Result{}, err
	}

	key, value := operatorcontroller.IstioRevLabelKey, operatorcontroller.IstioName("").Name
	log.Info("Adding label to gateway", "gateway", request.NamespacedName, "key", key, "value", value)
	if gateway.Labels == nil {
		gateway.Labels = map[string]string{}
	}
	gateway.Labels[key] = value
	if err := r.client.Update(ctx, &gateway); err != nil {
		return reconcile.Result{}, err
	}
	r.recorder.Eventf(&gateway, "Normal", "AddedLabel", "Added label %s=%s to gateway %s", key, value, gateway.Name)

	return reconcile.Result{}, nil
}
