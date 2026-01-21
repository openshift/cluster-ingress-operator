package gatewayclass

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/runtime"
	"reflect"
	"sync"
	"time"

	predicate2 "github.com/istio-ecosystem/sail-operator/pkg/predicate"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	operatorlogf "github.com/openshift/cluster-ingress-operator/pkg/log"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
)

const (
	controllerName = "gatewayclass_controller"

	// inferencepoolCrdName is the name of the InferencePool CRD from
	// Gateway API Inference Extension.
	inferencepoolCrdName = "inferencepools.inference.networking.k8s.io"
	// inferencepoolExperimentalCrdName is the name of the experimental
	// (alpha version) InferencePool CRD.
	inferencepoolExperimentalCrdName = "inferencepools.inference.networking.x-k8s.io"

	// subscriptionCatalogOverrideAnnotationKey is the key for an
	// unsupported annotation on the gatewayclass using which a custom
	// catalog source can be specified for the OSSM subscription.  This
	// annotation is only intended for use by OpenShift developers.  Note
	// that this annotation is intended to be used only when initially
	// creating the gatewayclass and subscription; changing the catalog
	// source on an existing subscription will likely have no effect or
	// cause errors.
	subscriptionCatalogOverrideAnnotationKey = "unsupported.do-not-use.openshift.io/ossm-catalog"
	// subscriptionChannelOverrideAnnotationKey is the key for an
	// unsupported annotation on the gatewayclass using which a custom
	// channel can be specified for the OSSM subscription.  This annotation
	// is only intended for use by OpenShift developers.  Note that this
	// annotation is intended to be used only when initially creating the
	// gatewayclass and subscription; changing the channel on an existing
	// subscription will likely have no effect or cause errors.
	subscriptionChannelOverrideAnnotationKey = "unsupported.do-not-use.openshift.io/ossm-channel"
	// subscriptionVersionOverrideAnnotationKey is the key for an
	// unsupported annotation on the gatewayclass using which a custom
	// version of OSSM can be specified.  This annotation is only intended
	// for use by OpenShift developers.  Note that this annotation is
	// intended to be used only when initially creating the gatewayclass and
	// subscription; OLM will not allow downgrades, and upgrades are
	// generally restricted to the next version after the currently
	// installed version.
	subscriptionVersionOverrideAnnotationKey = "unsupported.do-not-use.openshift.io/ossm-version"
	// istioVersionOverrideAnnotationKey is the key for an unsupported
	// annotation on the gatewayclass using which a custom version of Istio
	// can be specified.  This annotation is only intended for use by
	// OpenShift developers.
	istioVersionOverrideAnnotationKey = "unsupported.do-not-use.openshift.io/istio-version"
)

var log = operatorlogf.Logger.WithName(controllerName)
var gatewayClassController controller.Controller

