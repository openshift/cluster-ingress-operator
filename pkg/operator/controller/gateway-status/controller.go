package gatewaystatus

import (
	"context"
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlruntimemetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
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
	// maxGatewayConditions is the maximum number of conditions allowed on a Gateway resource
	// as defined by the Gateway API specification.
	// See: https://github.com/kubernetes-sigs/gateway-api/blob/main/apis/v1/gateway_types.go
	maxGatewayConditions = 8

	// staleResourcesConditionType flags a Gateway whose previous GatewayClass
	// resources were left behind by a gatewayClassName change.
	staleResourcesConditionType = "openshift.io/StaleResources"
)

var logger = logf.Logger.WithName(controllerName)

// staleResourcesMetric reports Gateways with resources left from a previous
// GatewayClass. See ensureStaleResourcesStatus.
var staleResourcesMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "ingress_controller_gateway_api_stale_resources",
	Help: "Set to 1 when a Gateway has Service or Deployment resources left from a previous GatewayClass after a gatewayClassName change. See https://github.com/istio/istio/issues/62498.",
}, []string{"gateway_namespace", "gateway_name"})

// RegisterMetrics registers the gateway-status controller metrics.
func RegisterMetrics() error {
	if err := ctrlruntimemetrics.Registry.Register(staleResourcesMetric); err != nil {
		return fmt.Errorf("failed to register stale resources metric: %w", err)
	}
	return nil
}

// reconciler reconciles gateways, adding missing conditions to the resource
type reconciler struct {
	cache  cache.Cache
	client client.Client
	// eventreader must be a direct API call because we want to get the latest events
	// that may not be yet on the lister/cache
	// today we could read directly from client.Client because it is constructed
	// without a Cache (see NewClient), but in case we decide to change the client
	// to be cached, we want to still be able to read events directly from the API
	eventreader  client.Reader
	modeAccessor *operatorcontroller.GatewayAPIModeAccessor
}

// NewUnmanaged creates and returns a controller that adds watches for changes on
// Services, DNSRecords and Gateways and when managed, adds the proper status
// conditions to these Gateways.
// This is an unmanaged controller, which means that the manager does not start it.
func NewUnmanaged(mgr manager.Manager, modeAccessor *operatorcontroller.GatewayAPIModeAccessor) (controller.Controller, error) {
	// Create a dedicated cache for this controller to watch resources only in the
	// "openshift-ingress" namespace. Using the operator's main cache would cause it
	// to watch Gateways, Services, Deployments, and DNSRecords in all namespaces,
	// which would increase memory usage unnecessarily.
	// This cache is optimized just for the resources that concern this controller:
	// * Gateway resources with OpenShift-managed GatewayClass
	// * Service, DNSRecord, and Deployment resources labeled with gateway.networking.k8s.io/gateway-name
	// * Only resources in the "openshift-ingress" namespace
	// * Managed fields stripped to reduce memory footprint
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
		cache:        gatewaysCache,
		client:       mgr.GetClient(),
		eventreader:  mgr.GetAPIReader(),
		modeAccessor: modeAccessor,
	}
	c, err := controller.NewUnmanaged(controllerName, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, fmt.Errorf("error initializing controller: %w", err)
	}

	gatewayHasOurController := operatorcontroller.GatewayHasOurController(logger, reconciler.cache, false)
	gatewayPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return gatewayHasOurController(e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return gatewayHasOurController(e.Object)
		},
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

	// Deployments are only used for stale-resource detection, which keys on the
	// GatewayClass label, so ignore updates that leave it unchanged.
	deploymentClassChanged := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return e.ObjectOld.GetLabels()[operatorcontroller.GatewayClassLabelKey] !=
				e.ObjectNew.GetLabels()[operatorcontroller.GatewayClassLabelKey]
		},
	}

	if err := c.Watch(source.Kind[client.Object](gatewaysCache, &gatewayapiv1.Gateway{}, &handler.EnqueueRequestForObject{}, gatewayPredicate, modeAccessor.DependentPredicate())); err != nil {
		return nil, fmt.Errorf("error initializing gateway watcher: %w", err)
	}

	if err := c.Watch(source.Kind[client.Object](gatewaysCache, &corev1.Service{}, handler.EnqueueRequestsFromMapFunc(gatewayFromResourceLabel), isManagedResource, modeAccessor.DependentPredicate())); err != nil {
		return nil, fmt.Errorf("error initializing service watcher: %w", err)
	}

	if err := c.Watch(source.Kind[client.Object](gatewaysCache, &iov1.DNSRecord{}, handler.EnqueueRequestsFromMapFunc(gatewayFromResourceLabel), isManagedResource, modeAccessor.DependentPredicate())); err != nil {
		return nil, fmt.Errorf("error initializing dnsrecord watcher: %w", err)
	}

	if err := c.Watch(source.Kind[client.Object](gatewaysCache, &appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(gatewayFromResourceLabel), isManagedResource, deploymentClassChanged, modeAccessor.DependentPredicate())); err != nil {
		return nil, fmt.Errorf("error initializing deployment watcher: %w", err)
	}

	// Watch the Ingress CR for mode changes. Any change to the Ingress
	// CR triggers re-evaluation of all gateways so that dependent
	// controllers can start or stop processing when the management mode
	// changes. Uses the operator's main cache (not gatewaysCache) because
	// the Ingress CR is a cluster-scoped operator API resource. Only
	// register when the management mode gate is enabled because the
	// Ingress CRD only exists on TechPreview/DevPreview clusters.
	if modeAccessor != nil && modeAccessor.GateEnabled() {
		ingressToGateways := operatorcontroller.IngressWakeUpMapper(gatewaysCache, func() client.ObjectList { return &gatewayapiv1.GatewayList{} })
		if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &operatorv1.Ingress{}, handler.EnqueueRequestsFromMapFunc(ingressToGateways), operatorcontroller.GatewayAPIModeChangePredicate())); err != nil {
			return nil, fmt.Errorf("error initializing ingress watcher: %w", err)
		}
	}

	return c, nil
}

