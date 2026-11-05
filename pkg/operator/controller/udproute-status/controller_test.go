package udproute_status

import (
	"context"
	"encoding/json"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	informercache "k8s.io/client-go/tools/cache"
	"os"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ctrltestutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"

	operatorv1 "github.com/openshift/api/operator/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestReconcileManagedParentsAndPreservesForeignStatus(t *testing.T) {
	managedClass := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
	unmanagedClass := gatewayClass("unmanaged", "example.com/other-controller")
	objects := []client.Object{
		managedClass,
		unmanagedClass,
		gateway("local", "route-ns", managedClass.Name),
		gateway("cross", "gateway-ns", managedClass.Name),
		gateway("other", "route-ns", unmanagedClass.Name),
	}
	serviceGroup := gatewayapiv1.Group("")
	serviceKind := gatewayapiv1.Kind("Service")
	gatewayGroup := gatewayapiv1.Group(gatewayapiv1.GroupName)
	gatewayKind := gatewayapiv1.Kind("Gateway")
	routeNamespace := gatewayapiv1.Namespace("route-ns")
	crossNamespace := gatewayapiv1.Namespace("gateway-ns")
	sectionOne := gatewayapiv1.SectionName("one")
	sectionTwo := gatewayapiv1.SectionName("two")
	sectionCross := gatewayapiv1.SectionName("cross")
	port53 := gatewayapiv1.PortNumber(53)
	managedParentRefs := []gatewayapiv1.ParentReference{
		{Name: "local", SectionName: &sectionOne},
		{Group: &gatewayGroup, Kind: &gatewayKind, Namespace: &routeNamespace, Name: "local", SectionName: &sectionTwo, Port: &port53},
		{Group: &gatewayGroup, Kind: &gatewayKind, Namespace: &crossNamespace, Name: "cross", SectionName: &sectionCross, Port: &port53},
	}
	route := udpRoute("test", "route-ns", []gatewayapiv1.ParentReference{
		managedParentRefs[0],
		managedParentRefs[1],
		managedParentRefs[2],
		{Name: "other"},
		{Group: &serviceGroup, Kind: &serviceKind, Name: "backend"},
	})
	route.SetGeneration(7)
	setRouteParents(t, route, []gatewayapiv1.RouteParentStatus{
		{
			ParentRef:      gatewayapiv1.ParentReference{Name: "other"},
			ControllerName: "example.com/other-controller",
			Conditions: []metav1.Condition{{
				Type:   string(gatewayapiv1.RouteConditionAccepted),
				Status: metav1.ConditionTrue,
				Reason: string(gatewayapiv1.RouteReasonAccepted),
			}},
		},
		{
			ParentRef:      gatewayapiv1.ParentReference{Name: "local", SectionName: &sectionOne},
			ControllerName: gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName),
			Conditions: []metav1.Condition{{
				Type:   string(gatewayapiv1.RouteConditionResolvedRefs),
				Status: metav1.ConditionFalse,
				Reason: string(gatewayapiv1.RouteReasonBackendNotFound),
			}},
		},
	})
	objects = append(objects, route)
	r := newTestReconciler(t, objects...)
	udpRouteOnManagedGatewayMetric.Reset()

	_, err := r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)

	updated := newUDPRoute("v1")
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, routeParents(t, updated), 4)
	assert.Equal(t, gatewayapiv1.GatewayController("example.com/other-controller"), routeParents(t, updated)[0].ControllerName)
	assert.Equal(t, metav1.ConditionTrue, routeParents(t, updated)[0].Conditions[0].Status)

	type parentStatusIdentity struct {
		ParentRef      gatewayapiv1.ParentReference
		ControllerName gatewayapiv1.GatewayController
	}
	wantOwnedIdentities := make([]parentStatusIdentity, 0, len(managedParentRefs))
	for _, parentRef := range managedParentRefs {
		wantOwnedIdentities = append(wantOwnedIdentities, parentStatusIdentity{
			ParentRef:      parentRef,
			ControllerName: gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName),
		})
	}
	gotOwnedIdentities := make([]parentStatusIdentity, 0, len(managedParentRefs))
	for _, parentStatus := range routeParents(t, updated)[1:] {
		assert.Equal(t, gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName), parentStatus.ControllerName)
		gotOwnedIdentities = append(gotOwnedIdentities, parentStatusIdentity{ParentRef: parentStatus.ParentRef, ControllerName: parentStatus.ControllerName})
		condition := meta.FindStatusCondition(parentStatus.Conditions, string(gatewayapiv1.RouteConditionAccepted))
		if assert.NotNil(t, condition) {
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, ReasonUnsupportedByController, condition.Reason)
			assert.Equal(t, unsupportedMessage, condition.Message)
			assert.Equal(t, int64(7), condition.ObservedGeneration)
		}
	}
	assert.ElementsMatch(t, wantOwnedIdentities, gotOwnedIdentities, "owned status must retain each complete ParentRef identity")
	resolvedRefs := meta.FindStatusCondition(routeParents(t, updated)[1].Conditions, string(gatewayapiv1.RouteConditionResolvedRefs))
	if assert.NotNil(t, resolvedRefs) {
		assert.Equal(t, string(gatewayapiv1.RouteReasonBackendNotFound), resolvedRefs.Reason)
	}
	assert.Equal(t, 1, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
	assert.Equal(t, float64(1), promtestutil.ToFloat64(udpRouteOnManagedGatewayMetric.WithLabelValues("route-ns", "test")))
}

