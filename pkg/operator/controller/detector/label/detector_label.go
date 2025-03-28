package label

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/detector"

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
	controllerName = "detector_label_controller"
)

// New creates and returns a controller that watches for known CRDs for unsupported use
func NewLabelMatch(mgr manager.Manager, status detector.StatusReporter, config Config) (controller.Controller, error) {
	instanceControllerName := fmt.Sprintf("%s_%s/%s", controllerName, config.WatchResource.APIVersion, config.WatchResource.Kind)
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		client: mgr.GetClient(),
		log:    logf.Logger.WithValues("GroupVersionKind", config.WatchResource),
		config: config,
		status: status,
	}
	c, err := controller.New(instanceControllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	matchesLabel := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return config.LabelValue == o.GetLabels()[config.LabelName]
	})

	resource := &metav1.PartialObjectMetadata{
		TypeMeta: config.WatchResource,
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, resource, &handler.EnqueueRequestForObject{}, matchesLabel)); err != nil {
		return nil, err
	}

	return c, nil
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	LabelName  string
	LabelValue string

	WatchResource metav1.TypeMeta

	IgnoreOwnerRefs []metav1.OwnerReference
	IgnoreLabels    map[string]string
}

// reconciler reconciles Config.WatchResource.
type reconciler struct {
	config Config
	status detector.StatusReporter

	client client.Client
	log    logr.Logger
}

// Reconcile check if the current object has the given label with given value. Report accordingly.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	r.log.Info("reconciling", "request", request)

	source := &metav1.PartialObjectMetadata{}
	source.SetGroupVersionKind(r.config.WatchResource.GroupVersionKind())

	err := r.client.Get(ctx, request.NamespacedName, source)
	if err != nil {
		return reconcile.Result{}, err
	}

	lables := source.GetLabels()

	// if any of the ignore labels match, return.
	for k, v := range r.config.IgnoreLabels {
		if v == lables[k] {
			return reconcile.Result{}, nil
		}
	}

	// if ignoreOwnerRef field(s) is set and all set fields match; ignore
	if len(r.config.IgnoreOwnerRefs) > 0 {
		for _, ignoreOwnerRef := range r.config.IgnoreOwnerRefs {
			for _, ownerRef := range source.GetOwnerReferences() {
				matchKind, matchAPIVersion, matchName := false, false, false
				if ignoreOwnerRef.Kind == "" || ignoreOwnerRef.Kind == ownerRef.Kind {
					matchKind = true
				}
				if ignoreOwnerRef.APIVersion == "" || ignoreOwnerRef.APIVersion == ownerRef.APIVersion {
					matchAPIVersion = true
				}
				if ignoreOwnerRef.Name == "" || ignoreOwnerRef.Name == ownerRef.Name {
					matchName = true
				}
				// all fields match, ignore
				if matchKind && matchAPIVersion && matchName {
					return reconcile.Result{}, nil
				}
			}
		}
	}

	currentStatus := r.config.LabelValue == lables[r.config.LabelName]
	r.status.Update(source, currentStatus)

	return reconcile.Result{}, nil
}
