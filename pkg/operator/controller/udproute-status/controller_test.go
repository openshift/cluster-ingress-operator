package udproute_status

import (
	"context"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ctrltestutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
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
	route.Generation = 7
	route.Status.Parents = []gatewayapiv1.RouteParentStatus{
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
	}
	objects = append(objects, route)
	r := newTestReconciler(t, objects...)
	udpRouteOnManagedGatewayMetric.Reset()

	_, err := r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)

	updated := &gatewayapiv1.UDPRoute{}
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, updated.Status.Parents, 4)
	assert.Equal(t, gatewayapiv1.GatewayController("example.com/other-controller"), updated.Status.Parents[0].ControllerName)
	assert.Equal(t, metav1.ConditionTrue, updated.Status.Parents[0].Conditions[0].Status)

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
	for _, parentStatus := range updated.Status.Parents[1:] {
		assert.Equal(t, gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName), parentStatus.ControllerName)
		gotOwnedIdentities = append(gotOwnedIdentities, parentStatusIdentity{ParentRef: parentStatus.ParentRef, ControllerName: parentStatus.ControllerName})
		condition := findCondition(parentStatus.Conditions, string(gatewayapiv1.RouteConditionAccepted))
		if assert.NotNil(t, condition) {
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, ReasonUnsupportedByController, condition.Reason)
			assert.Equal(t, unsupportedMessage, condition.Message)
			assert.Equal(t, int64(7), condition.ObservedGeneration)
		}
	}
	assert.ElementsMatch(t, wantOwnedIdentities, gotOwnedIdentities, "owned status must retain each complete ParentRef identity")
	resolvedRefs := findCondition(updated.Status.Parents[1].Conditions, string(gatewayapiv1.RouteConditionResolvedRefs))
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
	route.Generation = 2
	route.Status.Parents = []gatewayapiv1.RouteParentStatus{{
		ParentRef:      parentRef,
		ControllerName: gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName),
		Conditions: []metav1.Condition{
			{Type: string(gatewayapiv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: string(gatewayapiv1.RouteReasonAccepted)},
			{Type: string(gatewayapiv1.RouteConditionResolvedRefs), Status: metav1.ConditionFalse, Reason: string(gatewayapiv1.RouteReasonBackendNotFound)},
		},
	}}
	r := newTestReconciler(t, managedClass, managedGateway, route)
	foreignStatus := gatewayapiv1.RouteParentStatus{
		ParentRef:      gatewayapiv1.ParentReference{Name: "foreign"},
		ControllerName: "example.com/foreign-controller",
		Conditions: []metav1.Condition{
			{Type: string(gatewayapiv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: string(gatewayapiv1.RouteReasonAccepted), Message: "foreign accepted"},
			{Type: string(gatewayapiv1.RouteConditionResolvedRefs), Status: metav1.ConditionTrue, Reason: string(gatewayapiv1.RouteReasonResolvedRefs), Message: "foreign refs resolved"},
		},
	}
	writer := &concurrentForeignStatusWriter{
		StatusWriter: r.client.Status(),
		client:       r.client,
		foreign:      foreignStatus,
	}
	r.client = &statusWriterClient{Client: r.client, statusWriter: writer}
	udpRouteOnManagedGatewayMetric.Reset()

	_, err := r.Reconcile(context.Background(), requestFor(route))
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err), "the stale first status write must surface a conflict: %v", err)
	assert.Equal(t, 1, writer.patchCalls)

	_, err = r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	assert.Equal(t, 2, writer.patchCalls)

	updated := &gatewayapiv1.UDPRoute{}
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, updated.Status.Parents, 2)
	assert.Equal(t, foreignStatus, updated.Status.Parents[0], "retry must preserve the concurrent writer's complete status entry")
	owned := updated.Status.Parents[1]
	assert.Equal(t, parentRef, owned.ParentRef)
	resolvedRefs := findCondition(owned.Conditions, string(gatewayapiv1.RouteConditionResolvedRefs))
	require.NotNil(t, resolvedRefs)
	assert.Equal(t, string(gatewayapiv1.RouteReasonBackendNotFound), resolvedRefs.Reason)
	accepted := findCondition(owned.Conditions, string(gatewayapiv1.RouteConditionAccepted))
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
			updated := &gatewayapiv1.UDPRoute{}
			require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
			assert.Empty(t, updated.Status.Parents)
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
	route.Generation = 1
	route.Status.Parents = []gatewayapiv1.RouteParentStatus{{
		ParentRef:      gatewayapiv1.ParentReference{Name: "foreign"},
		ControllerName: "example.com/other-controller",
		Conditions:     []metav1.Condition{{Type: string(gatewayapiv1.RouteConditionAccepted), Status: metav1.ConditionTrue, Reason: string(gatewayapiv1.RouteReasonAccepted)}},
	}}
	r := newTestReconciler(t, managedClass, unmanagedClass, managedGateway, unmanagedGateway, route)
	udpRouteOnManagedGatewayMetric.Reset()

	_, err := r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	assert.Equal(t, 1, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))

	updated := &gatewayapiv1.UDPRoute{}
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, updated.Status.Parents, 2)
	firstTransition := updated.Status.Parents[1].Conditions[0].LastTransitionTime

	// An idempotent reconcile keeps the existing condition transition time.
	_, err = r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	assert.Equal(t, firstTransition, updated.Status.Parents[1].Conditions[0].LastTransitionTime)

	// A non-parent spec change refreshes ObservedGeneration without changing
	// the Accepted condition's transition time.
	updated.Generation = 2
	updated.Spec.Rules[0].BackendRefs[0].Name = "new-backend"
	require.NoError(t, r.client.Update(context.Background(), updated))
	_, err = r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, updated.Status.Parents, 2)
	accepted := findCondition(updated.Status.Parents[1].Conditions, string(gatewayapiv1.RouteConditionAccepted))
	require.NotNil(t, accepted)
	assert.Equal(t, int64(2), accepted.ObservedGeneration)
	assert.Equal(t, firstTransition, accepted.LastTransitionTime)

	updated.Spec.ParentRefs = []gatewayapiv1.ParentReference{{Name: "unmanaged"}}
	updated.Generation = 3
	require.NoError(t, r.client.Update(context.Background(), updated))
	_, err = r.Reconcile(context.Background(), requestFor(route))
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
	require.Len(t, updated.Status.Parents, 1)
	assert.Equal(t, gatewayapiv1.GatewayController("example.com/other-controller"), updated.Status.Parents[0].ControllerName)
	assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
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

			updated := &gatewayapiv1.UDPRoute{}
			require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
			assert.Empty(t, updated.Status.Parents)
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
	route.Status.Parents = []gatewayapiv1.RouteParentStatus{{
		ParentRef:      route.Spec.ParentRefs[0],
		ControllerName: gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName),
		Conditions:     []metav1.Condition{{Type: string(gatewayapiv1.RouteConditionAccepted), Status: metav1.ConditionFalse, Reason: ReasonUnsupportedByController}},
	}}

	t.Run("existing route", func(t *testing.T) {
		r := newTestReconciler(t, managedClass, managedGateway, route)
		r.modeAccessor.SetCRDsEstablished(false)
		udpRouteOnManagedGatewayMetric.Reset()
		udpRouteOnManagedGatewayMetric.WithLabelValues(route.Namespace, route.Name).Set(1)

		_, err := r.Reconcile(context.Background(), requestFor(route))
		require.NoError(t, err)
		assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
		updated := &gatewayapiv1.UDPRoute{}
		require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(route), updated))
		assert.Equal(t, route.Status, updated.Status, "blocked reconciliation must not patch status")
	})

	t.Run("deleted route", func(t *testing.T) {
		r := newTestReconciler(t, managedClass, managedGateway, route)
		r.modeAccessor.SetCRDsEstablished(false)
		udpRouteOnManagedGatewayMetric.Reset()
		udpRouteOnManagedGatewayMetric.WithLabelValues(route.Namespace, route.Name).Set(1)
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
				return gatewayToUDPRoutes(r.cache)(ctx, managedGateway)
			},
		},
		{
			name: "GatewayClass delete",
			requests: func(ctx context.Context, r *reconciler) []reconcile.Request {
				return allUDPRoutes(r.cache, "GatewayClass deletion")(ctx, managedClass)
			},
		},
		{
			name: "Ingress mode transition",
			requests: func(ctx context.Context, r *reconciler) []reconcile.Request {
				mapper := operatorcontroller.IngressWakeUpMapper(r.cache, func() client.ObjectList { return &gatewayapiv1.UDPRouteList{} })
				return mapper(ctx, &operatorv1alpha1.Ingress{})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestReconciler(t, managedClass, managedGateway, route)
			r.modeAccessor.SetCRDsEstablished(false)
			udpRouteOnManagedGatewayMetric.Reset()
			udpRouteOnManagedGatewayMetric.WithLabelValues(route.Namespace, route.Name).Set(1)

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
	oldRoute.Generation = 3
	statusOnly := oldRoute.DeepCopy()
	statusOnly.Status.Parents = []gatewayapiv1.RouteParentStatus{{ControllerName: "example.com/controller"}}
	generationChange := statusOnly.DeepCopy()
	generationChange.Generation = 4

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
	assert.Equal(t, []string{"route-ns/local", "gateway-ns/cross"}, ParentGatewayIndexKeys(route))
}

func newTestReconciler(t *testing.T, objects ...client.Object) *reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, gatewayapiv1.Install(scheme))
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&gatewayapiv1.UDPRoute{}).
		WithIndex(&gatewayapiv1.UDPRoute{}, UDPRouteParentGatewayIndex, func(obj client.Object) []string {
			return ParentGatewayIndexKeys(obj.(*gatewayapiv1.UDPRoute))
		}).
		Build()
	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := ctrltestutil.FakeCache{Informers: &informer, Reader: cl}
	modeAccessor := operatorcontroller.NewGatewayAPIModeAccessor(false)
	modeAccessor.SetCRDsEstablished(true)
	return &reconciler{client: cl, cache: fakeCache, modeAccessor: modeAccessor}
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