func TestReconcileConflictsWithConcurrentForeignStatusWriter(t *testing.T) {
	managedClass := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
	managedGateway := gateway("managed", "route-ns", managedClass.Name)
	parentRef := gatewayapiv1.ParentReference{Name: "managed"}
	route := udpRoute("test", "route-ns", []gatewayapiv1.ParentReference{parentRef})
	route.SetGeneration(2)
	setRouteParents(t, route, []gatewayapiv1.RouteParentStatus{{
		ParentRef:      parentRef,
		ControllerName: gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName),
		Conditions: []metav1.Condition{
			{Type: string(gatewayapiv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: string(gatewayapiv1.RouteReasonAccepted)},
			{Type: string(gatewayapiv1.RouteConditionResolvedRefs), Status: metav1.ConditionFalse, Reason: string(gatewayapiv1.RouteReasonBackendNotFound)},
		},
	}})
	r := newTestReconciler(t, managedClass, managedGateway, route)
	r.client = udpHTTPClient(t, route)
	foreignStatus := gatewayapiv1.RouteParentStatus{
		ParentRef:      gatewayapiv1.ParentReference{Name: "foreign"},
		ControllerName: "example.com/foreign-controller",
		Conditions: []metav1.Condition{
			{Type: string(gatewayapiv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: string(gatewayapiv1.RouteReasonAccepted), Message: "foreign accepted"},
			{Type: string(gatewayapiv1.RouteConditionResolvedRefs), Status: metav1.ConditionTrue, Reason: string(gatewayapiv1.RouteReasonResolvedRefs), Message: "foreign refs resolved"},
		},
	}
	writer := &concurrentForeignStatusWriter{
		t:            t,
		StatusWriter: r.client.Status(),
		client:       r.client,
		foreign:      foreignStatus,
	}
	r.client = &statusWriterClient{Client: r.client, statusWriter: writer}
	udpRouteOnManagedGatewayMetric.Reset()

	_, err := r.Reconcile(context.Background(), requestFor(route))
	assert.Equal(t, 1, writer.patchCalls)
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err), "the stale first status write must surface a conflict: %v", err)
	assert.Equal(t, 1, writer.patchCalls)

	_, err = r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	assert.Equal(t, 2, writer.patchCalls)

	updated := newUDPRoute("v1")
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, routeParents(t, updated), 2)
	assert.Equal(t, foreignStatus, routeParents(t, updated)[0], "retry must preserve the concurrent writer's complete status entry")
	parents, _, err := unstructured.NestedSlice(updated.Object, "status", "parents")
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"nested": "must survive concurrent writes"}, parents[0].(map[string]interface{})["futureField"])
	owned := routeParents(t, updated)[1]
	assert.Equal(t, parentRef, owned.ParentRef)
	resolvedRefs := meta.FindStatusCondition(owned.Conditions, string(gatewayapiv1.RouteConditionResolvedRefs))
	require.NotNil(t, resolvedRefs)
	assert.Equal(t, string(gatewayapiv1.RouteReasonBackendNotFound), resolvedRefs.Reason)
	accepted := meta.FindStatusCondition(owned.Conditions, string(gatewayapiv1.RouteConditionAccepted))
	require.NotNil(t, accepted)
	assert.Equal(t, metav1.ConditionFalse, accepted.Status)
	assert.Equal(t, ReasonUnsupportedByController, accepted.Reason)
}

