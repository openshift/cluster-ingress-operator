package gatewaystatus

import (
	"context"
	"fmt"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/resources/status"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var dnsConflicts = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "gateway_api_dns_conflicts",
	Help: "Current number of ListenerSet listeners reported Conflicted by Istio on an OpenShift-managed Gateway.",
}, []string{"listenerset_namespace", "listenerset_name"})

func RegisterMetrics() error {
	return ctrlmetrics.Registry.Register(dnsConflicts)
}

type listenerSetReconciler struct {
	client       client.Client
	cache        cache.Cache
	modeAccessor *operatorcontroller.GatewayAPIModeAccessor
}

func NewListenerSetUnmanaged(mgr manager.Manager, modeAccessor *operatorcontroller.GatewayAPIModeAccessor) (controller.Controller, error) {
	reconciler := &listenerSetReconciler{client: mgr.GetClient(), cache: mgr.GetCache(), modeAccessor: modeAccessor}
	ctrl, err := controller.NewUnmanaged("listenerset_dns_status_controller", controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := ctrl.Watch(source.Kind[client.Object](mgr.GetCache(), &gatewayapiv1.ListenerSet{}, &handler.EnqueueRequestForObject{})); err != nil {
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
		if err := mgr.GetCache().List(ctx, &listenerSets); err != nil {
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
		if err := ctrl.Watch(source.Kind[client.Object](mgr.GetCache(), &operatorv1alpha1.Ingress{}, handler.EnqueueRequestsFromMapFunc(allListenerSets), operatorcontroller.GatewayAPIModeChangePredicate())); err != nil {
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
		if client.IgnoreNotFound(err) == nil {
			dnsConflicts.DeleteLabelValues(request.Namespace, request.Name)
		}
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	updated := listenerSet.DeepCopy()
	hostnames := map[gatewayapiv1.SectionName]gatewayapiv1.Hostname{}
	records := map[string]*iov1.DNSRecord{}
	var dnsConfig configv1.DNS
	parent := operatorcontroller.ListenerSetParent(&listenerSet)
	managed := r.modeAccessor != nil && r.modeAccessor.AllowDependents() && parent.Namespace == operatorcontroller.DefaultOperandNamespace && listenerSet.DeletionTimestamp.IsZero() && len(operatorcontroller.IndexListenerSetParent(&listenerSet)) > 0
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
		count := 0
		for _, listener := range listenerSet.Status.Listeners {
			if meta.IsStatusConditionTrue(listener.Conditions, string(gatewayapiv1.ListenerEntryConditionConflicted)) {
				count++
			}
		}
		dnsConflicts.WithLabelValues(request.Namespace, request.Name).Set(float64(count))
		hostnames = operatorcontroller.ListenerSetDNSHostnames(&listenerSet)
		if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, &dnsConfig); err != nil {
			return reconcile.Result{}, err
		}
		var err error
		records, err = dnsRecordsForGateway(ctx, r.client, parent)
		if err != nil {
			return reconcile.Result{}, err
		}
	} else {
		dnsConflicts.DeleteLabelValues(request.Namespace, request.Name)
	}
	status.ComputeListenerSetDNSStatus(&dnsConfig, updated, hostnames, records)
	if !reflect.DeepEqual(listenerSet.Status, updated.Status) {
		// Optimistic locking prevents replacing a concurrent Istio status update.
		if err := r.client.Status().Patch(ctx, updated, client.MergeFromWithOptions(&listenerSet, client.MergeFromWithOptimisticLock{})); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func dnsRecordsForGateway(ctx context.Context, reader client.Reader, parent types.NamespacedName) (map[string]*iov1.DNSRecord, error) {
	var records iov1.DNSRecordList
	if err := reader.List(ctx, &records, client.InNamespace(parent.Namespace), client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: parent.Name}); err != nil {
		return nil, err
	}
	byHostname := map[string]*iov1.DNSRecord{}
	for _, record := range records.Items {
		hostname := operatorcontroller.NormalizeDNSHostname(record.Spec.DNSName)
		if previous := byHostname[hostname]; previous != nil {
			return nil, fmt.Errorf("duplicate DNSRecords %s and %s for hostname %s", previous.Name, record.Name, hostname)
		}
		byHostname[hostname] = record.DeepCopy()
	}
	return byHostname, nil
}
