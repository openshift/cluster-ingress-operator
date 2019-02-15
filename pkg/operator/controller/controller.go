package controller

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

	configv1 "github.com/openshift/api/config/v1"

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
		// redo out logging and wire up the controller-runtime logger because who
		// knows what else is being eaten.
		logrus.Errorf("error: %v", err)
	}
	return result, err
}

func (r *reconciler) reconcile(request reconcile.Request) (reconcile.Result, error) {
	// TODO: Should this be another controller?
	defer func() {
		err := r.syncOperatorStatus()
		if err != nil {
			logrus.Infof("failed to sync operator status: %v", err)
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
			ingress = nil
		} else {
			return reconcile.Result{}, fmt.Errorf("failed to get clusteringress %q: %v", request, err)
		}
	}

	// Collect errors as we go.
	errs := []error{}

	if ingress != nil {
		// Only reconcile the ingress itself if it exists.
		dnsConfig := &configv1.DNS{}
		err = r.Client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, dnsConfig)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to get dns 'cluster': %v", err)
		}

		infraConfig := &configv1.Infrastructure{}
		err = r.Client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to get infrastructure 'cluster': %v", err)
		}

		// Ensure we have all the necessary scaffolding on which to place router
		// instances.
		err = r.ensureRouterNamespace()
		if err != nil {
			return reconcile.Result{}, err
		}

		if ingress.DeletionTimestamp != nil {
			// Handle deletion.
			err := r.ensureIngressDeleted(ingress, dnsConfig)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to ensure ingress deletion: %v", err))
			}
		} else {
			// Handle everything else.
			err := r.ensureRouterForIngress(ingress, dnsConfig, infraConfig)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to ensure clusteringress: %v", err))
			}
		}
	}

	// TODO: This should be in a different reconciler as it's independnt of an
	// individual ingress. We only really need to trigger this when a
	// clusteringress is added or deleted...
	if len(errs) == 0 {
		// Find all clusteringresses to compute CA states.
		ingresses := &ingressv1alpha1.ClusterIngressList{}
		err = r.Client.List(context.TODO(), &client.ListOptions{Namespace: r.Namespace}, ingresses)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to list clusteringresses in namespace %s: %v", r.Namespace, err)
		}
		if shouldPublishRouterCA(ingresses.Items) {
			if err := r.ensureRouterCAIsPublished(); err != nil {
				errs = append(errs, fmt.Errorf("failed to ensure router CA was published: %v", err))
			}
		} else {
			if err := r.ensureRouterCAIsUnpublished(); err != nil {
				errs = append(errs, fmt.Errorf("failed to ensure router CA was unpublished: %v", err))
			}
		}
	}

	return reconcile.Result{}, utilerrors.NewAggregate(errs)
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
	updated := ingress.DeepCopyObject().(*ingressv1alpha1.ClusterIngress)
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
func (r *reconciler) ensureRouterForIngress(ci *ingressv1alpha1.ClusterIngress, dnsConfig *configv1.DNS, infraConfig *configv1.Infrastructure) error {
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

	lbService, err := r.ensureLoadBalancerService(ci, infraConfig, current)
	if err != nil {
		return fmt.Errorf("failed to ensure load balancer service for %s: %v", ci.Name, err)
	}

	err = r.ensureDNS(ci, dnsConfig, lbService)
	if err != nil {
		return fmt.Errorf("failed to ensure DNS for %s: %v", ci.Name, err)
	}

	internalSvc, err := r.ensureInternalRouterServiceForIngress(ci, deploymentRef)
	if err != nil {
		return fmt.Errorf("failed to create internal router service for clusteringress %s: %v", ci.Name, err)
	}

	if ci.Spec.DefaultCertificateSecret == nil {
		if err := r.ensureDefaultCertificateForIngress(current, ci); err != nil {
			return fmt.Errorf("failed to create default certificate for clusteringress %s: %v", ci.Name, err)
		}
	} else {
		if err := r.ensureDefaultCertificateDeleted(current, ci); err != nil {
			return fmt.Errorf("failed to delete operator-generated default certificate for clusteringress %s: %v", ci.Name, err)
		}
	}

	if err := r.ensureMetricsIntegration(ci, internalSvc, deploymentRef); err != nil {
		return fmt.Errorf("failed to integrate metrics with openshift-monitoring for clusteringress %s: %v", ci.Name, err)
	}

	_, err = r.ensureServiceMonitor(ci, internalSvc, current)
	if err != nil {
		return fmt.Errorf("failed to ensure servicemonitor for %s: %v", ci.Name, err)
	}

	if err := r.syncClusterIngressStatus(current, ci); err != nil {
		return fmt.Errorf("failed to update status of clusteringress %s/%s: %v", current.Namespace, current.Name, err)
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

// ensureDefaultCertificateForIngress ensures that a default certificate exists
// for a given ClusterIngress.
func (r *reconciler) ensureDefaultCertificateForIngress(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) error {
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

		ca, err := r.getRouterCA()
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

// ensureDefaultCertificateDeleted ensures any operator-generated default
// certificate for a given ClusterIngress is deleted.
func (r *reconciler) ensureDefaultCertificateDeleted(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("router-certs-%s", ci.Name),
			Namespace: deployment.Namespace,
		},
	}
	err := r.Client.Delete(context.TODO(), secret)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete secret %s/%s: %v", secret.Namespace, secret.Name, err)
	}

	logrus.Infof("deleted secret %s/%s", secret.Namespace, secret.Name)

	return nil
}

