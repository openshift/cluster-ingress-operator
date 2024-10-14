package gatewayclass

import (
	"context"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	"k8s.io/client-go/tools/record"

	gatewayapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"

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
	controllerName = "gatewayclass_controller"

	// OpenShiftGatewayClassControllerName is the string by which a
	// gatewayclass identifies itself as belonging to OpenShift Istio.  If a
	// gatewayclass's spec.controllerName field is set to this value, then
	// the gatewayclass is ours.
	OpenShiftGatewayClassControllerName = "openshift.io/gateway-controller"
	// OpenShiftDefaultGatewayClassName is the name of the default
	// gatewayclass that Istio creates when it is installed.
	OpenShiftDefaultGatewayClassName = "openshift-default"
)

var log = logf.Logger.WithName(controllerName)

// NewUnmanaged creates and returns a controller that watches gatewayclasses and
// installs and configures Istio.  This is an unmanaged controller, which means
// that the manager does not start it.
func NewUnmanaged(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		config:   config,
		client:   mgr.GetClient(),
		cache:    operatorCache,
		recorder: mgr.GetEventRecorderFor(controllerName),
	}
	c, err := controller.NewUnmanaged(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}
	isOurGatewayClass := predicate.NewPredicateFuncs(func(o client.Object) bool {
		class := o.(*gatewayapiv1beta1.GatewayClass)
		return class.Spec.ControllerName == OpenShiftGatewayClassControllerName
	})
	isIstioGatewayClass := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == "istio"
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1beta1.GatewayClass{}, &handler.EnqueueRequestForObject{}, isOurGatewayClass, predicate.Not(isIstioGatewayClass))); err != nil {
		return nil, err
	}
	isOurInstallPlan := predicate.NewPredicateFuncs(func(o client.Object) bool {
		installPlan := o.(*operatorsv1alpha1.InstallPlan)
		for _, csv := range installPlan.Spec.ClusterServiceVersionNames {
			if csv == serviceMeshOperatorDesiredVersion {
				return true
			}
		}
		return false
	})
	isInstallPlanApproved := predicate.NewPredicateFuncs(func(o client.Object) bool {
		installPlan := o.(*operatorsv1alpha1.InstallPlan)
		return installPlan.Spec.Approved
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorsv1alpha1.InstallPlan{}, &handler.EnqueueRequestForObject{}, isOurInstallPlan, predicate.Not(isInstallPlanApproved))); err != nil {
		return nil, err
	}
	return c, nil
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	// OperatorNamespace is the namespace in which the operator should
	// create the ServiceMeshControlPlane CR.
	OperatorNamespace string
	// OperandNamespace is the namespace in which Istio should be deployed.
	OperandNamespace string
}

// reconciler reconciles gatewayclasses.
type reconciler struct {
	config Config

	client   client.Client
	cache    cache.Cache
	recorder record.EventRecorder
}

// Reconcile expects request to refer to a GatewayClass and creates or
// reconciles an Istio deployment.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	var gatewayclass gatewayapiv1beta1.GatewayClass
	if err := r.cache.Get(ctx, request.NamespacedName, &gatewayclass); err != nil {
		return reconcile.Result{}, err
	}

	var errs []error
	if _, _, err := r.ensureServiceMeshOperatorSubscription(ctx); err != nil {
		errs = append(errs, err)
	}
	if _, _, err := r.ensureServiceMeshOperatorInstallPlan(ctx); err != nil {
		errs = append(errs, err)
	}
	if _, _, err := r.ensureServiceMeshControlPlane(ctx, &gatewayclass); err != nil {
		errs = append(errs, err)
	}
	return reconcile.Result{}, utilerrors.NewAggregate(errs)
}