// normalizeHostname removes the trailing root dot from a hostname for consistent matching.
// This ensures that "example.com" and "example.com." are treated as equivalent.
func normalizeHostname(hostname string) string {
	return strings.TrimSuffix(hostname, ".")
}

// Reconcile expects request to refer to a gateway and adds Openshift DNS and LoadBalancer
// conditions to it
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := logger.WithValues("name", request.Name, "namespace", request.Namespace)
	log.Info("Reconciling gateway")
	if r.modeAccessor == nil || !r.modeAccessor.AllowDependents() {
		// Stop asserting stale resources once CIO no longer manages Gateway API.
		staleResourcesMetric.DeleteLabelValues(request.Namespace, request.Name)
		log.Info("Management mode does not allow dependent controllers, skipping reconciliation")
		return reconcile.Result{}, nil
	}

	sourceGateway := &gatewayapiv1.Gateway{}
	if err := r.cache.Get(ctx, request.NamespacedName, sourceGateway); err != nil {
		if apierrors.IsNotFound(err) {
			// The Gateway was deleted; clear any stale-resources metric it left behind.
			staleResourcesMetric.DeleteLabelValues(request.Namespace, request.Name)
			return reconcile.Result{}, nil
		}
		log.Error(err, "error fetching the gateway object")
		return reconcile.Result{}, err
	}

	gateway := sourceGateway.DeepCopy()

	var errs []error

	listOpts := []client.ListOption{
		client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: gateway.GetName()},
		client.InNamespace(operatorcontroller.DefaultOperandNamespace),
	}

	// We want just the first service of the list, Gateway API controllers usually provision
	// just one service
	var childSvc *corev1.Service
	childSvcs := &corev1.ServiceList{}
	svcsListed := true
	if err := r.cache.List(ctx, childSvcs, listOpts...); err != nil {
		svcsListed = false
		log.Error(err, "error fetching the services from gateway")
		errs = append(errs, fmt.Errorf("failed to list services for gateway %s/%s: %w", gateway.Namespace, gateway.Name, err))
	} else if len(childSvcs.Items) > 0 {
		childSvc = childSvcs.Items[0].DeepCopy()
	} else {
		log.Info("no service was found for gateway")
	}

	// List Deployments to detect resources left from a previous GatewayClass.
	childDeploys := &appsv1.DeploymentList{}
	deploysListed := true
	if err := r.cache.List(ctx, childDeploys, listOpts...); err != nil {
		deploysListed = false
		log.Error(err, "error fetching the deployments from gateway")
		errs = append(errs, fmt.Errorf("failed to list deployments for gateway %s/%s: %w", gateway.Namespace, gateway.Name, err))
	}

	// Because we will have multiple DNS records per Gateway (one per listener)
	// we need all of the records that belong to a Gateway
	childDNSRecords := &iov1.DNSRecordList{}
	if err := r.cache.List(ctx, childDNSRecords, listOpts...); err != nil {
		log.Error(err, "error fetching the dnsrecords from gateway")
		errs = append(errs, fmt.Errorf("failed to list dnsrecords for gateway %s/%s: %w", gateway.Namespace, gateway.Name, err))
	} else if len(childDNSRecords.Items) == 0 {
		log.Info("no dnsrecords found for gateway")
	}

	// hostnameToDNSRecord will be used to verify that, given a listener, when it has a hostname
	// what is the matching dnsRecord of it.
	// This is required to cross-check the status of a specific DNSRecord from a hostname
	hostnameToDNSRecord := make(map[string]*iov1.DNSRecord)
	for _, dnsRecord := range childDNSRecords.Items {
		hostname := normalizeHostname(dnsRecord.Spec.DNSName)
		if existing, found := hostnameToDNSRecord[hostname]; found {
			log.Info("duplicate DNSRecord found for hostname, using latest",
				"hostname", hostname,
				"existing", existing.Name,
				"new", dnsRecord.Name)
		}
		hostnameToDNSRecord[hostname] = dnsRecord.DeepCopy()
	}

	// listenerToHostname will be used when verifying a Gateway Listener status,
	// given a listener name, what is the desired hostname of it. This information
	// will then be used to, given the hostnameToDNSRecord map, get the matching
	// dnsRecord to set its status.
	listenerToHostname := make(map[gatewayapiv1.SectionName]gatewayapiv1.Hostname)
	for _, listener := range gateway.Spec.Listeners {
		if listener.Hostname != nil {
			hostname := normalizeHostname(string(*listener.Hostname))
			listenerToHostname[listener.Name] = gatewayapiv1.Hostname(hostname)
		}
	}

	dnsConfig := &configv1.DNS{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
		log.Error(err, "error fetching the cluster object")
		return reconcile.Result{}, fmt.Errorf("failed to get dns 'cluster': %v", err)
	}

	operandEvents := &corev1.EventList{}
	if childSvc != nil && childSvc.UID != "" {
		fieldSelector := client.MatchingFields{
			"involvedObject.apiVersion": "v1",
			"involvedObject.kind":       "Service",
			"source":                    "service-controller",
			"involvedObject.uid":        string(childSvc.UID),
		}
		if err := r.eventreader.List(ctx, operandEvents, client.InNamespace(operatorcontroller.DefaultOperandNamespace), fieldSelector); err != nil {
			log.Error(err, "error fetching the events from namespace")
			errs = append(errs, fmt.Errorf("failed to list events in namespace %q: %v", operatorcontroller.DefaultOperandNamespace, err))
		}
	}

	// Initialize the conditions if they are null, as the compute functions receives a pointer
	if gateway.Status.Conditions == nil {
		gateway.Status.Conditions = make([]metav1.Condition, 0)
	}

	// A ClusterIP Gateway has no load balancer and no DNS records, so
	// LoadBalancerReady and DNSReady do not apply to it.
	if gateway.Spec.GatewayClassName == operatorcontroller.OpenShiftClusterIPGatewayClassName {
		removeLoadBalancerAndDNSReadyConditions(&gateway.Status)
	} else {
		// Compute status conditions for LoadBalancer and DNS.
		status.ComputeGatewayAPILoadBalancerStatus(childSvc, operandEvents.Items, gateway.GetGeneration(), &gateway.Status.Conditions)
		status.ComputeGatewayAPIDNSStatus(dnsConfig, gateway.GetGeneration(), &gateway.Status, listenerToHostname, hostnameToDNSRecord)
	}

	// Surface resources left from a previous GatewayClass after a
	// gatewayClassName change. Only recompute when both lists succeeded, so a
	// failed List does not mask stale resources.
	if svcsListed && deploysListed {
		r.ensureStaleResourcesStatus(gateway, childSvcs.Items, childDeploys.Items)
	}

	// The Gateway API specification limits Gateway status conditions to maxGatewayConditions (8).
	// If other controllers (e.g., Istio) are adding conditions, we need to ensure we don't exceed this limit.
	// Validate that we haven't exceeded the Gateway API condition limit
	if len(gateway.Status.Conditions) > maxGatewayConditions {
		log.Error(fmt.Errorf("gateway condition count exceeds maximum"),
			"gateway condition limit exceeded",
			"current", len(gateway.Status.Conditions),
			"max", maxGatewayConditions,
			"gateway", gateway.Name)
		// This is a warning - we'll still attempt to patch, but the API server may reject it
	}

	log.Info("updating gateway status conditions",
		"conditionCount", len(gateway.Status.Conditions),
		"conditions", gateway.Status.Conditions)

	if err := r.client.Status().Patch(ctx, gateway, client.MergeFrom(sourceGateway)); err != nil {
		log.Error(err, "error patching the gateway status")
		errs = append(errs, err)
	}

	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}