// getRouterCA gets the CA, or creates it if it does not already exist.
func (r *reconciler) getRouterCA() (*crypto.CA, error) {
	secret, err := r.ensureRouterCACertificateSecret()
	if err != nil {
		return nil, fmt.Errorf("failed to get CA secret: %v", err)
	}

	certBytes := secret.Data["tls.crt"]
	keyBytes := secret.Data["tls.key"]

	ca, err := crypto.GetCAFromBytes(certBytes, keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to get CA from secret %s/%s: %v", secret.Namespace, secret.Name, err)
	}

	return ca, nil
}

// ensureRouterCACertificateSecret ensures a CA certificate secret exists that
// can be used to sign the default certificates for ClusterIngresses.
func (r *reconciler) ensureRouterCACertificateSecret() (*corev1.Secret, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caCertSecretName,
			Namespace: r.Namespace,
		},
	}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, secret)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get secret %s/%s: %v", secret.Namespace, secret.Name, err)
		}

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
	}

	return secret, nil
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

// shouldPublishRouterCA checks if some ClusterIngress uses the default
// certificate, in which case the CA certificate needs to be published.
func shouldPublishRouterCA(ingresses []ingressv1alpha1.ClusterIngress) bool {
	for _, ci := range ingresses {
		if ci.Spec.DefaultCertificateSecret == nil {
			return true
		}
	}
	return false
}

// ensureRouterCAIsPublished ensures a configmap exists with the CA certificate
// in a well known namespace.
func (r *reconciler) ensureRouterCAIsPublished() error {
	secret, err := r.ensureRouterCACertificateSecret()
	if err != nil {
		return fmt.Errorf("failed to get CA secret: %v", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caCertConfigMapName,
			Namespace: GlobalMachineSpecifiedConfigNamespace,
		},
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, cm)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get configmap %s/%s: %v", cm.Namespace, cm.Name, err)
		}

		cm.Data = map[string]string{"ca-bundle.crt": string(secret.Data["tls.crt"])}
		if err := r.Client.Create(context.TODO(), cm); err != nil {
			if errors.IsAlreadyExists(err) {
				return nil
			}

			return fmt.Errorf("failed to create configmap %s/%s: %v", cm.Namespace, cm.Name, err)
		}

		logrus.Infof("created configmap %s/%s", cm.Namespace, cm.Name)

		return nil
	}

	if !bytes.Equal(secret.Data["tls.crt"], []byte(cm.Data["ca-bundle.crt"])) {
		cm.Data["ca-bundle.crt"] = string(secret.Data["tls.crt"])
		if err := r.Client.Update(context.TODO(), cm); err != nil {
			if errors.IsAlreadyExists(err) {
				return nil
			}

			return fmt.Errorf("failed to update configmap %s/%s: %v", cm.Namespace, cm.Name, err)
		}

		logrus.Infof("updated configmap %s/%s", cm.Namespace, cm.Name)

		return nil
	}

	return nil
}

// ensureRouterCAIsUnpublished ensures the configmap with the CA certificate is
// deleted.
func (r *reconciler) ensureRouterCAIsUnpublished() error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caCertConfigMapName,
			Namespace: GlobalMachineSpecifiedConfigNamespace,
		},
	}
	if err := r.Client.Delete(context.TODO(), cm); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete configmap %s/%s: %v", cm.Namespace, cm.Name, err)
	}

	logrus.Infof("deleted configmap %s/%s", cm.Namespace, cm.Name)

	return nil
}
