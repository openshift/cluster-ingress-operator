package controller

import (
	"context"
	"fmt"
	"reflect"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatormanager "github.com/openshift/cluster-ingress-operator/pkg/operator/manager"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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
// The controller will be pre-configured to watch events from a component
// source provided by the manager.
func New(mgr operatormanager.ComponentManager, config Config) (controller.Controller, error) {
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
	err = c.Watch(mgr.ComponentSource(), &handler.EnqueueRequestForObject{})
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
}

// reconciler handles the actual ingress reconciliation logic in response to
// events.
type reconciler struct {
	Config
}

// Reconcile currently performs a full reconciliation in response to any
// request. Status is recomputed and updated after every request is processed.
func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// TODO: Should this be another controller?
	defer func() {
		err := r.syncOperatorStatus()
		if err != nil {
			logrus.Infof("failed to sync operator status: %v", err)
		}
	}()

	logrus.Infof("reconciling request: %#v", request)

	result, err := r.reconcile()
	if err != nil {
		logrus.Errorf("failed to reconcile: %v", err)
	}
	return result, err
}

// reconcile performs a single full reconciliation loop for ingress. It ensures
// the ingress namespace exists, and then creates, updates, or deletes router
// clusters as appropriate given the ClusterIngresses that exist.
func (r *reconciler) reconcile() (reconcile.Result, error) {
	// Ensure we have all the necessary scaffolding on which to place router
	// instances.

	err := r.ensureRouterNamespace()
	if err != nil {
		return reconcile.Result{}, err
	}

	// Find all clusteringresses.
	ingresses := &ingressv1alpha1.ClusterIngressList{}
	err = r.Client.List(context.TODO(), &client.ListOptions{Namespace: r.Namespace}, ingresses)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list clusteringresses in namespace %s: %v", r.Namespace, err)
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
			err := r.ensureRouterDeleted(&ingress)
			if err != nil {
				errors = append(errors, fmt.Errorf("failed to delete clusteringress %s/%s: %v", ingress.Namespace, ingress.Name, err))
				continue
			}
			logrus.Infof("deleted router for clusteringress %s/%s", ingress.Namespace, ingress.Name)
			// Clean up the finalizer to allow the clusteringress to be deleted.
			if slice.ContainsString(ingress.Finalizers, ClusterIngressFinalizer) {
				ingress.Finalizers = slice.RemoveString(ingress.Finalizers, ClusterIngressFinalizer)
				err = r.Client.Update(context.TODO(), &ingress)
				if err != nil {
					errors = append(errors, fmt.Errorf("failed to remove finalizer from clusteringress %s/%s: %v", ingress.Namespace, ingress.Name, err))
				}
			}
			continue
		}

		// Handle active ingress.
		err := r.ensureRouterForIngress(&ingress)
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to ensure clusteringress %s/%s: %v", ingress.Namespace, ingress.Name, err))
		}
	}
	return reconcile.Result{}, utilerrors.NewAggregate(errors)
}

// ensureRouterNamespace ensures all the necessary scaffolding exists for
// routers generally, including a namespace and all RBAC setup.
func (r *reconciler) ensureRouterNamespace() error {
	if err := r.ensureRouterClusterRole(); err != nil {
		return err
	}

	if err := r.ensureRouterNamespaceAsset(); err != nil {
		return err
	}

	if err := r.ensureRouterServiceAccount(); err != nil {
		return err
	}

	if err := r.ensureRouterClusterRoleBinding(); err != nil {
		return err
	}

	return nil
}

// ensureRouterForIngress ensures all necessary router resources exist for a
// given clusteringress.
func (r *reconciler) ensureRouterForIngress(ci *ingressv1alpha1.ClusterIngress) error {
	deployment, err := r.ensureRouterDeployment(ci)
	if err != nil {
		return err
	}

	if ci.Spec.HighAvailability != nil {
		switch ci.Spec.HighAvailability.Type {
		case ingressv1alpha1.CloudClusterIngressHA:
			service, err := r.ensureRouterServiceCloud(deployment, ci)
			if err != nil {
				return err
			}

			if ci.Spec.IngressDomain != nil {
				err = r.ensureDNSForLoadBalancer(ci, service)
				if err != nil {
					return fmt.Errorf("failed to ensure DNS for router service %s/%s: %v", service.Namespace, service.Name, err)
				}
			}
		}
	}

	if err := r.ensureRouterServiceInternal(deployment, ci); err != nil {
		return fmt.Errorf("failed to create internal router service for clusteringress %s: %v", ci.Name, err)
	}

	return nil
}