// removeLoadBalancerAndDNSReadyConditions removes the LoadBalancerReady and
// DNSReady conditions from the given Gateway status, including any
// listener-level DNSReady conditions.
func removeLoadBalancerAndDNSReadyConditions(gatewayStatus *gatewayapiv1.GatewayStatus) {
	meta.RemoveStatusCondition(&gatewayStatus.Conditions, operatorv1.LoadBalancerReadyIngressConditionType)
	meta.RemoveStatusCondition(&gatewayStatus.Conditions, operatorv1.DNSReadyIngressConditionType)
	for i := range gatewayStatus.Listeners {
		meta.RemoveStatusCondition(&gatewayStatus.Listeners[i].Conditions, operatorv1.DNSReadyIngressConditionType)
	}
}

// ensureStaleResourcesStatus surfaces resources left behind by a
// gatewayClassName change. Istio removes a Gateway's generated resources only
// via ownerReferences on Gateway deletion, so a class change leaves the
// previous class's resources running. This sets a per-Gateway metric and the
// openshift.io/StaleResources condition so the leftovers are discoverable.
//
// TODO: Remove this, staleResourcesMetric, the Deployment watch, and the alert
// once this upstream bug is fixed: https://github.com/istio/istio/issues/62498
func (r *reconciler) ensureStaleResourcesStatus(gateway *gatewayapiv1.Gateway, services []corev1.Service, deployments []appsv1.Deployment) {
	// Identify current resources by their GatewayClass label rather than by
	// name, so long Gateway names that Istio renames are not misreported.
	currentClass := string(gateway.Spec.GatewayClassName)
	isStale := func(labels map[string]string) bool {
		class := labels[operatorcontroller.GatewayClassLabelKey]
		return class != "" && class != currentClass
	}
	var stale bool
	for i := range services {
		if isStale(services[i].Labels) {
			stale = true
			break
		}
	}
	if !stale {
		for i := range deployments {
			if isStale(deployments[i].Labels) {
				stale = true
				break
			}
		}
	}

	if !stale {
		meta.RemoveStatusCondition(&gateway.Status.Conditions, staleResourcesConditionType)
		staleResourcesMetric.DeleteLabelValues(gateway.Namespace, gateway.Name)
		return
	}

	if meta.FindStatusCondition(gateway.Status.Conditions, staleResourcesConditionType) != nil ||
		len(gateway.Status.Conditions) < maxGatewayConditions {
		meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
			Type:               staleResourcesConditionType,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: gateway.Generation,
			Reason:             "PreviousGatewayClassResourcesFound",
			Message: fmt.Sprintf("Resources from a previous GatewayClass remain in namespace %s after gatewayClassName changed to %q. "+
				"To list leaked resources, run: "+
				"oc -n %s get deploy,svc,sa,hpa -l gateway.networking.k8s.io/gateway-name=%s,gateway.networking.k8s.io/gateway-class-name,gateway.networking.k8s.io/gateway-class-name!=%s",
				operatorcontroller.DefaultOperandNamespace, gateway.Spec.GatewayClassName,
				operatorcontroller.DefaultOperandNamespace, gateway.Name, gateway.Spec.GatewayClassName),
		})
	}
	staleResourcesMetric.WithLabelValues(gateway.Namespace, gateway.Name).Set(1)
}

// The Service and DNSRecord are created with labels that indicate they are Gateway API
// managed, and which Gateway is related to them. The namespace is the same of the resource.
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
