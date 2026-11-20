package udproute_status

import (
	"context"
	"fmt"
	"testing"
	"time"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	informercache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type lifecycleInformer struct {
	informercache.SharedIndexInformer
	indexError   error
	handlerError error
}

func (informer *lifecycleInformer) AddIndexers(indexers informercache.Indexers) error {
	if informer.indexError != nil {
		return informer.indexError
	}
	return informer.SharedIndexInformer.AddIndexers(indexers)
}

func (informer *lifecycleInformer) AddEventHandler(handler informercache.ResourceEventHandler) (informercache.ResourceEventHandlerRegistration, error) {
	if informer.handlerError != nil {
		return nil, informer.handlerError
	}
	return informer.SharedIndexInformer.AddEventHandler(handler)
}

func (informer *lifecycleInformer) Run(stop <-chan struct{}) { <-stop }

func TestUDPInformerIdempotencyAndStoppedRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newTestReconciler(t)
	r.informer = nil
	r.events = make(chan event.GenericEvent, 16)
	attempts := 0
	r.newInformer = func(resource schema.GroupVersionResource) informercache.SharedIndexInformer {
		attempts++
		assert.Equal(t, udpRouteVersion, resource.Version, "informer must always use the fixed version")
		return &lifecycleInformer{SharedIndexInformer: informercache.NewSharedIndexInformer(&informercache.ListWatch{}, newUDPRoute(resource.Version), 0, informercache.Indexers{})}
	}
	require.NoError(t, r.ensureUDPInformer(ctx))
	require.NotNil(t, r.informer)
	first := r.informer
	require.NoError(t, r.ensureUDPInformer(ctx))
	assert.Same(t, first, r.informer)
	assert.Equal(t, 1, attempts, "repeated calls must be idempotent")
	route := udpRoute("route", "ns", []gatewayapiv1.ParentReference{{Name: "gateway"}})
	require.NoError(t, r.informer.GetIndexer().Add(route))
	assert.Equal(t, []client.ObjectKey{client.ObjectKeyFromObject(route)}, requestKeys(r.gatewayToUDPRoutes(ctx, gateway("gateway", "ns", "managed"))))
	done := r.done
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("UDP informer did not stop with its controller")
	}
	require.NoError(t, r.ensureUDPInformer(t.Context()))
	assert.Equal(t, 2, attempts, "stopped informer must be replaced")
	r.cancel()
	<-r.done
}

func requestKeys(requests []reconcile.Request) []client.ObjectKey {
	keys := make([]client.ObjectKey, len(requests))
	for index := range requests {
		keys[index] = requests[index].NamespacedName
	}
	return keys
}

func TestUDPInformerHandlerFailureRetries(t *testing.T) {
	r := newTestReconciler(t)
	r.informer = nil
	r.events = make(chan event.GenericEvent, 16)
	attempts := 0
	r.newInformer = func(resource schema.GroupVersionResource) informercache.SharedIndexInformer {
		attempts++
		informer := &lifecycleInformer{SharedIndexInformer: informercache.NewSharedIndexInformer(&informercache.ListWatch{}, newUDPRoute(resource.Version), 0, informercache.Indexers{})}
		if attempts == 1 {
			informer.handlerError = fmt.Errorf("handler registration failed")
		}
		return informer
	}
	require.ErrorContains(t, r.ensureUDPInformer(t.Context()), "failed to watch UDPRoutes")
	assert.Nil(t, r.informer)
	require.NoError(t, r.ensureUDPInformer(t.Context()))
	assert.Equal(t, 2, attempts)
	r.cancel()
	<-r.done
}

func TestUDPInformerUsesFixedGVKAndEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newTestReconciler(t)
	r.informer = nil
	r.events = make(chan event.GenericEvent, 16)
	watcher := watch.NewRaceFreeFake()
	route := udpRoute("route", "ns", []gatewayapiv1.ParentReference{{Name: "gateway"}})
	route.SetAPIVersion("gateway.networking.k8s.io/" + udpRouteVersion)
	route.SetGeneration(1)
	r.newInformer = func(resource schema.GroupVersionResource) informercache.SharedIndexInformer {
		require.Equal(t, udpRouteVersion, resource.Version)
		return informercache.NewSharedIndexInformer(&informercache.ListWatch{
			ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
				return &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*route.DeepCopy()}}, nil
			},
			WatchFuncWithContext: func(_ context.Context, options metav1.ListOptions) (watch.Interface, error) {
				if options.SendInitialEvents != nil && *options.SendInitialEvents {
					watcher.Add(route.DeepCopy())
					bookmark := newUDPRoute(resource.Version)
					bookmark.SetResourceVersion("1")
					bookmark.SetAnnotations(map[string]string{metav1.InitialEventsAnnotationKey: "true"})
					watcher.Action(watch.Bookmark, bookmark)
				}
				return watcher, nil
			},
		}, newUDPRoute(resource.Version), 0, informercache.Indexers{})
	}
	require.NoError(t, r.ensureUDPInformer(ctx))
	select {
	case created := <-r.events:
		assert.Equal(t, route.GroupVersionKind(), created.Object.GetObjectKind().GroupVersionKind())
	case <-time.After(5 * time.Second):
		t.Fatal("initial UDPRoute list did not enqueue")
	}
	updated := route.DeepCopy()
	updated.SetGeneration(2)
	watcher.Modify(updated)
	select {
	case modified := <-r.events:
		assert.Equal(t, int64(2), modified.Object.GetGeneration())
	case <-time.After(5 * time.Second):
		t.Fatal("UDPRoute generation event did not enqueue")
	}
	watcher.Delete(updated)
	select {
	case deleted := <-r.events:
		assert.Equal(t, route.GetName(), deleted.Object.GetName())
	case <-time.After(5 * time.Second):
		t.Fatal("UDPRoute deletion did not enqueue")
	}
	done := r.done
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("UDP watcher did not stop")
	}
}

func TestReconcilePreservesUnknownForeignStatusFields(t *testing.T) {
	class := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
	managedGateway := gateway("managed", "ns", class.Name)
	route := udpRoute("route", "ns", []gatewayapiv1.ParentReference{{Name: "managed"}})
	foreign := map[string]interface{}{"parentRef": map[string]interface{}{"name": "other", "futureReference": "keep"}, "controllerName": "other.example/controller", "futureField": map[string]interface{}{"nested": []interface{}{"keep", int64(1)}}, "conditions": []interface{}{map[string]interface{}{"type": "Accepted", "status": "True", "unknown": "keep"}}}
	route.Object["status"] = map[string]interface{}{"parents": []interface{}{foreign}, "futureTopLevel": "keep"}
	r := newTestReconciler(t, class, managedGateway, route)
	_, err := r.Reconcile(t.Context(), requestFor(route))
	require.NoError(t, err)
	updated := newUDPRoute(udpRouteVersion)
	require.NoError(t, r.client.Get(t.Context(), client.ObjectKeyFromObject(route), updated))
	parents, _, err := unstructured.NestedSlice(updated.Object, "status", "parents")
	require.NoError(t, err)
	require.Len(t, parents, 2)
	assert.Equal(t, foreign, parents[0])
	value, _, err := unstructured.NestedString(updated.Object, "status", "futureTopLevel")
	require.NoError(t, err)
	assert.Equal(t, "keep", value)
	before := updated.DeepCopy()
	_, err = r.Reconcile(t.Context(), requestFor(route))
	require.NoError(t, err)
	require.NoError(t, r.client.Get(t.Context(), client.ObjectKeyFromObject(route), updated))
	assert.Equal(t, before.Object, updated.Object, "repeat reconciliation must not churn status or resourceVersion")
}

func TestReconcileUsingFixedUDPVersion(t *testing.T) {
	class := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
	managedGateway := gateway("managed", "ns", class.Name)
	route := udpRoute("route", "ns", []gatewayapiv1.ParentReference{{Name: "managed"}})
	route.SetAPIVersion("gateway.networking.k8s.io/" + udpRouteVersion)
	route.SetGeneration(3)
	r := newTestReconciler(t, class, managedGateway, route)
	_, err := r.Reconcile(t.Context(), requestFor(route))
	require.NoError(t, err)
	updated := newUDPRoute(udpRouteVersion)
	require.NoError(t, r.client.Get(t.Context(), client.ObjectKeyFromObject(route), updated))
	parents := routeParents(t, updated)
	require.Len(t, parents, 1)
	condition := meta.FindStatusCondition(parents[0].Conditions, string(gatewayapiv1.RouteConditionAccepted))
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, int64(3), condition.ObservedGeneration)
	assert.Equal(t, ReasonUnsupportedByController, condition.Reason)
}
