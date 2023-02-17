package routestatus

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
)

// A brief overview of the operator's interactions with route status:
// Though the openshift-router is mainly responsible for route object status, the operator plays a small, but
// significant role in ensuring the route status is accurate. The openshift-router updates the route object's status
// when it is admitted to an ingress controller. However, the openshift-router is unable to reliably update the route's
// status when it stops managing the route. Here are the scenarios where the operator steps in:
// #1 When the ingress controller, the corresponding router deployment, and its pods are deleted.
//    - The operator knows when a router is deleted because it is the one responsible for deleting it. So it
//      simply calls ClearRouteStatus to clear status of routes that openshift-router has admitted.
//    - Handled by the ingress control loop.
// #2 When the ingress controller sharding configuration (i.e., selectors) is changed.
//    - When the selectors (routeSelector and namespaceSelector) are updated, the operator simply clears the status of
//      any route that it is no longer selecting using the updated selectors.
//    - We determine what routes are admitted by the current state of the selectors (just like the openshift-router).
//    - Handled by the ingress control loop.
// # 3 When the route's labels are changed.
//    - When the route labels are updated, the operator will reconcile that specific route and clean up any admitted
//      status that is now stale.
//    - Handled by the route-status control loop.
// # 4 When the namespace's labels are changed.
//    - When the namespace labels are updated, the operator will reconcile all the routes in the namespace and clean up
//      stale route statuses.
//    - Handled by the route-status control loop.

// ClearAllRoutesStatusForIngressController clears any route status that has been admitted by the ingress controller,
// regardless if it is actually admitted or not. This function should be used for deletions of ingress controllers.
func ClearAllRoutesStatusForIngressController(kclient client.Client, icName string) []error {
	// List all routes.
	errs := []error{}
	start := time.Now()
	routeList := &routev1.RouteList{}
	routesCleared := 0
	if err := kclient.List(context.TODO(), routeList); err != nil {
		return append(errs, fmt.Errorf("failed to list all routes in order to clear route status for deployment %s: %w", icName, err))
	}
	// Clear status on the routes that belonged to icName.
	for i := range routeList.Items {
		if cleared, err := ClearRouteStatus(kclient, &routeList.Items[i], icName); err != nil {
			errs = append(errs, err)
		} else if cleared {
			routesCleared++
		}
	}
	elapsed := time.Since(start)
	log.Info("cleared all route status for ingress", "Ingress Controller",
		icName, "Routes Status Cleared", routesCleared, "Time Elapsed", elapsed)

	return errs
}

// ClearRouteStatus clears a route's status that is admitted by a specific ingress controller.
func ClearRouteStatus(kclient client.Client, route *routev1.Route, icName string) (bool, error) {
	if route == nil {
		return false, fmt.Errorf("failed to clear route status: route is nil")
	}
	// Go through each route and clear status if admitted by this ingress controller.
	var updated routev1.Route
	for i := range route.Status.Ingress {
		if condition := findCondition(&route.Status.Ingress[i], routev1.RouteAdmitted); condition != nil {
			if route.Status.Ingress[i].RouterName == icName {
				// Remove this status since it matches our routerName.
				route.DeepCopyInto(&updated)
				updated.Status.Ingress = append(route.Status.Ingress[:i], route.Status.Ingress[i+1:]...)
				if err := kclient.Status().Update(context.TODO(), &updated); err != nil {
					return false, fmt.Errorf("failed to clear route status of %s/%s for routerName %s: %w",
						route.Namespace, route.Name, icName, err)
				}
				log.Info("cleared admitted status for route", "Route", route.Namespace+"/"+route.Name,
					"Ingress Controller", icName)
				return true, nil
			}
		}
	}

	return false, nil
}

