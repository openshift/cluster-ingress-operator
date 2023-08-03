package monitoringdashboard

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "monitoring_dashboard_controller"
)

var log = logf.Logger.WithName(controllerName)

// New creates the monitoring dashboard controller. This is the controller
// that handles all the logic about the monitoring dashboard
func New(mgr manager.Manager) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		client: mgr.GetClient(),
	}
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	CMPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == dashboardConfigMapName
	})

	if err := c.Watch(source.Kind(operatorCache, &corev1.ConfigMap{}), &handler.EnqueueRequestForObject{}, CMPredicate); err != nil {
		return nil, err
	}

	if err := c.Watch(source.Kind(operatorCache, &configv1.Infrastructure{}), &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}

	return c, nil
}

// reconciler handles the actual monitoringdashboard reconciliation logic in response to events.
type reconciler struct {
	client client.Client
}

// Reconcile will look at the cluster configuration and create the monitoring dashboard accordingly
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	infraConfig := &configv1.Infrastructure{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get infrastructure 'config': %v", err)
	}

	if err := r.ensureMonitoringDashboard(ctx, infraConfig.Status); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to ensure monitoring dashboard: %v", err)
	}

	return reconcile.Result{}, nil
}
