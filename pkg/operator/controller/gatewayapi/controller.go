package gatewayapi

import (
	"context"
	"sync"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/client-go/tools/record"

	configv1 "github.com/openshift/api/config/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	controllerName = "gatewayapi_controller"
)

var log = logf.Logger.WithName(controllerName)

// New creates and returns a controller that creates Gateway API CRDs when the
// appropriate featuregate is enabled.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		client: mgr.GetClient(),
		config: config,
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	clusterNamePredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		expectedName := operatorcontroller.FeatureGateClusterConfigName()
		actualName := types.NamespacedName{
			Namespace: o.GetNamespace(),
			Name:      o.GetName(),
		}
		return expectedName == actualName
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &configv1.FeatureGate{}, &handler.EnqueueRequestForObject{}, clusterNamePredicate)); err != nil {
		return nil, err
	}

	toFeatureGate := func(ctx context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: operatorcontroller.FeatureGateClusterConfigName(),
		}}
	}

	// watch for CRDs
	crdPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.(*apiextensionsv1.CustomResourceDefinition).Spec.Group == gatewayapiv1.GroupName
	})

	if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(toFeatureGate), crdPredicate)); err != nil {
		return nil, err
	}
	return c, nil
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	// GatewayAPIEnabled indicates that the "GatewayAPI" featuregate is enabled.
	GatewayAPIEnabled bool
	// GatewayAPIControllerEnabled indicates that the "GatewayAPIController" featuregate is enabled.
	GatewayAPIControllerEnabled bool

	// DependentControllers is a list of controllers that watch Gateway API
	// resources.  The gatewayapi controller starts these controllers once
	// the Gateway API CRDs have been created.
	DependentControllers []controller.Controller
}

// reconciler reconciles gatewayclasses.
type reconciler struct {
	config Config

	client           client.Client
	recorder         record.EventRecorder
	startControllers sync.Once
}

// Reconcile expects request to refer to a FeatureGate and creates or
// reconciles the Gateway API CRDs.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	if !r.config.GatewayAPIEnabled {
		return reconcile.Result{}, nil
	}

	if err := r.ensureGatewayAPICRDs(ctx); err != nil {
		return reconcile.Result{}, err
	}

	if !r.config.GatewayAPIControllerEnabled {
		return reconcile.Result{}, nil
	}

	r.startControllers.Do(func() {
		for i := range r.config.DependentControllers {
			c := &r.config.DependentControllers[i]
			go func() {
				if err := (*c).Start(ctx); err != nil {
					log.Error(err, "cannot start controller")
				}
			}()
		}
	})

	return reconcile.Result{}, nil
}