// ensureRouterAsset ensures the expected router asset exists and returns the
// current asset information.
func (r *reconciler) ensureRouterAsset(key types.NamespacedName, obj runtime.Object) (runtime.Object, error) {
	err := r.Client.Get(context.TODO(), key, obj)
	if err != nil {
		if !errors.IsNotFound(err) {
			return obj, fmt.Errorf("failed to get router %T %s: %v", obj, key.String(), err)
		}

		if err := r.Client.Create(context.TODO(), obj); err == nil {
			logrus.Infof("created router asset %T %s", obj, key.String())
		} else if !errors.IsAlreadyExists(err) {
			return obj, fmt.Errorf("failed to create router asset %T %s: %v", obj, key.String(), err)
		}
	}

	return obj, nil
}

// ensureRouterServiceInternal ensures that an internal router service exists
// for a given ClusterIngress.
func (r *reconciler) ensureRouterServiceInternal(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) error {
	svc, err := r.ManifestFactory.RouterServiceInternal(ci)
	if err != nil {
		return fmt.Errorf("failed to build router service internal: %v", err)
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

	current := svc.DeepCopy()
	key := types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}

	if _, err := r.ensureRouterAsset(key, current); err != nil {
		return err
	}

	modified := !reflect.DeepEqual(current.Labels, svc.Labels) || !reflect.DeepEqual(current.GetOwnerReferences(), svc.GetOwnerReferences())
	// Some annotations are generated, so check the serving secret one.
	modified = modified || !reflect.DeepEqual(current.Annotations[manifests.ServingCertSecretAnnotation], svc.Annotations[manifests.ServingCertSecretAnnotation])
	modified = modified || !reflect.DeepEqual(current.Spec, svc.Spec)
	if modified {
		if err := r.Client.Update(context.TODO(), svc); err != nil {
			return err
		}

		logrus.Infof("updated router service %s", key.String())
		return nil
	}

	return nil
}

// ensureDNSForLoadBalancer configures a wildcard DNS alias for a ClusterIngress
// targeting the given service.
func (r *reconciler) ensureDNSForLoadBalancer(ci *ingressv1alpha1.ClusterIngress, service *corev1.Service) error {
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
	return r.DNSManager.EnsureAlias(domain, target)
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

// ensureRouterClusterRole ensures that the router cluster role exists and
// matches the expected role.
func (r *reconciler) ensureRouterClusterRole() error {
	cr, err := r.ManifestFactory.RouterClusterRole()
	if err != nil {
		return fmt.Errorf("failed to build router cluster role: %v", err)
	}

	current := cr.DeepCopy()
	key := types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}

	if _, err := r.ensureRouterAsset(key, current); err != nil {
		return err
	}

	// TODO: should we check for label/annotation changes or ignore them?
	modified := !reflect.DeepEqual(current.Labels, cr.Labels) || !reflect.DeepEqual(current.Annotations, cr.Annotations)
	modified = modified || !reflect.DeepEqual(current.Rules, cr.Rules)
	modified = modified || !reflect.DeepEqual(current.AggregationRule, cr.AggregationRule)
	if modified {
		if err := r.Client.Update(context.TODO(), cr); err != nil {
			return err
		}

		logrus.Infof("updated router cluster role %s", key.String())
		return nil
	}

	return nil
}

// ensureRouterNamespaceAsset ensures that the router namespace exists and
// matches the expected namespace.
func (r *reconciler) ensureRouterNamespaceAsset() error {
	ns, err := r.ManifestFactory.RouterNamespace()
	if err != nil {
		return fmt.Errorf("failed to build router namespace: %v", err)
	}

	current := ns.DeepCopy()
	key := types.NamespacedName{Name: ns.Name}

	if _, err := r.ensureRouterAsset(key, current); err != nil {
		return err
	}

	// TODO: should we check for all label/annotation changes or just
	//       specific ones node-selector/run-level?
	modified := !reflect.DeepEqual(current.Labels, ns.Labels) || !reflect.DeepEqual(current.Annotations, ns.Annotations)
	modified = modified || !reflect.DeepEqual(current.Spec, ns.Spec)
	if modified {
		if err := r.Client.Update(context.TODO(), ns); err != nil {
			return err
		}

		logrus.Infof("updated router namespace %s", ns.Name)
		return nil
	}

	return nil
}

