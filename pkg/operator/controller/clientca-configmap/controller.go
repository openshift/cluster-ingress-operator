package clientcaconfigmap

import (
	"context"
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	controllerName = "clientca_configmap_controller"
)

var log = logf.Logger.WithName(controllerName)

// New creates a new controller that syncs client CA configmaps between the
// config and operand namespaces.  This controller also adds a finalizer to the
// IngressController so that the controller can delete the configmap from the
// operand namespace when the IngressController is marked for deletion.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		cache:  operatorCache,
		client: mgr.GetClient(),
		config: config,
	}
	c, err := controller.New(controllerName, mgr, controller.Options{
		Reconciler: reconciler,
	})
	if err != nil {
		return nil, err
	}

	hasConfigMap := func(o client.Object) bool {
		ic := o.(*operatorv1.IngressController)
		return len(ic.Spec.ClientTLS.ClientCA.Name) != 0
	}

	// If the ingresscontroller's configmap reference changes, reconcile the
	// ingresscontroller.
	if err := c.Watch(
		source.Kind(operatorCache, &operatorv1.IngressController{}),
		&handler.EnqueueRequestForObject{},
		predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return hasConfigMap(e.Object)
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return hasConfigMap(e.Object)
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldIC := e.ObjectOld.(*operatorv1.IngressController)
				newIC := e.ObjectNew.(*operatorv1.IngressController)
				oldName := oldIC.Spec.ClientTLS.ClientCA.Name
				newName := newIC.Spec.ClientTLS.ClientCA.Name
				return oldName != newName ||
					oldIC.DeletionTimestamp != newIC.DeletionTimestamp
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return hasConfigMap(e.Object)
			},
		},
	); err != nil {
		return nil, err
	}

	// Index ingresscontrollers by spec.clientTLS.clientCA.name so that we
	// can look up the ingresscontroller when the configmap is changed.
	const clientCAUserConfigmapIndexFieldName = "clientCAUserConfigmapName"
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&operatorv1.IngressController{},
		clientCAUserConfigmapIndexFieldName,
		client.IndexerFunc(func(o client.Object) []string {
			ic := o.(*operatorv1.IngressController)
			if len(ic.Spec.ClientTLS.ClientCA.Name) == 0 {
				return []string{}
			}
			return []string{ic.Spec.ClientTLS.ClientCA.Name}
		}),
	); err != nil {
		return nil, fmt.Errorf("failed to create index for user-managed client CA configmaps: %w", err)
	}

	// Index ingresscontrollers by their corresponding operator-managed
	// client CA configmaps so that we can look up the ingresscontroller
	// when the configmap is changed.
	const clientCAOperatorConfigmapIndexFieldName = "clientCAOperatorConfigmapName"
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&operatorv1.IngressController{},
		clientCAOperatorConfigmapIndexFieldName,
		client.IndexerFunc(func(o client.Object) []string {
			ic := o.(*operatorv1.IngressController)
			return []string{operatorcontroller.ClientCAConfigMapName(ic).Name}
		}),
	); err != nil {
		return nil, fmt.Errorf("failed to create index for operator-managed client CA configmaps: %w", err)
	}

	makeMapFunc := func(indexKey string) handler.MapFunc {
		return func(ctx context.Context, o client.Object) []reconcile.Request {
			controllers := &operatorv1.IngressControllerList{}
			listOpts := client.MatchingFields{indexKey: o.GetName()}
			requests := []reconcile.Request{}
			if err := reconciler.cache.List(ctx, controllers, listOpts); err != nil {
				log.Error(err, "failed to list ingresscontrollers for configmap")
				return requests
			}
			for _, ic := range controllers.Items {
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
	}

	isInNS := func(namespace string) func(o client.Object) bool {
		return func(o client.Object) bool {
			return o.GetNamespace() == namespace
		}
	}

	userCMToIC := makeMapFunc(clientCAUserConfigmapIndexFieldName)
	if err := c.Watch(
		source.Kind(operatorCache, &corev1.ConfigMap{}),
		handler.EnqueueRequestsFromMapFunc(userCMToIC),
		predicate.NewPredicateFuncs(isInNS(config.SourceNamespace)),
	); err != nil {
		return nil, err
	}

	operatorCMToIC := makeMapFunc(clientCAOperatorConfigmapIndexFieldName)
	if err := c.Watch(
		source.Kind(operatorCache, &corev1.ConfigMap{}),
		handler.EnqueueRequestsFromMapFunc(operatorCMToIC),
		predicate.NewPredicateFuncs(isInNS(config.TargetNamespace)),
	); err != nil {
		return nil, err
	}

	return c, nil
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	OperatorNamespace string
	SourceNamespace   string
	TargetNamespace   string
}

type reconciler struct {
	cache  cache.Cache
	client client.Client
	config Config
}

// Reconcile reconciles an ingresscontroller and its associated client CA
// configmap, if it specifies one.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	ic := &operatorv1.IngressController{}
	if err := r.client.Get(ctx, request.NamespacedName, ic); err != nil {
		if errors.IsNotFound(err) {
			log.Info("ingresscontroller not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get ingresscontroller %q: %w", request.NamespacedName, err)
	}

	const finalizer = "ingresscontroller.operator.openshift.io/finalizer-clientca-configmap"
	if len(ic.Spec.ClientTLS.ClientCA.Name) != 0 && ic.DeletionTimestamp == nil && !slice.ContainsString(ic.Finalizers, finalizer) {
		// Ensure the ingresscontroller has a finalizer so we get a
		// chance to delete the configmap when the ingresscontroller is
		// deleted.
		//
		// For other resources, we set an owner reference with the
		// router deployment as the owner so those resources get
		// automatically cleaned up when the deployment is deleted, but
		// we need to create the client CA configmap before creating the
		// deployment, so we must use a finalizer instead.
		ic.Finalizers = append(ic.Finalizers, finalizer)
		if err := r.client.Update(ctx, ic); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to add client-ca-configmap finalizer: %w", err)
		}
		log.Info("added client-ca-configmap finalizer", "request", request)
		if err := r.client.Get(ctx, request.NamespacedName, ic); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to get updated ingresscontroller: %w", err)
		}
	}

	if _, _, err := r.ensureClientCAConfigMap(ctx, ic); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to ensure client CA configmap for ingresscontroller %q: %w", ic.Name, err)
	}

	if ic.DeletionTimestamp != nil && slice.ContainsString(ic.Finalizers, finalizer) {
		ic.Finalizers = slice.RemoveString(ic.Finalizers, finalizer)
		if err := r.client.Update(ctx, ic); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to remove client-ca-configmap finalizer: %w", err)
		}
		log.Info("removed client-ca-configmap finalizer", "request", request)
	}

	return reconcile.Result{}, nil
}
