// The certificate-publisher controller is responsible for publishing in-use
// certificates to the "router-certs" secret in the "openshift-config-managed"
// namespace.
package certificatepublisher

import (
	"context"
	"fmt"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/client-go/tools/record"

	corev1 "k8s.io/api/core/v1"

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
// openshift-config-managed namespace with all in-use default certificates.
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
	// secretIsInUse and secretToIngressController can look up
	// ingresscontrollers that reference the secret.
	if err := operatorCache.IndexField(context.Background(), &operatorv1.IngressController{}, "defaultCertificateName", func(o runtime.Object) []string {
		secret := controller.RouterEffectiveDefaultCertificateSecretName(o.(*operatorv1.IngressController), operandNamespace)
		return []string{secret.Name}
	}); err != nil {
		return nil, fmt.Errorf("failed to create index for ingresscontroller: %v", err)
	}

	secretsInformer, err := operatorCache.GetInformer(context.Background(), &corev1.Secret{})
	if err != nil {
		return nil, fmt.Errorf("failed to create informer for secrets: %v", err)
	}
	if err := c.Watch(&source.Informer{Informer: secretsInformer}, &handler.EnqueueRequestsFromMapFunc{ToRequests: handler.ToRequestsFunc(reconciler.secretToIngressController)}, predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return reconciler.secretIsInUse(e.Meta) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return reconciler.secretIsInUse(e.Meta) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return reconciler.secretIsInUse(e.MetaNew) },
		GenericFunc: func(e event.GenericEvent) bool { return reconciler.secretIsInUse(e.Meta) },
	}); err != nil {
		return nil, err
	}

	if err := c.Watch(&source.Kind{Type: &operatorv1.IngressController{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return reconciler.hasSecret(e.Meta, e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return reconciler.hasSecret(e.Meta, e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return reconciler.secretChanged(e.ObjectOld, e.ObjectNew) },
		GenericFunc: func(e event.GenericEvent) bool { return reconciler.hasSecret(e.Meta, e.Object) },
	}); err != nil {
		return nil, err
	}

	return c, nil
}

// secretToIngressController maps a secret to a slice of reconcile requests,
// one request per ingresscontroller that references the secret.
func (r *reconciler) secretToIngressController(o handler.MapObject) []reconcile.Request {
	requests := []reconcile.Request{}
	controllers, err := r.ingressControllersWithSecret(o.Meta.GetName())
	if err != nil {
		log.Error(err, "failed to list ingresscontrollers for secret", "related", o.Meta.GetSelfLink())
		return requests
	}
	for _, ic := range controllers {
		log.Info("queueing ingresscontroller", "name", ic.Name, "related", o.Meta.GetSelfLink())
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

// ingressControllersWithSecret returns the ingresscontrollers that reference
// the given secret.
func (r *reconciler) ingressControllersWithSecret(secretName string) ([]operatorv1.IngressController, error) {
	controllers := &operatorv1.IngressControllerList{}
	if err := r.cache.List(context.Background(), controllers, client.MatchingField("defaultCertificateName", secretName)); err != nil {
		return nil, err
	}
	return controllers.Items, nil
}

// secretIsInUse returns true if the given secret is referenced by some
// ingresscontroller.
func (r *reconciler) secretIsInUse(meta metav1.Object) bool {
	controllers, err := r.ingressControllersWithSecret(meta.GetName())
	if err != nil {
		log.Error(err, "failed to list ingresscontrollers for secret", "related", meta.GetSelfLink())
		return false
	}
	return len(controllers) > 0
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

func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Info("Reconciling", "request", request)

	controllers := &operatorv1.IngressControllerList{}
	if err := r.cache.List(context.TODO(), controllers, client.InNamespace(r.operatorNamespace)); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list ingresscontrollers: %v", err)
	}

	secrets := &corev1.SecretList{}
	if err := r.cache.List(context.TODO(), secrets, client.InNamespace(r.operandNamespace)); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list secrets: %v", err)
	}

	if err := r.ensureRouterCertsGlobalSecret(secrets.Items, controllers.Items); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to ensure global secret: %v", err)
	}

	return reconcile.Result{}, nil
}