// ensureRouterServiceAccount ensures that the router service account exists and
// matches the expected service account.
func (r *reconciler) ensureRouterServiceAccount() error {
	sa, err := r.ManifestFactory.RouterServiceAccount()
	if err != nil {
		return fmt.Errorf("failed to build router service account: %v", err)
	}

	current := sa.DeepCopy()
	key := types.NamespacedName{Namespace: sa.Namespace, Name: sa.Name}

	if _, err := r.ensureRouterAsset(key, current); err != nil {
		return err
	}

	// TODO: Should we check for Secrets, ImagePullSecrets and
	//       AutomountServiceAccountToken - we don't really use those.
	// TODO: should we check for label/annotation changes or ignore them?
	modified := !reflect.DeepEqual(current.Labels, sa.Labels) || !reflect.DeepEqual(current.Annotations, sa.Annotations)
	if modified {
		if err := r.Client.Update(context.TODO(), sa); err != nil {
			return err
		}

		logrus.Infof("updated router service account %s", key.String())
		return nil
	}

	return nil
}

// ensureRouterClusterRoleBinding ensures that a router cluster role binding
// exists and matches the expected cluster role binding.
func (r *reconciler) ensureRouterClusterRoleBinding() error {
	crb, err := r.ManifestFactory.RouterClusterRoleBinding()
	if err != nil {
	}

	current := crb.DeepCopy()
	key := types.NamespacedName{Name: crb.Name}

	if _, err := r.ensureRouterAsset(key, current); err != nil {
		return err
	}

	// TODO: should we check for label/annotation changes or ignore them?
	modified := !reflect.DeepEqual(current.Labels, crb.Labels) || !reflect.DeepEqual(current.Annotations, crb.Annotations)
	modified = modified || !reflect.DeepEqual(current.Subjects, crb.Subjects)
	modified = modified || !reflect.DeepEqual(current.RoleRef, crb.RoleRef)
	if modified {
		if err := r.Client.Update(context.TODO(), crb); err != nil {
			return err
		}

		logrus.Infof("updated router cluster role binding %s", crb.Name)
		return nil
	}

	return nil
}

// ensureRouterDeployment ensures that the router deployment exists and matches
// the expected deployment.
func (r *reconciler) ensureRouterDeployment(ci *ingressv1alpha1.ClusterIngress) (*appsv1.Deployment, error) {
	deployment, err := r.ManifestFactory.RouterDeployment(ci)
	if err != nil {
		return nil, fmt.Errorf("failed to build router deployment: %v", err)
	}

	current := deployment.DeepCopy()
	key := types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}

	if _, err := r.ensureRouterAsset(key, current); err != nil {
		return nil, err
	}

	// TODO: should we check for label/annotation changes or ignore them?
	modified := !reflect.DeepEqual(current.Labels, deployment.Labels) || !reflect.DeepEqual(current.Annotations, deployment.Annotations)

	// TODO: this will need changes when unsupported extensions/patches get
	//       added to the router deployment.
	modified = modified || !reflect.DeepEqual(current.Spec, deployment.Spec)
	if modified {
		if err := r.Client.Update(context.TODO(), deployment); err != nil {
			return deployment, fmt.Errorf("failed to update router deployment %s, %v", key.String(), err)
		}

		logrus.Infof("updated router deployment %s", key.String())
		return deployment, nil
	}

	return deployment, nil
}

// ensureRouterServiceCloud ensures that the router cloud service exists and
// matches the expected service.
func (r *reconciler) ensureRouterServiceCloud(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) (*corev1.Service, error) {
	service, err := r.ManifestFactory.RouterServiceCloud(ci)
	if err != nil {
		return nil, fmt.Errorf("failed to build router cloud service: %v", err)
	}

	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
		Controller: &trueVar,
	}
	service.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})

	current := service.DeepCopy()
	key := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}

	if _, err := r.ensureRouterAsset(key, current); err != nil {
		return nil, err
	}

	// TODO: should we check for label/annotation changes or ignore them?
	modified := !reflect.DeepEqual(current.Labels, service.Labels) || !reflect.DeepEqual(current.Annotations, service.Annotations)
	modified = modified || !reflect.DeepEqual(current.GetOwnerReferences(), service.GetOwnerReferences())
	modified = modified || !reflect.DeepEqual(current.Spec, service.Spec)
	if modified {
		if err := r.Client.Update(context.TODO(), service); err != nil {
			return service, err
		}

		logrus.Infof("updated router cloud service %s", key.String())
		return service, nil
	}

	return service, nil
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
