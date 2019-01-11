package controller

import (
	"context"
	"fmt"
	"reflect"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// resourceMatcherFunc checks if the two objects match.
type resourceMatcherFunc func(old, new runtime.Object) bool

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
	cr, err := r.ManifestFactory.RouterClusterRole()
	if err != nil {
		return fmt.Errorf("failed to build router cluster role: %v", err)
	}
	_, err = r.verifyRouterAssetExists(types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, cr, nil)
	if err != nil {
		return err
	}

	if err := r.ensureRouterNamespaceAsset(); err != nil {
		return err
	}

	sa, err := r.ManifestFactory.RouterServiceAccount()
	if err != nil {
		return fmt.Errorf("failed to build router service account: %v", err)
	}
	// TODO: switch this to use verifyRouterAssetExists(key, obj, nil)
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
	// TODO: switch this to use verifyRouterAssetExists(key, obj, nil)
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
func (r *reconciler) ensureRouterForIngress(ci *ingressv1alpha1.ClusterIngress) error {
	deployment, err := r.ensureRouterDeployment(ci)
	if err != nil {
		return err
	}

	if ci.Spec.HighAvailability != nil {
		switch ci.Spec.HighAvailability.Type {
		case ingressv1alpha1.CloudClusterIngressHA:
			service, err := r.ManifestFactory.RouterServiceCloud(ci)
			if err != nil {
				return fmt.Errorf("failed to build router service: %v", err)
			}
			err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: service.Namespace, Name: service.Name}, service)
			if err != nil {
				if !errors.IsNotFound(err) {
					return fmt.Errorf("failed to get router service %s/%s, %v", service.Namespace, service.Name, err)
				}
				// Service doesn't exist; try to create it.
				trueVar := true
				deploymentRef := metav1.OwnerReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       deployment.Name,
					UID:        deployment.UID,
					Controller: &trueVar,
				}
				service.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
				err = r.Client.Create(context.TODO(), service)
				if err == nil {
					logrus.Infof("created router service %s/%s", service.Namespace, service.Name)
				} else if !errors.IsAlreadyExists(err) {
					return fmt.Errorf("failed to create router service %s/%s: %v", service.Namespace, service.Name, err)
				}
			}
			if ci.Spec.IngressDomain != nil {
				err = r.ensureDNSForLoadBalancer(ci, service)
				if err != nil {
					return fmt.Errorf("failed to ensure DNS for router service %s/%s: %v", service.Namespace, service.Name, err)
				}
			}
		}
	}

	if err := r.ensureInternalRouterServiceForIngress(deployment, ci); err != nil {
		return fmt.Errorf("failed to create internal router service for clusteringress %s: %v", ci.Name, err)
	}

	if err := r.syncClusterIngressStatus(deployment, ci); err != nil {
		return fmt.Errorf("failed to update status of clusteringress %s/%s: %v", deployment.Namespace, deployment.Name, err)
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

// verifyRouterAssetExists verifies that the desired router asset exists and
// matches the desired object.
func (r *reconciler) verifyRouterAssetExists(key types.NamespacedName, desired runtime.Object, matcherFunc resourceMatcherFunc) (runtime.Object, error) {
	current := desired.DeepCopyObject()
	err := r.Client.Get(context.TODO(), key, current)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get router %T %s: %v", desired, key.String(), err)
		}

		if err := r.Client.Create(context.TODO(), current); err == nil {
			logrus.Infof("created router asset %T %s", current, key.String())
			return current, nil
		} else if !errors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create router asset %T %s: %v", desired, key.String(), err)
		}
	}

	if matcherFunc != nil && !matcherFunc(current, desired) {
		if err := r.Client.Update(context.TODO(), desired); err != nil {
			return nil, err
		}

		logrus.Infof("updated router asset %T %s", desired, key.String())
		// Fetch back the updated asset.
		err = r.Client.Get(context.TODO(), key, current)
		if err != nil {
			return nil, err
		}
		return current, nil
	}

	return current, nil
}

// ensureInternalRouterServiceForIngress ensures that an internal service exists
// for a given ClusterIngress.
func (r *reconciler) ensureInternalRouterServiceForIngress(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) error {
	svc, err := r.ManifestFactory.RouterServiceInternal(ci)
	if err != nil {
		return fmt.Errorf("failed to build router service: %v", err)
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, svc)
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

		err = r.Client.Create(context.TODO(), svc)
		if err == nil {
			logrus.Infof("created router service %s/%s", svc.Namespace, svc.Name)
		} else if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create router service %s/%s: %v", svc.Namespace, svc.Name, err)
		}
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

	namespaceMatcher := func(obj1, obj2 runtime.Object) bool {
		ns1 := obj1.(*corev1.Namespace)
		ns2 := obj2.(*corev1.Namespace)

		if !reflect.DeepEqual(ns1.Labels, ns2.Labels) {
			return false
		}
		if !reflect.DeepEqual(ns1.Annotations, ns2.Annotations) {
			return false
		}

		return true
	}

	key := types.NamespacedName{Name: ns.Name}
	if _, err := r.verifyRouterAssetExists(key, ns, namespaceMatcher); err != nil {
		return err
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

	clusterIngressMatcher := func(obj1, obj2 runtime.Object) bool {
		ci1 := obj1.(*appsv1.Deployment)
		ci2 := obj2.(*appsv1.Deployment)

		if !reflect.DeepEqual(ci1.Labels, ci2.Labels) {
			return false
		}
		if !reflect.DeepEqual(ci1.Annotations, ci2.Annotations) {
			return false
		}
		if !reflect.DeepEqual(ci1.Spec, ci2.Spec) {
			return false
		}
		return true
	}

	key := types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}
	actual, err := r.verifyRouterAssetExists(key, deployment, clusterIngressMatcher)
	if err != nil {
		return nil, err
	}

	return actual.(*appsv1.Deployment), nil
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
