package cabundleconfigmap

import (
	"context"
	"fmt"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	"github.com/openshift/library-go/pkg/operator/events"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// controllerName is the name to be used by the logger.
	controllerName = "cabundle_configmap_controller"
)

var log = logf.Logger.WithName(controllerName)

// Config holds all the things necessary for the controller to run.
type Config struct {
	ServiceCAConfigMapName types.NamespacedName
	AdminCAConfigMapName   types.NamespacedName
	IngressCAConfigMapName types.NamespacedName
}

// reconciler holds all the things necessary for the reconciliation to happen.
type reconciler struct {
	cache         cache.Cache
	client        client.Client
	config        Config
	eventRecorder events.Recorder
}

// New creates a new controller that watches `admin-ca-bundle` configmap in
// `openshift-config` namespace and `service-ca-bundle` configmap in `openshift-ingress`
// namespace and creates a new configmap `ingress-ca-bundle` combining aforementioned
// configmaps in `openshift-ingress` namespace.
func New(mgr manager.Manager, config Config, eventRecorder events.Recorder) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		cache:         operatorCache,
		client:        mgr.GetClient(),
		config:        config,
		eventRecorder: eventRecorder,
	}
	c, err := controller.New(controllerName, mgr, controller.Options{
		Reconciler: reconciler,
	})
	if err != nil {
		return nil, err
	}

	matchesName := func(name types.NamespacedName) func(o client.Object) bool {
		return func(o client.Object) bool {
			return o.GetNamespace() == name.Namespace &&
				o.GetName() == name.Name
		}
	}

	// watch the Service CA configmap for any changes.
	if err := c.Watch(
		source.Kind(operatorCache, &corev1.ConfigMap{}),
		&handler.EnqueueRequestForObject{},
		predicate.NewPredicateFuncs(matchesName(config.ServiceCAConfigMapName)),
	); err != nil {
		return nil, err
	}

	// watch the Admin CA configmap for any changes.
	if err := c.Watch(
		source.Kind(operatorCache, &corev1.ConfigMap{}),
		&handler.EnqueueRequestForObject{},
		predicate.NewPredicateFuncs(matchesName(config.AdminCAConfigMapName)),
	); err != nil {
		return nil, err
	}

	// watch the Ingress CA configmap created by this controller for any changes.
	if err := c.Watch(
		source.Kind(operatorCache, &corev1.ConfigMap{}),
		&handler.EnqueueRequestForObject{},
		predicate.NewPredicateFuncs(matchesName(config.IngressCAConfigMapName)),
	); err != nil {
		return nil, err
	}

	return c, nil
}

// Reconcile reconciles the `ingress-ca-bundle` configmap in the DefaultOperandNamespace namespace
// which is created by combining the contents of `admin-ca-bundle` and `service-ca-bundle`
// configmaps in GlobalUserSpecifiedConfigNamespace and DefaultOperandNamespace namespace.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling ingress-ca-bundle", "request", request)

	if err := r.ensureIngressCABundleConfigMap(ctx); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to ensure ingress CA bundle configmap: %w", err)
	}

	return reconcile.Result{}, nil
}