// ClearRoutesNotAdmittedByIngress clears routes status that are not selected by a specific ingress controller.
func ClearRoutesNotAdmittedByIngress(kclient client.Client, ingress *operatorv1.IngressController) []error {
	var errs []error
	if ingress == nil {
		return append(errs, fmt.Errorf("ingress controller is nil"))
	}

	start := time.Now()

	// List all routes.
	routeList := &routev1.RouteList{}
	if err := kclient.List(context.TODO(), routeList); err != nil {
		return append(errs, fmt.Errorf("failed to list all routes in order to clear route status: %w", err))
	}

	// List all the Namespaces filtered by our ingress's Namespace selector.
	namespacesInShard, err := GetNamespacesSelectedByIngressController(kclient, ingress)
	if err != nil {
		return append(errs, err)
	}

	// List routes filtered by our ingress's route selector.
	routeMatchingLabelsSelector := client.MatchingLabelsSelector{Selector: labels.Everything()}
	if ingress.Spec.RouteSelector != nil {
		routeSelector, err := metav1.LabelSelectorAsSelector(ingress.Spec.RouteSelector)
		if err != nil {
			return append(errs, fmt.Errorf("ingresscontroller %s has an invalid route selector: %w", ingress.Name, err))
		}
		routeMatchingLabelsSelector = client.MatchingLabelsSelector{Selector: routeSelector}
	}

	// Iterate over the entire route list and clear if either the route selector OR the namespace selector does not
	// select it.
	routesCleared := 0
	for i := range routeList.Items {
		route := &routeList.Items[i]

		routeInShard := routeMatchingLabelsSelector.Matches(labels.Set(route.Labels))
		namespaceInShard := namespacesInShard.Has(route.Namespace)

		if !routeInShard || !namespaceInShard {
			if cleared, err := ClearRouteStatus(kclient, route, ingress.ObjectMeta.Name); err != nil {
				errs = append(errs, err)
			} else if cleared {
				routesCleared++
			}
		}

	}
	elapsed := time.Since(start)
	log.Info("cleared route status after selector update", "Ingress Controller", ingress.Name, "Routes Status Cleared", routesCleared, "Time Elapsed", elapsed)
	return errs
}

// clearStaleRouteAdmittedStatus cleans up a single route's admitted status by verifying it is actually admitted by
// the ingress controller its status claims to be admitted by.
func clearStaleRouteAdmittedStatus(kclient client.Client, route *routev1.Route) error {
	if route == nil {
		return fmt.Errorf("route is nil")
	}
	// Iterate through the Route's Ingresses.
	for _, ri := range route.Status.Ingress {
		// Check if the Route was admitted by the RouteIngress.
		for _, cond := range ri.Conditions {
			if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
				// Get the ingress controller that this route status claims to be admitted by.
				icName := types.NamespacedName{Name: ri.RouterName, Namespace: operatorcontroller.DefaultOperatorNamespace}
				ic := &operatorv1.IngressController{}
				if err := kclient.Get(context.TODO(), icName, ic); err != nil {
					// If the Ingress Controller doesn't exist at all, skip status clear, as it should have been cleared
					// upon Ingress Controller deletion. If it made it here, then it's probably a router status test.
					if kerrors.IsNotFound(err) {
						continue
					}
					return fmt.Errorf("failed to get ingresscontroller %q: %v", icName, err)
				}

				// If it is no longer admitted by this ingress controller, clear the status.
				if admitted, err := isRouteAdmittedByIngressController(kclient, route, ic); err != nil {
					return err
				} else if !admitted {
					ClearRouteStatus(kclient, route, ri.RouterName)
				}
			}
		}
	}
	return nil
}

// isRouteAdmittedByIngressController returns a boolean if the route is admitted by the ingress
// controller as determined by routeSelectors and namespaceSelectors on the ingress controller.
// Note: This is not using route status to determine admitted (see IsRouteStatusAdmitted)
func isRouteAdmittedByIngressController(kclient client.Reader, route *routev1.Route, ic *operatorv1.IngressController) (bool, error) {
	if route == nil {
		return false, fmt.Errorf("failed to check if route is admitted: route is nil")
	}
	if ic == nil {
		return false, fmt.Errorf("failed to check if route is admitted: ingress controller is nil")
	}
	// First check if the route's labels match the RouteSelector on the Ingress Controller.
	if ic.Spec.RouteSelector != nil {
		routeSelector, err := metav1.LabelSelectorAsSelector(ic.Spec.RouteSelector)
		if err != nil {
			return false, fmt.Errorf("ingresscontroller %s has an invalid route selector: %w", ic.Name, err)
		}
		if !routeSelector.Matches(labels.Set(route.Labels)) {
			return false, nil
		}
	}

	// Next let's check if the route's namespace labels match the NamespaceSelector on the Ingress Controller.
	if ic.Spec.NamespaceSelector != nil {
		ns := &corev1.Namespace{}
		nsName := types.NamespacedName{Name: route.Namespace}
		if err := kclient.Get(context.Background(), nsName, ns); err != nil {
			return false, fmt.Errorf("failed to get namespace %q: %v", nsName, err)
		}
		namespaceSelector, err := metav1.LabelSelectorAsSelector(ic.Spec.NamespaceSelector)
		if err != nil {
			return false, fmt.Errorf("ingresscontroller %s has an invalid namespace selector: %w", ic.Name, err)
		}
		if !namespaceSelector.Matches(labels.Set(ns.Labels)) {
			return false, nil
		}
	}

	return true, nil
}

