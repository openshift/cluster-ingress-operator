package clusteringress

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	"github.com/openshift/library-go/pkg/crypto"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

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

	// GlobalMachineSpecifiedConfigNamespace is the location for global
	// config.  In particular, the operator will put the configmap with the
	// CA certificate in this namespace.
	GlobalMachineSpecifiedConfigNamespace = "openshift-config-managed"

	// caCertSecretName is the name of the secret that holds the CA certificate
	// that the operator will use to create default certificates for
	// clusteringresses.
	caCertSecretName = "router-ca"

	// caCertConfigMapName is the name of the config map with the public key for
	// the CA certificate, which the operator publishes for other operators
	// to use.
	caCertConfigMapName = "router-ca"
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

// CACert holds the CA certificate as well as the resource versions of the most
// recently observed CA certificate secret and configmap.
type CACert struct {
	// CA is the CA certificate that the operator read from the secret
	// or generated and put in the secret.
	CA *crypto.CA

	// CACertConfigMapLastObservedResourceVersion is the resource version of
	// the config map at the time when the operator most recently
	// successfully read or created it.
	CACertConfigMapLastObservedResourceVersion string
	// CACertSecretLastObservedResourceVersion is the resource version of
	// the secret at the time when the operator most recently successfully
	// read or created it.
	CACertSecretLastObservedResourceVersion string
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	Client          client.Client
	ManifestFactory *manifests.Factory
	Namespace       string
	DNSManager      dns.Manager
	CACert
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
func (r *reconciler) ensureRouterForIngress(ci *ingressv1alpha1.ClusterIngress) error {
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
					Name:       current.Name,
					UID:        current.UID,
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

	if err := r.ensureInternalRouterServiceForIngress(current, ci); err != nil {
		return fmt.Errorf("failed to create internal router service for clusteringress %s: %v", ci.Name, err)
	}

	if err := r.ensureDefaultCertificateForIngress(current, ci); err != nil {
		return fmt.Errorf("failed to create default certificate for clusteringress %s: %v", ci.Name, err)
	}

	if err := r.syncClusterIngressStatus(current, ci); err != nil {
		return fmt.Errorf("failed to update status of clusteringress %s/%s: %v", current.Namespace, current.Name, err)
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

// ensureDefaultCertificateForIngress ensures that a default certificate exists
// for a given ClusterIngress.
func (r *reconciler) ensureDefaultCertificateForIngress(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) error {
	// Normally we would use the manifest factory to build the secret.
	// However, we want to avoid generating certificates if the secret
	// already exists.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("router-certs-%s", ci.Name),
			Namespace: deployment.Namespace,
		},
	}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, secret)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get secret %s/%s: %v", secret.Namespace, secret.Name, err)
		}

		ca, err := r.ensureRouterCACertificate()
		if err != nil {
			return fmt.Errorf("failed to get CA certificate: %v", err)
		}
		hostnames := sets.NewString(fmt.Sprintf("*.%s", *ci.Spec.IngressDomain))
		cert, err := ca.MakeServerCert(hostnames, 0)
		if err != nil {
			return fmt.Errorf("failed to make CA: %v", err)
		}

		secret.Type = corev1.SecretTypeTLS
		certBytes, keyBytes, err := cert.GetPEMBytes()
		if err != nil {
			return fmt.Errorf("failed to get certificate from secret %s/%s: %v", secret.Namespace, secret.Name, err)
		}
		secret.Data = map[string][]byte{
			"tls.crt": certBytes,
			"tls.key": keyBytes,
		}
		trueVar := true
		deploymentRef := metav1.OwnerReference{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Name:       deployment.Name,
			UID:        deployment.UID,
			Controller: &trueVar,
		}
		secret.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
		if err := r.Client.Create(context.TODO(), secret); err != nil {
			if !errors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create secret %s/%s: %v", secret.Namespace, secret.Name, err)
			}

			return nil
		}
		logrus.Infof("created secret %s/%s", secret.Namespace, secret.Name)
	}

	return nil
}

