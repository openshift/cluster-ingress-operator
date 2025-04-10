package label

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/detector"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	controllerName = "detector_label_controller"
)

// New creates and returns a controller that watches for known CRDs for unsupported use
func NewLabelMatch(mgr manager.Manager, status detector.StatusReporter, config Config) (controller.Controller, error) {
	instanceControllerName := fmt.Sprintf("%s_%s/%s", controllerName, config.WatchResource.APIVersion, config.WatchResource.Kind)

	resourceCache, err := cache.New(mgr.GetConfig(), cache.Options{
		Scheme: mgr.GetScheme(),
	})
	if err != nil {
		return nil, err
	}
	mgr.Add(resourceCache)
	reconciler := &reconciler{
		cache:  resourceCache,
		client: mgr.GetClient(),
		log:    logf.Logger.WithName(instanceControllerName),
		config: config,
		status: status,
	}

	c, err := controller.New(instanceControllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	// Ensure we reconcile on update if old version had the label set. This to clean up. Predicates are only applied on the new version.
	enqeueIfLabelWasSet := func(ctx context.Context, o client.Object) []reconcile.Request {
		if config.LabelValue == o.GetLabels()[config.LabelName] {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{Name: o.GetName(), Namespace: o.GetNamespace()},
			}}
		}
		return []reconcile.Request{}
	}

	resource := &metav1.PartialObjectMetadata{
		TypeMeta: config.WatchResource,
	}
	if err := c.Watch(source.Kind[client.Object](resourceCache, resource, handler.EnqueueRequestsFromMapFunc(enqeueIfLabelWasSet))); err != nil {
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

	cache  cache.Cache
	client client.Client
	log    logr.Logger
}

// Reconcile check if the current object has the given label with given value. Report accordingly.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	r.log.Info("reconciling", "request", request)

	source := &metav1.PartialObjectMetadata{}
	source.SetGroupVersionKind(r.config.WatchResource.GroupVersionKind())
	//source.SetName(request.Name)           // set so we can calculate the correct key inside statusReporter.. a bit leaky, extract somehow?
	//source.SetNamespace(request.Namespace) // see above

	err := r.cache.Get(ctx, request.NamespacedName, source)
	if err != nil {
		if apierrors.IsNotFound(err) { // deleted?
			r.status.Update(source, false) // requires name and namespace set for key calc
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if source.DeletionTimestamp != nil { // deleted
		r.status.Update(source, false)
		return reconcile.Result{}, nil
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