func TestReconcileIgnoresUnmanagedMissingAndNonGatewayParents(t *testing.T) {
	managedClass := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
	unmanagedClass := gatewayClass("unmanaged", "example.com/other-controller")
	serviceGroup := gatewayapiv1.Group("")
	serviceKind := gatewayapiv1.Kind("Service")
	tests := []struct {
		name    string
		objects []client.Object
		parent  gatewayapiv1.ParentReference
	}{
		{
			name:    "unmanaged GatewayClass",
			objects: []client.Object{unmanagedClass, gateway("parent", "route-ns", unmanagedClass.Name)},
			parent:  gatewayapiv1.ParentReference{Name: "parent"},
		},
		{
			name:    "missing Gateway",
			objects: []client.Object{managedClass},
			parent:  gatewayapiv1.ParentReference{Name: "missing"},
		},
		{
			name:    "missing GatewayClass",
			objects: []client.Object{gateway("parent", "route-ns", "missing")},
			parent:  gatewayapiv1.ParentReference{Name: "parent"},
		},
		{
			name:   "Service parent",
			parent: gatewayapiv1.ParentReference{Group: &serviceGroup, Kind: &serviceKind, Name: "backend"},
		},
		{
			name: "foreign group",
			parent: gatewayapiv1.ParentReference{
				Group: ptr.To(gatewayapiv1.Group("example.com")),
				Kind:  ptr.To(gatewayapiv1.Kind("Gateway")),
				Name:  "parent",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			route := udpRoute("test", "route-ns", []gatewayapiv1.ParentReference{tc.parent})
			objects := append(append([]client.Object{}, tc.objects...), route)
			r := newTestReconciler(t, objects...)
			udpRouteOnManagedGatewayMetric.Reset()

			_, err := r.Reconcile(context.Background(), requestFor(route))
			require.NoError(t, err)
			updated := newUDPRoute("v1")
			require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
			require.Nil(t, routeParents(t, updated), "a route never owned by CIO must not receive a status patch")
			assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
		})
	}
}

func TestReconcileCleansStaleStatusAndMetricAfterRetarget(t *testing.T) {
	managedClass := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
	unmanagedClass := gatewayClass("unmanaged", "example.com/other-controller")
	managedGateway := gateway("managed", "route-ns", managedClass.Name)
	unmanagedGateway := gateway("unmanaged", "route-ns", unmanagedClass.Name)
	route := udpRoute("test", "route-ns", []gatewayapiv1.ParentReference{{Name: "managed"}})
	route.SetGeneration(1)
	setRouteParents(t, route, []gatewayapiv1.RouteParentStatus{{
		ParentRef:      gatewayapiv1.ParentReference{Name: "foreign"},
		ControllerName: "example.com/other-controller",
		Conditions:     []metav1.Condition{{Type: string(gatewayapiv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: string(gatewayapiv1.RouteReasonAccepted)}},
	}})
	r := newTestReconciler(t, managedClass, unmanagedClass, managedGateway, unmanagedGateway, route)
	udpRouteOnManagedGatewayMetric.Reset()

	_, err := r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	assert.Equal(t, 1, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))

	updated := newUDPRoute("v1")
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, routeParents(t, updated), 2)
	firstTransition := routeParents(t, updated)[1].Conditions[0].LastTransitionTime

	// An idempotent reconcile keeps the existing condition transition time.
	_, err = r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	assert.Equal(t, firstTransition, routeParents(t, updated)[1].Conditions[0].LastTransitionTime)

	// A non-parent spec change refreshes ObservedGeneration without changing
	// the Accepted condition's transition time.
	updated.SetGeneration(2)
	require.NoError(t, unstructured.SetNestedSlice(updated.Object, []interface{}{map[string]interface{}{"backendRefs": []interface{}{map[string]interface{}{"name": "new-backend", "port": int64(53)}}}}, "spec", "rules"))
	require.NoError(t, r.client.Update(context.Background(), updated))
	_, err = r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, routeParents(t, updated), 2)
	accepted := meta.FindStatusCondition(routeParents(t, updated)[1].Conditions, string(gatewayapiv1.RouteConditionAccepted))
	require.NotNil(t, accepted)
	assert.Equal(t, int64(2), accepted.ObservedGeneration)
	assert.Equal(t, firstTransition, accepted.LastTransitionTime)

	setRouteParentRefs(t, updated, []gatewayapiv1.ParentReference{{Name: "unmanaged"}})
	updated.SetGeneration(3)
	require.NoError(t, r.client.Update(context.Background(), updated))
	_, err = r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, routeParents(t, updated), 1)
	assert.Equal(t, gatewayapiv1.GatewayController("example.com/other-controller"), routeParents(t, updated)[0].ControllerName)
	assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
}

