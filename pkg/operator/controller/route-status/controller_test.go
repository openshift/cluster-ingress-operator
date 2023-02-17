package routestatus

import (
	"context"
	v12 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/openshift/cluster-ingress-operator/test/unit"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"testing"
)

// Test_Reconcile verifies Reconcile behaves as expected.
// Note: This effectively tests clearStaleRouteAdmittedStatus as well.
func Test_Reconcile(t *testing.T) {
	routeName := "route"
	routeNs := "ns"
	reconcileRequestRoute := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      routeName,
			Namespace: routeNs,
		},
	}
	testCases := []struct {
		name              string
		route             *routev1.Route
		ingressController *v12.IngressController
		namespace         *corev1.Namespace
		expectedRoute     *routev1.Route
	}{
		{
			name:              "don't clear admitted status with no routeSelector and no namespaceSelector with route with no labels in namespace with no labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "", "default-ic"),
			namespace:         newNamespace(routeNs, ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "", "default-ic"),
		},
		{
			name:              "don't clear admitted status with no routeSelector and no namespaceSelector with route with labels in namespace with no labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "shard-label", "default-ic"),
			namespace:         newNamespace(routeNs, ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "shard-label", "default-ic"),
		},
		{
			name:              "don't clear admitted status with no routeSelector and no namespaceSelector with route with no labels in namespace with labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "", "default-ic"),
			namespace:         newNamespace(routeNs, "shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "", "default-ic"),
		},
		{
			name:              "don't clear admitted status with routeSelector and no namespaceSelector with route with matching labels in namespace with no labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "shard-label", "default-ic"),
			namespace:         newNamespace(routeNs, ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "shard-label", "default-ic"),
		},
		{
			name:              "clear stale admitted status with routeSelector and no namespaceSelector with route with no labels in namespace with no labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "", "default-ic"),
			namespace:         newNamespace(routeNs, ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, ""),
		},
		{
			name:              "clear stale admitted status with routeSelector and no namespaceSelector with route with different labels in namespace with no labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "not-shard-label", "default-ic"),
			namespace:         newNamespace(routeNs, ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "not-shard-label"),
		},
		{
			name:              "don't clear admitted status with routeSelector and namespaceSelector with route with matching labels in namespace with matching labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "shard-label", "default-ic"),
			namespace:         newNamespace(routeNs, "shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "shard-label", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "shard-label", "default-ic"),
		},
		{
			name:              "clear stale admitted status with routeSelector and namespaceSelector with route with different labels in namespace with matching labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "not-shard-label", "default-ic"),
			namespace:         newNamespace(routeNs, "shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "shard-label", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "not-shard-label"),
		},
		{
			name:              "clear stale admitted status with routeSelector and namespaceSelector with route with matching labels in namespace with different labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "shard-label", "default-ic"),
			namespace:         newNamespace(routeNs, "not-shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "shard-label", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "shard-label"),
		},
		{
			name:              "don't clear admitted status with expression routeSelector and no namespaceSelector with route with matching labels in namespace with no labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "shard-label", "default-ic"),
			namespace:         newNamespace(routeNs, ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "shard-label", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "shard-label", "default-ic"),
		},
		{
			name:              "clear stale admitted status with expression routeSelector and no namespaceSelector with route with different labels in namespace with no labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "not-shard-label", "default-ic"),
			namespace:         newNamespace(routeNs, ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "shard-label", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "not-shard-label"),
		},
		{
			name:              "don't clear admitted status with no routeSelector and expression namespaceSelector with route with matching labels in namespace with matching labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "", "default-ic"),
			namespace:         newNamespace(routeNs, "shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", "shard-label"),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "", "default-ic"),
		},
		{
			name:              "clear stale admitted status with no routeSelector and expression namespaceSelector with route with no labels in namespace with different labels",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "", "default-ic"),
			namespace:         newNamespace(routeNs, "not-shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", "shard-label"),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, ""),
		},
		{
			name:              "route is nil",
			route:             nil,
			namespace:         newNamespace(routeNs, "not-shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", "shard-label"),
		},
		{
			name:              "admitted by ingress controller that doesn't exist anymore",
			route:             newRouteWithLabelWithAdmittedStatuses(routeName, routeNs, "", "missing-ic"),
			namespace:         newNamespace(routeNs, "shard-label"),
			ingressController: nil,
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses(routeName, "ns", "", "missing-ic"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeReconciler(tc.namespace)
			if tc.route != nil {
				if err := r.client.Create(context.Background(), tc.route); err != nil {
					t.Fatalf("error creating route: %v", err)
				}
			}
			if tc.ingressController != nil {
				if err := r.client.Create(context.Background(), tc.ingressController); err != nil {
					t.Fatalf("error creating ingress controller: %v", err)
				}
			}

			if _, err := r.Reconcile(context.Background(), reconcileRequestRoute); err != nil {
				t.Fatalf("did not expected error: %v", err)
			} else if tc.expectedRoute != nil {
				actualRoute := routev1.Route{}
				actualRouteName := types.NamespacedName{Name: tc.route.Name, Namespace: tc.route.Namespace}
				if err := r.client.Get(context.Background(), actualRouteName, &actualRoute); err != nil {
					t.Fatalf("error retrieving route from client: %v", err)
				}
				assert.Equal(t, tc.expectedRoute.Status, actualRoute.Status, "route name", tc.expectedRoute.Name)
			}
		})
	}
}

// newFakeReconciler builds a reconciler object for configurable-route based on fake clients and caches.
func newFakeReconciler(initObjs ...client.Object) *reconciler {
	client := unit.NewFakeClient(initObjs...)
	cache := unit.NewFakeCache(client)
	r := reconciler{
		client: client,
		cache:  cache,
	}
	return &r
}
