package controller

import (
	"context"
	"fmt"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// ClusterIngressFinalizer is applied to all ClusterIngresses before they are
	// considered for processing; this ensures the operator has a chance to handle
	// all states. TODO: Make this generic and not tied to the "default" ingress.
	ClusterIngressFinalizer = "ingress.openshift.io/default-cluster-ingress"
)

var log = logf.Logger.WithName("controller")

// New creates the operator controller from configuration. This is the
// controller that handles all the logic for implementing ingress based on
// ClusterIngress resources.
//
// The controller will be pre-configured to watch for ClusterIngress resources
// in the manager namespace.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	reconciler := &reconciler{
		Config:   config,
		recorder: mgr.GetRecorder("operator-controller"),
	}
	c, err := controller.New("operator-controller", mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(&source.Kind{Type: &ingressv1alpha1.ClusterIngress{}}, &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}
	return c, nil
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	Client                 client.Client
	ManifestFactory        *manifests.Factory
	Namespace              string
	DNSManager             dns.Manager
	RouterImage            string
	OperatorReleaseVersion string
}

// reconciler handles the actual ingress reconciliation logic in response to
// events.
type reconciler struct {
	Config

	recorder record.EventRecorder
}

// Reconcile expects request to refer to a clusteringress in the operator
// namespace, and will do all the work to ensure the clusteringress is in the
// desired state.
func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	errs := []error{}
	result := reconcile.Result{}

	log.Info("reconciling", "request", request)

	// Get the current ingress state.
	ingress := &ingressv1alpha1.ClusterIngress{}
	if err := r.Client.Get(context.TODO(), request.NamespacedName, ingress); err != nil {
		if errors.IsNotFound(err) {
			// This means the ingress was already deleted/finalized and there are
			// stale queue entries (or something edge triggering from a related
			// resource that got deleted async).
			log.Info("clusteringress not found; reconciliation will be skipped", "request", request)
		} else {
			errs = append(errs, fmt.Errorf("failed to get clusteringress %q: %v", request, err))
		}
		ingress = nil
	}

	if ingress != nil {
		dnsConfig := &configv1.DNS{}
		if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
			errs = append(errs, fmt.Errorf("failed to get dns 'cluster': %v", err))
			dnsConfig = nil
		}
		infraConfig := &configv1.Infrastructure{}
		if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
			errs = append(errs, fmt.Errorf("failed to get infrastructure 'cluster': %v", err))
			infraConfig = nil
		}
		ingressConfig := &configv1.Ingress{}
		if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, ingressConfig); err != nil {
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

			if err := r.enforceEffectiveIngressDomain(ingress, ingressConfig); err != nil {
				errs = append(errs, fmt.Errorf("failed to enforce the effective ingress domain for clusteringress %s: %v", ingress.Name, err))
			} else if err := r.enforceEffectiveHighAvailability(ingress, infraConfig); err != nil {
				errs = append(errs, fmt.Errorf("failed to enforce the effective HA configuration for clusteringress %s: %v", ingress.Name, err))
			} else if err := r.ensureDefaultIngressController(); err != nil {
				errs = append(errs, fmt.Errorf("failed to ensure default ingress controller: %v", err))
			} else if ingress.DeletionTimestamp != nil {
				// Handle deletion.
				if err := r.ensureIngressDeleted(ingress, dnsConfig, infraConfig); err != nil {
					errs = append(errs, fmt.Errorf("failed to ensure ingress deletion: %v", err))
				}
			} else {
				// Handle everything else.
				if err := r.ensureClusterIngress(ingress, dnsConfig, infraConfig); err != nil {
					errs = append(errs, fmt.Errorf("failed to ensure clusteringress: %v", err))
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

// enforceEffectiveIngressDomain determines the effective ingress domain for the
// given clusteringress and ingress configuration and publishes it to the
// clusteringress's status.
func (r *reconciler) enforceEffectiveIngressDomain(ci *ingressv1alpha1.ClusterIngress, ingressConfig *configv1.Ingress) error {
	// The clusteringress's ingress domain is immutable, so if we have
	// published a domain to status, we must continue using it.
	if len(ci.Status.IngressDomain) > 0 {
		return nil
	}

	updated := ci.DeepCopy()
	switch {
	case ci.Spec.IngressDomain != nil:
		updated.Status.IngressDomain = *ci.Spec.IngressDomain
	default:
		updated.Status.IngressDomain = ingressConfig.Spec.Domain
	}
	// TODO Validate and check for conflicting claims.
	if err := r.Client.Status().Update(context.TODO(), updated); err != nil {
		return fmt.Errorf("failed to update status of clusteringress %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: updated.Namespace, Name: updated.Name}, ci); err != nil {
		return fmt.Errorf("failed to get clusteringress %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	return nil
}

// haTypeForInfra returns the appropriate HA type for the given infrastructure
// config.
func haTypeForInfra(infraConfig *configv1.Infrastructure) ingressv1alpha1.ClusterIngressHAType {
	switch infraConfig.Status.Platform {
	case configv1.AWSPlatform:
		return ingressv1alpha1.CloudClusterIngressHA
	}
	return ingressv1alpha1.UserDefinedClusterIngressHA
}

// enforceEffectiveHighAvailability determines the effective HA configuration
// for the given clusteringress and infrastructure config and publishes it to
// the clusteringress's status.
func (r *reconciler) enforceEffectiveHighAvailability(ci *ingressv1alpha1.ClusterIngress, infraConfig *configv1.Infrastructure) error {
	// The clusteringress's HA configuration is immutable, so if we have
	// published an HA type status, we must continue using it.
	if len(ci.Status.HighAvailability.Type) > 0 {
		return nil
	}

	updated := ci.DeepCopy()
	switch {
	case ci.Spec.HighAvailability != nil:
		updated.Status.HighAvailability.Type = ci.Spec.HighAvailability.Type
	default:
		updated.Status.HighAvailability.Type = haTypeForInfra(infraConfig)
	}
	if err := r.Client.Status().Update(context.TODO(), updated); err != nil {
		return fmt.Errorf("failed to update status of clusteringress %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: updated.Namespace, Name: updated.Name}, ci); err != nil {
		return fmt.Errorf("failed to get clusteringress %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	return nil
}

// ensureIngressDeleted tries to delete ingress, and if successful, will remove
// the finalizer.
func (r *reconciler) ensureIngressDeleted(ingress *ingressv1alpha1.ClusterIngress, dnsConfig *configv1.DNS, infraConfig *configv1.Infrastructure) error {
	if err := r.finalizeLoadBalancerService(ingress, dnsConfig); err != nil {
		return fmt.Errorf("failed to finalize load balancer service for %s: %v", ingress.Name, err)
	}
	log.Info("finalized load balancer service for ingress", "namespace", ingress.Namespace, "name", ingress.Name)

	if err := r.ensureRouterDeleted(ingress); err != nil {
		return fmt.Errorf("failed to delete deployment for ingress %s: %v", ingress.Name, err)
	}
	log.Info("deleted deployment for ingress", "namespace", ingress.Namespace, "name", ingress.Name)

	// Clean up the finalizer to allow the clusteringress to be deleted.
	updated := ingress.DeepCopy()
	if slice.ContainsString(ingress.Finalizers, ClusterIngressFinalizer) {
		updated.Finalizers = slice.RemoveString(updated.Finalizers, ClusterIngressFinalizer)
		if err := r.Client.Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to remove finalizer from clusteringress %s: %v", ingress.Name, err)
		}
	}
	return nil
}

// ensureRouterNamespace ensures all the necessary scaffolding exists for
// routers generally, including a namespace and all RBAC setup.
func (r *reconciler) ensureRouterNamespace() error {
	cr, err := r.ManifestFactory.RouterClusterRole()
	if err != nil {
		return fmt.Errorf("failed to build router cluster role: %v", err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: cr.Name}, cr); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role %s: %v", cr.Name, err)
		}
		if err := r.Client.Create(context.TODO(), cr); err != nil {
			return fmt.Errorf("failed to create router cluster role %s: %v", cr.Name, err)
		}
		log.Info("created router cluster role", "name", cr.Name)
	}

	ns, err := r.ManifestFactory.RouterNamespace()
	if err != nil {
		return fmt.Errorf("failed to build router namespace: %v", err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router namespace %q: %v", ns.Name, err)
		}
		if err := r.Client.Create(context.TODO(), ns); err != nil {
			return fmt.Errorf("failed to create router namespace %s: %v", ns.Name, err)
		}
		log.Info("created router namespace", "name", ns.Name)
	}

	sa, err := r.ManifestFactory.RouterServiceAccount()
	if err != nil {
		return fmt.Errorf("failed to build router service account: %v", err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: sa.Namespace, Name: sa.Name}, sa); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
		if err := r.Client.Create(context.TODO(), sa); err != nil {
			return fmt.Errorf("failed to create router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
		log.Info("created router service account", "namespace", sa.Namespace, "name", sa.Name)
	}

	crb, err := r.ManifestFactory.RouterClusterRoleBinding()
	if err != nil {
		return fmt.Errorf("failed to build router cluster role binding: %v", err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: crb.Name}, crb); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role binding %s: %v", crb.Name, err)
		}
		if err := r.Client.Create(context.TODO(), crb); err != nil {
			return fmt.Errorf("failed to create router cluster role binding %s: %v", crb.Name, err)
		}
		log.Info("created router cluster role binding", "name", crb.Name)
	}

	return nil
}

// ensureDefaultIngressController ensures that a default IngressController exists.
func (r *reconciler) ensureDefaultIngressController() error {
	ci := manifests.DefaultIngressController()
	err := r.Client.Create(context.TODO(), ci)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	} else if err == nil {
		log.Info("created default ingress controller", "namespace", ci.Namespace, "name", ci.Name)
	}
	return nil
}

// ensureClusterIngress ensures all necessary router resources exist for a given clusteringress.
func (r *reconciler) ensureClusterIngress(ci *ingressv1alpha1.ClusterIngress, dnsConfig *configv1.DNS, infraConfig *configv1.Infrastructure) error {
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

		if lbService, err := r.ensureLoadBalancerService(ci, deploymentRef, infraConfig); err != nil {
			errs = append(errs, fmt.Errorf("failed to ensure load balancer service for %s: %v", ci.Name, err))
		} else if lbService != nil {
			if err := r.ensureDNS(ci, lbService, dnsConfig); err != nil {
				errs = append(errs, fmt.Errorf("failed to ensure DNS for %s: %v", ci.Name, err))
			}
		}

		if internalSvc, err := r.ensureInternalRouterServiceForIngress(ci, deploymentRef); err != nil {
			errs = append(errs, fmt.Errorf("failed to create internal router service for clusteringress %s: %v", ci.Name, err))
		} else if err := r.ensureMetricsIntegration(ci, internalSvc, deploymentRef); err != nil {
			errs = append(errs, fmt.Errorf("failed to integrate metrics with openshift-monitoring for clusteringress %s: %v", ci.Name, err))
		}

		if err := r.syncClusterIngressStatus(deployment, ci); err != nil {
			errs = append(errs, fmt.Errorf("failed to sync status of clusteringress %s/%s: %v", deployment.Namespace, deployment.Name, err))
		}
	}

	return utilerrors.NewAggregate(errs)
}

// ensureMetricsIntegration ensures that router prometheus metrics is integrated with openshift-monitoring for the given clusteringress.
func (r *reconciler) ensureMetricsIntegration(ci *ingressv1alpha1.ClusterIngress, svc *corev1.Service, deploymentRef metav1.OwnerReference) error {
	statsSecret, err := r.ManifestFactory.RouterStatsSecret(ci)
	if err != nil {
		return fmt.Errorf("failed to build router stats secret: %v", err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: statsSecret.Namespace, Name: statsSecret.Name}, statsSecret); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router stats secret %s/%s, %v", statsSecret.Namespace, statsSecret.Name, err)
		}

		statsSecret.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
		if err := r.Client.Create(context.TODO(), statsSecret); err != nil {
			return fmt.Errorf("failed to create router stats secret %s/%s: %v", statsSecret.Namespace, statsSecret.Name, err)
		}
		log.Info("created router stats secret", "namespace", statsSecret.Namespace, "name", statsSecret.Name)
	}

	cr, err := r.ManifestFactory.MetricsClusterRole()
	if err != nil {
		return fmt.Errorf("failed to build router metrics cluster role: %v", err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: cr.Name}, cr); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics cluster role %s: %v", cr.Name, err)
		}
		if err := r.Client.Create(context.TODO(), cr); err != nil {
			return fmt.Errorf("failed to create router metrics cluster role %s: %v", cr.Name, err)
		}
		log.Info("created router metrics cluster role", "name", cr.Name)
	}

	crb, err := r.ManifestFactory.MetricsClusterRoleBinding()
	if err != nil {
		return fmt.Errorf("failed to build router metrics cluster role binding: %v", err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: crb.Name}, crb); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics cluster role binding %s: %v", crb.Name, err)
		}
		if err := r.Client.Create(context.TODO(), crb); err != nil {
			return fmt.Errorf("failed to create router metrics cluster role binding %s: %v", crb.Name, err)
		}
		log.Info("created router metrics cluster role binding", "name", crb.Name)
	}

	mr, err := r.ManifestFactory.MetricsRole()
	if err != nil {
		return fmt.Errorf("failed to build router metrics role: %v", err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: mr.Namespace, Name: mr.Name}, mr); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics role %s: %v", mr.Name, err)
		}
		if err := r.Client.Create(context.TODO(), mr); err != nil {
			return fmt.Errorf("failed to create router metrics role %s: %v", mr.Name, err)
		}
		log.Info("created router metrics role", "name", mr.Name)
	}

	mrb, err := r.ManifestFactory.MetricsRoleBinding()
	if err != nil {
		return fmt.Errorf("failed to build router metrics role binding: %v", err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: mrb.Namespace, Name: mrb.Name}, mrb); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics role binding %s: %v", mrb.Name, err)
		}
		if err := r.Client.Create(context.TODO(), mrb); err != nil {
			return fmt.Errorf("failed to create router metrics role binding %s: %v", mrb.Name, err)
		}
		log.Info("created router metrics role binding", "name", mrb.Name)
	}

	if _, err := r.ensureServiceMonitor(ci, svc, deploymentRef); err != nil {
		return fmt.Errorf("failed to ensure servicemonitor for %s: %v", ci.Name, err)
	}

	return nil
}

