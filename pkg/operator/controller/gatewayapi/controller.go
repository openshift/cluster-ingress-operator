package gatewayapi

import (
	"context"
	"sync"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/client-go/tools/record"

	configv1 "github.com/openshift/api/config/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	controllerName = "gatewayapi_controller"

	// featureGateName is the name of the feature gate that enables Gateway
	// API support in cluster-ingress-operator.
	featureGateName = "GatewayAPI"
)

var log = logf.Logger.WithName(controllerName)

// New creates and returns a controller that creates Gateway API CRDs when the
// appropriate featuregate is enabled.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	reconciler := &reconciler{
		client: mgr.GetClient(),
		cache:  mgr.GetCache(),
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
	if err := c.Watch(&source.Kind{Type: &configv1.FeatureGate{}}, &handler.EnqueueRequestForObject{}, clusterNamePredicate); err != nil {
		return nil, err
	}
	return c, nil
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	// DependentControllers is a list of controllers that watch Gateway API
	// resources.  The gatewayapi controller starts these controllers once
	// the Gateway API CRDs have been created.
	DependentControllers []controller.Controller
}

// reconciler reconciles gatewayclasses.
type reconciler struct {
	config Config

	client           client.Client
	cache            cache.Cache
	recorder         record.EventRecorder
	startControllers sync.Once
}

// Reconcile expects request to refer to a FeatureGate and creates or
// reconciles the Gateway API CRDs.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	var fg configv1.FeatureGate
	if err := r.cache.Get(ctx, request.NamespacedName, &fg); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("featuregate not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if !featureIsEnabled(featureGateName, &fg) {
		return reconcile.Result{}, nil
	}

	if err := r.ensureGatewayAPICRDs(ctx); err != nil {
		return reconcile.Result{}, err
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

// featureIsEnabled takes a feature name and a featuregate config API object and
// returns a Boolean indicating whether the named feature is enabled.
//
// This function determines whether a named feature is enabled as follows:
//
//   - First, if the featuregate's spec.featureGateSelection.featureSet field is
//     set to "CustomNoUpgrade", then the feature is enabled if, and only if, it
//     is specified in spec.featureGateSelection.customNoUpgrade.enabled.
//
//   - Second, if spec.featureGateSelection.featureSet is set to a value that
//     isn't defined in configv1.FeatureSets, then the feature is *not* enabled.
//
//   - Finally, the feature is enabled if, and only if, the feature is specified
//     in configv1.FeatureSets[spec.featureGateSelection.featureSet].enabled.
func featureIsEnabled(feature string, fg *configv1.FeatureGate) bool {
	if fg.Spec.FeatureSet == configv1.CustomNoUpgrade {
		if fg.Spec.FeatureGateSelection.CustomNoUpgrade == nil {
			return false
		}
		for _, f := range fg.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled {
			if f == feature {
				return true
			}
		}
		return false
	}

	if fs, ok := configv1.FeatureSets[fg.Spec.FeatureSet]; ok {
		for _, f := range fs.Enabled {
			if f == feature {
				return true
			}
		}
	}
	return false
}