// ensureRouterCACertificate ensures a CA certificate exists that can be used to
// sign the default certificates for ClusterIngresses.
func (r *reconciler) ensureRouterCACertificate() (*crypto.CA, error) {
	// Normally we would use the manifest factory to build the secret and
	// configmap.  However, we want to avoid generating certificates if the
	// resources already exist.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caCertSecretName,
			Namespace: r.Namespace,
		},
	}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, secret)
	if err != nil && !errors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get secret %s/%s: %v", secret.Namespace, secret.Name, err)
	}
	haveSecret := !errors.IsNotFound(err)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caCertConfigMapName,
			Namespace: GlobalMachineSpecifiedConfigNamespace,
		},
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, cm)
	if err != nil && !errors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get configmap %s/%s: %v", cm.Namespace, cm.Name, err)
	}
	haveConfigMap := !errors.IsNotFound(err)

	if haveSecret && haveConfigMap &&
		secret.ResourceVersion == r.CACertSecretLastObservedResourceVersion &&
		cm.ResourceVersion == r.CACertConfigMapLastObservedResourceVersion {
		return r.CACert.CA, nil
	}

	var ca *crypto.CA

	if haveSecret {
		if _, ok := secret.Data["tls.crt"]; !ok {
			return nil, fmt.Errorf("failed to read %q from secret %s/%s: %3v", "tls.crt", secret.Namespace, secret.Name, err)
		}
		if _, ok := secret.Data["tls.key"]; !ok {
			return nil, fmt.Errorf("failed to read %q from secret %s/%s: %3v", "tls.key", secret.Namespace, secret.Name, err)
		}
		ca, err = crypto.GetCAFromBytes(secret.Data["tls.crt"], secret.Data["tls.key"])
		if err != nil {
			return nil, fmt.Errorf("failed to get CA from secret %s/%s: %v", secret.Namespace, secret.Name, err)
		}
	} else {
		// TODO Use certrotationcontroller from library-go.
		signerName := fmt.Sprintf("%s@%d", "cluster-ingress-operator", time.Now().Unix())
		caConfig, err := crypto.MakeCAConfig(signerName, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to make CA config: %v", err)
		}

		secret.Type = corev1.SecretTypeTLS
		certBytes, keyBytes, err := caConfig.GetPEMBytes()
		if err != nil {
			return nil, fmt.Errorf("failed to get certificate: %v", err)
		}
		secret.Data = map[string][]byte{
			"tls.crt": certBytes,
			"tls.key": keyBytes,
		}
		if err := r.Client.Create(context.TODO(), secret); err != nil {
			return nil, fmt.Errorf("failed to create secret %s/%s: %v", secret.Namespace, secret.Name, err)
		}
		logrus.Infof("created secret %s/%s", secret.Namespace, secret.Name)

		ca = &crypto.CA{
			Config:          caConfig,
			SerialGenerator: &crypto.RandomSerialGenerator{},
		}
	}

	if haveConfigMap {
		if _, ok := cm.Data["ca-bundle.crt"]; !ok {
			return nil, fmt.Errorf("failed to read %q from configmap %s/%s: %v", "ca-bundle.crt", cm.Namespace, cm.Name, err)
		}
		if !bytes.Equal(secret.Data["tls.crt"], []byte(cm.Data["ca-bundle.crt"])) {
			return nil, fmt.Errorf("content of %q in configmap %s/%s does not match content of %q in secret %s/%s", "ca-bundle.crt", cm.Namespace, cm.Name, "tls.crt", secret.Namespace, secret.Name)
			// TODO Update the configmap based off the secret?
		}
	} else {
		cm.Data = map[string]string{"ca-bundle.crt": string(secret.Data["tls.crt"])}
		if err := r.Client.Create(context.TODO(), cm); err != nil {
			return nil, fmt.Errorf("failed to create configmap %s/%s: %v", cm.Namespace, cm.Name, err)
		}
		logrus.Infof("created configmap %s/%s", cm.Namespace, cm.Name)
	}

	r.CACert.CA = ca
	r.CACertConfigMapLastObservedResourceVersion = cm.ResourceVersion
	r.CACertSecretLastObservedResourceVersion = secret.ResourceVersion

	return ca, nil
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
