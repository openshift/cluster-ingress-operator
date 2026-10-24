package gateway_service_dns

import (
	"context"
	"fmt"
	"reflect"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/resources/dnsrecord"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"k8s.io/client-go/tools/record"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "service_dns_controller"
)

var log = logf.Logger.WithName(controllerName)

// NewUnmanaged creates and returns a controller that watches services that are
// associated with gateways and creates dnsrecord objects for them.  This is an
// unmanaged controller, which means that the manager does not start it.
func NewUnmanaged(mgr manager.Manager, config Config, modeAccessor *operatorcontroller.GatewayAPIModeAccessor, listenerSetCache cache.Cache) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		config:           config,
		client:           mgr.GetClient(),
		cache:            operatorCache,
		listenerSetCache: listenerSetCache,
		recorder:         mgr.GetEventRecorderFor(controllerName),
		modeAccessor:     modeAccessor,
	}
	options := controller.Options{Reconciler: reconciler}
	options.DefaultFromConfig(mgr.GetControllerOptions())
	c, err := controller.NewUnmanaged(controllerName, options)
	if err != nil {
		return nil, err
	}
	scheme := mgr.GetClient().Scheme()
	mapper := mgr.GetClient().RESTMapper()
	isServiceNeedingDNS := predicate.NewPredicateFuncs(func(o client.Object) bool {
		_, ok := o.(*corev1.Service).Labels[operatorcontroller.ManagedByIstioLabelKey]
		return ok
	})
	gatewayListenersChanged := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			old := e.ObjectOld.(*gatewayapiv1.Gateway).Spec
			new := e.ObjectNew.(*gatewayapiv1.Gateway).Spec
			// Policy and status changes can revoke ListenerSet DNS eligibility.
			changed := gatewayListenersHostnamesChanged(old.Listeners, new.Listeners) || old.GatewayClassName != new.GatewayClassName || !reflect.DeepEqual(old.AllowedListeners, new.AllowedListeners) || !reflect.DeepEqual(e.ObjectOld.(*gatewayapiv1.Gateway).Status, e.ObjectNew.(*gatewayapiv1.Gateway).Status) || !reflect.DeepEqual(e.ObjectOld.GetDeletionTimestamp(), e.ObjectNew.GetDeletionTimestamp())
			if changed {
				log.Info("Gateway DNS inputs changed", "gateway", e.ObjectNew.(*gatewayapiv1.Gateway).Name)
			}
			return changed
		},
	}
	isInOperandNamespace := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == config.OperandNamespace
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(reconciler.requestsForGateway), isInOperandNamespace, gatewayListenersChanged, modeAccessor.DependentPredicate())); err != nil {
		return nil, err
	}
	// The map handler visits both old and new objects on reparenting.
	if err := c.Watch(source.Kind[client.Object](listenerSetCache, &gatewayapiv1.ListenerSet{}, handler.EnqueueRequestsFromMapFunc(reconciler.requestsForListenerSet), modeAccessor.DependentPredicate())); err != nil {
		return nil, err
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &corev1.Service{}, &handler.EnqueueRequestForObject{}, isServiceNeedingDNS, isInOperandNamespace, modeAccessor.DependentPredicate())); err != nil {
		return nil, err
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &iov1.DNSRecord{}, handler.EnqueueRequestForOwner(scheme, mapper, &corev1.Service{}), isInOperandNamespace, modeAccessor.DependentPredicate())); err != nil {
		return nil, err
	}

	// Watch the Ingress CR for mode changes. Any change to the Ingress
	// CR triggers re-evaluation of all services in the operand namespace
	// so that dependent controllers can start or stop processing when
	// the management mode changes. Only register when the management mode
	// gate is enabled because the Ingress CRD only exists on
	// TechPreview/DevPreview clusters.
	if modeAccessor != nil && modeAccessor.GateEnabled() {
		ingressToServices := operatorcontroller.IngressWakeUpMapper(operatorCache, func() client.ObjectList { return &corev1.ServiceList{} }, client.InNamespace(config.OperandNamespace), client.HasLabels{operatorcontroller.ManagedByIstioLabelKey})
		if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1alpha1.Ingress{}, handler.EnqueueRequestsFromMapFunc(ingressToServices), operatorcontroller.GatewayAPIModeChangePredicate())); err != nil {
			return nil, err
		}
	}

	return c, nil
}