// IsRouteStatusAdmitted returns true if a given route's status shows admitted by the Ingress Controller.
func IsRouteStatusAdmitted(route routev1.Route, ingressControllerName string) bool {
	// Iterate through the related Ingress Controllers.
	for _, ingress := range route.Status.Ingress {
		// Check if the RouterName matches the name of the Ingress Controller.
		if ingress.RouterName == ingressControllerName {
			// Check if the Route was admitted by the Ingress Controller.
			for _, cond := range ingress.Conditions {
				if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
					return true
				}
			}
			return false
		}
	}
	return false
}

// GetNamespacesSelectedByIngressController gets a set of strings that represents the namespaces that were selected by
// the ingress controller's namespacesSelector. If ingress controller's namespace selector is empty, then it returns all.
func GetNamespacesSelectedByIngressController(kclient client.Reader, ic *operatorv1.IngressController) (sets.String, error) {
	if ic == nil {
		return nil, fmt.Errorf("failed to get selected namespaces: ingress controller is nil")
	}
	// List all the Namespaces filtered by our ingress's Namespace selector.
	namespaceMatchingLabelsSelector := client.MatchingLabelsSelector{Selector: labels.Everything()}
	if ic.Spec.NamespaceSelector != nil {
		namespaceSelector, err := metav1.LabelSelectorAsSelector(ic.Spec.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("ingresscontroller %s has an invalid namespace selector %q: %w",
				ic.Name, ic.Spec.NamespaceSelector, err)
		}
		namespaceMatchingLabelsSelector = client.MatchingLabelsSelector{Selector: namespaceSelector}
	}
	filteredNamespaceList := &corev1.NamespaceList{}
	if err := kclient.List(context.TODO(), filteredNamespaceList, namespaceMatchingLabelsSelector); err != nil {
		return nil, fmt.Errorf("failed to list all namespaces in order to clear route status for %s: %w", ic.Name, err)
	}
	// Create a set of namespaces to easily look up namespaces in this shard.
	namespacesInShard := sets.NewString()
	for i := range filteredNamespaceList.Items {
		namespacesInShard.Insert(filteredNamespaceList.Items[i].Name)
	}
	return namespacesInShard, nil
}

// GetRoutesSelectedByIngressController gets route list that represents the routes that were selected by
// the ingress controller's routeSelector. If ingress controller's route selector is empty, then it returns all.
func GetRoutesSelectedByIngressController(kclient client.Reader, ic *operatorv1.IngressController) (routev1.RouteList, error) {
	if ic == nil {
		return routev1.RouteList{}, fmt.Errorf("failed to get selected routes: ingress controller is nil")
	}
	// List routes filtered by our ingress's route selector.
	routeMatchingLabelsSelector := client.MatchingLabelsSelector{Selector: labels.Everything()}
	if ic.Spec.RouteSelector != nil {
		routeSelector, err := metav1.LabelSelectorAsSelector(ic.Spec.RouteSelector)
		if err != nil {
			return routev1.RouteList{}, fmt.Errorf("ingresscontroller %s has an invalid route selector %q: %w",
				ic.Name, ic.Spec.RouteSelector, err)
		}
		routeMatchingLabelsSelector = client.MatchingLabelsSelector{Selector: routeSelector}
	}
	routeList := routev1.RouteList{}
	if err := kclient.List(context.Background(), &routeList, routeMatchingLabelsSelector); err != nil {
		return routev1.RouteList{}, fmt.Errorf("failed to list routes for the ingresscontroller %s: %w", ic.Name, err)
	}
	return routeList, nil
}

// findCondition locates the first condition that corresponds to the requested type.
func findCondition(ingress *routev1.RouteIngress, t routev1.RouteIngressConditionType) *routev1.RouteIngressCondition {
	for i := range ingress.Conditions {
		if ingress.Conditions[i].Type == t {
			return &ingress.Conditions[i]
		}
	}
	return nil
}
