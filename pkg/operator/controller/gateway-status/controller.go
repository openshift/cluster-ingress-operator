package gatewaystatus

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
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

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/resources/status"
)

const (
	controllerName = "gateway_status_controller"
)

var logger = logf.Logger.WithName(controllerName)

// reconciler reconciles gateways, adding missing conditions to the resource
type reconciler struct {
	cache    cache.Cache
	client   client.Client
	recorder record.EventRecorder
}

// NewUnmanaged creates and returns a controller that adds watches for changes on
// Services, DNSRecords and Gateways and when managed, adds the proper status
// conditions to these Gateways.
// This is an unmanaged controller, which means that the manager does not start it.
func NewUnmanaged(mgr manager.Manager) (controller.Controller, error) {
	// Create a new cache for gateways so it can watch all namespaces.
	// (Using the operator cache for gateways in all namespaces would cause
	// it to cache other resources in all namespaces.)
	// This cache is optimized just for the resources that concern this controller:
	// * Just Openshift managed Gateway Class
	// * Just resources on "openshift-ingress" namespace
	// * Removing managed fields to reduce memory footprint
	gatewaysCache, err := cache.New(mgr.GetConfig(), cache.Options{
		Scheme: mgr.GetScheme(),
		Mapper: mgr.GetRESTMapper(),
		// defaultCacheConfig is a config that will be set on the cache of this controller
		// while watching for resources.
		// It will:
		// - Strip managed fields to reduce memory footprint
		// - Default to watch just resources on the "openshift-ingress" namespace,
		// as this is the only managed namespace by this controller
		DefaultNamespaces: map[string]cache.Config{
			operatorcontroller.DefaultOperandNamespace: {
				Transform: cache.TransformStripManagedFields(),
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("error initializing cache: %w", err)
	}
	// Make sure the manager starts the cache with the other runnables.
	if err := mgr.Add(gatewaysCache); err != nil {
		// We skip returning the error here to follow the pattern of other cache scenarios.
		// We may not have the CRDs added/initialized yet, and this will happen
		// during gatewayapi controller initialization so having the cache added
		// but not synced is fine for now
		logger.Error(err, "error adding the cache to manager")
	}

	reconciler := &reconciler{
		cache:    gatewaysCache,
		client:   mgr.GetClient(),
		recorder: mgr.GetEventRecorderFor(controllerName),
	}
	c, err := controller.NewUnmanaged(controllerName, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, fmt.Errorf("error initializing controller: %w", err)
	}

	gatewayHasOurController := operatorcontroller.GatewayHasOurController(logger, reconciler.cache)
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

	isManagedResource := predicate.NewPredicateFuncs(func(object client.Object) bool {
		labels := object.GetLabels()
		gwName, ok := labels[operatorcontroller.GatewayNameLabelKey]
		return ok && gwName != ""
	})

	if err := c.Watch(source.Kind[client.Object](gatewaysCache, &gatewayapiv1.Gateway{}, &handler.EnqueueRequestForObject{}, gatewayPredicate)); err != nil {
		return nil, fmt.Errorf("error initializing gateway watcher: %w", err)
	}

	if err := c.Watch(source.Kind[client.Object](gatewaysCache, &corev1.Service{}, handler.EnqueueRequestsFromMapFunc(gatewayFromResourceLabel), isManagedResource)); err != nil {
		return nil, fmt.Errorf("error initializing service watcher: %w", err)
	}

	if err := c.Watch(source.Kind[client.Object](gatewaysCache, &iov1.DNSRecord{}, handler.EnqueueRequestsFromMapFunc(gatewayFromResourceLabel), isManagedResource)); err != nil {
		return nil, fmt.Errorf("error initializing dnsrecord watcher: %w", err)
	}

	return c, nil
}

// Reconcile expects request to refer to a gateway and adds Openshift DNS and LoadBalancer
// conditions to it
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := logger.WithValues("name", request.Name, "namespace", request.Namespace)
	log.Info("Reconciling gateway")

	sourceGateway := &gatewayapiv1.Gateway{}
	if err := r.cache.Get(ctx, request.NamespacedName, sourceGateway); err != nil {
		log.Error(err, "error fetching the gateway object")
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	gateway := sourceGateway.DeepCopy()

	var errs []error

	childSvc := &corev1.Service{}
	err := fetchFirstMatchingFromGateway(ctx, r.cache, childSvc, gateway)
	if err != nil {
		log.Error(err, "error fetching the service from gateway")
	}

	childDNSRecord := &iov1.DNSRecord{}
	err = fetchFirstMatchingFromGateway(ctx, r.cache, childDNSRecord, gateway)
	if err != nil {
		log.Error(err, "error fetching the dnsrecord from gateway")
	}

	dnsConfig := &configv1.DNS{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
		log.Error(err, "error fetching the cluster object")
		return reconcile.Result{}, fmt.Errorf("failed to get dns 'cluster': %v", err)
	}

	infraConfig := &configv1.Infrastructure{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		log.Error(err, "error fetching the infrastructure 'cluster' object")
		return reconcile.Result{}, fmt.Errorf("failed to get infrastructure 'cluster': %v", err)
	}

	operandEvents := &corev1.EventList{}
	if err := r.client.List(ctx, operandEvents, client.InNamespace(operatorcontroller.DefaultOperandNamespace)); err != nil {
		log.Error(err, "error fetching the events from namespace")
		errs = append(errs, fmt.Errorf("failed to list events in namespace %q: %v", operatorcontroller.DefaultOperandNamespace, err))
	}

	// Initialize the conditions if they are null, as the compute* functions receives a pointer
	if gateway.Status.Conditions == nil {
		gateway.Status.Conditions = make([]metav1.Condition, 0)
	}

	// WARNING: one thing to be aware is that conditions on Gateway resource are limited to 8: https://github.com/kubernetes-sigs/gateway-api/blob/a8fe5c8732a37ef471d86afaf570ff8ad0ef0221/apis/v1/gateway_types.go#L691
	// So if the Gateway controller (in our case Istio) is adding 2 conditions, we have 6 more to add.
	status.ComputeGatewayAPIDNSStatus(childDNSRecord, dnsConfig, gateway.GetGeneration(), &gateway.Status.Conditions)
	status.ComputeGatewayAPILoadBalancerStatus(childSvc, operandEvents.Items, gateway.GetGeneration(), &gateway.Status.Conditions)

	log.Info("new conditions to be added", "conditions", gateway.Status.Conditions)
	if err := r.client.Status().Patch(ctx, gateway, client.MergeFrom(sourceGateway)); err != nil {
		log.Error(err, "error patching the gateway status")
		errs = append(errs, err)
	} else {
		r.recorder.Eventf(gateway, "Normal", "AddedConditions", "Added Openshift conditions to gateway %s", gateway.Name)
	}

	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}

// The Service and DNSRecord are created with labels that indicate they are Gateway API
// managed, and which Gateway is related to them. The namespace is the same of the resouce.
// We can then simply rely on these labels to decide which Gateway to add to the reconciliation
// queue instead of going with different approaches for the same resource
// Example of the labels we care: gateway.networking.k8s.io/gateway-name: gatewayname
func gatewayFromResourceLabel(_ context.Context, o client.Object) []reconcile.Request {
	labels := o.GetLabels()
	namespace := o.GetNamespace()
	gwName, ok := labels[operatorcontroller.GatewayNameLabelKey]
	// No label is present, we should not add anything to reconciliation queue
	if !ok || gwName == "" {
		logger.Info(fmt.Sprintf("object %T does not contain the label", o), "namespace", o.GetNamespace(), "name", o.GetName())
		return []reconcile.Request{}
	}

	logger.Info(fmt.Sprintf("object %T does contain the label", o), "namespace", o.GetNamespace(), "name", o.GetName(), "gateway", gwName)
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Namespace: namespace,
				Name:      gwName,
			},
		},
	}
}

