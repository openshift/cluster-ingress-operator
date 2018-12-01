package stub

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	"github.com/operator-framework/operator-sdk/pkg/sdk"

	cvoclientset "github.com/openshift/cluster-version-operator/pkg/generated/clientset/versioned"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

const (
	// ClusterIngressFinalizer is applied to all ClusterIngress resources before
	// they are considered for processing; this ensures the operator has a chance
	// to handle all states.
	// TODO: Make this generic and not tied to the "default" ingress.
	ClusterIngressFinalizer = "ingress.openshift.io/default-cluster-ingress"
)

type Handler struct {
	CvoClient       *cvoclientset.Clientset
	InstallConfig   *util.InstallConfig
	ManifestFactory *manifests.Factory
	Namespace       string
	DNSManager      dns.Manager
}

func (h *Handler) Handle(ctx context.Context, event sdk.Event) error {
	defer h.syncOperatorStatus()

	// TODO: This should be adding an item to a rate limited work queue, but for
	// now correctness is more important than performance.
	logrus.Infof("reconciling in response to event: %#v", event)

	return h.reconcile()
}

// EnsureDefaultClusterIngress ensures that a default ClusterIngress exists.
func (h *Handler) EnsureDefaultClusterIngress() error {
	ci, err := h.ManifestFactory.DefaultClusterIngress()
	if err != nil {
		return err
	}

	err = sdk.Create(ci)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	} else if err == nil {
		logrus.Infof("created default clusteringress %s/%s", ci.Namespace, ci.Name)
	}
	return nil
}

