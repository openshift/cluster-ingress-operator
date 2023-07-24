package gateway_service_dns

import (
	"context"
	"net"
	"reflect"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/resources/dnsrecord"

	gatewayapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"k8s.io/client-go/tools/record"

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
	gatewayNameLabelKey = "istio.io/gateway-name"
)

var log = logf.Logger.WithName(controllerName)

// NewUnmanaged creates and returns a controller that watches gateways and
// creates dnsrecord objects for them.  This is an unmanaged controller, which
// means that the manager does not start it.
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
	gatewayAddressesChangedPredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			old := e.ObjectOld.(*gatewayapiv1beta1.Gateway).Status.Addresses
			new := e.ObjectNew.(*gatewayapiv1beta1.Gateway).Status.Addresses
			// The targets of associated DNSRecord CRs may need to
			// be updated if the addresses have changed.
			return gatewayAddressesChanged(old, new)
		},
	}
	gatewayListenersChangedPredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			old := e.ObjectOld.(*gatewayapiv1beta1.Gateway).Spec.Listeners
			new := e.ObjectNew.(*gatewayapiv1beta1.Gateway).Spec.Listeners
			// The DNS names of associated DNSRecord CRs need to be
			// updated if the host name has changed (a listener's
			// port and protocol have no bearing on the DNS record).
			return gatewayListenersHostnamesChanged(old, new)
		},
	}
	isInOperandNamespace := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == config.OperandNamespace
	})
	if err := c.Watch(source.Kind(operatorCache, &gatewayapiv1beta1.Gateway{}), &handler.EnqueueRequestForObject{}, isInOperandNamespace, predicate.Or(gatewayAddressesChangedPredicate, gatewayListenersChangedPredicate)); err != nil {
		return nil, err
	}
	if err := c.Watch(source.Kind(operatorCache, &iov1.DNSRecord{}), handler.EnqueueRequestForOwner(scheme, mapper, &gatewayapiv1beta1.Gateway{}), isInOperandNamespace); err != nil {
		return nil, err
	}
	return c, nil
}

// gatewayAddressesChanged returns a Boolean indicating whether any
// addresses changed in the given list of gateway addresses.
func gatewayAddressesChanged(xs, ys []gatewayapiv1beta1.GatewayAddress) bool {
	cmpAddresses := func(a, b gatewayapiv1beta1.GatewayAddress) bool {
		const defaultType = gatewayapiv1beta1.IPAddressType
		var (
			aType = defaultType
			bType = defaultType
		)
		if a.Type != nil {
			aType = *a.Type
		}
		if b.Type != nil {
			bType = *b.Type
		}
		return aType < bType || aType == bType && a.Value < b.Value
	}
	cmpOpts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(cmpAddresses),
	}
	return !cmp.Equal(xs, ys, cmpOpts...)
}

// gatewayListenersHostnamesChanged returns a Boolean indicating whether any
// hostnames changed in the given gateway listeners.
func gatewayListenersHostnamesChanged(xs, ys []gatewayapiv1beta1.Listener) bool {
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
	// OperandNamespace is the namespace in which to watch for gateways and
	// dnsrecords and in which to create dnsrecords.
	OperandNamespace string
}

// reconciler handles the actual reconciliation logic for gateways and
// dnsrecords.
type reconciler struct {
	config Config

	client   client.Client
	cache    cache.Cache
	recorder record.EventRecorder
}

