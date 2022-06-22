//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	admittedCondition = routev1.RouteIngressCondition{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue}
)

// TestDeleteIngressControllerShouldClearRouteStatus deletes an Ingress Controller and
// makes sure a previously admitted route has its status cleared since.
func TestDeleteIngressControllerShouldClearRouteStatus(t *testing.T) {
	t.Parallel()
	// Create an ingress controller that can admit our route.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "ic-delete-test"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.RouteSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": icName.Name,
		},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Create a route to be admitted by this ingress controller.
	// Use openshift-console namespace to get a namespace outside the ingress-operator's cache.
	routeName := types.NamespacedName{Namespace: "openshift-console", Name: "route-" + icName.Name}
	route := newRouteWithLabel(routeName, icName.Name)
	if err := kclient.Create(context.TODO(), route); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), route); err != nil {
			t.Fatalf("failed to delete route %s: %v", routeName, err)
		}
	}()

	// Wait for route to be admitted.
	if err := waitForRouteIngressConditions(t, kclient, routeName, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Delete the ingress controller to trigger route status clean up.
	assertIngressControllerDeleted(t, kclient, ic)

	// Wait until route status goes unadmitted.
	if err := waitForRouteStatusClear(t, kclient, routeName, ic.Name); err != nil {
		t.Fatalf("failed to observe route status clear when ingress controller was deleted: %v", err)
	}
}

// TestIngressControllerRouteSelectorUpdateShouldClearRouteStatus modifies an ingress controller's selectors to
// unadmit a route and verifies it clears the admitted route's status.
func TestIngressControllerRouteSelectorUpdateShouldClearRouteStatus(t *testing.T) {
	t.Parallel()
	// Create an ingress controller that can admit our route.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "ic-route-selector-test"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.RouteSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "foo",
		},
	}

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Create a route to be immediately admitted by this ingress controller and then when the IC label selectors are
	// updated, the status should clear.
	// Use openshift-console namespace to get a namespace outside the ingress-operator's cache.
	routeFooLabelName := types.NamespacedName{Namespace: "openshift-console", Name: "route-foo-label"}
	routeFooLabel := newRouteWithLabel(routeFooLabelName, "foo")
	if err := kclient.Create(context.TODO(), routeFooLabel); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), routeFooLabel); err != nil {
			t.Fatalf("failed to delete route %s: %v", routeFooLabelName, err)
		}
	}()

	// Create a route that will NOT be immediately admitted by the ingress controller, but will be admitted AFTER
	// the IC selectors are updated. The status SHOULD be successfully admitted.
	// Use openshift-console namespace to get a namespace outside the ingress-operator's cache.
	routeBarLabelName := types.NamespacedName{Namespace: "openshift-console", Name: "route-bar-label"}
	routeBarLabel := newRouteWithLabel(routeBarLabelName, "bar")
	if err := kclient.Create(context.TODO(), routeBarLabel); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), routeBarLabel); err != nil {
			t.Fatalf("failed to delete route %s: %v", routeBarLabel, err)
		}
	}()

	// Wait for routeFooLabel to be admitted upon creation.
	if err := waitForRouteIngressConditions(t, kclient, routeFooLabelName, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Update the ingress controller to not select routeFooLabel to be admitted, but to select
	// routeBarLabel instead.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.RouteSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "bar",
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait until routeFooLabel status is cleared.
	if err := waitForRouteStatusClear(t, kclient, routeFooLabelName, ic.Name); err != nil {
		t.Fatalf("failed to observe route status clear when the ingress controller route selector was updated: %v", err)
	}

	// Wait until routeBarLabel status is admitted.
	if err := waitForRouteIngressConditions(t, kclient, routeBarLabelName, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe route status admitted when the ingress controller route selector was updated: %v", err)
	}
}

