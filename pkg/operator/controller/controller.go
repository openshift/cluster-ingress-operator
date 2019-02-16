package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
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
	result, err := r.reconcile(request)
	if err != nil {
		// TODO: We're not setting up the controller-runtime logger, so errors we
		// bubble out are not being logged. For now, log them here, but we need to
		// redo our logging and wire up the controller-runtime logger because who
		// knows what else is being eaten.
		logrus.Errorf("error: %v", err)
	}
	return result, err
}

func (r *reconciler) reconcile(request reconcile.Request) (reconcile.Result, error) {
	result := reconcile.Result{}

	// TODO: Should this be another controller?
	defer func() {
		err := r.syncOperatorStatus()
		if err != nil {
			logrus.Infof("failed to sync operator status: %v", err)
			result.Requeue = true
		}
	}()

	logrus.Infof("reconciling request: %v", request)

	// Get the current ingress state.
	ingress := &ingressv1alpha1.ClusterIngress{}
	err := r.Client.Get(context.TODO(), request.NamespacedName, ingress)
	if err != nil {
		if errors.IsNotFound(err) {
			// This means the ingress was already deleted/finalized and there are
			// stale queue entries (or something edge triggering from a related
			// resource that got deleted async).
			logrus.Infof("clusteringress %q not found and reconciliation will be skipped", request)
		} else {
			// Try again later.
			logrus.Errorf("failed to get clusteringress %q: %v", request, err)
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
			logrus.Errorf("failed to get dns 'cluster': %v", err)
			dnsConfig = nil
		}
		infraConfig := &configv1.Infrastructure{}
		if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
			logrus.Errorf("failed to get infrastructure 'cluster': %v", err)
			infraConfig = nil
		}
		if dnsConfig == nil || infraConfig == nil {
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

			if ingress.DeletionTimestamp != nil {
				// Handle deletion.
				if err := r.ensureIngressDeleted(ingress, dnsConfig); err != nil {
					errs = append(errs, fmt.Errorf("failed to ensure ingress deletion: %v", err))
				}
			} else {
				// Handle everything else.
				if err := r.ensureRouterForIngress(ingress, caSecret, infraConfig, dnsConfig, &result); err != nil {
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

// ensureIngressDeleted tries to delete ingress, and if successful, will remove
// the finalizer.
func (r *reconciler) ensureIngressDeleted(ingress *ingressv1alpha1.ClusterIngress, dnsConfig *configv1.DNS) error {
	// TODO: This should also be tied to the HA type.
	err := r.finalizeLoadBalancerService(ingress, dnsConfig)
	if err != nil {
		return fmt.Errorf("failed to finalize load balancer service for %s: %v", ingress.Name, err)
	}
	logrus.Infof("finalized load balancer service for ingress %s", ingress.Name)

	err = r.ensureRouterDeleted(ingress)
	if err != nil {
		return fmt.Errorf("failed to delete deployment for ingress %s: %v", ingress.Name, err)
	}
	logrus.Infof("deleted deployment for ingress %s", ingress.Name)

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
			logrus.Infof("created router cluster role %s", cr.Name)
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
			logrus.Infof("created router namespace %s", ns.Name)
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
			logrus.Infof("created router service account %s/%s", sa.Namespace, sa.Name)
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
			logrus.Infof("created router cluster role binding %s", crb.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router cluster role binding %s: %v", crb.Name, err)
		}
	}

	return nil
}

// ensureRouterForIngress ensures all necessary router resources exist for a
// given clusteringress.
func (r *reconciler) ensureRouterForIngress(ci *ingressv1alpha1.ClusterIngress, caSecret *corev1.Secret, infraConfig *configv1.Infrastructure, dnsConfig *configv1.DNS, result *reconcile.Result) error {
	expected, err := r.ManifestFactory.RouterDeployment(ci)
	if err != nil {
		return fmt.Errorf("failed to build router deployment: %v", err)
	}
	current := expected.DeepCopy()
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: expected.Namespace, Name: expected.Name}, current)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router deployment %s/%s, %v", expected.Namespace, expected.Name, err)
		}

		err = r.Client.Create(context.TODO(), current)
		if err == nil {
			logrus.Infof("created router deployment %s/%s", current.Namespace, current.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router deployment %s/%s: %v", current.Namespace, current.Name, err)
		}
	}

	if changed, updated := deploymentConfigChanged(current, expected); changed {
		err = r.Client.Update(context.TODO(), updated)
		if err == nil {
			logrus.Infof("updated router deployment %s/%s", updated.Namespace, updated.Name)
			current = updated
		} else {
			return fmt.Errorf("failed to update router deployment %s/%s, %v", updated.Namespace, updated.Name, err)
		}
	}

	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       current.Name,
		UID:        current.UID,
		Controller: &trueVar,
	}

	errs := []error{}
	lbService, err := r.ensureLoadBalancerService(ci, current, infraConfig)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure load balancer service for %s: %v", ci.Name, err))
	}
	if err = r.ensureDNS(ci, lbService, dnsConfig); err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure DNS for %s: %v", ci.Name, err))
	}

	internalSvc, err := r.ensureInternalRouterServiceForIngress(ci, deploymentRef)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to create internal router service for clusteringress %s: %v", ci.Name, err))
		return utilerrors.NewAggregate(errs)
	}

	if err := r.ensureDefaultCertificateForIngress(caSecret, current, ci); err != nil {
		errs = append(errs, err)
		return utilerrors.NewAggregate(errs)
	}

	if err := r.ensureMetricsIntegration(ci, internalSvc, deploymentRef); err != nil {
		errs = append(errs, fmt.Errorf("failed to integrate metrics with openshift-monitoring for clusteringress %s: %v", ci.Name, err))
		return utilerrors.NewAggregate(errs)
	}

	_, err = r.ensureServiceMonitor(ci, internalSvc, current)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to ensure servicemonitor for %s: %v", ci.Name, err))
		if meta.IsNoMatchError(err) {
			result.RequeueAfter = 10 * time.Second
		}
	}

	if err := r.syncClusterIngressStatus(current, ci); err != nil {
		errs = append(errs, fmt.Errorf("failed to update status of clusteringress %s/%s: %v", current.Namespace, current.Name, err))
		return utilerrors.NewAggregate(errs)
	}

	return nil
}

