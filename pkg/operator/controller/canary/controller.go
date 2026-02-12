package canary

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"k8s.io/client-go/tools/record"
)

const (
	canaryControllerName = "canary_controller"
	// canaryCheckFrequency is how long to wait in between canary checks.
	canaryCheckFrequency = 1 * time.Minute
	// canaryCheckCycleCount is how many successful canary checks should be observed
	// before rotating the canary endpoint.
	canaryCheckCycleCount = 5
	// canaryCheckFailureCount is how many successive failing canary checks should
	// be observed before the default ingress controller goes degraded.
	canaryCheckFailureCount = 5
	// canaryFailingNumErrors is how many error messages to include in the
	// CanaryChecksSucceeding status condition when checks are failing
	canaryFailingNumErrors = 3

	// CanaryRouteRotationAnnotation is an annotation on the default ingress controller
	// that specifies whether or not the canary check loop should periodically rotate
	// the endpoints of the canary route. Canary route rotation is disabled by default
	// to prevent router reloads from impacting ingress performance periodically.
	// Canary route rotation is enabled when the canary route rotation annotation has
	// a value of "true" (disabled otherwise).
	CanaryRouteRotationAnnotation = "ingress.operator.openshift.io/rotate-canary-route"

	// CanaryHealthcheckCommand is a parameter to pass to the ingress-operator to call
	// into the handler for the canary daemonset health check
	CanaryHealthcheckCommand = "serve-healthcheck"
	// CanaryHealthcheckResponse is the message that signals a successful health check
	CanaryHealthcheckResponse = "Healthcheck requested"
)

var (
	log              = logf.Logger.WithName(canaryControllerName)
	routeProbeRunner sync.Once
)

// New creates the canary controller.
//
// The canary controller will watch the Default IngressController, as well as
// the canary service, daemonset, and route resources.
func New(mgr manager.Manager, config Config) (controller.Controller, error) {
	operatorCache := mgr.GetCache()
	reconciler := &reconciler{
		config:                    config,
		client:                    mgr.GetClient(),
		recorder:                  mgr.GetEventRecorderFor(canaryControllerName),
		enableCanaryRouteRotation: false,
	}
	c, err := controller.New(canaryControllerName, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return nil, err
	}

	// trigger reconcile requests for the canary controller via events for the default ingress controller.
	defaultIcPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == manifests.DefaultIngressControllerName
	})

	if err := c.Watch(source.Kind[client.Object](operatorCache, &operatorv1.IngressController{}, &handler.EnqueueRequestForObject{}, defaultIcPredicate)); err != nil {
		return nil, err
	}

	// trigger reconcile requests for the canary controller via events for the canary route.
	canaryRoutePredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == operatorcontroller.CanaryRouteName().Name
	})

	// filter out canary route updates where the canary controller changes the canary route's Spec.Port,
	// so that the controller isn't immediately reverting its own changes.
	updateFilter := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRoute, ok := e.ObjectOld.(*routev1.Route)
			if !ok {
				return false
			}
			newRoute, ok := e.ObjectNew.(*routev1.Route)
			if !ok {
				return false
			}

			// Check if the port in newRoute is one of the ports exposed by the canary service.
			isValidPort := false
			for _, portNum := range CanaryPorts {
				switch newRoute.Spec.Port.TargetPort.Type {
				case intstr.Int:
					if newRoute.Spec.Port.TargetPort.IntVal == portNum {
						isValidPort = true
					}
				case intstr.String:
					// Not currently supported; for simplicity, the canary check doesn't do name to port translations,
					// so if the canary's target port is a port name, we want to treat it as an invalid port and
					// reconcile the route.
				}
				if isValidPort {
					break
				}
			}

			// If newRoute's port isn't one of the canary service ports, reconcile the route.
			if !isValidPort {
				return true
			}

			// If newRoute's port is one of the canary service ports, the change may just be the ingress operator
			// rotating canary ports. If the *only* change between oldRoute and newRoute is the port change, ignore it.
			return !cmp.Equal(oldRoute.Spec, newRoute.Spec, cmpopts.IgnoreFields(routev1.RouteSpec{}, "Port"))
		},
	}

	if err := c.Watch(source.Kind[client.Object](operatorCache, &routev1.Route{}, enqueueRequestForDefaultIngressController(config.Namespace), canaryRoutePredicate, updateFilter)); err != nil {
		return nil, err
	}
	canaryDaemonSetPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		canaryDaemonSet := operatorcontroller.CanaryDaemonSetName()
		return o.GetNamespace() == canaryDaemonSet.Namespace && o.GetName() == canaryDaemonSet.Name
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &appsv1.DaemonSet{}, enqueueRequestForDefaultIngressController(config.Namespace), canaryDaemonSetPredicate)); err != nil {
		return nil, err
	}
	canaryServicePredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		canaryService := operatorcontroller.CanaryServiceName()
		return o.GetNamespace() == canaryService.Namespace && o.GetName() == canaryService.Name
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &corev1.Service{}, enqueueRequestForDefaultIngressController(config.Namespace), canaryServicePredicate)); err != nil {
		return nil, err
	}

	// Watch the canary serving cert secret and enqueue the default ingress controller so
	// that changes to the serving cert cause the canary daemonset to be reconciled.
	canarySecretPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		name := operatorcontroller.CanaryCertificateName()
		return o.GetNamespace() == name.Namespace && o.GetName() == name.Name
	})
	if err := c.Watch(source.Kind[client.Object](operatorCache, &corev1.Secret{}, enqueueRequestForDefaultIngressController(config.Namespace), canarySecretPredicate)); err != nil {
		return nil, err
	}

	return c, nil
}