// syncClusterIngressStatus updates the status for a given clusteringress.
func (r *reconciler) syncClusterIngressStatus(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) error {
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return fmt.Errorf("router deployment %s/%s has invalid spec.selector: %v", deployment.Namespace, deployment.Name, err)
	}

	if ci.Status.Replicas == deployment.Status.AvailableReplicas &&
		ci.Status.Selector == selector.String() {
		return nil
	}

	updated := ci.DeepCopy()
	updated.Status.Replicas = deployment.Status.AvailableReplicas
	updated.Status.Selector = selector.String()
	if err := r.Client.Status().Update(context.TODO(), updated); err != nil {
		return fmt.Errorf("failed to update status of clusteringress %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: updated.Namespace, Name: updated.Name}, ci); err != nil {
		return fmt.Errorf("failed to get clusteringress %s/%s: %v", updated.Namespace, updated.Name, err)
	}

	return nil
}

// ensureInternalRouterServiceForIngress ensures that an internal service exists
// for a given ClusterIngress.
func (r *reconciler) ensureInternalRouterServiceForIngress(ci *ingressv1alpha1.ClusterIngress, deploymentRef metav1.OwnerReference) (*corev1.Service, error) {
	svc, err := r.ManifestFactory.RouterServiceInternal(ci)
	if err != nil {
		return nil, fmt.Errorf("failed to build router service: %v", err)
	}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, svc); err != nil {
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get router service %s/%s, %v", svc.Namespace, svc.Name, err)
		}

		svc.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
		if err := r.Client.Create(context.TODO(), svc); err != nil {
			return nil, fmt.Errorf("failed to create router service %s/%s: %v", svc.Namespace, svc.Name, err)
		}
		log.Info("created router service", "namespace", svc.Namespace, "name", svc.Name)
	}

	return svc, nil
}
