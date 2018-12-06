package clusteringress

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
	// ClusterIngressFinalizer is applied to all ClusterIngress resources before
	// they are considered for processing; this ensures the operator has a chance
	// to handle all states.
	// TODO: Make this generic and not tied to the "default" ingress.
	ClusterIngressFinalizer = "ingress.openshift.io/default-cluster-ingress"
)

// New creates a ClusterIngress controller and registers it with manager.
func New(mgr manager.Manager, reconciler *Reconciler) (controller.Controller, error) {
	c, err := controller.New("clusteringress-controller", mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	// Watch for changes to primary resource ClusterIngress
	err = c.Watch(&source.Kind{Type: &ingressv1alpha1.ClusterIngress{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return nil, err
	}
	return c, nil
}

type Reconciler struct {
	Client          client.Client
	InstallConfig   *util.InstallConfig
	ManifestFactory *manifests.Factory
	Namespace       string
	DNSManager      dns.Manager
}

func (h *Reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// TODO: Should this be another controller?
	defer func() {
		err := h.syncOperatorStatus()
		if err != nil {
			logrus.Infof("failed to sync operator status: %v", err)
		}
	}()

	logrus.Infof("reconciling request: %#v", request)

	result, err := h.reconcile()
	if err != nil {
		logrus.Errorf("failed to reconcile: %v", err)
	}
	return result, err
}

// Reconcile performs a full reconciliation loop for ingress, including
// generalized setup and handling of all clusteringress resources in the
// operator namespace.
func (h *Reconciler) reconcile() (reconcile.Result, error) {
	// Ensure we have all the necessary scaffolding on which to place router
	// instances.
	err := h.ensureRouterNamespace()
	if err != nil {
		return reconcile.Result{}, err
	}

	// Find all clusteringresses.
	ingresses := &ingressv1alpha1.ClusterIngressList{}
	err = h.Client.List(context.TODO(), &client.ListOptions{Namespace: h.Namespace}, ingresses)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list clusteringresses in namespace %s: %v", h.Namespace, err)
	}

	// Reconcile all the ingresses.
	errors := []error{}
	for _, ingress := range ingresses.Items {
		logrus.Infof("reconciling clusteringress %#v", ingress)
		// Handle deleted ingress.
		// TODO: Assert/ensure that the ingress has a finalizer so we can reliably detect
		// deletion.
		if ingress.DeletionTimestamp != nil {
			// Destroy any router associated with the clusteringress.
			err := h.ensureRouterDeleted(&ingress)
			if err != nil {
				errors = append(errors, fmt.Errorf("failed to delete clusteringress %s/%s: %v", ingress.Namespace, ingress.Name, err))
				continue
			}
			logrus.Infof("deleted router for clusteringress %s/%s", ingress.Namespace, ingress.Name)
			// Clean up the finalizer to allow the clusteringress to be deleted.
			if slice.ContainsString(ingress.Finalizers, ClusterIngressFinalizer) {
				ingress.Finalizers = slice.RemoveString(ingress.Finalizers, ClusterIngressFinalizer)
				err = h.Client.Update(context.TODO(), &ingress)
				if err != nil {
					errors = append(errors, fmt.Errorf("failed to remove finalizer from clusteringress %s/%s: %v", ingress.Namespace, ingress.Name, err))
				}
			}
			continue
		}

		// Handle active ingress.
		err := h.ensureRouterForIngress(&ingress)
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to ensure clusteringress %s/%s: %v", ingress.Namespace, ingress.Name, err))
		}
	}
	return reconcile.Result{}, utilerrors.NewAggregate(errors)
}

// ensureRouterNamespace ensures all the necessary scaffolding exists for
// routers generally, including a namespace and all RBAC setup.
func (h *Reconciler) ensureRouterNamespace() error {
	cr, err := h.ManifestFactory.RouterClusterRole()
	if err != nil {
		return fmt.Errorf("failed to build router cluster role: %v", err)
	}
	err = h.Client.Get(context.TODO(), types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, cr)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role %s: %v", cr.Name, err)
		}
		err = h.Client.Create(context.TODO(), cr)
		if err == nil {
			logrus.Infof("created router cluster role %s", cr.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router cluster role %s: %v", cr.Name, err)
		}
	}

	ns, err := h.ManifestFactory.RouterNamespace()
	if err != nil {
		return fmt.Errorf("failed to build router namespace: %v", err)
	}
	err = h.Client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router namespace %q: %v", ns.Name, err)
		}
		err = h.Client.Create(context.TODO(), ns)
		if err == nil {
			logrus.Infof("created router namespace %s", ns.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router namespace %s: %v", ns.Name, err)
		}
	}

	sa, err := h.ManifestFactory.RouterServiceAccount()
	if err != nil {
		return fmt.Errorf("failed to build router service account: %v", err)
	}
	err = h.Client.Get(context.TODO(), types.NamespacedName{Namespace: sa.Namespace, Name: sa.Name}, sa)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
		err = h.Client.Create(context.TODO(), sa)
		if err == nil {
			logrus.Infof("created router service account %s/%s", sa.Namespace, sa.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
	}

	crb, err := h.ManifestFactory.RouterClusterRoleBinding()
	if err != nil {
		return fmt.Errorf("failed to build router cluster role binding: %v", err)
	}
	err = h.Client.Get(context.TODO(), types.NamespacedName{Name: crb.Name}, crb)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role binding %s: %v", crb.Name, err)
		}
		err = h.Client.Create(context.TODO(), crb)
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
func (h *Reconciler) ensureRouterForIngress(ci *ingressv1alpha1.ClusterIngress) error {
	expected, err := h.ManifestFactory.RouterDeployment(ci)
	if err != nil {
		return fmt.Errorf("failed to build router deployment: %v", err)
	}
	current := expected.DeepCopy()
	err = h.Client.Get(context.TODO(), types.NamespacedName{Namespace: expected.Namespace, Name: expected.Name}, current)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router deployment %s/%s, %v", expected.Namespace, expected.Name, err)
		}

		err = h.Client.Create(context.TODO(), current)
		if err == nil {
			logrus.Infof("created router deployment %s/%s", current.Namespace, current.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router deployment %s/%s: %v", current.Namespace, current.Name, err)
		}
	}

	if changed, updated := deploymentConfigChanged(current, expected); changed {
		err = h.Client.Update(context.TODO(), updated)
		if err == nil {
			logrus.Infof("updated router deployment %s/%s", updated.Namespace, updated.Name)
			current = updated
		} else {
			return fmt.Errorf("failed to update router deployment %s/%s, %v", updated.Namespace, updated.Name, err)
		}
	}

	if ci.Spec.HighAvailability != nil {
		switch ci.Spec.HighAvailability.Type {
		case ingressv1alpha1.CloudClusterIngressHA:
			service, err := h.ManifestFactory.RouterServiceCloud(ci)
			if err != nil {
				return fmt.Errorf("failed to build router service: %v", err)
			}
			err = h.Client.Get(context.TODO(), types.NamespacedName{Namespace: service.Namespace, Name: service.Name}, service)
			if err != nil {
				if !errors.IsNotFound(err) {
					return fmt.Errorf("failed to get router service %s/%s, %v", service.Namespace, service.Name, err)
				}
				// Service doesn't exist; try to create it.
				trueVar := true
				deploymentRef := metav1.OwnerReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       current.Name,
					UID:        current.UID,
					Controller: &trueVar,
				}
				service.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
				err = h.Client.Create(context.TODO(), service)
				if err == nil {
					logrus.Infof("created router service %s/%s", service.Namespace, service.Name)
				} else if !errors.IsAlreadyExists(err) {
					return fmt.Errorf("failed to create router service %s/%s: %v", service.Namespace, service.Name, err)
				}
			}
			if ci.Spec.IngressDomain != nil {
				err = h.ensureDNSForLoadBalancer(ci, service)
				if err != nil {
					return fmt.Errorf("failed to ensure DNS for router service %s/%s: %v", service.Namespace, service.Name, err)
				}
			}
		}
	}

	if err := h.ensureInternalRouterServiceForIngress(current, ci); err != nil {
		return fmt.Errorf("failed to create internal router service for clusteringress %s: %v", ci.Name, err)
	}

	return nil
}

