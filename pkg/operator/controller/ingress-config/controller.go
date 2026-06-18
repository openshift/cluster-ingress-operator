package ingressconfig

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayapi/managementmode"
	"github.com/openshift/library-go/pkg/operator/events"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/client-go/kubernetes"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const ControllerName = "ingress_config_controller"

var log = logf.Logger.WithName(ControllerName)

// reconciler owns Gateway API management mode transitions, VAP lifecycle, and
// Ingress operator config status conditions.
type reconciler struct {
	config        Config
	client        client.Client
	cache         cache.Cache
	kclient       kubernetes.Interface
	eventRecorder events.Recorder
}

// New creates the ingress-config controller, which watches the Ingress operator
// config singleton and drives Managed/Unmanaged mode transitions.
func New(mgr manager.Manager, config Config, eventRecorder events.Recorder) (controller.Controller, error) {
	if err := managementmode.ValidateManagementModeConfig(config.GatewayAPIManagementModeEnabled, config.StateStore); err != nil {
		return nil, err
	}
	kubeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %w", err)
	}

	operatorCache := mgr.GetCache()
	r := &reconciler{
		config:        config,
		client:        mgr.GetClient(),
		cache:         operatorCache,
		kclient:       kubeClient,
		eventRecorder: eventRecorder,
	}

	c, err := controller.New(ControllerName, mgr, controller.Options{Reconciler: r})
	if err != nil {
		return nil, err
	}

	clusterSingletonPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == operatorcontroller.IngressOperatorConfigClusterName().Name
	})

	if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1alpha1.Ingress{}, &handler.EnqueueRequestForObject{}, clusterSingletonPredicate)); err != nil {
		return nil, err
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, &configv1.FeatureGate{}, &handler.EnqueueRequestForObject{}, predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == operatorcontroller.FeatureGateClusterConfigName().Name
	}))); err != nil {
		return nil, err
	}

	toIngressConfig := func(ctx context.Context, _ client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: operatorcontroller.IngressOperatorConfigClusterName()}}
	}
	isGatewayAPICRD := func(o client.Object) bool {
		crd, ok := o.(*apiextensionsv1.CustomResourceDefinition)
		return ok && crd.Spec.Group == gatewayapiv1.GroupName
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(toIngressConfig), predicate.NewPredicateFuncs(isGatewayAPICRD))); err != nil {
		return nil, fmt.Errorf("failed to watch gateway API CRDs: %w", err)
	}

	return c, nil
}