func (r *reconciler) requestsForGateway(ctx context.Context, object client.Object) []reconcile.Request {
	if object.GetNamespace() != r.config.OperandNamespace {
		return nil
	}
	var services corev1.ServiceList
	if err := r.cache.List(ctx, &services, client.InNamespace(r.config.OperandNamespace), client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: object.GetName()}); err != nil {
		log.Error(err, "failed to list services for gateway", "gateway", object.GetName())
		return nil
	}
	var requests []reconcile.Request
	for _, service := range services.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&service)})
	}
	return requests
}

func (r *reconciler) requestsForListenerSet(ctx context.Context, object client.Object) []reconcile.Request {
	parent := operatorcontroller.ListenerSetParent(object.(*gatewayapiv1.ListenerSet))
	return r.requestsForGateway(ctx, &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: parent.Namespace, Name: parent.Name}})
}

// gatewayListenersHostnamesChanged returns a Boolean indicating whether any
// hostnames changed in the given gateway listeners.
func gatewayListenersHostnamesChanged(xs, ys []gatewayapiv1.Listener) bool {
	x := map[string]string{}
	y := map[string]string{}
	for i := range xs {
		if xs[i].Hostname != nil {
			x[string(xs[i].Name)] = string(*xs[i].Hostname)
		}
	}
	for i := range ys {
		if ys[i].Hostname != nil {
			y[string(ys[i].Name)] = string(*ys[i].Hostname)
		}
	}
	return !reflect.DeepEqual(x, y)
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	// OperandNamespace is the namespace in which to watch for services and
	// dnsrecords and in which to create dnsrecords.
	OperandNamespace string
}

// reconciler handles the actual service reconciliation logic.
type reconciler struct {
	config Config

	client           client.Client
	cache            cache.Cache
	listenerSetCache cache.Cache
	recorder         record.EventRecorder
	modeAccessor     *operatorcontroller.GatewayAPIModeAccessor
}

// Reconcile expects request to refer to a service and creates or reconciles a
// dnsrecord.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)
	if r.modeAccessor == nil || !r.modeAccessor.AllowDependents() {
		log.Info("Management mode does not allow dependent controllers, skipping reconciliation")
		return reconcile.Result{}, nil
	}

	var service corev1.Service
	if err := r.cache.Get(ctx, request.NamespacedName, &service); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("service not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if len(service.Labels[operatorcontroller.GatewayNameLabelKey]) == 0 {
		log.Info("service does not have a label with the expected key; reconciliation will be skipped", "request", request, "labelKey", operatorcontroller.GatewayNameLabelKey)
		return reconcile.Result{}, nil
	}
	if service.Namespace != r.config.OperandNamespace {
		return reconcile.Result{}, nil
	}

	var gateway gatewayapiv1.Gateway
	gatewayName := types.NamespacedName{
		Namespace: service.Namespace,
		Name:      service.Labels[operatorcontroller.GatewayNameLabelKey],
	}
	if err := r.cache.Get(ctx, gatewayName, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			gateway.Namespace, gateway.Name = gatewayName.Namespace, gatewayName.Name
			return reconcile.Result{}, utilerrors.NewAggregate(r.deleteStaleDNSRecordsForGateway(ctx, &gateway, &service, sets.NewString()))
		}
		return reconcile.Result{}, err
	}

	dnsConfig := &configv1.DNS{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get dns 'cluster': %v", err)
	}

	infraConfig := &configv1.Infrastructure{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get infrastructure 'cluster': %v", err)
	}
	if infraConfig.Status.PlatformStatus == nil {
		log.Info("infrastructure 'cluster' has nil status.platformStatus; reconciliation will be skipped")
		return reconcile.Result{}, nil
	}

	domains := getGatewayHostnames(&gateway)
	var listenerSets gatewayapiv1.ListenerSetList
	if err := r.listenerSetCache.List(ctx, &listenerSets, client.MatchingFields{operatorcontroller.ListenerSetParentGatewayIndex: gatewayName.String()}); err != nil {
		return reconcile.Result{}, err
	}
	if len(listenerSets.Items) > 0 && operatorcontroller.GatewayHasOurController(log, r.client, false)(&gateway) {
		for index := range listenerSets.Items {
			for _, hostname := range operatorcontroller.ListenerSetDNSHostnames(&listenerSets.Items[index]) {
				domains.Insert(string(hostname) + ".")
			}
		}
	}
	if !gateway.DeletionTimestamp.IsZero() {
		domains = sets.NewString()
	}
	var errs []error
	errs = append(errs, r.ensureDNSRecordsForGateway(ctx, &gateway, &service, domains.List(), infraConfig, dnsConfig)...)
	errs = append(errs, r.deleteStaleDNSRecordsForGateway(ctx, &gateway, &service, domains)...)
	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}

