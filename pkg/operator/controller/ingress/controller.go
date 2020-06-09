package ingress

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	iov1 "github.com/openshift/api/operatoringress/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	retryable "github.com/openshift/cluster-ingress-operator/pkg/util/retryableerror"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/client-go/tools/record"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "ingress_controller"
)

// TODO: consider moving these to openshift/api
const (
	IngressControllerAdmittedConditionType           = "Admitted"
	IngressControllerDeploymentDegradedConditionType = "DeploymentDegraded"
)

var (
	log = logf.Logger.WithName(controllerName)
	// tlsVersion13Ciphers is a list of TLS v1.3 cipher suites as specified by
	// https://www.openssl.org/docs/man1.1.1/man1/ciphers.html
	tlsVersion13Ciphers = sets.NewString("TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "TLS_CHACHA20_POLY1305_SHA256",
		"TLS_AES_128_CCM_SHA256", "TLS_AES_128_CCM_8_SHA256", "TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384",
		"TLS_CHACHA20_POLY1305_SHA256", "TLS_AES_128_CCM_SHA256", "TLS_AES_128_CCM_8_SHA256")
)

// New creates the ingress controller from configuration. This is the controller
// that handles all the logic for implementing ingress based on
// IngressController resources.
//
// The controller will be pre-configured to watch for IngressController resources
// in the manager namespace.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	reconciler := &reconciler{
		Config:   config,
		client:   mgr.GetClient(),
		cache:    mgr.GetCache(),
		recorder: mgr.GetEventRecorderFor(controllerName),
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &operatorv1.IngressController{}}, &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, enqueueRequestForOwningIngressController(config.Namespace)); err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &corev1.Service{}}, enqueueRequestForOwningIngressController(config.Namespace)); err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &iov1.DNSRecord{}}, &handler.EnqueueRequestForOwner{OwnerType: &operatorv1.IngressController{}}); err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &configv1.Ingress{}}, &handler.EnqueueRequestsFromMapFunc{ToRequests: handler.ToRequestsFunc(reconciler.ingressConfigToIngressController)}); err != nil {
		return nil, err
	}
	return c, nil
}

func (r *reconciler) ingressConfigToIngressController(o handler.MapObject) []reconcile.Request {
	var requests []reconcile.Request
	controllers := &operatorv1.IngressControllerList{}
	if err := r.cache.List(context.Background(), controllers, client.InNamespace(r.Namespace)); err != nil {
		log.Error(err, "failed to list ingresscontrollers for ingress", "related", o.Meta.GetSelfLink())
		return requests
	}
	for _, ic := range controllers.Items {
		log.Info("queueing ingresscontroller", "name", ic.Name, "related", o.Meta.GetSelfLink())
		request := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: ic.Namespace,
				Name:      ic.Name,
			},
		}
		requests = append(requests, request)
	}
	return requests
}

func enqueueRequestForOwningIngressController(namespace string) handler.EventHandler {
	return &handler.EnqueueRequestsFromMapFunc{
		ToRequests: handler.ToRequestsFunc(func(a handler.MapObject) []reconcile.Request {
			labels := a.Meta.GetLabels()
			if ingressName, ok := labels[manifests.OwningIngressControllerLabel]; ok {
				log.Info("queueing ingress", "name", ingressName, "related", a.Meta.GetSelfLink())
				return []reconcile.Request{
					{
						NamespacedName: types.NamespacedName{
							Namespace: namespace,
							Name:      ingressName,
						},
					},
				}
			} else {
				return []reconcile.Request{}
			}
		}),
	}
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	Namespace              string
	IngressControllerImage string
}

// reconciler handles the actual ingress reconciliation logic in response to
// events.
type reconciler struct {
	Config

	client   client.Client
	cache    cache.Cache
	recorder record.EventRecorder
}

// admissionRejection is an error type for ingresscontroller admission
// rejections.
type admissionRejection struct {
	// Reason describes why the ingresscontroller was rejected.
	Reason string
}

// Error returns the reason or reasons why an ingresscontroller was rejected.
func (e *admissionRejection) Error() string {
	return e.Reason
}