func enqueueRequestForDefaultIngressController(namespace string) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, a client.Object) []reconcile.Request {
			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: namespace,
						Name:      manifests.DefaultIngressControllerName,
					},
				},
			}
		})
}

// Reconcile ensures that the canary controller's resources
// are in the desired state.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	result := reconcile.Result{}

	if _, _, err := r.ensureCanaryNamespace(ctx); err != nil {
		// Return if the canary namespace cannot be created since
		// resource creation in a namespace that does not exist will fail.
		return result, fmt.Errorf("failed to ensure canary namespace: %v", err)
	}

	haveDs, daemonset, err := r.ensureCanaryDaemonSet(ctx)
	if err != nil {
		return result, fmt.Errorf("failed to ensure canary daemonset: %v", err)
	} else if !haveDs {
		return result, fmt.Errorf("failed to get canary daemonset: %v", err)
	}

	trueVar := true
	daemonsetRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "daemonset",
		Name:       daemonset.Name,
		UID:        daemonset.UID,
		Controller: &trueVar,
	}

	haveService, service, err := r.ensureCanaryService(ctx, daemonsetRef)
	if err != nil {
		return result, fmt.Errorf("failed to ensure canary service: %v", err)
	} else if !haveService {
		return result, fmt.Errorf("failed to get canary service: %v", err)
	}

	haveRoute, _, err := r.ensureCanaryRoute(ctx, service)
	if err != nil {
		return result, fmt.Errorf("failed to ensure canary route: %v", err)
	} else if !haveRoute {
		return result, fmt.Errorf("failed to get canary route: %v", err)
	}

	// Get the canary route rotation annotation value
	// from the default ingress controller.
	ic := &operatorv1.IngressController{}
	if err := r.client.Get(ctx, request.NamespacedName, ic); err != nil {
		return result, fmt.Errorf("failed to get ingress controller %s: %v", request.NamespacedName.Name, err)
	}

	val, ok := ic.Annotations[CanaryRouteRotationAnnotation]
	v, _ := strconv.ParseBool(val)
	r.mu.Lock()
	r.enableCanaryRouteRotation = ok && v
	r.mu.Unlock()

	// Start probing the canary route.
	routeProbeRunner.Do(func() {
		r.startCanaryRoutePolling(r.config.Stop)
	})

	return result, nil
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
	config Config

	client   client.Client
	recorder record.EventRecorder

	// Use a mutex so enableCanaryRotation is
	// go-routine safe.
	mu                        sync.Mutex
	enableCanaryRouteRotation bool
}

func (r *reconciler) isCanaryRouteRotationEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enableCanaryRouteRotation
}

type timestampedError struct {
	timestamp time.Time
	err       error
}

func (err timestampedError) Error() string {
	return err.err.Error()
}

