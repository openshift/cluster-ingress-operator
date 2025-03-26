package crd

import (
	"context"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "crd_controller"
)

var log = logf.Logger.WithName(controllerName)

func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		config: config,
		client: mgr.GetClient(),
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, &handler.EnqueueRequestForObject{}, predicate.GenerationChangedPredicate{})); err != nil {
		return nil, err
	}
	return c, nil
}

type ControllerFunc func() (controller.Controller, error)

// Config holds all the things necessary for the controller to run.
type Config struct {
	Mappings map[metav1.GroupKind]ControllerFunc
}

// state holds the internal controller state, started/failed or not.
type state struct {
	controller controller.Controller
	err        error
}

type reconciler struct {
	config Config

	client client.Client
	state  map[metav1.GroupKind]state
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	if r.state == nil {
		r.state = map[metav1.GroupKind]state{}
	}

	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := r.client.Get(ctx, request.NamespacedName, crd); err != nil {
		return reconcile.Result{}, err
	}

	key := metav1.GroupKind{Group: crd.Spec.Group, Kind: crd.Spec.Names.Kind}
	if ctrlFunc, configured := r.config.Mappings[key]; configured {
		if _, running := r.state[key]; running {
			return reconcile.Result{}, nil // already started, assume all done
		}

		controller, err := ctrlFunc()
		if err != nil {
			return reconcile.Result{}, err // failed to create controller, retry
		}
		r.state[key] = state{controller: controller, err: err}
	}
	return reconcile.Result{}, nil
}