// Reconcile expects request to refer to a ingresscontroller in the operator
// namespace, and will do all the work to ensure the ingresscontroller is in the
// desired state.
func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	// Only proceed if we can get the ingresscontroller's state.
	ingress := &operatorv1.IngressController{}
	if err := r.client.Get(context.TODO(), request.NamespacedName, ingress); err != nil {
		if errors.IsNotFound(err) {
			// This means the ingress was already deleted/finalized and there are
			// stale queue entries (or something edge triggering from a related
			// resource that got deleted async).
			log.Info("ingresscontroller not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get ingresscontroller %q: %v", request, err)
	}

	// If the ingresscontroller is deleted, handle that and return early.
	if ingress.DeletionTimestamp != nil {
		if err := r.ensureIngressDeleted(ingress); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to ensure ingress deletion: %v", err)
		}
		log.Info("ingresscontroller was successfully deleted", "ingresscontroller", ingress)
		return reconcile.Result{}, nil
	}

	// Only proceed if we can collect cluster config.
	apiConfig := &configv1.APIServer{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, apiConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get apiserver 'cluster': %v", err)
	}
	dnsConfig := &configv1.DNS{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get dns 'cluster': %v", err)
	}
	infraConfig := &configv1.Infrastructure{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get infrastructure 'cluster': %v", err)
	}
	ingressConfig := &configv1.Ingress{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, ingressConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get ingress 'cluster': %v", err)
	}
	networkConfig := &configv1.Network{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, networkConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get network 'cluster': %v", err)
	}

	// Admit if necessary. Don't process until admission succeeds. If admission is
	// successful, immediately re-queue to refresh state.
	if !isAdmitted(ingress) || needsReadmission(ingress) {
		if err := r.admit(ingress, ingressConfig, infraConfig); err != nil {
			switch err := err.(type) {
			case *admissionRejection:
				r.recorder.Event(ingress, "Warning", "Rejected", err.Reason)
				return reconcile.Result{}, nil
			default:
				return reconcile.Result{}, fmt.Errorf("failed to admit ingresscontroller: %v", err)
			}
		}
		r.recorder.Event(ingress, "Normal", "Admitted", "ingresscontroller passed validation")
		// Just re-queue for simplicity
		return reconcile.Result{Requeue: true}, nil
	}

	// The ingresscontroller is safe to process, so ensure it.
	if err := r.ensureIngressController(ingress, dnsConfig, infraConfig, ingressConfig, apiConfig, networkConfig); err != nil {
		switch e := err.(type) {
		case retryable.Error:
			log.Error(e, "got retryable error; requeueing", "after", e.After())
			return reconcile.Result{RequeueAfter: e.After()}, nil
		default:
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

// admit processes the given ingresscontroller by defaulting and validating its
// fields.  Returns an error value, which will have a non-nil value of type
// admissionRejection if the ingresscontroller was rejected, or a non-nil
// value of a different type if the ingresscontroller could not be processed.
func (r *reconciler) admit(current *operatorv1.IngressController, ingressConfig *configv1.Ingress, infraConfig *configv1.Infrastructure) error {
	updated := current.DeepCopy()

	setDefaultDomain(updated, ingressConfig)
	setDefaultPublishingStrategy(updated, infraConfig)

	// The TLS security profile need not be defaulted.  If none is set, we
	// get the default from the APIServer config (which is assumed to be
	// valid).

	if err := r.validate(updated); err != nil {
		switch err := err.(type) {
		case *admissionRejection:
			updated.Status.Conditions = mergeConditions(updated.Status.Conditions, operatorv1.OperatorCondition{
				Type:    IngressControllerAdmittedConditionType,
				Status:  operatorv1.ConditionFalse,
				Reason:  "Invalid",
				Message: err.Reason,
			})
			updated.Status.ObservedGeneration = updated.Generation
			if !ingressStatusesEqual(current.Status, updated.Status) {
				if err := r.client.Status().Update(context.TODO(), updated); err != nil {
					return fmt.Errorf("failed to update status: %v", err)
				}
			}
		}
		return err
	}

	updated.Status.Conditions = mergeConditions(updated.Status.Conditions, operatorv1.OperatorCondition{
		Type:   IngressControllerAdmittedConditionType,
		Status: operatorv1.ConditionTrue,
		Reason: "Valid",
	})
	updated.Status.ObservedGeneration = updated.Generation
	if !ingressStatusesEqual(current.Status, updated.Status) {
		if err := r.client.Status().Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to update status: %v", err)
		}
	}
	return nil
}

func isAdmitted(ic *operatorv1.IngressController) bool {
	for _, cond := range ic.Status.Conditions {
		if cond.Type == IngressControllerAdmittedConditionType && cond.Status == operatorv1.ConditionTrue {
			return true
		}
	}
	return false
}

// needsReadmission returns a Boolean value indicating whether the given
// ingresscontroller needs to be re-admitted.  Re-admission is necessary in
// order to revalidate mutable fields that are subject to admission checks.  The
// determination whether re-admission is needed is based on the
// ingresscontroller's current generation and the observed generation recorded
// in its status.
func needsReadmission(ic *operatorv1.IngressController) bool {
	if ic.Generation != ic.Status.ObservedGeneration {
		return true
	}
	return false
}

func setDefaultDomain(ic *operatorv1.IngressController, ingressConfig *configv1.Ingress) bool {
	var effectiveDomain string
	switch {
	case len(ic.Spec.Domain) > 0:
		effectiveDomain = ic.Spec.Domain
	default:
		effectiveDomain = ingressConfig.Spec.Domain
	}
	if len(ic.Status.Domain) == 0 {
		ic.Status.Domain = effectiveDomain
		return true
	}
	return false
}

func setDefaultPublishingStrategy(ic *operatorv1.IngressController, infraConfig *configv1.Infrastructure) bool {
	effectiveStrategy := ic.Spec.EndpointPublishingStrategy
	if effectiveStrategy == nil {
		var strategyType operatorv1.EndpointPublishingStrategyType
		switch infraConfig.Status.Platform {
		case configv1.AWSPlatformType, configv1.AzurePlatformType, configv1.GCPPlatformType, configv1.IBMCloudPlatformType:
			strategyType = operatorv1.LoadBalancerServiceStrategyType
		case configv1.LibvirtPlatformType:
			strategyType = operatorv1.HostNetworkStrategyType
		default:
			strategyType = operatorv1.HostNetworkStrategyType
		}
		effectiveStrategy = &operatorv1.EndpointPublishingStrategy{
			Type: strategyType,
		}
	}
	switch effectiveStrategy.Type {
	case operatorv1.LoadBalancerServiceStrategyType:
		if effectiveStrategy.LoadBalancer == nil {
			effectiveStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			}
		}
	case operatorv1.NodePortServiceStrategyType:
		// No parameters.
	case operatorv1.HostNetworkStrategyType:
		// No parameters.
	case operatorv1.PrivateStrategyType:
		// No parameters.
	}
	if ic.Status.EndpointPublishingStrategy == nil {
		ic.Status.EndpointPublishingStrategy = effectiveStrategy
		return true
	}
	return false
}