// NewUnmanaged creates and returns a controller that watches gatewayclasses and
// installs and configures Istio.  This is an unmanaged controller, which means
// that the manager does not start it.
func NewUnmanaged(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()

	restConfig := mgr.GetConfig()

	reconciler := &reconciler{
		config:        config,
		client:        mgr.GetClient(),
		cache:         operatorCache,
		recorder:      mgr.GetEventRecorderFor(controllerName),
		helmInstaller: newHelmInstaller(restConfig),
		scheme:        mgr.GetScheme(),
	}
	options := controller.Options{Reconciler: reconciler}
	options.DefaultFromConfig(mgr.GetControllerOptions())
	c, err := controller.NewUnmanaged(controllerName, options)
	if err != nil {
		return nil, err
	}
	// Predicates for GatewayClass filtering
	isOurGatewayClass := predicate.NewPredicateFuncs(func(o client.Object) bool {
		class := o.(*gatewayapiv1.GatewayClass)
		return class.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName
	})
	notIstioGatewayClass := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() != "istio"
	})

	if err := c.Watch(source.Kind[client.Object](operatorCache, &gatewayapiv1.GatewayClass{}, reconciler.enqueueRequestForSomeGatewayClass(), isOurGatewayClass, notIstioGatewayClass)); err != nil {
		return nil, err
	}

	// Watch for the InferencePool CRD to determine whether to enable
	// Gateway API Inference Extension (GIE) on the Istio control-plane.
	isInferencepoolCrd := predicate.NewPredicateFuncs(func(o client.Object) bool {
		switch o.GetName() {
		case inferencepoolCrdName, inferencepoolExperimentalCrdName:
			return true
		default:
			return false
		}
	})

	ownedResourceHandler := handler.EnqueueRequestForOwner(reconciler.scheme, mgr.GetRESTMapper(), &gatewayapiv1.GatewayClass{}, handler.OnlyControllerOwner())
	if err := c.Watch(source.Kind[client.Object](operatorCache, &apiextensionsv1.CustomResourceDefinition{}, reconciler.enqueueRequestForSomeGatewayClass(), isInferencepoolCrd)); err != nil {
		return nil, err
	}
	// Watch Helm-managed resources
	// These resources are created by the Helm charts and need to be monitored to recreate if modified.
	// This was adapted from https://github.com/istio-ecosystem/sail-operator/blob/main/controllers/istiorevision/istiorevision_controller.go
	// TODO: Do we need predicate2.IgnoreUpdateWhenAnnotation()?
	if err := c.Watch(source.Kind[client.Object](operatorCache, &corev1.ConfigMap{}, ownedResourceHandler, predicate2.IgnoreUpdateWhenAnnotation())); err != nil {
		return nil, err
	}
	// Watch Deployments (istiod)
	if err := c.Watch(source.Kind[client.Object](operatorCache, &appsv1.Deployment{}, ownedResourceHandler, predicate2.IgnoreUpdateWhenAnnotation())); err != nil {
		return nil, err
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &corev1.Service{}, ownedResourceHandler, ignoreStatusChange(), predicate2.IgnoreUpdateWhenAnnotation())); err != nil {
		return nil, err
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &networkingv1.NetworkPolicy{}, ownedResourceHandler, ignoreStatusChange(), predicate2.IgnoreUpdateWhenAnnotation())); err != nil {
		return nil, err
	}
	// ServiceAccount watch with predicate.Funcs to ignore auto-added pull secrets
	ignoreServiceAccountUpdate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Ignore updates that only change the secrets field (auto-added pull secrets)
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() ||
				!reflect.DeepEqual(e.ObjectNew.GetLabels(), e.ObjectOld.GetLabels()) ||
				!reflect.DeepEqual(e.ObjectNew.GetAnnotations(), e.ObjectOld.GetAnnotations())
		},
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &corev1.ServiceAccount{}, ownedResourceHandler, ignoreServiceAccountUpdate)); err != nil {
		return nil, err
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, &rbacv1.Role{}, ownedResourceHandler, predicate2.IgnoreUpdateWhenAnnotation())); err != nil {
		return nil, err
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, &rbacv1.RoleBinding{}, ownedResourceHandler, predicate2.IgnoreUpdateWhenAnnotation())); err != nil {
		return nil, err
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, &policyv1.PodDisruptionBudget{}, ownedResourceHandler, ignoreStatusChange(), predicate2.IgnoreUpdateWhenAnnotation())); err != nil {
		return nil, err
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, &autoscalingv2.HorizontalPodAutoscaler{}, ownedResourceHandler, ignoreStatusChange(), predicate2.IgnoreUpdateWhenAnnotation())); err != nil {
		return nil, err
	}
	// Cluster-scoped resources
	if err := c.Watch(source.Kind[client.Object](operatorCache, &rbacv1.ClusterRole{}, ownedResourceHandler, predicate2.IgnoreUpdateWhenAnnotation())); err != nil {
		return nil, err
	}
	if err := c.Watch(source.Kind[client.Object](operatorCache, &rbacv1.ClusterRoleBinding{}, ownedResourceHandler, predicate2.IgnoreUpdateWhenAnnotation())); err != nil {
		return nil, err
	}

	gatewayClassController = c
	return c, nil
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	// OperatorNamespace is the namespace in which the operator should
	// create the Istio CR.
	OperatorNamespace string
	// OperandNamespace is the namespace in which Istio should be deployed.
	OperandNamespace string
	// IstioVersion is the version of Istio to configure on the Istio CR.
	IstioVersion string
}

// reconciler reconciles gatewayclasses.
type reconciler struct {
	config Config

	client   client.Client
	cache    cache.Cache
	recorder record.EventRecorder

	scheme *runtime.Scheme

	// POC: helmInstaller replaces OLM subscription approach
	helmInstaller *helmInstaller

	startIstioWatch sync.Once
}

// enqueueRequestForSomeGatewayClass enqueues a reconciliation request for the
// gatewayclass that has the earliest creation timestamp and that specifies our
// controller name.
func (r *reconciler) enqueueRequestForSomeGatewayClass() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			requests := []reconcile.Request{}
			log.Info("reconciling for: ", obj.GetName(), obj.GetNamespace(), "kind:", obj.GetObjectKind())
			var gatewayClasses gatewayapiv1.GatewayClassList
			if err := r.cache.List(context.Background(), &gatewayClasses); err != nil {
				log.Error(err, "Failed to list gatewayclasses")

				return requests
			}

			var (
				found  bool
				oldest metav1.Time
				name   string
			)
			for i := range gatewayClasses.Items {
				if gatewayClasses.Items[i].Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName {
					continue
				}

				ctime := gatewayClasses.Items[i].CreationTimestamp
				if !found || ctime.Before(&oldest) {
					found, oldest, name = true, ctime, gatewayClasses.Items[i].Name
				}
			}

			if found {
				request := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: "", // GatewayClass is cluster-scoped.
						Name:      name,
					},
				}
				requests = append(requests, request)
			}

			return requests
		},
	)
}