func TestReconcileCleansLastOwnedStatusAfterRetarget(t *testing.T) {
	managedClass := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
	unmanagedClass := gatewayClass("unmanaged", "example.com/other-controller")
	managedGateway := gateway("managed", "route-ns", managedClass.Name)
	unmanagedGateway := gateway("unmanaged", "route-ns", unmanagedClass.Name)
	managedParent := gatewayapiv1.ParentReference{Name: "managed"}
	route := udpRoute("test", "route-ns", []gatewayapiv1.ParentReference{managedParent})
	route.SetGeneration(1)
	setRouteParents(t, route, []gatewayapiv1.RouteParentStatus{{
		ParentRef:      managedParent,
		ControllerName: gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName),
		Conditions: []metav1.Condition{{
			Type:   string(gatewayapiv1.RouteConditionAccepted),
			Status: metav1.ConditionFalse,
			Reason: ReasonUnsupportedByController,
		}},
	}})
	r := newTestReconciler(t, managedClass, unmanagedClass, managedGateway, unmanagedGateway, route)
	udpRouteOnManagedGatewayMetric.Reset()
	udpRouteOnManagedGatewayMetric.WithLabelValues(route.GetNamespace(), route.GetName()).Set(1)

	updated := newUDPRoute("v1")
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, routeParents(t, updated), 1)
	assert.Equal(t, gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName), routeParents(t, updated)[0].ControllerName)

	setRouteParentRefs(t, updated, []gatewayapiv1.ParentReference{{Name: "unmanaged"}})
	updated.SetGeneration(2)
	require.NoError(t, r.client.Update(context.Background(), updated))
	_, err := r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.NotNil(t, routeParents(t, updated))
	assert.Empty(t, routeParents(t, updated))
	assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))

	statusJSON, err := json.Marshal(updated.Object["status"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"parents":[]}`, string(statusJSON))

	var statusValue map[string]interface{}
	require.NoError(t, json.Unmarshal(statusJSON, &statusValue))
	assertUDPRouteStatusMatchesShippedParentsSchema(t, statusValue)
}

func TestReconcileCleansStaleStatusAfterParentDisappears(t *testing.T) {
	tests := []struct {
		name   string
		delete func(context.Context, client.Client, *gatewayapiv1.GatewayClass, *gatewayapiv1.Gateway) error
	}{
		{
			name: "Gateway deletion",
			delete: func(ctx context.Context, cl client.Client, _ *gatewayapiv1.GatewayClass, gateway *gatewayapiv1.Gateway) error {
				return cl.Delete(ctx, gateway)
			},
		},
		{
			name: "GatewayClass deletion",
			delete: func(ctx context.Context, cl client.Client, class *gatewayapiv1.GatewayClass, _ *gatewayapiv1.Gateway) error {
				return cl.Delete(ctx, class)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			managedClass := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
			managedGateway := gateway("managed", "route-ns", managedClass.Name)
			route := udpRoute("test", "route-ns", []gatewayapiv1.ParentReference{{Name: "managed"}})
			r := newTestReconciler(t, managedClass, managedGateway, route)
			udpRouteOnManagedGatewayMetric.Reset()
			_, err := r.Reconcile(context.Background(), requestFor(route))
			require.NoError(t, err)
			require.NoError(t, tc.delete(context.Background(), r.client, managedClass, managedGateway))
			_, err = r.Reconcile(context.Background(), requestFor(route))
			require.NoError(t, err)

			updated := newUDPRoute("v1")
			require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
			assert.Empty(t, routeParents(t, updated))
			assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
		})
	}
}

func TestReconcileCleansMetricAfterDeletion(t *testing.T) {
	managedClass := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
	managedGateway := gateway("managed", "route-ns", managedClass.Name)
	route := udpRoute("test", "route-ns", []gatewayapiv1.ParentReference{{Name: "managed"}})
	r := newTestReconciler(t, managedClass, managedGateway, route)
	udpRouteOnManagedGatewayMetric.Reset()

	_, err := r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	assert.Equal(t, 1, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
	require.NoError(t, r.client.Delete(context.Background(), route))
	_, err = r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
}

func TestReconcileCleansMetricWhileDependentsBlocked(t *testing.T) {
	managedClass := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
	managedGateway := gateway("managed", "route-ns", managedClass.Name)
	route := udpRoute("test", "route-ns", []gatewayapiv1.ParentReference{{Name: "managed"}})
	setRouteParents(t, route, []gatewayapiv1.RouteParentStatus{{
		ParentRef:      routeParentRefs(t, route)[0],
		ControllerName: gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName),
		Conditions:     []metav1.Condition{{Type: string(gatewayapiv1.RouteConditionAccepted), Status: metav1.ConditionFalse, Reason: ReasonUnsupportedByController}},
	}})

	t.Run("existing route", func(t *testing.T) {
		r := newTestReconciler(t, managedClass, managedGateway, route)
		r.modeAccessor.SetCRDsEstablished(false)
		udpRouteOnManagedGatewayMetric.Reset()
		udpRouteOnManagedGatewayMetric.WithLabelValues(route.GetNamespace(), route.GetName()).Set(1)

		_, err := r.Reconcile(context.Background(), requestFor(route))
		require.NoError(t, err)
		assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
		updated := newUDPRoute("v1")
		require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
		assert.Equal(t, route.Object["status"], updated.Object["status"], "blocked reconciliation must not patch status")
	})

	t.Run("deleted route", func(t *testing.T) {
		r := newTestReconciler(t, managedClass, managedGateway, route)
		r.modeAccessor.SetCRDsEstablished(false)
		udpRouteOnManagedGatewayMetric.Reset()
		udpRouteOnManagedGatewayMetric.WithLabelValues(route.GetNamespace(), route.GetName()).Set(1)
		require.NoError(t, r.client.Delete(context.Background(), route))

		_, err := r.Reconcile(context.Background(), requestFor(route))
		require.NoError(t, err)
		assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
	})
}

func TestBlockedDeleteAndModeWakeupsCleanMetric(t *testing.T) {
	managedClass := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
	managedGateway := gateway("managed", "route-ns", managedClass.Name)
	route := udpRoute("test", "route-ns", []gatewayapiv1.ParentReference{{Name: "managed"}})

	tests := []struct {
		name     string
		requests func(context.Context, *reconciler) []reconcile.Request
	}{
		{
			name: "Gateway delete",
			requests: func(ctx context.Context, r *reconciler) []reconcile.Request {
				return r.gatewayToUDPRoutes(ctx, managedGateway)
			},
		},
		{
			name: "GatewayClass delete",
			requests: func(ctx context.Context, r *reconciler) []reconcile.Request {
				return r.allUDPRoutes(ctx, managedClass)
			},
		},
		{
			name: "Ingress mode transition",
			requests: func(ctx context.Context, r *reconciler) []reconcile.Request {
				return r.allUDPRoutes(ctx, &operatorv1.Ingress{})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestReconciler(t, managedClass, managedGateway, route)
			r.modeAccessor.SetCRDsEstablished(false)
			udpRouteOnManagedGatewayMetric.Reset()
			udpRouteOnManagedGatewayMetric.WithLabelValues(route.GetNamespace(), route.GetName()).Set(1)

			requests := tc.requests(context.Background(), r)
			require.Equal(t, []reconcile.Request{requestFor(route)}, requests)
			for _, request := range requests {
				_, err := r.Reconcile(context.Background(), request)
				require.NoError(t, err)
			}
			assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
		})
	}
}

func TestUDPRouteEventPredicate(t *testing.T) {
	p := udpRouteEventPredicate()
	oldRoute := udpRoute("test", "route-ns", nil)
	oldRoute.SetGeneration(3)
	statusOnly := oldRoute.DeepCopy()
	setRouteParents(t, statusOnly, []gatewayapiv1.RouteParentStatus{{ControllerName: "example.com/controller"}})
	generationChange := statusOnly.DeepCopy()
	generationChange.SetGeneration(4)

	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: oldRoute, ObjectNew: statusOnly}), "status-only updates must not enqueue")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: statusOnly, ObjectNew: generationChange}), "generation/spec updates must enqueue")
	assert.True(t, p.Create(event.CreateEvent{Object: oldRoute}))
	assert.True(t, p.Delete(event.DeleteEvent{Object: oldRoute}))
	assert.False(t, p.Generic(event.GenericEvent{Object: oldRoute}))
}

func TestParentGatewayIndexKeys(t *testing.T) {
	sectionOne := gatewayapiv1.SectionName("one")
	sectionTwo := gatewayapiv1.SectionName("two")
	serviceGroup := gatewayapiv1.Group("")
	serviceKind := gatewayapiv1.Kind("Service")
	route := udpRoute("test", "route-ns", []gatewayapiv1.ParentReference{
		{Name: "local", SectionName: &sectionOne},
		{Name: "local", SectionName: &sectionTwo},
		{Name: "cross", Namespace: ptr.To(gatewayapiv1.Namespace("gateway-ns"))},
		{Group: &serviceGroup, Kind: &serviceKind, Name: "backend"},
		{Group: ptr.To(gatewayapiv1.Group("example.com")), Kind: ptr.To(gatewayapiv1.Kind("Gateway")), Name: "foreign"},
	})
	keys, err := parentGatewayIndexKeys(route)
	require.NoError(t, err)
	assert.Equal(t, []string{"route-ns/local", "gateway-ns/cross"}, keys)
}

func newTestReconciler(t *testing.T, objects ...client.Object) *reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, gatewayapiv1.Install(scheme))
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(newUDPRoute("v1"), newUDPRoute("v1alpha2")).
		WithIndex(newUDPRoute("v1"), udpRouteParentGatewayIndex, func(obj client.Object) []string {
			keys, err := parentGatewayIndexKeys(obj.(*unstructured.Unstructured))
			if err != nil {
				panic(err)
			}
			return keys
		}).
		Build()
	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := ctrltestutil.FakeCache{Informers: &informer, Reader: cl}
	modeAccessor := operatorcontroller.NewGatewayAPIModeAccessor(false)
	modeAccessor.SetCRDsEstablished(true)
	routeInformer := informercache.NewSharedIndexInformer(&informercache.ListWatch{}, newUDPRoute("v1"), 0, informercache.Indexers{udpRouteParentGatewayIndex: func(object interface{}) ([]string, error) {
		return parentGatewayIndexKeys(object.(*unstructured.Unstructured))
	}})
	for _, object := range objects {
		if route, ok := object.(*unstructured.Unstructured); ok {
			require.NoError(t, routeInformer.GetIndexer().Add(route))
		}
	}
	return &reconciler{client: cl, cache: fakeCache, gatewayCache: fakeCache, modeAccessor: modeAccessor, resource: schema.GroupVersionResource{Group: gatewayapiv1.GroupName, Version: "v1", Resource: "udproutes"}, informer: routeInformer}
}

func gatewayClass(name string, controller gatewayapiv1.GatewayController) *gatewayapiv1.GatewayClass {
	return &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: controller},
	}
}

func gateway(name, namespace, className string) *gatewayapiv1.Gateway {
	return &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: gatewayapiv1.ObjectName(className),
			Listeners:        []gatewayapiv1.Listener{{Name: "udp", Port: 53, Protocol: gatewayapiv1.UDPProtocolType}},
		},
	}
}

func udpRoute(name, namespace string, parents []gatewayapiv1.ParentReference) *unstructured.Unstructured {
	route := newUDPRoute("v1")
	route.SetName(name)
	route.SetNamespace(namespace)
	values, err := parentRefFixtureValues(parents)
	if err != nil {
		panic(err)
	}
	route.Object["spec"] = map[string]interface{}{"parentRefs": values, "rules": []interface{}{map[string]interface{}{"backendRefs": []interface{}{map[string]interface{}{"name": "backend", "port": int64(53)}}}}}
	return route
}

func requestFor(route *unstructured.Unstructured) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: route.GetNamespace(), Name: route.GetName()}}
}

func assertUDPRouteStatusMatchesShippedParentsSchema(t *testing.T, status map[string]interface{}) {
	t.Helper()
	data, err := os.ReadFile("testdata/udproute-v1.6.2-status-array-schema.json")
	require.NoError(t, err)
	var statusSchema apiextensionsv1.JSONSchemaProps
	require.NoError(t, json.Unmarshal(data, &statusSchema))

	require.Contains(t, statusSchema.Required, "parents")
	parentsSchema, ok := statusSchema.Properties["parents"]
	require.True(t, ok, "shipped UDPRoute v1 status schema must define parents")
	require.Equal(t, "array", parentsSchema.Type)
	parents, ok := status["parents"]
	require.True(t, ok, "serialized UDPRoute status must contain required parents")
	assert.IsType(t, []interface{}{}, parents)
	assert.Empty(t, parents)
}

// concurrentForeignStatusWriter simulates another controller updating its
// status.parents entry after CIO's cache read but before CIO's first patch.
type concurrentForeignStatusWriter struct {
	t *testing.T
	client.StatusWriter
	client     client.Client
	foreign    gatewayapiv1.RouteParentStatus
	updated    bool
	patchCalls int
}

func (w *concurrentForeignStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	w.patchCalls++
	data, dataErr := patch.Data(obj)
	require.NoError(w.t, dataErr)
	var patchValue map[string]interface{}
	require.NoError(w.t, json.Unmarshal(data, &patchValue))
	metadata, ok := patchValue["metadata"].(map[string]interface{})
	require.True(w.t, ok, "optimistic patch must include metadata")
	require.Equal(w.t, obj.GetResourceVersion(), metadata["resourceVersion"])
	if !w.updated {
		w.updated = true
		route := newUDPRoute("v1")
		if err := w.client.Get(ctx, client.ObjectKeyFromObject(obj), route); err != nil {
			return err
		}
		parents, _, err := unstructured.NestedSlice(route.Object, "status", "parents")
		require.NoError(w.t, err)
		foreign, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&w.foreign)
		require.NoError(w.t, err)
		foreign["futureField"] = map[string]interface{}{"nested": "must survive concurrent writes"}
		require.NoError(w.t, unstructured.SetNestedSlice(route.Object, append(parents, foreign), "status", "parents"))
		if err := w.StatusWriter.Update(ctx, route); err != nil {
			return err
		}
	}
	return w.StatusWriter.Patch(ctx, obj, patch, opts...)
}

type statusWriterClient struct {
	client.Client
	statusWriter client.StatusWriter
}

func (c *statusWriterClient) Status() client.StatusWriter {
	return c.statusWriter
}

func routeParents(t *testing.T, route *unstructured.Unstructured) []gatewayapiv1.RouteParentStatus {
	t.Helper()
	values, found, err := unstructured.NestedSlice(route.Object, "status", "parents")
	require.NoError(t, err)
	if !found {
		return nil
	}
	parents := make([]gatewayapiv1.RouteParentStatus, len(values))
	for index, value := range values {
		require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(value.(map[string]interface{}), &parents[index]))
	}
	return parents
}

func setRouteParents(t *testing.T, route *unstructured.Unstructured, parents []gatewayapiv1.RouteParentStatus) {
	t.Helper()
	values := make([]interface{}, len(parents))
	for index := range parents {
		value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&parents[index])
		require.NoError(t, err)
		values[index] = value
	}
	require.NoError(t, unstructured.SetNestedSlice(route.Object, values, "status", "parents"))
}

func routeParentRefs(t *testing.T, route *unstructured.Unstructured) []gatewayapiv1.ParentReference {
	t.Helper()
	parents, err := parentReferences(route)
	require.NoError(t, err)
	return parents
}

func setRouteParentRefs(t *testing.T, route *unstructured.Unstructured, parents []gatewayapiv1.ParentReference) {
	t.Helper()
	values, err := parentRefFixtureValues(parents)
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedSlice(route.Object, values, "spec", "parentRefs"))
}

func parentRefFixtureValues(parents []gatewayapiv1.ParentReference) ([]interface{}, error) {
	values := make([]interface{}, len(parents))
	for index := range parents {
		value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&parents[index])
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return values, nil
}