type gatewayMatcheableResource interface {
	*corev1.Service | *iov1.DNSRecord
}

func fetchFirstMatchingFromGateway[T gatewayMatcheableResource](ctx context.Context, kclient client.Reader, obj T, gw *gatewayapiv1.Gateway) error {
	// At this moment we just know that services must have the Gateway API label with the
	// same name of the Gateway. So we need a list, this list should have at least
	// 1 item, and we care just about the first one (1:1 on Gateway/Service relation)
	listOpts := []client.ListOption{
		client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: gw.GetName()},
		client.InNamespace(operatorcontroller.DefaultOperandNamespace),
	}

	switch t := any(obj).(type) {
	case *corev1.Service:
		list := &corev1.ServiceList{}
		if err := kclient.List(ctx, list, listOpts...); err != nil {
			return err
		}
		if len(list.Items) == 0 {
			err := fmt.Errorf("no services where found for Gateway")
			return err
		}

		*t = *list.Items[0].DeepCopy()

	case *iov1.DNSRecord:
		list := &iov1.DNSRecordList{}
		if err := kclient.List(ctx, list, listOpts...); err != nil {
			return err
		}
		if len(list.Items) == 0 {
			err := fmt.Errorf("no dnsrecord where found for Gateway")
			return err
		}
		*t = *list.Items[0].DeepCopy()

	default:
		return fmt.Errorf("unsupported type %T", obj)
	}

	return nil
}
