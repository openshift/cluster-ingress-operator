package controller

import (
	"context"
	"fmt"
	"time"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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

var log = logf.Logger.WithName("cluster-ingress-controller")

// New creates the operator controller from configuration. This is the
// controller that handles all the logic for implementing ingress based on
// ClusterIngress resources.
//
// The controller will be pre-configured to watch for ClusterIngress resources
// in the manager namespace.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	reconciler := &reconciler{
		Config: config,
	}
	c, err := controller.New("operator-controller", mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	err = c.Watch(&source.Kind{Type: &ingressv1alpha1.ClusterIngress{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	Client          client.Client
	ManifestFactory *manifests.Factory
	Namespace       string
	DNSManager      dns.Manager
	RouterImage     string
}

// reconciler handles the actual ingress reconciliation logic in response to
// events.
type reconciler struct {
	Config
}

// Reconcile expects request to refer to a clusteringress in the operator
// namespace, and will do all the work to ensure the clusteringress is in the
// desired state.
func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	result := reconcile.Result{}

	// TODO: Should this be another controller?
	defer func() {
		err := r.syncOperatorStatus()
		if err != nil {
			log.Error(err, "failed to sync operator status")
		}
	}()

	log.Info("reconciling", "request", request)

	// Get the current ingress state.
	ingress := &ingressv1alpha1.ClusterIngress{}
	err := r.Client.Get(context.TODO(), request.NamespacedName, ingress)
	if err != nil {
		if errors.IsNotFound(err) {
			// This means the ingress was already deleted/finalized and there are
			// stale queue entries (or something edge triggering from a related
			// resource that got deleted async).
			log.Info("clusteringress not found; reconciliation will be skipped", "request", request)
		} else {
			// Try again later.
			log.Error(err, "failed to get clusteringress", "request", request)
			result.RequeueAfter = 10 * time.Second
		}
		ingress = nil
	}

	caSecret, err := r.ensureRouterCACertificateSecret()
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to ensure CA secret: %v", err)
	}

	// Collect errors as we go.
	errs := []error{}

	// Only reconcile the ingress itself if it exists.
	if ingress != nil {
		dnsConfig := &configv1.DNS{}
		if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig); err != nil {
			log.Error(err, "failed to get dns 'cluster'")
			dnsConfig = nil
		}
		infraConfig := &configv1.Infrastructure{}
		if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
			log.Error(err, "failed to get infrastructure 'cluster'")
			infraConfig = nil
		}
		ingressConfig := &configv1.Ingress{}
		if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, ingressConfig); err != nil {
			log.Error(err, "failed to get ingress 'cluster'")
			ingressConfig = nil
		}

		if dnsConfig == nil || infraConfig == nil || ingressConfig == nil {
			// For now, if the cluster configs are unavailable, defer reconciliation
			// because weaving conditionals everywhere to deal with various nil states
			// is too complicated. It doesn't seem too risky to rely on the invariant
			// of the cluster config being available.
			result.RequeueAfter = 10 * time.Second
		} else {
			// Ensure we have all the necessary scaffolding on which to place router
			// instances.
			err = r.ensureRouterNamespace()
			if err != nil {
				errs = append(errs, err)
				return result, utilerrors.NewAggregate(errs)
			}

			if err := r.enforceEffectiveIngressDomain(ingress, ingressConfig); err != nil {
				errs = append(errs, fmt.Errorf("failed to enforce the effective ingress domain for clusteringress %s: %v", ingress.Name, err))
			} else if ingress.DeletionTimestamp != nil {
				// Handle deletion.
				if err := r.ensureIngressDeleted(ingress, dnsConfig, infraConfig); err != nil {
					errs = append(errs, fmt.Errorf("failed to ensure ingress deletion: %v", err))
				}
			} else {
				// Handle everything else.
				if err := r.ensureClusterIngress(ingress, caSecret, infraConfig, dnsConfig, &result); err != nil {
					errs = append(errs, fmt.Errorf("failed to ensure clusteringress: %v", err))
				}
			}
		}
	}

	// TODO: This should be in a different reconciler as it's independent of an
	// individual ingress. We only really need to trigger this when a
	// clusteringress is added or deleted...
	// Find all clusteringresses to compute CA states.
	ingresses := &ingressv1alpha1.ClusterIngressList{}
	err = r.Client.List(context.TODO(), &client.ListOptions{Namespace: r.Namespace}, ingresses)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list clusteringresses in namespace %s: %v", r.Namespace, err)
	}
	errs = append(errs, r.ensureRouterCAConfigMap(caSecret, ingresses.Items))

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

	switch {
	case ci.Spec.IngressDomain != nil:
		ci.Status.IngressDomain = *ci.Spec.IngressDomain
	default:
		ci.Status.IngressDomain = ingressConfig.Spec.Domain
	}
	// TODO Validate and check for conflicting claims.
	if err := r.Client.Status().Update(context.TODO(), ci); err != nil {
		return fmt.Errorf("failed to update status of clusteringress %s/%s: %v", ci.Namespace, ci.Name, err)
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

// effectiveHighAvailability returns the HA configuration as derived from the
// given clusteringress and infrastructure config.
func effectiveHighAvailability(ci *ingressv1alpha1.ClusterIngress, infraConfig *configv1.Infrastructure) (ha ingressv1alpha1.ClusterIngressHighAvailability) {
	switch {
	case ci.Spec.HighAvailability != nil:
		ha.Type = ci.Spec.HighAvailability.Type
	default:
		ha.Type = haTypeForInfra(infraConfig)
	}
	return
}

// ensureIngressDeleted tries to delete ingress, and if successful, will remove
// the finalizer.
func (r *reconciler) ensureIngressDeleted(ingress *ingressv1alpha1.ClusterIngress, dnsConfig *configv1.DNS, infraConfig *configv1.Infrastructure) error {
	ha := effectiveHighAvailability(ingress, infraConfig)

	err := r.finalizeLoadBalancerService(ingress, dnsConfig, ha)
	if err != nil {
		return fmt.Errorf("failed to finalize load balancer service for %s: %v", ingress.Name, err)
	}
	log.Info("finalized load balancer service for ingress", "namespace", ingress.Namespace, "name", ingress.Name)

	err = r.ensureRouterDeleted(ingress)
	if err != nil {
		return fmt.Errorf("failed to delete deployment for ingress %s: %v", ingress.Name, err)
	}
	log.Info("deleted deployment for ingress", "namespace", ingress.Namespace, "name", ingress.Name)

	// Clean up the finalizer to allow the clusteringress to be deleted.
	updated := ingress.DeepCopy()
	if slice.ContainsString(ingress.Finalizers, ClusterIngressFinalizer) {
		updated.Finalizers = slice.RemoveString(updated.Finalizers, ClusterIngressFinalizer)
		err = r.Client.Update(context.TODO(), updated)
		if err != nil {
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
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: cr.Name}, cr)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role %s: %v", cr.Name, err)
		}
		err = r.Client.Create(context.TODO(), cr)
		if err == nil {
			log.Info("created router cluster role", "name", cr.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router cluster role %s: %v", cr.Name, err)
		}
	}

	ns, err := r.ManifestFactory.RouterNamespace()
	if err != nil {
		return fmt.Errorf("failed to build router namespace: %v", err)
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router namespace %q: %v", ns.Name, err)
		}
		err = r.Client.Create(context.TODO(), ns)
		if err == nil {
			log.Info("created router namespace", "name", ns.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router namespace %s: %v", ns.Name, err)
		}
	}

	sa, err := r.ManifestFactory.RouterServiceAccount()
	if err != nil {
		return fmt.Errorf("failed to build router service account: %v", err)
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: sa.Namespace, Name: sa.Name}, sa)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
		err = r.Client.Create(context.TODO(), sa)
		if err == nil {
			log.Info("created router service account", "namespace", sa.Namespace, "name", sa.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
	}

	crb, err := r.ManifestFactory.RouterClusterRoleBinding()
	if err != nil {
		return fmt.Errorf("failed to build router cluster role binding: %v", err)
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: crb.Name}, crb)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role binding %s: %v", crb.Name, err)
		}
		err = r.Client.Create(context.TODO(), crb)
		if err == nil {
			log.Info("created router cluster role binding", "name", crb.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router cluster role binding %s: %v", crb.Name, err)
		}
	}

	return nil
}

