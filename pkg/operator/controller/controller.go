package controller

import (
	"context"
	"fmt"

	iov1 "github.com/openshift/cluster-ingress-operator/pkg/api/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
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

var log = logf.Logger.WithName("controller")

// New creates the operator controller from configuration. This is the
// controller that handles all the logic for implementing ingress based on
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
	return c, nil
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
	OperatorReleaseVersion string
}

// reconciler handles the actual ingress reconciliation logic in response to
// events.
type reconciler struct {
	Config

	client   client.Client
	cache    cache.Cache
	recorder record.EventRecorder
}

// Reconcile expects request to refer to a ingresscontroller in the operator
// namespace, and will do all the work to ensure the ingresscontroller is in the
// desired state.
func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	errs := []error{}
	result := reconcile.Result{}

	log.Info("reconciling", "request", request)

	// Get the current ingress state.
	ingress := &operatorv1.IngressController{}
	if err := r.client.Get(context.TODO(), request.NamespacedName, ingress); err != nil {
		if errors.IsNotFound(err) {
			// This means the ingress was already deleted/finalized and there are
			// stale queue entries (or something edge triggering from a related
			// resource that got deleted async).
			log.Info("ingresscontroller not found; reconciliation will be skipped", "request", request)
		} else {
			errs = append(errs, fmt.Errorf("failed to get ingresscontroller %q: %v", request, err))
		}
		ingress = nil
	}

	if ingress != nil {
		if ingress.DeletionTimestamp != nil {
			// Handle deletion.
			if err := r.ensureIngressDeleted(ingress); err != nil {
				errs = append(errs, fmt.Errorf("failed to ensure ingress deletion: %v", err))
			}
		} else {
			dnsConfig := &configv1.DNS{}
			if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
				errs = append(errs, fmt.Errorf("failed to get dns 'cluster': %v", err))
				dnsConfig = nil
			}
			infraConfig := &configv1.Infrastructure{}
			if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
				errs = append(errs, fmt.Errorf("failed to get infrastructure 'cluster': %v", err))
				infraConfig = nil
			}
			ingressConfig := &configv1.Ingress{}
			if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, ingressConfig); err != nil {
				errs = append(errs, fmt.Errorf("failed to get ingress 'cluster': %v", err))
				ingressConfig = nil
			}

			// For now, if the cluster configs are unavailable, defer reconciliation
			// because weaving conditionals everywhere to deal with various nil states
			// is too complicated. It doesn't seem too risky to rely on the invariant
			// of the cluster config being available.
			if dnsConfig != nil && infraConfig != nil && ingressConfig != nil {
				// Ensure we have all the necessary scaffolding on which to place router instances.
				if err := r.ensureRouterNamespace(); err != nil {
					errs = append(errs, fmt.Errorf("failed to ensure router namespace: %v", err))
				}
				if !IsStatusDomainSet(ingress) {
					deployment := &appsv1.Deployment{}
					service := &corev1.Service{}
					operandEvents := []corev1.Event{}
					wildcardRecord := &iov1.DNSRecord{}
					if err := r.syncIngressControllerStatus(ingress, ingressConfig, deployment, service, operandEvents, wildcardRecord, dnsConfig); err != nil {
						errs = append(errs, fmt.Errorf("failed to sync ingresscontroller status: %v", err))
					}
					// To avoid another reconciliation loop, check the status.domain again as
					// syncIngressControllerStatus may have not set it.
				} else if IsStatusDomainSet(ingress) {
					// Handle everything else.
					if err := r.enforceEffectiveEndpointPublishingStrategy(ingress, infraConfig); err != nil {
						errs = append(errs, fmt.Errorf("failed to enforce the effective HA configuration for ingresscontroller %s: %v", ingress.Name, err))
					} else if err := r.enforceIngressFinalizer(ingress); err != nil {
						errs = append(errs, fmt.Errorf("failed to enforce ingress finalizer %s/%s: %v", ingress.Namespace, ingress.Name, err))
					} else {
						if err := r.ensureIngressController(ingress, ingressConfig, dnsConfig, infraConfig); err != nil {
							errs = append(errs, fmt.Errorf("failed to ensure ingresscontroller: %v", err))
						}
					}
				}
			}
		}
	}

	// TODO: Should this be another controller?
	if err := r.syncOperatorStatus(); err != nil {
		errs = append(errs, fmt.Errorf("failed to sync operator status: %v", err))
	}

	return result, utilerrors.NewAggregate(errs)
}