func (r *reconciler) startCanaryRoutePolling(stop <-chan struct{}) error {
	// Keep track of how many canary checks have passed
	// so the route endpoint can be periodically cycled
	// (when canary route rotation is enabled).
	checkCount := 0

	// Keep track of successive canary check failures
	// for status reporting.
	successiveFail := 0

	errors := []timestampedError{}

	// using wait.NonSlidingUntil so that the canary runs every canaryCheckFrequency, regardless of how long the function takes
	go wait.NonSlidingUntil(func() {
		ctx := context.TODO()
		// Get the current canary route every iteration in case it has been modified
		haveRoute, route, err := r.currentCanaryRoute(ctx)
		if err != nil {
			log.Error(err, "failed to get current canary route for canary check")
			return
		} else if !haveRoute {
			log.Info("canary check route does not exist")
			if err := r.setCanaryDoesNotExistStatusCondition(); err != nil {
				log.Error(err, "error updating canary status condition")
			}
			return
		}

		// Don't attempt to probe if route is not actually admitted.
		if !checkRouteAdmitted(route) {
			if err := r.setCanaryNotAdmittedStatusCondition(); err != nil {
				log.Error(err, "error updating canary status condition")
			}
			return
		}

		err = probeRouteEndpoint(route)
		if err != nil {
			log.Error(err, "error performing canary route check")
			SetCanaryRouteReachableMetric(getRouteHost(route), false)
			successiveFail += 1
			errors = append(errors, timestampedError{err: err, timestamp: time.Now()})
			// Mark the default ingress controller degraded after 5 successive canary check failures
			if successiveFail >= canaryCheckFailureCount {
				if err := r.setCanaryFailingStatusCondition(errors); err != nil {
					log.Error(err, "error updating canary status condition")
				}
			}
			return
		}

		SetCanaryRouteReachableMetric(getRouteHost(route), true)
		if err := r.setCanaryPassingStatusCondition(); err != nil {
			log.Error(err, "error updating canary status condition")
		}
		successiveFail = 0
		errors = []timestampedError{}

		// Check if canary route rotations are enabled every iteration.
		rotationEnabled := r.isCanaryRouteRotationEnabled()
		// Increment checkCount and periodically rotate the canary route endpoint if canary route rotation is enabled.
		if rotationEnabled {
			checkCount++
			if checkCount >= canaryCheckCycleCount {
				haveService, service, err := r.currentCanaryService(ctx)
				if err != nil {
					log.Error(err, "failed to get canary service")
					return
				} else if !haveService {
					log.Info("canary check service does not exist")
					return
				}
				route, err = r.rotateRouteEndpoint(ctx, service, route)
				if err != nil {
					log.Error(err, "failed to rotate canary route endpoint")
					return
				}
				checkCount = 0
			}
		}
	}, canaryCheckFrequency, stop)

	return nil
}

func (r *reconciler) setCanaryFailingStatusCondition(errors []timestampedError) error {
	errorStrings := deduplicateErrorStrings(errors, time.Now())
	if len(errorStrings) > canaryFailingNumErrors {
		errorStrings = errorStrings[len(errorStrings)-canaryFailingNumErrors:]
	}
	cond := operatorv1.OperatorCondition{
		Type:    ingresscontroller.IngressControllerCanaryCheckSuccessConditionType,
		Status:  operatorv1.ConditionFalse,
		Reason:  "CanaryChecksRepetitiveFailures",
		Message: fmt.Sprintf("Canary route checks for the default ingress controller are failing. Last %d error messages:\n%s", len(errorStrings), strings.Join(errorStrings, "\n")),
	}

	return r.setCanaryStatusCondition(cond)
}

type dedupCounter struct {
	Count           int
	FirstOccurrence time.Time
}

