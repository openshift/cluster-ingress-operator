package gatewaystatus

import (
	"context"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/resources/status"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type listenerSetReconciler struct {
	client       client.Client
	cache        cache.Cache
	modeAccessor *operatorcontroller.GatewayAPIModeAccessor
}

func NewListenerSetUnmanaged(mgr manager.Manager, modeAccessor *operatorcontroller.GatewayAPIModeAccessor, listenerSetCache cache.Cache) (controller.Controller, error) {
	reconciler := &listenerSetReconciler{client: mgr.GetClient(), cache: listenerSetCache, modeAccessor: modeAccessor}
	ctrl, err := controller.NewUnmanaged("listenerset_dns_status_controller", controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := ctrl.Watch(source.Kind[client.Object](listenerSetCache, &gatewayapiv1.ListenerSet{}, &handler.EnqueueRequestForObject{})); err != nil {
		return nil, err
	}
	parentToListenerSets := func(ctx context.Context, object client.Object) []reconcile.Request {
		parent := client.ObjectKeyFromObject(object)
		if _, ok := object.(*iov1.DNSRecord); ok {
			parent.Name = object.GetLabels()[operatorcontroller.GatewayNameLabelKey]
		}
		return reconciler.requestsForParent(ctx, parent)
	}
	for _, object := range []client.Object{&gatewayapiv1.Gateway{}, &iov1.DNSRecord{}} {
		if err := ctrl.Watch(source.Kind[client.Object](mgr.GetCache(), object, handler.EnqueueRequestsFromMapFunc(parentToListenerSets))); err != nil {
			return nil, err
		}
	}
	allListenerSets := func(ctx context.Context, _ client.Object) []reconcile.Request {
		var listenerSets gatewayapiv1.ListenerSetList
		if err := listenerSetCache.List(ctx, &listenerSets); err != nil {
			logger.Error(err, "failed to list ListenerSets")
			return nil
		}
		var requests []reconcile.Request
		for _, listenerSet := range listenerSets.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&listenerSet)})
		}
		return requests
	}
	if err := ctrl.Watch(source.Kind[client.Object](mgr.GetCache(), &gatewayapiv1.GatewayClass{}, handler.EnqueueRequestsFromMapFunc(allListenerSets))); err != nil {
		return nil, err
	}
	if modeAccessor != nil && modeAccessor.GateEnabled() {
		if err := ctrl.Watch(source.Kind[client.Object](mgr.GetCache(), &operatorv1.Ingress{}, handler.EnqueueRequestsFromMapFunc(allListenerSets), operatorcontroller.GatewayAPIModeChangePredicate())); err != nil {
			return nil, err
		}
	}
	return ctrl, nil
}

func (r *listenerSetReconciler) requestsForParent(ctx context.Context, parent types.NamespacedName) []reconcile.Request {
	var listenerSets gatewayapiv1.ListenerSetList
	if parent.Name == "" {
		return nil
	}
	if err := r.cache.List(ctx, &listenerSets, client.MatchingFields{operatorcontroller.ListenerSetParentGatewayIndex: parent.String()}); err != nil {
		logger.Error(err, "failed to list attached ListenerSets", "parent", parent)
		return nil
	}
	var requests []reconcile.Request
	for _, listenerSet := range listenerSets.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&listenerSet)})
	}
	return requests
}

func (r *listenerSetReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	var listenerSet gatewayapiv1.ListenerSet
	if err := r.cache.Get(ctx, request.NamespacedName, &listenerSet); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	updatedListenerSet := listenerSet.DeepCopy()
	hostnames := map[gatewayapiv1.SectionName]gatewayapiv1.Hostname{}
	records := map[string]*iov1.DNSRecord{}
	var dnsConfig configv1.DNS
	parent := operatorcontroller.ListenerSetParent(&listenerSet)

	// TODO - investigate why the managed variable isn't global, and why listenerSet has anything to do with managed status
	// and why managed status can change here.
	managed := r.modeAccessor != nil && r.modeAccessor.AllowDependents() &&
		parent.Namespace == operatorcontroller.DefaultOperandNamespace &&
		listenerSet.DeletionTimestamp.IsZero() && len(operatorcontroller.IndexListenerSetParent(&listenerSet)) > 0
	if managed {
		var gateway gatewayapiv1.Gateway
		if err := r.client.Get(ctx, parent, &gateway); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return reconcile.Result{}, err
			}
			managed = false
		} else {
			managed = gateway.DeletionTimestamp.IsZero() && operatorcontroller.GatewayHasOurController(logger, r.client, false)(&gateway)
		}
	}
	if managed {
		hostnames = operatorcontroller.ListenerSetDNSHostnames(&listenerSet)
		if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, &dnsConfig); err != nil {
			return reconcile.Result{}, err
		}
		var err error
		records, err = dnsRecordsForGateway(ctx, r.client, parent)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	// TODO if it is not managed, we don't have any dnsConfig, hostnames, or records
	status.ComputeListenerSetDNSStatus(&dnsConfig, updatedListenerSet, hostnames, records)
	if !reflect.DeepEqual(listenerSet.Status, updatedListenerSet.Status) {
		// Optimistic locking prevents replacing a concurrent Istio status update.
		// TODO investigate whether optimistic logging can cause problems or latency.
		if err := r.client.Status().Patch(ctx, updatedListenerSet, client.MergeFromWithOptions(&listenerSet, client.MergeFromWithOptimisticLock{})); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

// Collect all the dnsRecords affiliated with the given Gateway.
func dnsRecordsForGateway(ctx context.Context, reader client.Reader, parentGateway types.NamespacedName) (map[string]*iov1.DNSRecord, error) {
	var dnsRecords iov1.DNSRecordList
	if err := reader.List(ctx, &dnsRecords, client.InNamespace(parentGateway.Namespace), client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: parentGateway.Name}); err != nil {
		return nil, err
	}
	hostnameToDNSRecord := map[string]*iov1.DNSRecord{}
	for _, record := range dnsRecords.Items {
		hostname := dns.NormalizeHostname(record.Spec.DNSName)
		if _, exists := hostnameToDNSRecord[hostname]; exists {
			// Ambiguous claim: report DNS as not ready. gateway-service-dns reports the duplicate itself.
			logger.Info("duplicate DNSRecords on Gateway; treating DNS as not ready", "gateway", parentGateway.String())
			hostnameToDNSRecord[hostname] = nil
			continue
		}
		hostnameToDNSRecord[hostname] = record.DeepCopy()
	}
	return hostnameToDNSRecord, nil
}