// Reconcile expects request to refer to a GatewayClass and creates or
// reconciles an Istio deployment.
// TODO: Add watches for helm-created objects
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	var gatewayclass gatewayapiv1.GatewayClass
	if err := r.cache.Get(ctx, request.NamespacedName, &gatewayclass); err != nil {
		return reconcile.Result{}, err
	}

	// Get Istio version from config or annotation override
	istioVersion := r.config.IstioVersion
	if v, ok := gatewayclass.Annotations[istioVersionOverrideAnnotationKey]; ok {
		istioVersion = v
	}

	// Check if InferencePool CRD exists to enable inference extension
	enableInferenceExtension, err := r.inferencepoolCrdExists(ctx)
	if err != nil {
		log.Error(err, "failed to check for InferencePool CRD", "request", request)
		return reconcile.Result{}, fmt.Errorf("failed to check for InferencePool CRD: %w", err)
	}

	//////////////////////// 4.21 -> 4.22 Migration from Sail Operator ////////////////////////////
	// Method: Simple Delete & Recreate
	// Steps:
	//  1. Delete Istio CR
	//  2. Kubernetes cascade deletes the IstioRevision, and Sail Operator finalizer deletes the helm chart
	//  3. Deploy CIO-managed Helm chart
	//  4. New istiod deployment rolls out, reconnects to existing Gateway Pods (same revision label)
	//  5. Istiod reconciles gateway pod (no changes == no restart)
	// TODO: Remove Sail Operator?
	sailOperatorIstioCRExists, err := r.deleteSailOperatorIstioCRIfExists(ctx)
	if err != nil {
		log.Error(err, "failed to handle Sail Operator Istio CR migration", "request", request)
		return reconcile.Result{}, fmt.Errorf("failed to handle Sail Operator Istio CR migration: %w", err)
	}
	if sailOperatorIstioCRExists {
		log.Info("Sail Operator Istio CR deleted, requeueing to wait for cleanup", "request", request)
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Check if IstioRevision still exists before doing our Helm installation.
	// Once IstioRevision is removed, the old helm charts will be deleted via the Sail Operator finalizer.
	istioRevisionExists, err := r.istioRevisionExists(ctx)
	if err != nil {
		log.Error(err, "failed to check for IstioRevision", "request", request)
		return reconcile.Result{}, fmt.Errorf("failed to check for IstioRevision: %w", err)
	}
	if istioRevisionExists {
		log.Info("IstioRevision still exists, requeueing to wait for cleanup", "request", request, "name", operatorcontroller.IstioName("").Name)
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}

	log.Info("installing/upgrading istiod via Helm", "request", request, "version", istioVersion, "revision", operatorcontroller.IstioName("").Name)

	if err := r.helmInstaller.installIstio(ctx, &gatewayclass, istioVersion, enableInferenceExtension); err != nil {
		log.Error(err, "failed to install istiod via Helm chart", "request", request)
		return reconcile.Result{}, fmt.Errorf("failed to install istiod via Helm chart: %w", err)
	}

	log.Info("successfully installed/updated istiod via Helm chart", "request", request, "revision", operatorcontroller.IstioName("").Name)
	return reconcile.Result{}, nil
}

// crdExists returns a Boolean value indicating whether the named CRD exists.
func (r *reconciler) crdExists(ctx context.Context, crdName string) (bool, error) {
	namespacedName := types.NamespacedName{Name: crdName}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := r.cache.Get(ctx, namespacedName, &crd); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get CRD %s: %w", crdName, err)
	}
	return true, nil
}

// inferencepoolCrdExists returns a Boolean value indicating whether the
// InferencePool CRD exists under the inference.networking.k8s.io or
// inference.networking.x-k8s.io API group.
func (r *reconciler) inferencepoolCrdExists(ctx context.Context) (bool, error) {
	if v, err := r.crdExists(ctx, inferencepoolCrdName); err != nil {
		return false, err
	} else if v {
		return true, nil
	}

	if v, err := r.crdExists(ctx, inferencepoolExperimentalCrdName); err != nil {
		return false, err
	} else if v {
		return true, nil
	}

	return false, nil
}

