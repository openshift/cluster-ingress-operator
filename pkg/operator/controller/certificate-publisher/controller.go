// The certificate-publisher controller is responsible for publishing the
// certificate and key of the ingresscontroller for the cluster ingress domain
// to the "router-certs" secret in the "openshift-config-managed" namespace and
// for publishing the certificate for the default ingresscontroller to the
// "default-ingress-cert" configmap in the same namespace.  Note that the
// "default" ingresscontroller is typically but not necessarily the
// ingresscontroller with the cluster ingress domain.
package certificatepublisher

import (
	"context"
	"fmt"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/client-go/tools/record"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimecontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "certificate_publisher_controller"
)

var log = logf.Logger.WithName(controllerName)

type reconciler struct {
	client            client.Client
	cache             cache.Cache
	recorder          record.EventRecorder
	operatorNamespace string
	operandNamespace  string
}

// New returns a new controller that publishes a "router-certs" secret in the
// openshift-config-managed namespace with the certificate and key for the
// ingresscontroller for the cluster ingress domain, as well as a
// "default-ingress-cert" configmap with the certificate for the "default"
// ingresscontroller (which is usually the same ingresscontroller).
func New(mgr manager.Manager, operatorNamespace, operandNamespace string) (runtimecontroller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		client:            mgr.GetClient(),
		cache:             operatorCache,
		recorder:          mgr.GetEventRecorderFor(controllerName),
		operatorNamespace: operatorNamespace,
		operandNamespace:  operandNamespace,
	}
	c, err := runtimecontroller.New(controllerName, mgr, runtimecontroller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	// Index ingresscontrollers over the default certificate name so that
	// secretToIngressController can look up ingresscontrollers that
	// reference the secret.
	if err := operatorCache.IndexField(context.Background(), &operatorv1.IngressController{}, "defaultCertificateName", client.IndexerFunc(func(o client.Object) []string {
		secret := controller.RouterEffectiveDefaultCertificateSecretName(o.(*operatorv1.IngressController), operandNamespace)
		return []string{secret.Name}
	})); err != nil {
		return nil, fmt.Errorf("failed to create index for ingresscontroller: %v", err)
	}

	secretsInformer, err := operatorCache.GetInformer(context.Background(), &corev1.Secret{})
	if err != nil {
		return nil, fmt.Errorf("failed to create informer for secrets: %v", err)
	}
	if err := c.Watch(&source.Informer{Informer: secretsInformer}, handler.EnqueueRequestsFromMapFunc(reconciler.secretToIngressController)); err != nil {
		return nil, err
	}

	if err := c.Watch(&source.Kind{Type: &operatorv1.IngressController{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return reconciler.hasSecret(e.Object, e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return reconciler.hasSecret(e.Object, e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return reconciler.secretChanged(e.ObjectOld, e.ObjectNew) },
		GenericFunc: func(e event.GenericEvent) bool { return reconciler.hasSecret(e.Object, e.Object) },
	}, predicate.NewPredicateFuncs(func(o client.Object) bool {
		return reconciler.hasClusterIngressDomain(o) || isDefaultIngressController(o)
	})); err != nil {
		return nil, err
	}

	return c, nil
}

// secretToIngressController maps a secret to a slice of reconcile requests,
// one request per ingresscontroller that references the secret.
func (r *reconciler) secretToIngressController(o client.Object) []reconcile.Request {
	var (
		requests []reconcile.Request
		list     operatorv1.IngressControllerList
		listOpts = client.MatchingFields(map[string]string{
			"defaultCertificateName": o.GetName(),
		})
		ingressConfig configv1.Ingress
	)
	if err := r.cache.List(context.Background(), &list, listOpts); err != nil {
		log.Error(err, "failed to list ingresscontrollers for secret", "secret", o.GetName())
		return requests
	}
	if err := r.cache.Get(context.Background(), controller.IngressClusterConfigName(), &ingressConfig); err != nil {
		log.Error(err, "failed to get ingresses.config.openshift.io", "name", controller.IngressClusterConfigName())
		return requests
	}
	for _, ic := range list.Items {
		if ic.Status.Domain != ingressConfig.Spec.Domain {
			continue
		}
		log.Info("queueing ingresscontroller", "name", ic.Name)
		request := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: ic.Namespace,
				Name:      ic.Name,
			},
		}
		requests = append(requests, request)
	}
	return requests
}