// tlsProfileSpecForIngressController returns a TLS profile spec based on either
// the profile specified by the given ingresscontroller, the profile specified
// by the APIServer config if the ingresscontroller does not specify one, or the
// "Intermediate" profile if neither the ingresscontroller nor the APIServer
// config specifies one.  Note that the return value must not be mutated by the
// caller; the caller must make a copy if it needs to mutate the value.
func tlsProfileSpecForIngressController(ic *operatorv1.IngressController, apiConfig *configv1.APIServer) *configv1.TLSProfileSpec {
	if hasTLSSecurityProfile(ic) {
		return tlsProfileSpecForSecurityProfile(ic.Spec.TLSSecurityProfile)
	}
	return tlsProfileSpecForSecurityProfile(apiConfig.Spec.TLSSecurityProfile)
}

// hasTLSSecurityProfile checks whether the given ingresscontroller specifies a
// TLS security profile.
func hasTLSSecurityProfile(ic *operatorv1.IngressController) bool {
	if ic.Spec.TLSSecurityProfile == nil {
		return false
	}
	if len(ic.Spec.TLSSecurityProfile.Type) == 0 {
		return false
	}
	return true
}

// tlsProfileSpecForSecurityProfile returns a TLS profile spec based on the
// provided security profile, or the "Intermediate" profile if an unknown
// or "Modern" security profile type is provided.  Note that the return value
// must not be mutated by the caller; the caller must make a copy if it needs
// to mutate the value.
func tlsProfileSpecForSecurityProfile(profile *configv1.TLSSecurityProfile) *configv1.TLSProfileSpec {
	if profile != nil {
		if profile.Type == configv1.TLSProfileCustomType {
			if profile.Custom != nil {
				return &profile.Custom.TLSProfileSpec
			}
			return &configv1.TLSProfileSpec{}
		} else if spec, ok := configv1.TLSProfiles[profile.Type]; ok {
			// TODO remove when haproxy is built with an openssl version that supports tls v1.3.
			if profile.Type == configv1.TLSProfileModernType {
				return configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
			}
			return spec
		}
	}
	return configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
}