// publishingStrategyTypeForInfra returns the appropriate endpoint publishing
// strategy type for the given infrastructure config.
func publishingStrategyTypeForInfra(infraConfig *configv1.Infrastructure) operatorv1.EndpointPublishingStrategyType {
	switch infraConfig.Status.Platform {
	case configv1.AWSPlatformType, configv1.AzurePlatformType, configv1.GCPPlatformType:
		return operatorv1.LoadBalancerServiceStrategyType
	case configv1.LibvirtPlatformType:
		return operatorv1.HostNetworkStrategyType
	}
	return operatorv1.HostNetworkStrategyType
}

// enforceEffectiveEndpointPublishingStrategy uses the infrastructure config to
// determine the appropriate endpoint publishing strategy configuration for the
// given ingresscontroller and publishes it to the ingresscontroller's status.
func (r *reconciler) enforceEffectiveEndpointPublishingStrategy(ci *operatorv1.IngressController, infraConfig *configv1.Infrastructure) error {
	// The ingresscontroller's endpoint publishing strategy is immutable, so
	// if we have previously published a strategy in status, we must
	// continue to use that strategy it.
	if ci.Status.EndpointPublishingStrategy != nil {
		return nil
	}

	updated := ci.DeepCopy()

	switch {
	case ci.Spec.EndpointPublishingStrategy != nil:
		updated.Status.EndpointPublishingStrategy = ci.Spec.EndpointPublishingStrategy.DeepCopy()
	default:
		updated.Status.EndpointPublishingStrategy = &operatorv1.EndpointPublishingStrategy{
			Type: publishingStrategyTypeForInfra(infraConfig),
		}
	}

	switch updated.Status.EndpointPublishingStrategy.Type {
	case operatorv1.LoadBalancerServiceStrategyType:
		if updated.Status.EndpointPublishingStrategy.LoadBalancer == nil {
			updated.Status.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
			}
		}
	case operatorv1.HostNetworkStrategyType:
		// No parameters.
	case operatorv1.PrivateStrategyType:
		// No parameters.
	}

	if err := r.client.Status().Update(context.TODO(), updated); err != nil {
		return fmt.Errorf("failed to update status of ingresscontroller %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: updated.Namespace, Name: updated.Name}, ci); err != nil {
		return fmt.Errorf("failed to get ingresscontroller %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	return nil
}

// enforceIngressFinalizer adds IngressControllerFinalizer to ingress if it doesn't exist.
func (r *reconciler) enforceIngressFinalizer(ingress *operatorv1.IngressController) error {
	if !slice.ContainsString(ingress.Finalizers, manifests.IngressControllerFinalizer) {
		ingress.Finalizers = append(ingress.Finalizers, manifests.IngressControllerFinalizer)
		if err := r.client.Update(context.TODO(), ingress); err != nil {
			return err
		}
		log.Info("enforced finalizer for ingress", "namespace", ingress.Namespace, "name", ingress.Name)
	}
	return nil
}

// ensureIngressDeleted tries to delete ingress, and if successful, will remove
// the finalizer.
func (r *reconciler) ensureIngressDeleted(ingress *operatorv1.IngressController) error {
	if err := r.finalizeLoadBalancerService(ingress); err != nil {
		return fmt.Errorf("failed to finalize load balancer service for %s: %v", ingress.Name, err)
	}
	log.Info("finalized load balancer service for ingress", "namespace", ingress.Namespace, "name", ingress.Name)

	// Delete the wildcard DNS record, and block ingresscontroller finalization
	// until the dnsrecord has been finalized.
	if err := r.deleteWildcardDNSRecord(ingress); err != nil {
		return fmt.Errorf("failed to delete wildcard dnsrecord: %v", err)
	}
	if record, err := r.currentWildcardDNSRecord(ingress); err != nil {
		return fmt.Errorf("failed to get current wildcard dnsrecord: %v", err)
	} else {
		if record != nil {
			log.V(1).Info("waiting for wildcard dnsrecord to be deleted", "dnsrecord", record)
			return nil
		}
	}

	if err := r.ensureRouterDeleted(ingress); err != nil {
		return fmt.Errorf("failed to delete deployment for ingress %s: %v", ingress.Name, err)
	}
	log.Info("deleted deployment for ingress", "namespace", ingress.Namespace, "name", ingress.Name)

	// Clean up the finalizer to allow the ingresscontroller to be deleted.
	if slice.ContainsString(ingress.Finalizers, manifests.IngressControllerFinalizer) {
		updated := ingress.DeepCopy()
		updated.Finalizers = slice.RemoveString(updated.Finalizers, manifests.IngressControllerFinalizer)
		if err := r.client.Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to remove finalizer from ingresscontroller %s: %v", ingress.Name, err)
		}
	}
	return nil
}

// ensureRouterNamespace ensures all the necessary scaffolding exists for
// routers generally, including a namespace and all RBAC setup.
func (r *reconciler) ensureRouterNamespace() error {
	cr := manifests.RouterClusterRole()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: cr.Name}, cr); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role %s: %v", cr.Name, err)
		}
		if err := r.client.Create(context.TODO(), cr); err != nil {
			return fmt.Errorf("failed to create router cluster role %s: %v", cr.Name, err)
		}
		log.Info("created router cluster role", "name", cr.Name)
	}

	ns := manifests.RouterNamespace()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router namespace %q: %v", ns.Name, err)
		}
		if err := r.client.Create(context.TODO(), ns); err != nil {
			return fmt.Errorf("failed to create router namespace %s: %v", ns.Name, err)
		}
		log.Info("created router namespace", "name", ns.Name)
	}

	sa := manifests.RouterServiceAccount()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: sa.Namespace, Name: sa.Name}, sa); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
		if err := r.client.Create(context.TODO(), sa); err != nil {
			return fmt.Errorf("failed to create router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
		log.Info("created router service account", "namespace", sa.Namespace, "name", sa.Name)
	}

	crb := manifests.RouterClusterRoleBinding()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: crb.Name}, crb); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role binding %s: %v", crb.Name, err)
		}
		if err := r.client.Create(context.TODO(), crb); err != nil {
			return fmt.Errorf("failed to create router cluster role binding %s: %v", crb.Name, err)
		}
		log.Info("created router cluster role binding", "name", crb.Name)
	}

	return nil
}

