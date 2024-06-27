package canary

import (
	"context"
	"fmt"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// ensureCanaryRoute ensures the canary route exists
func (r *reconciler) ensureCanaryRoute(service *corev1.Service) (bool, *routev1.Route, error) {
	desired, err := desiredCanaryRoute(service)
	if err != nil {
		return false, nil, fmt.Errorf("failed to build canary route: %v", err)
	}

	haveRoute, current, err := r.currentCanaryRoute()
	if err != nil {
		return false, nil, err
	}

	switch {
	case !haveRoute:
		if err := r.createCanaryRoute(desired); err != nil {
			return false, nil, err
		}
		return r.currentCanaryRoute()
	case haveRoute:
		if updated, err := r.updateCanaryRoute(current, desired); err != nil {
			return true, current, err
		} else if updated {
			return r.currentCanaryRoute()
		}
	}

	return true, current, nil
}

// currentCanaryRoute gets the current canary route resource
func (r *reconciler) currentCanaryRoute() (bool, *routev1.Route, error) {
	route := &routev1.Route{}
	if err := r.client.Get(context.TODO(), controller.CanaryRouteName(), route); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, route, nil
}

// createCanaryRoute creates the given route
func (r *reconciler) createCanaryRoute(route *routev1.Route) error {
	if err := r.client.Create(context.TODO(), route); err != nil {
		return fmt.Errorf("failed to create canary route %s/%s: %v", route.Namespace, route.Name, err)
	}

	log.Info("created canary route", "namespace", route.Namespace, "name", route.Name)
	return nil
}

// updateCanaryRoute updates the canary route if an appropriate change
// has been detected
func (r *reconciler) updateCanaryRoute(current, desired *routev1.Route) (bool, error) {
	changed, updated := canaryRouteChanged(current, desired)
	if !changed {
		return false, nil
	}

	if len(updated.Spec.Host) == 0 && len(current.Spec.Host) != 0 {
		// Attempts to clear spec.host may be ignored.  Thus, to clear
		// spec.host, it is necessary to delete and recreate the route.
		log.Info("deleting and recreating the canary route to clear spec.host", "namespace", current.Namespace, "name", current.Name, "old spec.host", current.Spec.Host)
		foreground := metav1.DeletePropagationForeground
		deleteOptions := crclient.DeleteOptions{PropagationPolicy: &foreground}
		if _, err := r.deleteCanaryRoute(current, &deleteOptions); err != nil {
			return false, err
		}
		if err := r.createCanaryRoute(desired); err != nil {
			return false, err
		}
		return true, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, fmt.Errorf("failed to update canary route %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	log.Info("updated canary route", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// deleteCanaryRoute deletes a given route
func (r *reconciler) deleteCanaryRoute(route *routev1.Route, options *crclient.DeleteOptions) (bool, error) {

	if err := r.client.Delete(context.TODO(), route, options); err != nil {
		return false, fmt.Errorf("failed to delete canary route %s/%s: %v", route.Namespace, route.Name, err)
	}

	log.Info("deleted canary route", "namespace", route.Namespace, "name", route.Name)
	return true, nil
}

// canaryRouteChanged returns true if current and expected differ by Spec.Port,
// Spec.To, or Spec.TLS.
func canaryRouteChanged(current, expected *routev1.Route) (bool, *routev1.Route) {
	changed := false
	updated := current.DeepCopy()

	if current.Spec.Host != expected.Spec.Host {
		updated.Spec.Host = expected.Spec.Host
		changed = true
	}

	if current.Spec.Subdomain != expected.Spec.Subdomain {
		updated.Spec.Subdomain = expected.Spec.Subdomain
		changed = true
	}

	if !cmp.Equal(current.Spec.Port, expected.Spec.Port, cmpopts.EquateEmpty()) {
		updated.Spec.Port = expected.Spec.Port
		changed = true
	}

	if !cmp.Equal(current.Spec.To, expected.Spec.To, cmpopts.EquateEmpty()) {
		updated.Spec.To = expected.Spec.To
		changed = true
	}

	if !cmp.Equal(current.Spec.TLS, expected.Spec.TLS, cmpopts.EquateEmpty()) {
		updated.Spec.TLS = expected.Spec.TLS
		changed = true
	}

	if !changed {
		return false, nil
	}
	return true, updated
}

// desiredCanaryRoute returns the desired canary route read in
// from manifests
func desiredCanaryRoute(service *corev1.Service) (*routev1.Route, error) {
	route := manifests.CanaryRoute()

	name := controller.CanaryRouteName()

	route.Namespace = name.Namespace
	route.Name = name.Name
	route.Spec.Subdomain = fmt.Sprintf("%s-%s", route.Name, route.Namespace)

	if service == nil {
		return route, fmt.Errorf("expected non-nil canary service for canary route %s/%s", route.Namespace, route.Name)
	}

	route.Labels = map[string]string{
		// associate the route with the canary controller
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	route.Spec.To.Name = controller.CanaryServiceName().Name

	// Set spec.port.targetPort to the first port available in the canary service.
	// The canary controller may toggle which targetPort the route targets
	// to test > 1 endpoint, so it does not matter which port is selected as long
	// as the canary service has > 1 ports available. If the canary service only has one
	// available port, then route.Spec.Port.TargetPort will remain unchanged.
	if len(service.Spec.Ports) == 0 {
		return route, fmt.Errorf("expected spec.ports to be non-empty for canary service %s/%s", service.Namespace, service.Name)
	}
	route.Spec.Port.TargetPort = service.Spec.Ports[0].TargetPort

	route.SetOwnerReferences(service.OwnerReferences)

	return route, nil
}

// checkRouteAdmitted returns true if a given route has been admitted
// by the default Ingress Controller.
func checkRouteAdmitted(route *routev1.Route) bool {
	for _, routeIngress := range route.Status.Ingress {
		if routeIngress.RouterName != manifests.DefaultIngressControllerName {
			continue
		}
		conditions := routeIngress.Conditions
		for _, cond := range conditions {
			if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
				return true
			}
		}
	}

	return false
}

// getRouteHost returns the host name of the route for the default
// IngressController.  If the default IngressController has not added its host
// name to the route's status, this function returns the empty string.  For
// simplicity, this function does not check the "Admitted" status condition, so
// it will return the host name if it is set even if the IngressController has
// rejected the route.
func getRouteHost(route *routev1.Route) string {
	if route == nil {
		return ""
	}

	for _, ingress := range route.Status.Ingress {
		if ingress.RouterName == manifests.DefaultIngressControllerName {
			return ingress.Host
		}
	}

	return ""
}
