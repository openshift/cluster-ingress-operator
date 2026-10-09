package gateway_service_dns

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	operatorv1 "github.com/openshift/api/operator/v1"
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
func NewUnmanaged(mgr manager.Manager, config Config, modeAccessor *operatorcontroller.GatewayAPIModeAccessor) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		config:       config,
		client:       mgr.GetClient(),
		cache:        operatorCache,
		recorder:     mgr.GetEventRecorderFor(controllerName),
		modeAccessor: modeAccessor,
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
			oldGateway := e.ObjectOld.(*gatewayapiv1.Gateway)
			newGateway := e.ObjectNew.(*gatewayapiv1.Gateway)
			// A DNSRecord CR needs to be updated if the hostname
			// has changed (a listener's port and protocol have
			// no bearing on the DNS record), or if the annotation
			// that overrides the DNS management policy has
			// changed.
			hostnamesChanged := gatewayListenersHostnamesChanged(oldGateway.Spec.Listeners, newGateway.Spec.Listeners)
			dnsPolicyAnnotationChanged := oldGateway.Annotations[operatorcontroller.GatewayDNSManagementPolicyAnnotation] != newGateway.Annotations[operatorcontroller.GatewayDNSManagementPolicyAnnotation]
			changed := hostnamesChanged || dnsPolicyAnnotationChanged
			if changed {
				log.Info("Listener hostname or DNS management policy annotation changed", "gateway", newGateway.Name)
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
			client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: o.GetName()},
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
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(gatewayToService), isInOperandNamespace, gatewayListenersChanged, modeAccessor.DependentPredicate())); err != nil {
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

	client       client.Client
	cache        cache.Cache
	recorder     record.EventRecorder
	modeAccessor *operatorcontroller.GatewayAPIModeAccessor
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

	var gateway gatewayapiv1.Gateway
	gatewayName := types.NamespacedName{
		Namespace: service.Namespace,
		Name:      service.Labels[operatorcontroller.GatewayNameLabelKey],
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
		dnsPolicy := dnsPolicyForGatewayDomain(gateway, domain, infraConfig, dnsConfig)

		// A domain that is Unmanaged only because the annotation forces it
		// (i.e. it would otherwise be Managed) may already have a DNSRecord
		// published to a zone from before the annotation was set. Simply
		// updating the DNSRecord's DNSManagementPolicy to Unmanaged does not
		// retract any records that are already published to a zone -- only
		// deleting the DNSRecord does that, since deletion runs the DNS
		// provider's cleanup logic. So instead of calling EnsureDNSRecord
		// (which would leave a stale, never-cleaned-up DNSRecord behind),
		// delete any existing DNSRecord for the domain and skip creating a
		// new one.
		//
		// This does not apply to a domain that is Unmanaged because it's
		// out-of-cluster: such a domain is never published to any zone, so
		// its DNSRecord (if any) is safe to leave as-is, matching the
		// pre-existing behavior for out-of-cluster domains.
		if dnsPolicy == iov1.UnmanagedDNS && dnsrecord.ManageDNSForDomain(domain, infraConfig.Status.PlatformStatus, dnsConfig) {
			if haveWC, _, err := dnsrecord.CurrentDNSRecord(r.client, name); err != nil {
				errs = append(errs, err)
			} else if haveWC {
				if err := dnsrecord.DeleteDNSRecord(r.client, name); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}

		_, _, err := dnsrecord.EnsureDNSRecord(r.client, name, labels, ownerRef, domain, dnsPolicy, service)
		errs = append(errs, err)
	}
	return errs
}

// dnsPolicyForGatewayDomain determines the DNSManagementPolicy to apply to
// the DNSRecord for the given gateway listener domain.
//
// By default, this is Managed whenever dnsrecord.ManageDNSForDomain reports
// that the domain is a subdomain of the cluster's base domain, and Unmanaged
// otherwise -- this is the pre-existing, automatic behavior and cannot be
// overridden to be more permissive.
//
// However, an operator or cluster admin can force the policy to Unmanaged
// (even for a domain that would otherwise be Managed) by setting
// operatorcontroller.GatewayDNSManagementPolicyAnnotation to "Unmanaged" on
// the gateway. This mirrors how an IngressController with an Internal scope
// sets spec.endpointPublishingStrategy.loadBalancer.dnsManagementPolicy to
// "Unmanaged" (see the "router-internal" IngressController pattern): the
// operator then creates no DNSRecord at all for the affected hostname, and
// publishing that hostname into any zone (typically only the private zone)
// becomes the responsibility of a separately-configured ExternalDNS CR.
//
// Setting the annotation to "Managed" is accepted but has no effect beyond
// the default behavior, since a domain that fails ManageDNSForDomain is not
// one that the platform can safely publish (it is not resolvable via the
// zones defined on dns.config.openshift.io/cluster).
func dnsPolicyForGatewayDomain(gateway *gatewayapiv1.Gateway, domain string, infraConfig *configv1.Infrastructure, dnsConfig *configv1.DNS) iov1.DNSManagementPolicy {
	if !dnsrecord.ManageDNSForDomain(domain, infraConfig.Status.PlatformStatus, dnsConfig) {
		return iov1.UnmanagedDNS
	}
	if gateway.Annotations[operatorcontroller.GatewayDNSManagementPolicyAnnotation] == string(operatorv1.UnmanagedLoadBalancerDNS) {
		return iov1.UnmanagedDNS
	}
	return iov1.ManagedDNS
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