// ensureIngressController ensures all necessary router resources exist for a given ingresscontroller.
func (r *reconciler) ensureIngressController(ci *operatorv1.IngressController, ingressConfig *configv1.Ingress, dnsConfig *configv1.DNS, infraConfig *configv1.Infrastructure) error {
	errs := []error{}

	if deployment, err := r.ensureRouterDeployment(ci, infraConfig); err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure router deployment for %s: %v", ci.Name, err))
	} else {
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
		if lb, err := r.ensureLoadBalancerService(ci, deploymentRef, infraConfig); err != nil {
			errs = append(errs, fmt.Errorf("failed to ensure load balancer service for %s: %v", ci.Name, err))
		} else {
			lbService = lb
			if record, err := r.ensureWildcardDNSRecord(ci, lbService); err != nil {
				errs = append(errs, fmt.Errorf("failed to ensure wildcard dnsrecord for %s: %v", ci.Name, err))
			} else {
				wildcardRecord = record
			}
		}

		if internalSvc, err := r.ensureInternalIngressControllerService(ci, deploymentRef); err != nil {
			errs = append(errs, fmt.Errorf("failed to create internal router service for ingresscontroller %s: %v", ci.Name, err))
		} else if err := r.ensureMetricsIntegration(ci, internalSvc, deploymentRef); err != nil {
			errs = append(errs, fmt.Errorf("failed to integrate metrics with openshift-monitoring for ingresscontroller %s: %v", ci.Name, err))
		}

		operandEvents := &corev1.EventList{}
		if err := r.cache.List(context.TODO(), operandEvents, client.InNamespace("openshift-ingress")); err != nil {
			errs = append(errs, fmt.Errorf("failed to list events in namespace %q: %v", "openshift-ingress", err))
		}

		if err := r.syncIngressControllerStatus(ci, ingressConfig, deployment, lbService, operandEvents.Items, wildcardRecord, dnsConfig); err != nil {
			errs = append(errs, fmt.Errorf("failed to sync ingresscontroller status: %v", err))
		}
	}

	return utilerrors.NewAggregate(errs)
}