func TestIngressControllerNamespaceSelectorUpdateShouldClearRouteStatus(t *testing.T) {
	t.Parallel()
	// Create an ingress controller that can admit our route.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "ic-namespace-selector-test"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "foo",
		},
	}

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Create a new namespace for the route that we can immediately match with the IC's namespace selector.
	nsFoo := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo-namespace-selector-test",
			Labels: map[string]string{
				"type": "foo",
			},
		},
	}
	if err := kclient.Create(context.TODO(), nsFoo); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), nsFoo); err != nil {
			t.Fatalf("failed to delete test namespace %v: %v", nsFoo.Name, err)
		}
	}()

	// Create a new namespace for the route that we can NOT immediately match with the IC's namespace selector.
	nsBar := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bar-namespace-selector-test",
			Labels: map[string]string{
				"type": "bar",
			},
		},
	}
	if err := kclient.Create(context.TODO(), nsBar); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), nsBar); err != nil {
			t.Fatalf("failed to delete test namespace %v: %v", nsBar.Name, err)
		}
	}()

	// Create a route to be immediately admitted by this ingress controller and then when the IC label selectors are
	// updated, the status should clear.
	routeFooLabelName := types.NamespacedName{Namespace: nsFoo.Name, Name: "route-foo-label"}
	routeFooLabel := newRouteWithLabel(routeFooLabelName, "")
	if err := kclient.Create(context.TODO(), routeFooLabel); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), routeFooLabel); err != nil {
			t.Fatalf("failed to delete route %s: %v", routeFooLabelName, err)
		}
	}()

	// Create a route that will NOT be immediately admitted by the ingress controller, but will be admitted AFTER
	// the IC selectors are updated. The status SHOULD be successfully admitted.
	routeBarLabelName := types.NamespacedName{Namespace: nsBar.Name, Name: "route-bar-label"}
	routeBarLabel := newRouteWithLabel(routeBarLabelName, "bar")
	if err := kclient.Create(context.TODO(), routeBarLabel); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), routeBarLabel); err != nil {
			t.Fatalf("failed to delete route %s: %v", routeBarLabel, err)
		}
	}()

	// Ensure routeBarLabel starts with a clear status.
	if err := waitForRouteStatusClear(t, kclient, routeBarLabelName, ic.Name); err != nil {
		t.Fatalf("failed to observe route has cleared status: %v", err)
	}

	// Wait for routeFooLabel to be admitted upon creation.
	if err := waitForRouteIngressConditions(t, kclient, routeFooLabelName, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Update the ingress controller to not select routeFooLabel to be admitted, but to select
	// routeBarLabel instead.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "bar",
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait until routeFooLabel status is cleared.
	if err := waitForRouteStatusClear(t, kclient, routeFooLabelName, ic.Name); err != nil {
		t.Fatalf("failed to observe route status clear when the ingress controller route selector was updated: %v", err)
	}

	// Wait until routeBarLabel status is admitted.
	if err := waitForRouteIngressConditions(t, kclient, routeBarLabelName, ic.Name, admittedCondition); err != nil {
		t.Fatalf("failed to observe route status admitted when the ingress controller route selector was updated: %v", err)
	}
}

// waitForRouteStatusClear waits for route to be unadmitted (e.g. admitted status cleared) by given ingress controller.
func waitForRouteStatusClear(t *testing.T, cl client.Client, routeName types.NamespacedName, routerName string) error {
	t.Helper()
	return wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		route := &routev1.Route{}
		if err := cl.Get(context.TODO(), routeName, route); err != nil {
			t.Logf("failed to get route %s: %v", routeName, err)
			return false, nil
		}

		// If we find a status with our ingress, keep trying.
		found := false
		for _, routeIngress := range route.Status.Ingress {
			if routeIngress.RouterName == routerName {
				found = true
			}
		}

		return !found, nil
	})
}

// newRouteWithLabel generates a route object with given label as "type".
func newRouteWithLabel(routeName types.NamespacedName, label string) *routev1.Route {
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: routeName.Namespace,
			Name:      routeName.Name,
			Labels: map[string]string{
				"type": label,
			},
		},
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: "foo",
			},
		},
	}
}
