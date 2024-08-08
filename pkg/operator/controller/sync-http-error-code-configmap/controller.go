package sync_http_error_code_configmap

import (
	"context"
	"fmt"
	"strings"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

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
	controllerName = "error_page_configmap_controller"
)

var log = logf.Logger.WithName(controllerName)

// New creates a new controller that syncs HTTP error page configmaps between
// namespaces.
func New(mgr manager.Manager, config Config) (runtimecontroller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		cache:    operatorCache,
		client:   mgr.GetClient(),
		config:   config,
		recorder: mgr.GetEventRecorderFor(controllerName),
	}
	c, err := runtimecontroller.New(controllerName, mgr, runtimecontroller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	// If the ingresscontroller's error-page configmap reference changes,
	// reconcile the ingresscontroller.
	if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1.IngressController{}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return reconciler.hasConfigMap(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return reconciler.hasConfigMap(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return reconciler.configMapChanged(e.ObjectOld, e.ObjectNew) },
		GenericFunc: func(e event.GenericEvent) bool { return reconciler.hasConfigMap(e.Object) },
	})); err != nil {
		return nil, err
	}

	// Index ingresscontrollers by spec.httpErrorCodePages.name so that
	// configmapToIngressController and configmapIsInUse can look up
	// ingresscontrollers that reference the configmap.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&operatorv1.IngressController{},
		"spec.httpErrorCodePages.name",
		client.IndexerFunc(func(o client.Object) []string {
			ic := o.(*operatorv1.IngressController)
			if len(ic.Spec.HttpErrorCodePages.Name) == 0 {
				return []string{}
			}
			return []string{ic.Spec.HttpErrorCodePages.Name}
		})); err != nil {
		return nil, fmt.Errorf("failed to create index for ingresscontroller: %w", err)
	}

	isInNamespace := func(namespace string) func(o client.Object) bool {
		return func(o client.Object) bool {
			return o.GetNamespace() == namespace
		}
	}
	// If a configmap in the source namespace that is referenced by an
	// ingresscontroller changes, reconcile the ingresscontroller.
	if err := c.Watch(source.Kind[client.Object](operatorCache, &corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(reconciler.userConfigMapToIngressController), predicate.NewPredicateFuncs(isInNamespace(config.ConfigNamespace)), predicate.NewPredicateFuncs(reconciler.configmapIsInUse))); err != nil {
		return nil, err
	}

	// If a configmap in the destination (operand) namespace that is used by
	// an ingresscontroller's deployment changes, reconcile the
	// ingresscontroller.
	if err := c.Watch(source.Kind[client.Object](operatorCache, &corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(reconciler.operatorConfigMapToIngressController), predicate.NewPredicateFuncs(isInNamespace(config.OperandNamespace)))); err != nil {
		return nil, err
	}

	return c, nil
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	OperatorNamespace string
	ConfigNamespace   string
	OperandNamespace  string
}

type reconciler struct {
	cache    cache.Cache
	client   client.Client
	config   Config
	recorder record.EventRecorder
}

// userConfigMapToIngressController maps a user-created error-page configmap to
// a slice of reconcile requests, one request per ingresscontroller that
// references the configmap for custom error pages.
func (r *reconciler) userConfigMapToIngressController(ctx context.Context, o client.Object) []reconcile.Request {
	requests := []reconcile.Request{}
	controllers, err := r.ingressControllersWithConfigMap(ctx, o.GetName())
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

// operatorConfigMapToIngressController maps an operator-created error-page
// configmap to a slice of reconcile requests, one request per ingresscontroller
// that references the configmap for custom error pages.
func (r *reconciler) operatorConfigMapToIngressController(ctx context.Context, o client.Object) []reconcile.Request {
	configmapName := o.GetName()
	if !strings.HasSuffix(configmapName, "-errorpages") {
		return []reconcile.Request{}
	}
	icName := strings.TrimSuffix(configmapName, "-errorpages")
	icNamespace := r.config.OperatorNamespace
	log.Info("queueing ingresscontroller", "name", icName, "related", o.GetSelfLink())
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: icNamespace,
			Name:      icName,
		},
	}}
}

// ingressControllersWithConfigMap returns the ingresscontrollers that reference
// the given configmap for custom error pages.
func (r *reconciler) ingressControllersWithConfigMap(ctx context.Context, configmapName string) ([]operatorv1.IngressController, error) {
	controllers := &operatorv1.IngressControllerList{}
	if err := r.cache.List(ctx, controllers, client.MatchingFields{"spec.httpErrorCodePages.name": configmapName}); err != nil {
		return nil, err
	}
	names := []string{}
	for _, ic := range controllers.Items {
		names = append(names, ic.Name)
	}
	return controllers.Items, nil
}

// configmapIsInUse returns true if the given configmap is used for custom error
// pages by some ingresscontroller.
func (r *reconciler) configmapIsInUse(o client.Object) bool {
	controllers, err := r.ingressControllersWithConfigMap(context.Background(), o.GetName())
	if err != nil {
		log.Error(err, "failed to list ingresscontrollers for configmap", "related", o.GetSelfLink())
		return false
	}
	return len(controllers) > 0
}

// hasConfigMap returns true if the given ingresscontroller specifies a
// configmap for custom error pages, false otherwise.
func (r *reconciler) hasConfigMap(o client.Object) bool {
	ic := o.(*operatorv1.IngressController)
	return len(ic.Spec.HttpErrorCodePages.Name) != 0
}

// configMapChanged returns true if the name of configmap that the given
// ingresscontroller uses for custom error pages has changed, false otherwise.
func (r *reconciler) configMapChanged(old, new runtime.Object) bool {
	oldController := old.(*operatorv1.IngressController)
	newController := new.(*operatorv1.IngressController)
	oldName := oldController.Spec.HttpErrorCodePages.Name
	newName := newController.Spec.HttpErrorCodePages.Name
	return oldName != newName
}

// Reconcile reconciles an ingresscontroller and its associated error-page
// configmap, if it specifies one.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	ingress := &operatorv1.IngressController{}
	if err := r.client.Get(ctx, request.NamespacedName, ingress); err != nil {
		if errors.IsNotFound(err) {
			log.Info("ingresscontroller not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get ingresscontroller: %w", err)
	}
	deployment := &appsv1.Deployment{}
	if err := r.client.Get(ctx, controller.RouterDeploymentName(ingress), deployment); err != nil {
		if errors.IsNotFound(err) {
			log.Info("deployment not found; will retry configmap sync", "ingresscontroller", ingress.Name)
			return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get deployment: %w", err)
	}
	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: appsv1.SchemeGroupVersion.String(),
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
		Controller: &trueVar,
	}
	controllers := &operatorv1.IngressControllerList{}
	if err := r.cache.List(ctx, controllers, client.InNamespace(r.config.OperatorNamespace)); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list ingresscontrollers: %w", err)
	}
	if _, _, err := r.ensureHttpErrorCodeConfigMap(ingress, deploymentRef); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to ensure errorpage configmap for ingresscontroller %q: %w", ingress.Name, err)
	}
	return reconcile.Result{}, nil
}
