package routestatus

import (
	"context"
	v12 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/test/unit"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"testing"
)

// Test_Reconcile verifies Reconcile
// Note: This effectively tests clearStaleRouteAdmittedStatus as well
func Test_Reconcile(t *testing.T) {
	reconcileRequestRoute := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "route",
			Namespace: "ns",
		},
	}
	testCases := []struct {
		name              string
		request           reconcile.Request
		route             *routev1.Route
		ingressController *v12.IngressController
		namespace         *corev1.Namespace
		expectedRoute     *routev1.Route
		expectedErr       bool
	}{
		{
			name:              "admitted with no routeSelector and no namespaceSelector with route with no labels in namespace with no labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
			namespace:         newNamespace("ns", ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
		},
		{
			name:              "admitted with no routeSelector and no namespaceSelector with route with correct labels in namespace with no labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
			namespace:         newNamespace("ns", ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
		},
		{
			name:              "admitted with no routeSelector and no namespaceSelector with route with no labels in namespace with correct labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
			namespace:         newNamespace("ns", "shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
		},
		{
			name:              "admitted with routeSelector and no namespaceSelector with route with correct labels in namespace with no labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "shard-label", "default-ic"),
			namespace:         newNamespace("ns", ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
		},
		{
			name:              "not admitted with routeSelector and no namespaceSelector with route with no labels in namespace with no labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
			namespace:         newNamespace("ns", ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", ""),
		},
		{
			name:              "not admitted with routeSelector and no namespaceSelector with route with incorrect labels in namespace with no labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "not-shard-label", "default-ic"),
			namespace:         newNamespace("ns", ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", ""),
		},
		{
			name:              "admitted with routeSelector and namespaceSelector with route with correct labels in namespace with correct labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "shard-label", "default-ic"),
			namespace:         newNamespace("ns", "shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "shard-label", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
		},
		{
			name:              "not admitted with routeSelector and namespaceSelector with route with incorrect labels in namespace with correct labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "not-shard-label", "default-ic"),
			namespace:         newNamespace("ns", "shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "shard-label", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", ""),
		},
		{
			name:              "not admitted with routeSelector and namespaceSelector with route with correct labels in namespace with incorrect labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "shard-label", "default-ic"),
			namespace:         newNamespace("ns", "not-shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "shard-label", "", "shard-label", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", ""),
		},
		{
			name:              "admitted with expression routeSelector and no namespaceSelector with route with correct labels in namespace with no labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "shard-label", "default-ic"),
			namespace:         newNamespace("ns", ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "shard-label", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
		},
		{
			name:              "not admitted with expression routeSelector and no namespaceSelector with route with incorrect labels in namespace with no labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "not-shard-label", "default-ic"),
			namespace:         newNamespace("ns", ""),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "shard-label", "", ""),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", ""),
		},
		{
			name:              "admitted with no routeSelector and expression namespaceSelector with route with correct labels in namespace with no labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
			namespace:         newNamespace("ns", "shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", "shard-label"),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
		},
		{
			name:              "not admitted with no routeSelector and expression namespaceSelector with route with incorrect labels in namespace with no labels",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "default-ic"),
			namespace:         newNamespace("ns", "not-shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", "shard-label"),
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", ""),
		},
		{
			name:              "route is nil",
			request:           reconcileRequestRoute,
			route:             nil,
			namespace:         newNamespace("ns", "not-shard-label"),
			ingressController: newIngressControllerWithSelectors("default-ic", "", "", "", "shard-label"),
			expectedErr:       false,
		},
		{
			name:              "admitted by ingress controller that doesn't exist anymore",
			request:           reconcileRequestRoute,
			route:             newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "missing-ic"),
			namespace:         newNamespace("ns", "shard-label"),
			ingressController: nil,
			expectedRoute:     newRouteWithLabelWithAdmittedStatuses("route", "ns", "", "missing-ic"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeReconciler(tc.namespace)
			if tc.route != nil {
				if err := r.client.Create(context.Background(), tc.route); err != nil {
					t.Errorf("error creating route: %v", err)
				}
			}
			if tc.ingressController != nil {
				if err := r.client.Create(context.Background(), tc.ingressController); err != nil {
					t.Errorf("error creating ingress controller: %v", err)
				}
			}

			if _, err := r.Reconcile(context.Background(), tc.request); tc.expectedErr && err == nil {
				t.Errorf("expected error, got no error")
			} else if !tc.expectedErr && err != nil {
				t.Errorf("did not expected error: %v", err)
			} else if tc.expectedRoute != nil && !tc.expectedErr {
				actualRoute := routev1.Route{}
				actualRouteName := types.NamespacedName{Name: tc.route.Name, Namespace: tc.route.Namespace}
				if err := r.client.Get(context.Background(), actualRouteName, &actualRoute); err != nil {
					t.Errorf("error retrieving route from client: %v", err)
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
		client:    client,
		cache:     cache,
		namespace: controller.DefaultOperandNamespace,
	}
	return &r
}