// validate attempts to perform validation of the given ingresscontroller and
// returns an error value, which will have a non-nil value of type
// admissionRejection if the ingresscontroller is invalid, or a non-nil value of
// a different type if validation could not be completed.
func (r *reconciler) validate(ic *operatorv1.IngressController) error {
	var errors []error

	ingresses := &operatorv1.IngressControllerList{}
	if err := r.cache.List(context.TODO(), ingresses, client.InNamespace(r.Namespace)); err != nil {
		return fmt.Errorf("failed to list ingresscontrollers: %v", err)
	}

	if err := validateDomain(ic); err != nil {
		errors = append(errors, err)
	}
	if err := validateDomainUniqueness(ic, ingresses.Items); err != nil {
		errors = append(errors, err)
	}
	if err := validateTLSSecurityProfile(ic); err != nil {
		errors = append(errors, err)
	}

	if err := utilerrors.NewAggregate(errors); err != nil {
		return &admissionRejection{err.Error()}
	}

	return nil
}

func validateDomain(ic *operatorv1.IngressController) error {
	if len(ic.Status.Domain) == 0 {
		return fmt.Errorf("domain is required")
	}
	return nil
}

// validateDomainUniqueness returns an error if the desired controller's domain
// conflicts with any other admitted controllers.
func validateDomainUniqueness(desired *operatorv1.IngressController, existing []operatorv1.IngressController) error {
	for i := range existing {
		current := existing[i]
		if !isAdmitted(&current) {
			continue
		}
		if desired.UID != current.UID && desired.Status.Domain == current.Status.Domain {
			return fmt.Errorf("conflicts with: %s", current.Name)
		}
	}

	return nil
}