// Reconcile performs a full reconciliation loop for ingress, including
// generalized setup and handling of all clusteringress resources in the
// operator namespace.
func (h *Handler) reconcile() error {
	// Ensure we have all the necessary scaffolding on which to place router
	// instances.
	err := h.ensureRouterNamespace()
	if err != nil {
		return err
	}

	// Find all clusteringresses.
	ingresses := &ingressv1alpha1.ClusterIngressList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterIngress",
			APIVersion: "ingress.openshift.io/v1alpha1",
		},
	}
	err = sdk.List(h.Namespace, ingresses, sdk.WithListOptions(&metav1.ListOptions{}))
	if err != nil {
		return fmt.Errorf("failed to list clusteringresses in namespace %s: %v", h.Namespace, err)
	}

	// Reconcile all the ingresses.
	errors := []error{}
	for _, ingress := range ingresses.Items {
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
			// Clean up the finalizer to allow the clusteringress to be deleted.
			if slice.ContainsString(ingress.Finalizers, ClusterIngressFinalizer) {
				ingress.Finalizers = slice.RemoveString(ingress.Finalizers, ClusterIngressFinalizer)
				err = sdk.Update(&ingress)
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
	return utilerrors.NewAggregate(errors)
}

// ensureRouterNamespace ensures all the necessary scaffolding exists for
// routers generally, including a namespace and all RBAC setup.
func (h *Handler) ensureRouterNamespace() error {
	cr, err := h.ManifestFactory.RouterClusterRole()
	if err != nil {
		return fmt.Errorf("failed to build router cluster role: %v", err)
	}
	err = sdk.Get(cr)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role %s: %v", cr.Name, err)
		}
		err = sdk.Create(cr)
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
	err = sdk.Get(ns)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router namespace %q: %v", ns.Name, err)
		}
		err = sdk.Create(ns)
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
	err = sdk.Get(sa)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
		err = sdk.Create(sa)
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
	err = sdk.Get(crb)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role binding %s: %v", crb.Name, err)
		}
		err = sdk.Create(crb)
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
func (h *Handler) ensureRouterForIngress(ci *ingressv1alpha1.ClusterIngress) error {
	ds, err := h.ManifestFactory.RouterDaemonSet(ci)
	if err != nil {
		return fmt.Errorf("failed to build router daemonset: %v", err)
	}
	expected := ds.DeepCopy()
	err = sdk.Get(ds)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router daemonset %s/%s, %v", ds.Namespace, ds.Name, err)
		}
		err = sdk.Create(ds)
		if err == nil {
			logrus.Infof("created router daemonset %s/%s", ds.Namespace, ds.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router daemonset %s/%s: %v", ds.Namespace, ds.Name, err)
		}
	}

	if changed, obj := daemonsetConfigChanged(ds, expected); changed {
		err = sdk.Update(obj)
		if err == nil {
			logrus.Infof("updated router daemonset %s/%s", obj.Namespace, obj.Name)
		} else {
			return fmt.Errorf("failed to update router daemonset %s/%s, %v", obj.Namespace, obj.Name, err)
		}
	}

	if ci.Spec.HighAvailability != nil {
		switch ci.Spec.HighAvailability.Type {
		case ingressv1alpha1.CloudClusterIngressHA:
			service, err := h.ManifestFactory.RouterServiceCloud(ci)
			if err != nil {
				return fmt.Errorf("failed to build router service: %v", err)
			}
			err = sdk.Get(service)
			if err != nil {
				if !errors.IsNotFound(err) {
					return fmt.Errorf("failed to get router service %s/%s, %v", service.Namespace, service.Name, err)
				}
				// Service doesn't exist; try to create it.
				trueVar := true
				dsRef := metav1.OwnerReference{
					APIVersion: ds.APIVersion,
					Kind:       ds.Kind,
					Name:       ds.Name,
					UID:        ds.UID,
					Controller: &trueVar,
				}
				service.SetOwnerReferences([]metav1.OwnerReference{dsRef})
				err = sdk.Create(service)
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

	if err := h.ensureRouterServiceForIngress(ds, ci); err != nil {
		return err
	}

	return nil
}

// ensureRouterServiceForIngress ensures that a Router service exists for a
// given ClusterIngress exists.
func (h *Handler) ensureRouterServiceForIngress(ds *appsv1.DaemonSet, ci *ingressv1alpha1.ClusterIngress) error {
	svc, err := h.ManifestFactory.RouterServiceInternal(ci)
	if err != nil {
		return fmt.Errorf("failed to build router service: %v", err)
	}
	err = sdk.Get(svc)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router service %s/%s, %v", svc.Namespace, svc.Name, err)
		}

		trueVar := true
		dsRef := metav1.OwnerReference{
			APIVersion: ds.APIVersion,
			Kind:       ds.Kind,
			Name:       ds.Name,
			UID:        ds.UID,
			Controller: &trueVar,
		}
		svc.SetOwnerReferences([]metav1.OwnerReference{dsRef})

		err = sdk.Create(svc)
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
func (h *Handler) ensureDNSForLoadBalancer(ci *ingressv1alpha1.ClusterIngress, service *corev1.Service) error {
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
func (h *Handler) ensureRouterDeleted(ci *ingressv1alpha1.ClusterIngress) error {
	ds, err := h.ManifestFactory.RouterDaemonSet(ci)
	if err != nil {
		return fmt.Errorf("failed to build router daemonset for deletion: %v", err)
	}
	err = sdk.Delete(ds)
	if !errors.IsNotFound(err) {
		return err
	}
	return nil
}

// daemonsetConfigChanged checks if current config matches the expected config
// for the cluster ingress daemonset and if not returns the updated config.
func daemonsetConfigChanged(current, expected *appsv1.DaemonSet) (bool, *appsv1.DaemonSet) {
	// As per an offline conversation, this checks only the secret name
	// for now but can be updated to a `reflect.DeepEqual` if needed.
	if current.Spec.Template.Spec.Volumes[0].Secret.SecretName == expected.Spec.Template.Spec.Volumes[0].Secret.SecretName {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec.Template.Spec.Volumes[0].Secret.SecretName = expected.Spec.Template.Spec.Volumes[0].Secret.SecretName
	return true, updated
}