// hasSecret returns true if the effective default certificate secret for the
// given ingresscontroller exists, false otherwise.
func (r *reconciler) hasSecret(meta metav1.Object, o runtime.Object) bool {
	ic := o.(*operatorv1.IngressController)
	secretName := controller.RouterEffectiveDefaultCertificateSecretName(ic, r.operandNamespace)
	secret := &corev1.Secret{}
	if err := r.client.Get(context.Background(), secretName, secret); err != nil {
		if errors.IsNotFound(err) {
			return false
		}
		log.Error(err, "failed to look up secret for ingresscontroller", "name", secretName, "related", meta.GetSelfLink())
	}
	return true
}

// secretChanged returns true if the effective domain or effective default
// certificate secret for the given ingresscontroller has changed, false
// otherwise.
func (r *reconciler) secretChanged(old, new runtime.Object) bool {
	oldController := old.(*operatorv1.IngressController)
	newController := new.(*operatorv1.IngressController)
	oldSecret := controller.RouterEffectiveDefaultCertificateSecretName(oldController, r.operandNamespace)
	newSecret := controller.RouterEffectiveDefaultCertificateSecretName(newController, r.operandNamespace)
	oldStatus := oldController.Status.Domain
	newStatus := newController.Status.Domain
	return oldSecret != newSecret || oldStatus != newStatus
}

// hasClusterIngressDomain returns true if the effective domain for the given
// ingresscontroller is the cluster ingress domain.
func (r *reconciler) hasClusterIngressDomain(o client.Object) bool {
	var ingressConfig configv1.Ingress
	if err := r.cache.Get(context.Background(), controller.IngressClusterConfigName(), &ingressConfig); err != nil {
		log.Error(err, "failed to get ingresses.config.openshift.io", "name", controller.IngressClusterConfigName())
		// Assume it might be a match.  Better to reconcile an extra
		// time than to miss an update.
		return true
	}
	ic := o.(*operatorv1.IngressController)
	return ic.Status.Domain == ingressConfig.Spec.Domain
}

// isDefaultIngressController returns true if the given ingresscontroller is the
// "default" ingresscontroller.
func isDefaultIngressController(o client.Object) bool {
	return o.GetNamespace() == controller.DefaultOperatorNamespace && o.GetName() == manifests.DefaultIngressControllerName
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("Reconciling", "request", request)

	controllers := &operatorv1.IngressControllerList{}
	if err := r.cache.List(ctx, controllers, client.InNamespace(r.operatorNamespace)); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list ingresscontrollers: %v", err)
	}

	secrets := &corev1.SecretList{}
	if err := r.cache.List(ctx, secrets, client.InNamespace(r.operandNamespace)); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list secrets: %v", err)
	}

	var ingressConfig configv1.Ingress
	if err := r.cache.Get(ctx, controller.IngressClusterConfigName(), &ingressConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get ingresses.config.openshift.io %s: %v", controller.IngressClusterConfigName(), err)
	}

	if err := r.ensureRouterCertsGlobalSecret(secrets.Items, controllers.Items, &ingressConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to ensure global secret: %v", err)
	}

	// We need to construct the CA bundle that can be used to verify the ingress used to serve the console and the oauth-server.
	// In an operator maintained cluster, this is always `oc get -n openshift-ingress-operator ingresscontroller/default`, skip the rest and return here.
	// TODO if network-edge wishes to expand the scope of the CA bundle (and you could legitimately see a need/desire to have one CA that verifies all ingress traffic).
	// TODO this could be accomplished using union logic similar to the kube-apiserver's join of multiple CAs.
	if request.NamespacedName.Namespace == controller.DefaultOperatorNamespace && request.NamespacedName.Name == manifests.DefaultIngressControllerName {
		var defaultIngressController *operatorv1.IngressController
		for i := range controllers.Items {
			ic := &controllers.Items[i]
			if isDefaultIngressController(ic) {
				defaultIngressController = ic
				break
			}
		}
		if defaultIngressController == nil {
			return reconcile.Result{}, fmt.Errorf("failed to lookup default ingresscontroller %s does not exist", manifests.DefaultIngressControllerName)
		}

		var wildcardServingCertKeySecret *corev1.Secret
		secretName := controller.RouterEffectiveDefaultCertificateSecretName(defaultIngressController, controller.DefaultOperandNamespace)
		for i := range secrets.Items {
			secret := &secrets.Items[i]
			if secret.Namespace == secretName.Namespace && secret.Name == secretName.Name {
				wildcardServingCertKeySecret = secret
				break
			}
		}
		if wildcardServingCertKeySecret == nil {
			return reconcile.Result{}, fmt.Errorf("failed to lookup wildcard cert: secret %s does not exist", secretName)
		}

		caBundle := string(wildcardServingCertKeySecret.Data["tls.crt"])
		if err := r.ensureDefaultIngressCertConfigMap(caBundle); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to publish router CA: %w", err)
		}
	}

	return reconcile.Result{}, nil
}