var (
	// validTLSVersions is all allowed values for TLSProtocolVersion.
	validTLSVersions = map[configv1.TLSProtocolVersion]struct{}{
		configv1.VersionTLS10: {},
		configv1.VersionTLS11: {},
		configv1.VersionTLS12: {},
		// TODO: Add VersionTLS13 support after haproxy is built with an openssl
		//  version that supports tls v1.3.
	}

	// isValidCipher is a regexp for strings that look like cipher names.
	isValidCipher = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]+$`).MatchString
)

// validateTLSSecurityProfile validates the given ingresscontroller's TLS
// security profile, if it specifies one.
func validateTLSSecurityProfile(ic *operatorv1.IngressController) error {
	if !hasTLSSecurityProfile(ic) {
		return nil
	}

	if ic.Spec.TLSSecurityProfile.Type != configv1.TLSProfileCustomType {
		return nil
	}

	spec := ic.Spec.TLSSecurityProfile.Custom
	if spec == nil {
		return fmt.Errorf("security profile is not defined")
	}

	var errs []error

	if len(spec.Ciphers) == 0 {
		errs = append(errs, fmt.Errorf("security profile has an empty ciphers list"))
	} else {
		invalidCiphers := []string{}
		for _, cipher := range spec.Ciphers {
			if !isValidCipher(strings.TrimPrefix(cipher, "!")) {
				invalidCiphers = append(invalidCiphers, cipher)
			}
		}
		if len(invalidCiphers) != 0 {
			errs = append(errs, fmt.Errorf("security profile has invalid ciphers: %s", strings.Join(invalidCiphers, ", ")))
		}
		filteredCiphers := filterTLS13Ciphers(spec.Ciphers)
		if len(filteredCiphers) == 0 {
			errs = append(errs, fmt.Errorf("security profile contains only tls v1.3 cipher suites"))
		}
	}

	if _, ok := validTLSVersions[spec.MinTLSVersion]; !ok {
		errs = append(errs, fmt.Errorf("security profile has invalid minimum security protocol version: %q", spec.MinTLSVersion))
	}

	return utilerrors.NewAggregate(errs)
}

// filterTLS13Ciphers filters any TLS v1.3 cipher suites from ciphers returning
// a filtered list of cipher suites.
func filterTLS13Ciphers(ciphers []string) []string {
	filteredCiphers := []string{}
	for i := 0; i < len(ciphers); i++ {
		exist := false
		if tlsVersion13Ciphers.Has(ciphers[i]) {
			exist = true
		}
		if !exist {
			filteredCiphers = append(filteredCiphers, ciphers[i])
		}
	}
	return filteredCiphers
}

// ensureIngressDeleted tries to delete ingress, and if successful, will remove
// the finalizer.
func (r *reconciler) ensureIngressDeleted(ingress *operatorv1.IngressController) error {
	errs := []error{}
	if svcExists, err := r.finalizeLoadBalancerService(ingress); err != nil {
		errs = append(errs, fmt.Errorf("failed to finalize load balancer service for ingress %s/%s: %v", ingress.Namespace, ingress.Name, err))
	} else if svcExists {
		errs = append(errs, fmt.Errorf("load balancer service exists for ingress %s/%s", ingress.Namespace, ingress.Name))
	}

	// Delete the wildcard DNS record, and block ingresscontroller finalization
	// until the dnsrecord has been finalized.
	if err := r.deleteWildcardDNSRecord(ingress); err != nil {
		errs = append(errs, fmt.Errorf("failed to delete wildcard dnsrecord for ingress %s/%s: %v", ingress.Namespace, ingress.Name, err))
	}
	if haveRec, _, err := r.currentWildcardDNSRecord(ingress); err != nil {
		errs = append(errs, fmt.Errorf("failed to get current wildcard dnsrecord for ingress %s/%s: %v", ingress.Namespace, ingress.Name, err))
	} else if haveRec {
		errs = append(errs, fmt.Errorf("wildcard dnsrecord exists for ingress %s/%s", ingress.Namespace, ingress.Name))
	}

	if err := r.ensureRouterDeleted(ingress); err != nil {
		errs = append(errs, fmt.Errorf("failed to delete deployment for ingress %s/%s: %v", ingress.Namespace, ingress.Name, err))
	}
	if haveDepl, _, err := r.currentRouterDeployment(ingress); err != nil {
		errs = append(errs, fmt.Errorf("failed to get deployment for ingress %s/%s: %v", ingress.Namespace, ingress.Name, err))
	} else if haveDepl {
		errs = append(errs, fmt.Errorf("deployment still exists for ingress %s/%s", ingress.Namespace, ingress.Name))
	}

	if len(errs) == 0 {
		// Remove the ingresscontroller finalizer.
		if slice.ContainsString(ingress.Finalizers, manifests.IngressControllerFinalizer) {
			updated := ingress.DeepCopy()
			updated.Finalizers = slice.RemoveString(updated.Finalizers, manifests.IngressControllerFinalizer)
			if err := r.client.Update(context.TODO(), updated); err != nil {
				errs = append(errs, fmt.Errorf("failed to remove finalizer from ingresscontroller %s: %v", ingress.Name, err))
			}
		}
	}
	return utilerrors.NewAggregate(errs)
}

// ensureIngressController ensures all necessary router resources exist for a
// given ingresscontroller.  Any error values are collected into either a
// retryable.Error value, if any of the error values are retryable, or else an
// Aggregate error value.
func (r *reconciler) ensureIngressController(ci *operatorv1.IngressController, dnsConfig *configv1.DNS, infraConfig *configv1.Infrastructure, ingressConfig *configv1.Ingress, apiConfig *configv1.APIServer, networkConfig *configv1.Network) error {
	// Before doing anything at all with the controller, ensure it has a finalizer
	// so we can clean up later.
	if !slice.ContainsString(ci.Finalizers, manifests.IngressControllerFinalizer) {
		updated := ci.DeepCopy()
		updated.Finalizers = append(updated.Finalizers, manifests.IngressControllerFinalizer)
		if err := r.client.Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to update finalizers: %v", err)
		}
		if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: updated.Namespace, Name: updated.Name}, updated); err != nil {
			return fmt.Errorf("failed to get ingresscontroller: %v", err)
		}
		ci = updated
	}

	if err := r.ensureRouterNamespace(); err != nil {
		return fmt.Errorf("failed to ensure namespace: %v", err)
	}

	var errs []error
	if _, _, err := r.ensureServiceCAConfigMap(); err != nil {
		// Even if we were unable to create the configmap at this time,
		// it is still safe try to create the deployment, as it
		// specifies that the volume mount is non-optional, meaning the
		// deployment will not start until the configmap exists.
		errs = append(errs, err)
	}

	haveDepl, deployment, err := r.ensureRouterDeployment(ci, infraConfig, ingressConfig, apiConfig, networkConfig)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure deployment: %v", err))
		return utilerrors.NewAggregate(errs)
	} else if !haveDepl {
		errs = append(errs, fmt.Errorf("failed to get router deployment %s/%s", ci.Namespace, ci.Name))
		return utilerrors.NewAggregate(errs)
	}

	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
		Controller: &trueVar,
	}

	var lbService *corev1.Service
	var wildcardRecord *iov1.DNSRecord
	if haveLB, lb, err := r.ensureLoadBalancerService(ci, deploymentRef, infraConfig); err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure load balancer service for %s: %v", ci.Name, err))
	} else {
		lbService = lb
		if _, record, err := r.ensureWildcardDNSRecord(ci, lbService, haveLB); err != nil {
			errs = append(errs, fmt.Errorf("failed to ensure wildcard dnsrecord for %s: %v", ci.Name, err))
		} else {
			wildcardRecord = record
		}
	}

	if _, _, err := r.ensureNodePortService(ci, deploymentRef); err != nil {
		errs = append(errs, err)
	}

	if internalSvc, err := r.ensureInternalIngressControllerService(ci, deploymentRef); err != nil {
		errs = append(errs, fmt.Errorf("failed to create internal router service for ingresscontroller %s: %v", ci.Name, err))
	} else if err := r.ensureMetricsIntegration(ci, internalSvc, deploymentRef); err != nil {
		errs = append(errs, fmt.Errorf("failed to integrate metrics with openshift-monitoring for ingresscontroller %s: %v", ci.Name, err))
	}

	if _, _, err := r.ensureRsyslogConfigMap(ci, deploymentRef, ingressConfig); err != nil {
		errs = append(errs, err)
	}

	if _, _, err := r.ensureRouterPodDisruptionBudget(ci, deploymentRef); err != nil {
		errs = append(errs, err)
	}

	operandEvents := &corev1.EventList{}
	if err := r.cache.List(context.TODO(), operandEvents, client.InNamespace("openshift-ingress")); err != nil {
		errs = append(errs, fmt.Errorf("failed to list events in namespace %q: %v", "openshift-ingress", err))
	}

	errs = append(errs, r.syncIngressControllerStatus(ci, deployment, lbService, operandEvents.Items, wildcardRecord, dnsConfig))

	return retryable.NewMaybeRetryableAggregate(errs)
}

// IsStatusDomainSet checks whether status.domain of ingress is set.
func IsStatusDomainSet(ingress *operatorv1.IngressController) bool {
	if len(ingress.Status.Domain) == 0 {
		return false
	}
	return true
}