func udpRoute(name, namespace string, parents []gatewayapiv1.ParentReference) *gatewayapiv1.UDPRoute {
	return &gatewayapiv1.UDPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gatewayapiv1.UDPRouteSpec{
			CommonRouteSpec: gatewayapiv1.CommonRouteSpec{ParentRefs: parents},
			Rules: []gatewayapiv1.UDPRouteRule{{
				BackendRefs: []gatewayapiv1.BackendRef{{
					BackendObjectReference: gatewayapiv1.BackendObjectReference{Name: "backend", Port: ptr.To(gatewayapiv1.PortNumber(53))},
				}},
			}},
		},
	}
}

func requestFor(route *gatewayapiv1.UDPRoute) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: route.Namespace, Name: route.Name}}
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// concurrentForeignStatusWriter simulates another controller updating its
// status.parents entry after CIO's cache read but before CIO's first patch.
type concurrentForeignStatusWriter struct {
	client.StatusWriter
	client     client.Client
	foreign    gatewayapiv1.RouteParentStatus
	updated    bool
	patchCalls int
}

func (w *concurrentForeignStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	w.patchCalls++
	if !w.updated {
		w.updated = true
		route := &gatewayapiv1.UDPRoute{}
		if err := w.client.Get(ctx, client.ObjectKeyFromObject(obj), route); err != nil {
			return err
		}
		route.Status.Parents = append(route.Status.Parents, w.foreign)
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