// getGatewayHostnames returns a sets.String with the hostnames from the given
// gateway's listeners.  Adds a trailing dot if it's missing from the hostname.
func getGatewayHostnames(gateway *gatewayapiv1.Gateway) sets.String {
	domains := sets.NewString()
	for _, listener := range gateway.Spec.Listeners {
		if listener.Hostname == nil || len(*listener.Hostname) == 0 {
			continue
		}
		domain := operatorcontroller.NormalizeDNSHostname(string(*listener.Hostname)) + "."
		domains.Insert(domain)
	}
	return domains
}

// ensureDNSRecordsForGateway ensures that a DNSRecord CR exists, associated
// with the given gateway and service, for each of the given domains.  It
// returns a list of any errors that result from ensuring those DNSRecord CRs.
func (r *reconciler) ensureDNSRecordsForGateway(ctx context.Context, gateway *gatewayapiv1.Gateway, service *corev1.Service, domains []string, infraConfig *configv1.Infrastructure, dnsConfig *configv1.DNS) []error {
	var existing iov1.DNSRecordList
	if err := r.client.List(ctx, &existing, client.InNamespace(gateway.Namespace), client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: gateway.Name}); err != nil {
		return []error{err}
	}
	desiredDomains := sets.NewString(domains...)
	for _, record := range existing.Items {
		domain := operatorcontroller.NormalizeDNSHostname(record.Spec.DNSName) + "."
		name := operatorcontroller.GatewayDNSRecordName(gateway, domain)
		if desiredDomains.Has(domain) && record.Name != name.Name {
			return []error{fmt.Errorf("duplicate DNSRecords for Gateway %s/%s", gateway.Namespace, gateway.Name)}
		}
	}
	labels := map[string]string{
		operatorcontroller.GatewayNameLabelKey: gateway.Name,
	}
	for k, v := range service.Labels {
		labels[k] = v
	}
	ownerRef := metav1.OwnerReference{
		APIVersion: corev1.SchemeGroupVersion.String(),
		Kind:       "Service",
		Name:       service.Name,
		UID:        service.UID,
	}
	var errs []error
	for _, domain := range domains {
		name := operatorcontroller.GatewayDNSRecordName(gateway, domain)
		dnsPolicy := iov1.UnmanagedDNS
		if dnsrecord.ManageDNSForDomain(domain, infraConfig.Status.PlatformStatus, dnsConfig) {
			dnsPolicy = iov1.ManagedDNS
		}
		_, _, err := dnsrecord.EnsureDNSRecord(r.client, name, labels, ownerRef, domain, dnsPolicy, service)
		errs = append(errs, err)
	}
	return errs
}

// deleteStaleDNSRecordsForGateway deletes any DNSRecord CRs that are associated
// with the given gateway but specify a DNS name that is not in the given set of
// domains.  Such DNSRecord CRs may exist if a hostname was modified or deleted
// on the gateway.  deleteStaleDNSRecordsForGateway returns a list of any errors
// that result from deleting those DNSRecord CRs.
func (r *reconciler) deleteStaleDNSRecordsForGateway(ctx context.Context, gateway *gatewayapiv1.Gateway, service *corev1.Service, domains sets.String) []error {
	listOpts := []client.ListOption{
		client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: gateway.Name},
		client.InNamespace(r.config.OperandNamespace),
	}
	var dnsrecords iov1.DNSRecordList
	// Use the client rather than the cache to make sure we don't use stale
	// data and fail to clean up stale dnsrecords.
	if err := r.client.List(ctx, &dnsrecords, listOpts...); err != nil {
		return []error{err}
	}
	var errs []error
	for i := range dnsrecords.Items {
		if domains.Has(dnsrecords.Items[i].Spec.DNSName) {
			continue
		}
		name := types.NamespacedName{
			Namespace: dnsrecords.Items[i].Namespace,
			Name:      dnsrecords.Items[i].Name,
		}
		errs = append(errs, dnsrecord.DeleteDNSRecord(r.client, name))
	}
	return errs
}