// ensureClusterIngress ensures all necessary router resources exist for a
// given clusteringress.
func (r *reconciler) ensureClusterIngress(ci *ingressv1alpha1.ClusterIngress, caSecret *corev1.Secret, infraConfig *configv1.Infrastructure, dnsConfig *configv1.DNS, result *reconcile.Result) error {
	errs := []error{}

	ha := effectiveHighAvailability(ci, infraConfig)

	deployment, err := r.ensureRouterDeployment(ci, infraConfig, ha)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure deployment: %v", err))
		return utilerrors.NewAggregate(errs)
	}

	if lbService, err := r.ensureLoadBalancerService(ci, deployment, infraConfig, ha); err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure load balancer service: %v", err))
	} else {
		if err = r.ensureDNS(ci, lbService, dnsConfig, ha); err != nil {
			errs = append(errs, fmt.Errorf("failed to ensure DNS: %v", err))
		}
	}

	if err := r.ensureDefaultCertificateForIngress(caSecret, deployment, ci); err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure default certificate: %v", err))
	}

	if internalSvc, err := r.ensureInternalRouterServiceForIngress(ci, deployment); err != nil {
		errs = append(errs, fmt.Errorf("failed to create internal router service: %v", err))
	} else {
		if err := r.ensureMetricsIntegration(ci, internalSvc, deployment); err != nil {
			errs = append(errs, fmt.Errorf("failed to integrate metrics: %v", err))
		} else {
			if _, err = r.ensureServiceMonitor(ci, internalSvc, deployment); err != nil {
				if meta.IsNoMatchError(err) {
					log.Error(err, "failed to ensure servicemonitor because of missing type; will retry")
					result.RequeueAfter = 10 * time.Second
				} else {
					errs = append(errs, fmt.Errorf("failed to ensure servicemonitor: %v", err))
				}
			}
		}
	}

	if err := r.syncClusterIngressStatus(deployment, ci, ha); err != nil {
		errs = append(errs, fmt.Errorf("failed to sync status: %v", err))
	}

	return utilerrors.NewAggregate(errs)
}