// ensureInternalRouterServiceForIngress ensures that a Router service exists for a
// given ClusterIngress exists.
func (h *Reconciler) ensureInternalRouterServiceForIngress(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) error {
	svc, err := h.ManifestFactory.RouterServiceInternal(ci)
	if err != nil {
		return fmt.Errorf("failed to build router service: %v", err)
	}
	err = h.Client.Get(context.TODO(), types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, svc)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router service %s/%s, %v", svc.Namespace, svc.Name, err)
		}

		trueVar := true
		deploymentRef := metav1.OwnerReference{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Name:       deployment.Name,
			UID:        deployment.UID,
			Controller: &trueVar,
		}
		svc.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})

		err = h.Client.Create(context.TODO(), svc)
		if err == nil {
			logrus.Infof("created router service %s/%s", svc.Namespace, svc.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router service %s/%s: %v", svc.Namespace, svc.Name, err)
		}
	}

	return nil
}

// ensureDNSForLoadBalancer configures a wildcard DNS alias for a ClusterIngress
// targeting the given service.
func (h *Reconciler) ensureDNSForLoadBalancer(ci *ingressv1alpha1.ClusterIngress, service *corev1.Service) error {
	if len(service.Status.LoadBalancer.Ingress) == 0 {
		logrus.Infof("won't update DNS record for load balancer service %s/%s because status contains no ingresses", service.Namespace, service.Name)
		return nil
	}
	target := service.Status.LoadBalancer.Ingress[0].Hostname
	if len(target) == 0 {
		logrus.Infof("won't update DNS record for load balancer service %s/%s because ingress hostname is empty", service.Namespace, service.Name)
		return nil
	}
	// TODO: the routing wildcard prefix should come from cluster config or
	// ClusterIngress.
	domain := fmt.Sprintf("*.%s", *ci.Spec.IngressDomain)
	return h.DNSManager.EnsureAlias(domain, target)
}

// ensureRouterDeleted ensures that any router resources associated with the
// clusteringress are deleted.
func (h *Reconciler) ensureRouterDeleted(ci *ingressv1alpha1.ClusterIngress) error {
	deployment, err := h.ManifestFactory.RouterDeployment(ci)
	if err != nil {
		return fmt.Errorf("failed to build router deployment for deletion: %v", err)
	}

	err = h.Client.Delete(context.TODO(), deployment)
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
	if current.Spec.Template.Spec.Volumes[0].Secret.SecretName == expected.Spec.Template.Spec.Volumes[0].Secret.SecretName {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec.Template.Spec.Volumes[0].Secret.SecretName = expected.Spec.Template.Spec.Volumes[0].Secret.SecretName
	return true, updated
}
