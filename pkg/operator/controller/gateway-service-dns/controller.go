package gateway_service_dns

import (
	"context"
	"fmt"
	"reflect"
	"strings"

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

	// gatewayNameLabelKey is the key of a label that Istio adds to
	// deployments that it creates for gateways that it manages.  Istio uses
	// this label in the selector of any service that it creates for a
	// gateway.
	gatewayNameLabelKey = "gateway.networking.k8s.io/gateway-name"
	// managedByIstioLabelKey is the key of a label that Istio adds to
	// resources that it manages.
	managedByIstioLabelKey = "gateway.istio.io/managed"
)

var log = logf.Logger.WithName(controllerName)

// NewUnmanaged creates and returns a controller that watches services that are
// associated with gateways and creates dnsrecord objects for them.  This is an
// unmanaged controller, which means that the manager does not start it.
func NewUnmanaged(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		config:   config,
		client:   mgr.GetClient(),
		cache:    operatorCache,
		recorder: mgr.GetEventRecorderFor(controllerName),
	}
	c, err := controller.NewUnmanaged(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	scheme := mgr.GetClient().Scheme()
	mapper := mgr.GetClient().RESTMapper()
	isServiceNeedingDNS := predicate.NewPredicateFuncs(func(o client.Object) bool {
		_, ok := o.(*corev1.Service).Labels[managedByIstioLabelKey]
		return ok
	})
	gatewayListenersChanged := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			old := e.ObjectOld.(*gatewayapiv1.Gateway).Spec.Listeners
			new := e.ObjectNew.(*gatewayapiv1.Gateway).Spec.Listeners
			// A DNSRecord CR needs to be updated if, and only if,
			// the hostname has changed (a listener's port and
			// protocol have no bearing on the DNS record).
			changed := gatewayListenersHostnamesChanged(old, new)
			if changed {
				log.Info("Listener hostname changed", "gateway", e.ObjectNew.(*gatewayapiv1.Gateway).Name)
			}
			return changed
		},
	}
	isInOperandNamespace := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == config.OperandNamespace
	})
	gatewayToService := func(ctx context.Context, o client.Object) []reconcile.Request {
		var services corev1.ServiceList
		listOpts := []client.ListOption{
			client.MatchingLabels{gatewayNameLabelKey: o.GetName()},
			client.InNamespace(config.OperandNamespace),
		}
		requests := []reconcile.Request{}
		if err := reconciler.cache.List(ctx, &services, listOpts...); err != nil {
			log.Error(err, "failed to list services for gateway", "gateway", o.GetName())
			return requests
		}
		for i := range services.Items {
			request := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: services.Items[i].Namespace,
					Name:      services.Items[i].Name,
				},
			}
			requests = append(requests, request)
			log.Info("Enqueuing service for gateway", "gateway", o.GetName(), "service", request.NamespacedName)
		}
		return requests
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(gatewayToService), isInOperandNamespace, gatewayListenersChanged)); err != nil {
		return nil, err
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &corev1.Service{}, &handler.EnqueueRequestForObject{}, isServiceNeedingDNS, isInOperandNamespace)); err != nil {
		return nil, err
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &iov1.DNSRecord{}, handler.EnqueueRequestForOwner(scheme, mapper, &corev1.Service{}), isInOperandNamespace)); err != nil {
		return nil, err
	}
	return c, nil
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

	client   client.Client
	cache    cache.Cache
	recorder record.EventRecorder
}

// Reconcile expects request to refer to a service and creates or reconciles a
// dnsrecord.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	var service corev1.Service
	if err := r.cache.Get(ctx, request.NamespacedName, &service); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("service not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if len(service.Labels[gatewayNameLabelKey]) == 0 {
		log.Info("service does not have a label with the expected key; reconciliation will be skipped", "request", request, "labelKey", gatewayNameLabelKey)
		return reconcile.Result{}, nil
	}

	var gateway gatewayapiv1.Gateway
	gatewayName := types.NamespacedName{
		Namespace: service.Namespace,
		Name:      service.Labels[gatewayNameLabelKey],
	}
	if err := r.cache.Get(ctx, gatewayName, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("gateway not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
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
		domain := string(*listener.Hostname)
		// If domain doesn't have a trailing dot, add it.
		if !strings.HasSuffix(domain, ".") {
			domain = domain + "."
		}
		domains.Insert(domain)
	}
	return domains
}

// ensureDNSRecordsForGateway ensures that a DNSRecord CR exists, associated
// with the given gateway and service, for each of the given domains.  It
// returns a list of any errors that result from ensuring those DNSRecord CRs.
func (r *reconciler) ensureDNSRecordsForGateway(ctx context.Context, gateway *gatewayapiv1.Gateway, service *corev1.Service, domains []string, infraConfig *configv1.Infrastructure, dnsConfig *configv1.DNS) []error {
	labels := map[string]string{
		gatewayNameLabelKey: gateway.Name,
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
		client.MatchingLabels{gatewayNameLabelKey: gateway.Name},
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
