package canary

import (
	"fmt"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	"github.com/google/go-cmp/cmp"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	canaryControllerName = "canary_controller"
)

var (
	log = logf.Logger.WithName(canaryControllerName)
)

// New creates the canary controller.
//
// The canary controller will watch the Default IngressController, as well as
// the canary service, daemonset, and route resources.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	reconciler := &reconciler{
		Config: config,
		client: mgr.GetClient(),
	}
	c, err := controller.New(canaryControllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	// Only trigger a reconcile request for the canary controller via events for the default ingress controller.
	defaultIcPredicate := predicate.NewPredicateFuncs(func(meta metav1.Object, object runtime.Object) bool {
		return meta.GetName() == manifests.DefaultIngressControllerName
	})

	if err := c.Watch(&source.Kind{Type: &operatorv1.IngressController{}}, &handler.EnqueueRequestForObject{}, defaultIcPredicate); err != nil {
		return nil, err
	}

	return c, nil
}

// Reconcile ensures that the canary controller's resources
// are in the desired state.
func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	result := reconcile.Result{}
	errors := []error{}

	if err := r.ensureCanaryNamespace(); err != nil {
		// Return if the canary namespace cannot be created since
		// resource creation in a namespace that does not exist will fail.
		return result, fmt.Errorf("failed to ensure canary namespace: %v", err)
	}

	haveDs, daemonset, err := r.ensureCanaryDaemonSet()
	if err != nil {
		errors = append(errors, fmt.Errorf("failed to ensure canary daemonset: %v", err))
	} else if !haveDs {
		errors = append(errors, fmt.Errorf("failed to get canary daemonset: %v", err))
	}

	trueVar := true
	daemonsetRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "daemonset",
		Name:       daemonset.Name,
		UID:        daemonset.UID,
		Controller: &trueVar,
	}

	haveService, service, err := r.ensureCanaryService(daemonsetRef)
	if err != nil {
		errors = append(errors, fmt.Errorf("failed to ensure canary service: %v", err))
	} else if !haveService {
		errors = append(errors, fmt.Errorf("failed to get canary service: %v", err))
	}

	if haveRoute, _, err := r.ensureCanaryRoute(service); err != nil {
		errors = append(errors, fmt.Errorf("failed to ensure canary route: %v", err))
	} else if !haveRoute {
		errors = append(errors, fmt.Errorf("failed to get canary route: %v", err))
	}

	return result, utilerrors.NewAggregate(errors)
}

// Config holds all the things necessary for the controller to run.
type Config struct {
	Namespace   string
	CanaryImage string
	Stop        chan struct{}
}

// reconciler handles the actual canary reconciliation logic in response to
// events.
type reconciler struct {
	Config

	client client.Client
}

// TODO: Canary Controller Phase 2
// Add callers for these 2 functions
//
// Switch the current RoutePort that the route points to.
// Use this function to periodically update the canary route endpoint
// to verify if the router has wedged.
func (r *reconciler) rotateRouteEndpoint(service *corev1.Service, current *routev1.Route) (*routev1.Route, error) {
	updated, err := cycleServicePort(service, current)
	if err != nil {
		return nil, fmt.Errorf("failed to rotate route port: %v", err)
	}

	_, err = r.updateCanaryRoute(current, updated)
	if err != nil {
		return current, err
	}

	return updated, nil
}

// cycleServicePort returns a route resource with Spec.Port set to the
// next available port in service.Spec.Ports that is not the current route.Spec.Port.
func cycleServicePort(service *corev1.Service, route *routev1.Route) (*routev1.Route, error) {
	servicePorts := service.Spec.Ports
	currentPort := route.Spec.Port

	if currentPort == nil {
		return nil, fmt.Errorf("route does not have Spec.Port set")
	}

	switch len(servicePorts) {
	case 0:
		return nil, fmt.Errorf("service has no ports")
	case 1:
		return nil, fmt.Errorf("service has only one port, no change possible")
	}

	updated := route.DeepCopy()
	currentIndex := 0

	// Find the current port index in the service ports slice
	for i, port := range servicePorts {
		if cmp.Equal(port.TargetPort, currentPort.TargetPort) {
			currentIndex = i
		}
	}

	updated.Spec.Port = &routev1.RoutePort{
		TargetPort: servicePorts[(currentIndex+1)%len(servicePorts)].TargetPort,
	}

	return updated, nil
}