// deleteSailOperatorIstioCRIfExists checks for Sail Operator's Istio CR and deletes it if found.
// This enables automatic migration from Sail Operator to GatewayClass controller in 4.21 to 4.22.
func (r *reconciler) deleteSailOperatorIstioCRIfExists(ctx context.Context) (bool, error) {
	// First check if the Istio CRD exists
	istioCrdExists, err := r.crdExists(ctx, "istios.sailoperator.io")
	if err != nil {
		return false, fmt.Errorf("failed to check for Istio CRD: %w", err)
	}
	if !istioCrdExists {
		// No Istio CRD means no Sail Operator installation
		log.V(2).Info("Istio CRD not found, no Sail Operator migration needed")
		return false, nil
	}

	// Check if the specific Istio CR "openshift-gateway" exists
	istioName := operatorcontroller.IstioName("")
	istio := &unstructured.Unstructured{}
	istio.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "sailoperator.io",
		Version: "v1",
		Kind:    "Istio",
	})

	err = r.client.Get(ctx, istioName, istio)
	if err != nil {
		if errors.IsNotFound(err) {
			// No Istio CR, migration already complete or not needed
			log.V(2).Info("Istio CR not found, no migration needed", "name", istioName.Name)
			return false, nil
		}
		return false, fmt.Errorf("failed to get Istio CR: %w", err)
	}

	// Istio CR exists - delete it to trigger Sail Operator cleanup
	log.Info("Migrating from Sail Operator: deleting Istio CR", "name", istioName.Name)

	if err := r.client.Delete(ctx, istio); err != nil {
		if errors.IsNotFound(err) {
			// Already deleted between Get and Delete, that's fine
			log.V(1).Info("Istio CR already deleted during migration", "name", istioName.Name)
			return true, nil
		}
		return false, fmt.Errorf("failed to delete Istio CR: %w", err)
	}

	log.Info("Istio CR deleted, Sail Operator will clean up Helm release", "name", istioName.Name)
	// Return true to indicate we should requeue and wait for cleanup
	return true, nil
}

// istioRevisionExists checks if an IstioRevision resource exists with the expected name.
func (r *reconciler) istioRevisionExists(ctx context.Context) (bool, error) {
	istioRevisionCrdExists, err := r.crdExists(ctx, "istiorevisions.sailoperator.io")
	if err != nil {
		return false, fmt.Errorf("failed to check for IstioRevision CRD: %w", err)
	}
	if !istioRevisionCrdExists {
		log.V(2).Info("IstioRevision CRD not found, no cleanup needed")
		return false, nil
	}

	// Check if the IstioRevision resource exists
	istioRevisionName := operatorcontroller.IstioName("")
	istioRevision := &unstructured.Unstructured{}
	istioRevision.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "sailoperator.io",
		Version: "v1",
		Kind:    "IstioRevision",
	})

	err = r.client.Get(ctx, istioRevisionName, istioRevision)
	if err != nil {
		if errors.IsNotFound(err) {
			log.V(2).Info("IstioRevision not found, cleanup complete", "name", istioRevisionName.Name)
			return false, nil
		}
		return false, fmt.Errorf("failed to get IstioRevision: %w", err)
	}

	log.Info("IstioRevision still exists, waiting for Sail Operator cleanup", "name", istioRevisionName.Name)
	return true, nil
}

// From https://github.com/istio-ecosystem/sail-operator/blob/main/controllers/istiorevision/istiorevision_controller.go
// ignoreStatusChange returns a predicate that ignores watch events where only the resource status changes; if
// there are any other changes to the resource, the event is not ignored.
// This ensures that the controller doesn't reconcile the entire IstioRevision every time the status of an owned
// resource is updated. Without this predicate, the controller would continuously reconcile the IstioRevision
// because the status.currentMetrics of the HorizontalPodAutoscaler object was updated.
func ignoreStatusChange() predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return specWasUpdated(e.ObjectOld, e.ObjectNew) ||
				!reflect.DeepEqual(e.ObjectNew.GetLabels(), e.ObjectOld.GetLabels()) ||
				!reflect.DeepEqual(e.ObjectNew.GetAnnotations(), e.ObjectOld.GetAnnotations()) ||
				!reflect.DeepEqual(e.ObjectNew.GetOwnerReferences(), e.ObjectOld.GetOwnerReferences()) ||
				!reflect.DeepEqual(e.ObjectNew.GetFinalizers(), e.ObjectOld.GetFinalizers())
		},
	}
}

// From https://github.com/istio-ecosystem/sail-operator/blob/main/controllers/istiorevision/istiorevision_controller.go
func specWasUpdated(oldObject client.Object, newObject client.Object) bool {
	// for HPAs, k8s doesn't set metadata.generation, so we actually have to check whether the spec was updated
	if oldHpa, ok := oldObject.(*autoscalingv2.HorizontalPodAutoscaler); ok {
		if newHpa, ok := newObject.(*autoscalingv2.HorizontalPodAutoscaler); ok {
			return !reflect.DeepEqual(oldHpa.Spec, newHpa.Spec)
		}
	}

	// for other resources, comparing the metadata.generation suffices
	return oldObject.GetGeneration() != newObject.GetGeneration()
}