// Reconcile expects request to refer to a gateway and creates or reconciles
// dnsrecords.
//
// Reconciliation ensures that there is a DNSRecord CR for every gateway
// listener, with targets coming from the gateway addresses.  The listeners are
// read from the gateway spec because it has required information that is not
// reported in status, namely the host name.  The addresses are read from the
// gateway status because it has required information that is not specified in
// spec, namely the address that was assigned by Istio's gateway controller.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	var gateway gatewayapiv1beta1.Gateway
	gatewayName := request.NamespacedName
	if err := r.cache.Get(ctx, gatewayName, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("gateway not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	targets, recordType := getGatewayAddresses(&gateway)
	if len(targets) == 0 {
		log.Info("gateway has no addresses; reconciliation will be skipped", "request", request, "addresses", gateway.Spec.Addresses)
		return reconcile.Result{}, nil
	}

	domains := getGatewayHostnames(&gateway)

	var errs []error
	errs = append(errs, r.ensureDNSRecordsForGateway(ctx, &gateway, domains.List(), sets.List(targets), recordType)...)
	errs = append(errs, r.deleteStaleDNSRecordsForGateway(ctx, &gateway, domains, targets)...)
	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}

// getGatewayAddresses returns a slice of DNS targets and a DNS record type
// derived from the given gateway's addresses, as reported in the gateway's
// status.
//
// The Gateway API spec puts few restrictions on allowed addresses.  Notably, a
// gateway can specify that an address has type "IPAddress" while specifying a
// host name as the value, which Istio does to indicate that the address is
// external to the cluster (for example, to represent a public cloud
// load-balancer).

// Also of note, a gateway can specify multiple addresses, including multiple
// host names.  In the case of IP addresses, we can create A records for all the
// addresses.  However, DNS does not allow multiple canonical names for the same
// host name, so if the gateway has multiple addresses that are host names, we
// must pick just one of them.
//
// This function looks through all the gateway's addresses.  If the first one is
// an IP address, this function returns the set of all IP addresses and the "A"
// record type.  If the first address is a host name, this function uses this
// address, ignores any other addresses, and returns the "CNAME" record type.
func getGatewayAddresses(gateway *gatewayapiv1beta1.Gateway) (sets.Set[string], iov1.DNSRecordType) {
	var (
		targets    = sets.New[string]()
		recordType iov1.DNSRecordType
	)

	for _, address := range gateway.Status.Addresses {
		// Istio specifies "IPAddress" if the address is an external
		// address, irrespective of whether it be IP address or whether
		// it be host name.
		if address.Type == nil || *address.Type != gatewayapiv1beta1.IPAddressType {
			continue
		}

		// A DNS name can have multiple A record targets or a single
		// CNAME record.  If the first address is a host name, we just
		// create a CNAME for that host name.  Otherwise, we accumulate
		// any IP addresses and create A's for the lot of them.
		if net.ParseIP(address.Value) == nil {
			if targets.Len() > 0 {
				// Ignore host names if we are already
				// accumulating IP addresses.
				continue
			}
			// If the first address we got is a host name, use it.
			recordType = iov1.CNAMERecordType
			targets.Insert(address.Value)
			break
		} else {
			// Accumulate IP addresses for A record targets.
			recordType = iov1.ARecordType
			targets.Insert(address.Value)
		}
	}

	return targets, recordType
}

// getGatewayHostnames returns a sets.String with the hostnames from the given
// gateway's listeners.  Adds a trailing dot if it's missing from the hostname.
func getGatewayHostnames(gateway *gatewayapiv1beta1.Gateway) sets.String {
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
// with the given gateway, for each of the given domains and target addresses.
// It returns a list of any errors that result from ensuring those DNSRecord
// CRs.
func (r *reconciler) ensureDNSRecordsForGateway(ctx context.Context, gateway *gatewayapiv1beta1.Gateway, domains, targets []string, recordType iov1.DNSRecordType) []error {
	labels := map[string]string{
		gatewayNameLabelKey: gateway.Name,
	}
	for k, v := range gateway.Labels {
		labels[k] = v
	}
	ownerRef := metav1.OwnerReference{
		APIVersion: gatewayapiv1beta1.GroupVersion.String(),
		Kind:       "Gateway",
		Name:       gateway.Name,
		UID:        gateway.UID,
	}
	var errs []error
	for _, domain := range domains {
		name := operatorcontroller.GatewayDNSRecordName(gateway, domain)
		_, _, err := dnsrecord.EnsureDNSRecord(r.client, name, labels, ownerRef, domain, targets, recordType)
		errs = append(errs, err)
	}
	return errs
}

// deleteStaleDNSRecordsForGateway deletes any DNSRecord CRs that are associated
// with the given gateway but specify a DNS name that is not in the given set of
// domains.  Such DNSRecord CRs may exist if a hostname was modified or deleted
// on the gateway.  deleteStaleDNSRecordsForGateway returns a list of any errors
// that result from deleting those DNSRecord CRs.
func (r *reconciler) deleteStaleDNSRecordsForGateway(ctx context.Context, gateway *gatewayapiv1beta1.Gateway, domains sets.String, targets sets.Set[string]) []error {
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
		if domains.Has(dnsrecords.Items[i].Spec.DNSName) && targets.HasAny(dnsrecords.Items[i].Spec.Targets...) {
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