// ensureMetricsIntegration ensures that router prometheus metrics is integrated with openshift-monitoring for the given clusteringress.
func (r *reconciler) ensureMetricsIntegration(ci *ingressv1alpha1.ClusterIngress, svc *corev1.Service, deployment *appsv1.Deployment) error {
	if deployment == nil {
		return nil
	}

	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
		Controller: &trueVar,
	}

	statsSecret, err := r.ManifestFactory.RouterStatsSecret(ci)
	if err != nil {
		return fmt.Errorf("failed to build router stats secret: %v", err)
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: statsSecret.Namespace, Name: statsSecret.Name}, statsSecret)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router stats secret %s/%s, %v", statsSecret.Namespace, statsSecret.Name, err)
		}

		statsSecret.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
		err = r.Client.Create(context.TODO(), statsSecret)
		if err == nil {
			log.Info("created router stats secret", "namespace", statsSecret.Namespace, "name", statsSecret.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router stats secret %s/%s: %v", statsSecret.Namespace, statsSecret.Name, err)
		}
	}

	cr, err := r.ManifestFactory.MetricsClusterRole()
	if err != nil {
		return fmt.Errorf("failed to build router metrics cluster role: %v", err)
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: cr.Name}, cr)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics cluster role %s: %v", cr.Name, err)
		}
		err = r.Client.Create(context.TODO(), cr)
		if err == nil {
			log.Info("created router metrics cluster role", "name", cr.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router metrics cluster role %s: %v", cr.Name, err)
		}
	}

	crb, err := r.ManifestFactory.MetricsClusterRoleBinding()
	if err != nil {
		return fmt.Errorf("failed to build router metrics cluster role binding: %v", err)
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: crb.Name}, crb)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics cluster role binding %s: %v", crb.Name, err)
		}
		err = r.Client.Create(context.TODO(), crb)
		if err == nil {
			log.Info("created router metrics cluster role binding", "name", crb.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router metrics cluster role binding %s: %v", crb.Name, err)
		}
	}

	mr, err := r.ManifestFactory.MetricsRole()
	if err != nil {
		return fmt.Errorf("failed to build router metrics role: %v", err)
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: mr.Namespace, Name: mr.Name}, mr)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics role %s: %v", mr.Name, err)
		}
		err = r.Client.Create(context.TODO(), mr)
		if err == nil {
			log.Info("created router metrics role", "name", mr.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router metrics role %s: %v", mr.Name, err)
		}
	}

	mrb, err := r.ManifestFactory.MetricsRoleBinding()
	if err != nil {
		return fmt.Errorf("failed to build router metrics role binding: %v", err)
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: mrb.Namespace, Name: mrb.Name}, mrb)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router metrics role binding %s: %v", mrb.Name, err)
		}
		err = r.Client.Create(context.TODO(), mrb)
		if err == nil {
			log.Info("created router metrics role binding", "name", mrb.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router metrics role binding %s: %v", mrb.Name, err)
		}
	}

	return nil
}

// syncClusterIngressStatus updates the status for a given clusteringress.
func (r *reconciler) syncClusterIngressStatus(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress, ha ingressv1alpha1.ClusterIngressHighAvailability) error {
	if deployment == nil {
		return nil
	}

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return fmt.Errorf("router deployment %s/%s has invalid spec.selector: %v", deployment.Namespace, deployment.Name, err)
	}

	if ci.Status.Replicas == deployment.Status.AvailableReplicas &&
		ci.Status.Selector == selector.String() &&
		ci.Status.HighAvailability == ha {
		return nil
	}

	ci.Status.Replicas = deployment.Status.AvailableReplicas
	ci.Status.Selector = selector.String()
	ci.Status.HighAvailability = ha
	if err := r.Client.Status().Update(context.TODO(), ci); err != nil {
		return fmt.Errorf("failed to update status of clusteringress %s/%s: %v", ci.Namespace, ci.Name, err)
	}

	return nil
}

// ensureInternalRouterServiceForIngress ensures that an internal service exists
// for a given ClusterIngress.
func (r *reconciler) ensureInternalRouterServiceForIngress(ci *ingressv1alpha1.ClusterIngress, deployment *appsv1.Deployment) (*corev1.Service, error) {
	if deployment == nil {
		return nil, nil
	}

	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
		Controller: &trueVar,
	}

	svc, err := r.ManifestFactory.RouterServiceInternal(ci)
	if err != nil {
		return nil, fmt.Errorf("failed to build router service: %v", err)
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, svc)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get router service %s/%s, %v", svc.Namespace, svc.Name, err)
		}

		svc.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
		err = r.Client.Create(context.TODO(), svc)
		if err == nil {
			log.Info("created router service", "namespace", svc.Namespace, "name", svc.Name)
		} else if !errors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create router service %s/%s: %v", svc.Namespace, svc.Name, err)
		}
	}

	return svc, nil
}
