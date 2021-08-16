package crl

import (
	"context"
	"fmt"
	"time"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	operatorv1 "github.com/openshift/api/operator/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "crl"

	clientCAConfigmapIndexFieldName = "clientCAConfigmapName"
)

var log = logf.Logger.WithName(controllerName)

type reconciler struct {
	client client.Client
	cache  cache.Cache
}

// New returns a new controller that manages a certificate revocation list
// configmap for each ingress controller that has any client CA certificates
// that specify a CRL distribution point.
func New(mgr manager.Manager) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		client: mgr.GetClient(),
		cache:  operatorCache,
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	// Index ingresscontrollers over the client CA configmap name so that
	// configmapToIngressController can look up ingresscontrollers that
	// reference the client CA configmap.
	if err := operatorCache.IndexField(context.Background(), &operatorv1.IngressController{}, clientCAConfigmapIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
		ic := o.(*operatorv1.IngressController)
		if len(ic.Spec.ClientTLS.ClientCA.Name) == 0 {
			return []string{}
		}
		return []string{operatorcontroller.ClientCAConfigMapName(ic).Name}
	})); err != nil {
		return nil, fmt.Errorf("failed to create index for ingresscontroller: %w", err)
	}

	configmapsInformer, err := operatorCache.GetInformer(context.Background(), &corev1.ConfigMap{})
	if err != nil {
		return nil, fmt.Errorf("failed to create informer for configmaps: %w", err)
	}
	if err := c.Watch(&source.Informer{Informer: configmapsInformer}, handler.EnqueueRequestsFromMapFunc(reconciler.configmapToIngressController)); err != nil {
		return nil, err
	}

	if err := c.Watch(&source.Kind{Type: &operatorv1.IngressController{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return reconciler.hasConfigmap(e.Object, e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return false },
		UpdateFunc:  func(e event.UpdateEvent) bool { return reconciler.configmapReferenceChanged(e.ObjectOld, e.ObjectNew) },
		GenericFunc: func(e event.GenericEvent) bool { return reconciler.hasConfigmap(e.Object, e.Object) },
	}); err != nil {
		return nil, err
	}

	return c, nil
}

// ingressControllersWithConfigmap returns the ingresscontrollers that reference
// the specified client CA configmap in the "openshift-ingress" operand
// namespace.
func (r *reconciler) ingressControllersWithConfigmap(name string) ([]operatorv1.IngressController, error) {
	controllers := &operatorv1.IngressControllerList{}
	listOpts := client.MatchingFields(map[string]string{
		clientCAConfigmapIndexFieldName: name,
	})
	if err := r.cache.List(context.Background(), controllers, listOpts); err != nil {
		return nil, err
	}
	return controllers.Items, nil
}

// configmapToIngressController maps a configmap to a slice of reconcile requests,
// one request per ingresscontroller that references the configmap.
func (r *reconciler) configmapToIngressController(o client.Object) []reconcile.Request {
	requests := []reconcile.Request{}
	if o.GetNamespace() != operatorcontroller.DefaultOperandNamespace {
		return requests
	}
	controllers, err := r.ingressControllersWithConfigmap(o.GetName())
	if err != nil {
		log.Error(err, "failed to list ingresscontrollers for configmap", "related", o.GetSelfLink())
		return requests
	}
	for _, ic := range controllers {
		log.Info("queueing ingresscontroller", "name", ic.Name, "related", o.GetSelfLink())
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

// hasConfigmap returns true if a client CA configmap for the given
// ingresscontroller exists, false otherwise.
func (r *reconciler) hasConfigmap(meta metav1.Object, o runtime.Object) bool {
	ic := o.(*operatorv1.IngressController)
	name := operatorcontroller.ClientCAConfigMapName(ic)
	if len(name.Name) == 0 {
		return false
	}
	cm := &corev1.ConfigMap{}
	if err := r.client.Get(context.Background(), name, cm); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "failed to look up configmap for ingresscontroller", "name", name, "related", meta.GetSelfLink())
		}
		return false
	}
	return true
}

// configmapReferenceChanged returns true if the client CA configmap reference
// for the given ingresscontroller has changed, false otherwise.
func (r *reconciler) configmapReferenceChanged(old, new runtime.Object) bool {
	oldController := old.(*operatorv1.IngressController)
	newController := new.(*operatorv1.IngressController)
	oldConfigmap := oldController.Spec.ClientTLS.ClientCA.Name
	newConfigmap := newController.Spec.ClientTLS.ClientCA.Name
	return oldConfigmap != newConfigmap
}

// Reconcile processes a request to reconcile an ingresscontroller.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("Reconciling", "request", request)

	ic := &operatorv1.IngressController{}
	if err := r.cache.Get(ctx, request.NamespacedName, ic); err != nil {
		if errors.IsNotFound(err) {
			// When we create a client CA CRL configmap, we set an
			// owner reference so it gets cleaned up automatically.
			// Thus no further cleanup is necessary.
			log.Info("ingresscontroller not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get ingresscontroller %q: %w", request.NamespacedName, err)
	}

	deployment := &appsv1.Deployment{}
	if err := r.cache.Get(ctx, operatorcontroller.RouterDeploymentName(ic), deployment); err != nil {
		if errors.IsNotFound(err) {
			log.Info("deployment not found; will retry client CA CRL sync", "ingresscontroller", ic.Name)
			return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get deployment for ingresscontroller %q: %w", request.NamespacedName, err)
	}

	trueVar := true
	ownerRef := metav1.OwnerReference{
		APIVersion: appsv1.SchemeGroupVersion.String(),
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
		Controller: &trueVar,
	}

	var haveCAConfigmap bool
	clientCAConfigmapName := operatorcontroller.ClientCAConfigMapName(ic)
	clientCAConfigmap := &corev1.ConfigMap{}
	if err := r.cache.Get(ctx, clientCAConfigmapName, clientCAConfigmap); err != nil {
		if !errors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("failed to get client CA configmap %s for ingresscontroller %s: %w", clientCAConfigmapName, request.NamespacedName, err)
		}
	} else {
		haveCAConfigmap = true
	}

	// TODO Consider letting ensureCRLConfigmap get the deployment and build
	// the owner reference as we don't know yet whether we need it.
	if _, _, err := r.ensureCRLConfigmap(ctx, ic, deployment.Namespace, ownerRef, haveCAConfigmap, clientCAConfigmap); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to ensure client CA CRL configmap for ingresscontroller %s: %w", request.NamespacedName, err)
	}

	// TODO Maybe parse the CRLs and set RequeueAfter based on the earliest
	// expiration date.

	return reconcile.Result{}, nil
}