// ensureMetricsIntegration ensures that router prometheus metrics is integrated with openshift-monitoring for the given clusteringress.
func (r *reconciler) ensureMetricsIntegration(ci *ingressv1alpha1.ClusterIngress, svc *corev1.Service, deploymentRef metav1.OwnerReference) error {
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
			logrus.Infof("created router stats secret %s/%s", statsSecret.Namespace, statsSecret.Name)
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
			logrus.Infof("created router metrics cluster role %s", cr.Name)
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
			logrus.Infof("created router metrics cluster role binding %s", crb.Name)
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
			logrus.Infof("created router metrics role %s", mr.Name)
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
			logrus.Infof("created router metrics role binding %s", mrb.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router metrics role binding %s: %v", mrb.Name, err)
		}
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

	ci.Status.Replicas = deployment.Status.AvailableReplicas
	ci.Status.Selector = selector.String()
	if err := r.Client.Status().Update(context.TODO(), ci); err != nil {
		return fmt.Errorf("failed to update status of clusteringress %s/%s: %v", ci.Namespace, ci.Name, err)
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
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, svc)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get router service %s/%s, %v", svc.Namespace, svc.Name, err)
		}

		svc.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
		err = r.Client.Create(context.TODO(), svc)
		if err == nil {
			logrus.Infof("created router service %s/%s", svc.Namespace, svc.Name)
		} else if !errors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create router service %s/%s: %v", svc.Namespace, svc.Name, err)
		}
	}

	return svc, nil
}

// ensureRouterDeleted ensures that any router resources associated with the
// clusteringress are deleted.
func (r *reconciler) ensureRouterDeleted(ci *ingressv1alpha1.ClusterIngress) error {
	deployment, err := r.ManifestFactory.RouterDeployment(ci)
	if err != nil {
		return fmt.Errorf("failed to build router deployment for deletion: %v", err)
	}

	err = r.Client.Delete(context.TODO(), deployment)
	if !errors.IsNotFound(err) {
		return err
	}
	return nil
}

// deploymentConfigChanged checks if current config matches the expected config
// for the cluster ingress deployment and if not returns the updated config.
func deploymentConfigChanged(current, expected *appsv1.Deployment) (bool, *appsv1.Deployment) {
	// As per an offline conversation, this checks only the secret name
	// for now but can be updated to a `reflect.DeepEqual` if needed.
	if current.Spec.Template.Spec.Volumes[0].Secret.SecretName == expected.Spec.Template.Spec.Volumes[0].Secret.SecretName &&
		current.Spec.Replicas != nil &&
		*current.Spec.Replicas == *expected.Spec.Replicas {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec.Template.Spec.Volumes[0].Secret.SecretName = expected.Spec.Template.Spec.Volumes[0].Secret.SecretName
	replicas := int32(1)
	if expected.Spec.Replicas != nil {
		replicas = *expected.Spec.Replicas
	}
	updated.Spec.Replicas = &replicas
	return true, updated
}
