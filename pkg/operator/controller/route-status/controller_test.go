package routestatus

import (
	"context"
	"testing"

	v12 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/openshift/cluster-ingress-operator/test/unit"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/stretchr/testify/assert"
)

// Test_Reconcile verifies Reconcile behaves as expected.
// Note: This effectively tests clearStaleRouteAdmittedStatus as well.
func Test_Reconcile(t *testing.T) {
	reconcileRequestRoute := reconcile.Request{
		NamespacedName: fooRouteNsName,
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
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcNsName.Name).Build(),
		},
		{
			name:              "don't clear admitted status with no routeSelector and no namespaceSelector with route with labels in namespace with no labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
		},
		{
			name:              "don't clear admitted status with no routeSelector and no namespaceSelector with route with no labels in namespace with labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).WithLabels(shardLabel).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcNsName.Name).Build(),
		},
		{
			name:              "don't clear admitted status with routeSelector and no namespaceSelector with route with matching labels in namespace with no labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteSelectors(shardLabel).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
		},
		{
			name:              "clear stale admitted status with routeSelector and no namespaceSelector with route with no labels in namespace with no labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteSelectors(shardLabel).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).Build(),
		},
		{
			name:              "clear stale admitted status with routeSelector and no namespaceSelector with route with different labels in namespace with no labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(notShardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteSelectors(shardLabel).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(notShardLabel).Build(),
		},
		{
			name:              "don't clear admitted status with routeSelector and namespaceSelector with route with matching labels in namespace with matching labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).WithLabels(shardLabel).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteSelectors(shardLabel).WithNamespaceSelectors(shardLabel).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
		},
		{
			name:              "clear stale admitted status with routeSelector and namespaceSelector with route with different labels in namespace with matching labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(notShardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).WithLabels(shardLabel).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteSelectors(shardLabel).WithNamespaceSelectors(shardLabel).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(notShardLabel).Build(),
		},
		{
			name:              "clear stale admitted status with routeSelector and namespaceSelector with route with matching labels in namespace with different labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).WithLabels(notShardLabel).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteSelectors(shardLabel).WithNamespaceSelectors(shardLabel).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).Build(),
		},
		{
			name:              "don't clear admitted status with expression routeSelector and no namespaceSelector with route with matching labels in namespace with no labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteExpressionSelector(shardLabelExpression).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(shardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
		},
		{
			name:              "clear stale admitted status with expression routeSelector and no namespaceSelector with route with different labels in namespace with no labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(notShardLabel).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithRouteExpressionSelector(shardLabelExpression).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithLabels(notShardLabel).Build(),
		},
		{
			name:              "don't clear admitted status with no routeSelector and expression namespaceSelector with route with matching labels in namespace with matching labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).WithLabels(shardLabel).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithNamespaceExpressionSelector(shardLabelExpression).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcNsName.Name).Build(),
		},
		{
			name:              "clear stale admitted status with no routeSelector and expression namespaceSelector with route with no labels in namespace with different labels",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs(fooIcNsName.Name).Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).WithLabels(notShardLabel).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).WithNamespaceExpressionSelector(shardLabelExpression).Build(),
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).Build(),
		},
		{
			name:              "route is nil",
			route:             nil,
			namespace:         unit.NewNamespaceBuilder().WithName(fooRouteNsName.Name).Build(),
			ingressController: unit.NewIngressControllerBuilder().WithName(fooIcNsName).Build(),
		},
		{
			name:              "don't clear admitted by ingress controller that doesn't exist anymore",
			route:             unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs("missing-ic").Build(),
			namespace:         unit.NewNamespaceBuilder().WithName(ns1Name).Build(),
			ingressController: nil,
			expectedRoute:     unit.NewRouteBuilder().WithName(fooRouteNsName).WithAdmittedICs("missing-ic").Build(),
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