// deduplicateErrorStrings takes a slice of errors in chronological order, prunes the list for unique error messages
// It returns a slice of the error message as strings, with any string seen more than once incuding how many times it
// was seen.
// The chronological order is preserved, based on the last time each error message was seen
func deduplicateErrorStrings(errors []timestampedError, now time.Time) []string {
	encountered := map[string]dedupCounter{}
	uniqueErrors := []string{}
	// Iterate over the list of errors from newest to oldest, keeping track of how many times each error message was
	// seen. This iteration order makes sure we preserve order based on the newest time each message was seen.
	for i := len(errors) - 1; i >= 0; i-- {
		err := errors[i]
		if errCounter := encountered[err.Error()]; errCounter.Count >= 1 {
			errCounter.Count += 1
			if errCounter.FirstOccurrence.After(err.timestamp) {
				errCounter.FirstOccurrence = err.timestamp
			}
			encountered[err.Error()] = errCounter
			continue
		}
		encountered[err.Error()] = dedupCounter{
			Count:           1,
			FirstOccurrence: err.timestamp,
		}
		uniqueErrors = append(uniqueErrors, err.Error())
	}
	ret, j := make([]string, len(uniqueErrors)), len(uniqueErrors)-1
	// Now that all error messages have been ordered by their most recent occurrence, reverse the list to switch it to
	// cronological order, and append a message indicating how often an error has occurred if it has more than one
	// occurrence.
	for _, err := range uniqueErrors {
		if counter := encountered[err]; counter.Count > 1 {
			err = fmt.Sprintf("%s (x%d over %v)", err, counter.Count, now.Sub(counter.FirstOccurrence).Round(time.Second))
		}
		ret[j] = err
		j--
	}
	return ret
}

func (r *reconciler) setCanaryPassingStatusCondition() error {
	cond := operatorv1.OperatorCondition{
		Type:    ingresscontroller.IngressControllerCanaryCheckSuccessConditionType,
		Status:  operatorv1.ConditionTrue,
		Reason:  "CanaryChecksSucceeding",
		Message: "Canary route checks for the default ingress controller are successful",
	}

	return r.setCanaryStatusCondition(cond)
}

func (r *reconciler) setCanaryNotAdmittedStatusCondition() error {
	cond := operatorv1.OperatorCondition{
		Type:    ingresscontroller.IngressControllerCanaryCheckSuccessConditionType,
		Status:  operatorv1.ConditionUnknown,
		Reason:  "CanaryRouteNotAdmitted",
		Message: "Canary route is not admitted by the default ingress controller",
	}

	return r.setCanaryStatusCondition(cond)
}

func (r *reconciler) setCanaryDoesNotExistStatusCondition() error {
	cond := operatorv1.OperatorCondition{
		Type:    ingresscontroller.IngressControllerCanaryCheckSuccessConditionType,
		Status:  operatorv1.ConditionUnknown,
		Reason:  "CanaryRouteDoesNotExist",
		Message: "Canary route does not exist",
	}

	return r.setCanaryStatusCondition(cond)
}

// setCanaryStatusCondition applies the given condition to the default ingress controller.
// The assumption here is that cond is a condition that does not overlap with any of the status
// conditions set by the ingress controller in pkg/operator/controller/ingress/status.go.
func (r *reconciler) setCanaryStatusCondition(cond operatorv1.OperatorCondition) error {
	ic := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name:      manifests.DefaultIngressControllerName,
			Namespace: r.config.Namespace,
		},
	}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}, ic); err != nil {
		return fmt.Errorf("failed to get ingress controller %s: %v", ic.Name, err)
	}

	updated := ic.DeepCopy()
	updated.Status.Conditions = ingresscontroller.MergeConditions(updated.Status.Conditions, cond)

	if !ingresscontroller.IngressStatusesEqual(updated.Status, ic.Status) {
		if err := r.client.Status().Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to update ingresscontroller %s status: %v", ic.Name, err)
		}
	}

	return nil
}

// Switch the current RoutePort that the route points to.
// Use this function to periodically update the canary route endpoint
// to verify if the router has wedged.
func (r *reconciler) rotateRouteEndpoint(ctx context.Context, service *corev1.Service, current *routev1.Route) (*routev1.Route, error) {
	updated, err := cycleServicePort(service, current)
	if err != nil {
		return nil, fmt.Errorf("failed to rotate route port: %v", err)
	}

	if changed, err := r.updateCanaryRoute(ctx, current, updated); err != nil {
		return current, err
	} else if !changed {
		return current, fmt.Errorf("expected canary route to be updated: No relevant changes detected")
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

	// Find the current port index in the service ports slice.
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