// ensureMetricsIntegration ensures that router prometheus metrics is integrated with openshift-monitoring for the given ingresscontroller.
func (r *reconciler) ensureMetricsIntegration(ci *operatorv1.IngressController, svc *corev1.Service, deploymentRef metav1.OwnerReference) error {
	statsSecret := manifests.RouterStatsSecret(ci)
	if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: statsSecret.Namespace, Name: statsSecret.Name}, statsSecret); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router stats secret %s/%s, %v", statsSecret.Namespace, statsSecret.Name, err)
		}

		statsSecret.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
		if err := r.client.Create(context.TODO(), statsSecret); err != nil {
			return fmt.Errorf("failed to create router stats secret %s/%s: %v", statsSecret.Namespace, statsSecret.Name, err)
		}
		log.Info("created router stats secret", "namespace", statsSecret.Namespace, "name", statsSecret.Name)
	}

	cr := manifests.MetricsClusterRole()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: cr.Name}, cr); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics cluster role %s: %v", cr.Name, err)
		}
		if err := r.client.Create(context.TODO(), cr); err != nil {
			return fmt.Errorf("failed to create router metrics cluster role %s: %v", cr.Name, err)
		}
		log.Info("created router metrics cluster role", "name", cr.Name)
	}

	crb := manifests.MetricsClusterRoleBinding()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: crb.Name}, crb); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics cluster role binding %s: %v", crb.Name, err)
		}
		if err := r.client.Create(context.TODO(), crb); err != nil {
			return fmt.Errorf("failed to create router metrics cluster role binding %s: %v", crb.Name, err)
		}
		log.Info("created router metrics cluster role binding", "name", crb.Name)
	}

	mr := manifests.MetricsRole()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: mr.Namespace, Name: mr.Name}, mr); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics role %s: %v", mr.Name, err)
		}
		if err := r.client.Create(context.TODO(), mr); err != nil {
			return fmt.Errorf("failed to create router metrics role %s: %v", mr.Name, err)
		}
		log.Info("created router metrics role", "name", mr.Name)
	}

	mrb := manifests.MetricsRoleBinding()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: mrb.Namespace, Name: mrb.Name}, mrb); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics role binding %s: %v", mrb.Name, err)
		}
		if err := r.client.Create(context.TODO(), mrb); err != nil {
			return fmt.Errorf("failed to create router metrics role binding %s: %v", mrb.Name, err)
		}
		log.Info("created router metrics role binding", "name", mrb.Name)
	}

	if _, err := r.ensureServiceMonitor(ci, svc, deploymentRef); err != nil {
		return fmt.Errorf("failed to ensure servicemonitor for %s: %v", ci.Name, err)
	}

	return nil
}

// IsStatusDomainSet checks whether status.domain of ingress is set.
func IsStatusDomainSet(ingress *operatorv1.IngressController) bool {
	if len(ingress.Status.Domain) == 0 {
		return false
	}
	return true
}
