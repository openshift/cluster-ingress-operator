package udproute_status

import (
	"context"
	"fmt"
	"testing"
	"time"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	informercache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func udpCRD(uid, version string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: UDPRouteCRDName, UID: types.UID(uid)},
		Spec:       apiextensionsv1.CustomResourceDefinitionSpec{Group: gatewayapiv1.GroupName, Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: "UDPRoute", Plural: "udproutes"}, Scope: apiextensionsv1.NamespaceScoped, Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: version, Served: true, Storage: true}}},
		Status:     apiextensionsv1.CustomResourceDefinitionStatus{Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue}}},
	}
}

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

func TestOptionalUDPInformerAbsentLateAdditionRetryDeletionRecreation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newTestReconciler(t)
	r.informer = nil
	r.resource = schema.GroupVersionResource{}
	r.events = make(chan event.GenericEvent, 16)
	attempts := 0
	r.newInformer = func(resource schema.GroupVersionResource) informercache.SharedIndexInformer {
		attempts++
		informer := &lifecycleInformer{SharedIndexInformer: informercache.NewSharedIndexInformer(&informercache.ListWatch{}, newUDPRoute(resource.Version), 0, informercache.Indexers{})}
		if attempts == 1 {
			informer.indexError = fmt.Errorf("index registration failed")
		}
		return informer
	}
	require.NoError(t, r.ensureUDPInformer(ctx))
	assert.Equal(t, 0, attempts, "absent UDP kind must not register any informer or index")
	assert.Nil(t, r.informer)
	crd := udpCRD("first", "v1")
	require.NoError(t, r.client.Create(ctx, crd))
	require.ErrorContains(t, r.ensureUDPInformer(ctx), "failed to add UDPRoute field indexer")
	assert.Nil(t, r.informer)
	require.NoError(t, r.ensureUDPInformer(ctx))
	require.NotNil(t, r.informer)
	assert.Equal(t, "v1", r.resource.Version)
	first := r.informer
	require.NoError(t, r.ensureUDPInformer(ctx))
	assert.Same(t, first, r.informer)
	assert.Equal(t, 2, attempts, "success must be idempotent and failed registration retriable")
	route := udpRoute("route", "ns", []gatewayapiv1.ParentReference{{Name: "gateway"}})
	require.NoError(t, r.informer.GetIndexer().Add(route))
	assert.Equal(t, []client.ObjectKey{client.ObjectKeyFromObject(route)}, requestKeys(r.gatewayToUDPRoutes(ctx, gateway("gateway", "ns", "managed"))))
	udpRouteOnManagedGatewayMetric.WithLabelValues("ns", "route").Set(1)
	require.NoError(t, r.client.Delete(ctx, crd))
	require.NoError(t, r.ensureUDPInformer(ctx))
	assert.Nil(t, r.informer)
	assert.Empty(t, r.allUDPRoutes(ctx, nil))
	assert.Equal(t, 0, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
	crd = udpCRD("second", "v1alpha2")
	require.NoError(t, r.client.Create(ctx, crd))
	require.NoError(t, r.ensureUDPInformer(ctx))
	assert.Equal(t, "v1alpha2", r.resource.Version)
	assert.NotSame(t, first, r.informer)
	assert.Equal(t, 3, attempts)
	crd.UID = "third"
	require.NoError(t, r.client.Update(ctx, crd))
	require.NoError(t, r.ensureUDPInformer(ctx))
	assert.Equal(t, 4, attempts, "same-version CRD recreation must replace the old informer")
	done := r.done
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("UDP informer did not stop with its controller")
	}
}

func requestKeys(requests []reconcile.Request) []client.ObjectKey {
	keys := make([]client.ObjectKey, len(requests))
	for index := range requests {
		keys[index] = requests[index].NamespacedName
	}
	return keys
}

func TestUDPInformerHandlerFailureRetries(t *testing.T) {
	r := newTestReconciler(t, udpCRD("test", "v1"))
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
}

func TestServedUDPResource(t *testing.T) {
	for _, version := range []string{"v1", "v1alpha2"} {
		assert.Equal(t, version, servedUDPResource(udpCRD("test", version)).Version)
	}
	crd := udpCRD("test", "v1alpha2")
	crd.Spec.Versions = append(crd.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{Name: "v1", Served: true})
	assert.Equal(t, "v1", servedUDPResource(crd).Version)
	for _, modify := range []func(*apiextensionsv1.CustomResourceDefinition){
		func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Status.Conditions = nil },
		func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Versions[0].Served = false },
		func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Scope = apiextensionsv1.ClusterScoped },
		func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Group = "other.example" },
		func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Names.Kind = "Other" },
		func(crd *apiextensionsv1.CustomResourceDefinition) { now := metav1.Now(); crd.DeletionTimestamp = &now },
	} {
		crd := udpCRD("test", "v1")
		modify(crd)
		assert.True(t, servedUDPResource(crd).Empty())
	}
}

func TestUDPInformerUsesServedGVKAndEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newTestReconciler(t, udpCRD("test", "v1alpha2"))
	r.informer = nil
	r.events = make(chan event.GenericEvent, 16)
	watcher := watch.NewRaceFreeFake()
	route := udpRoute("route", "ns", []gatewayapiv1.ParentReference{{Name: "gateway"}})
	route.SetAPIVersion("gateway.networking.k8s.io/v1alpha2")
	route.SetGeneration(1)
	r.newInformer = func(resource schema.GroupVersionResource) informercache.SharedIndexInformer {
		require.Equal(t, "v1alpha2", resource.Version)
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
	updated := newUDPRoute("v1")
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

func TestReconcileUsingServedUDPVersion(t *testing.T) {
	for _, version := range []string{"v1", "v1alpha2"} {
		t.Run(version, func(t *testing.T) {
			class := gatewayClass("managed", operatorcontroller.OpenShiftGatewayClassControllerName)
			managedGateway := gateway("managed", "ns", class.Name)
			route := udpRoute("route", "ns", []gatewayapiv1.ParentReference{{Name: "managed"}})
			route.SetAPIVersion("gateway.networking.k8s.io/" + version)
			route.SetGeneration(3)
			r := newTestReconciler(t, class, managedGateway, route)
			r.resource.Version = version
			_, err := r.Reconcile(t.Context(), requestFor(route))
			require.NoError(t, err)
			updated := newUDPRoute(version)
			require.NoError(t, r.client.Get(t.Context(), client.ObjectKeyFromObject(route), updated))
			parents := routeParents(t, updated)
			require.Len(t, parents, 1)
			condition := meta.FindStatusCondition(parents[0].Conditions, string(gatewayapiv1.RouteConditionAccepted))
			require.NotNil(t, condition)
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, int64(3), condition.ObservedGeneration)
			assert.Equal(t, ReasonUnsupportedByController, condition.Reason)
		})
	}
}
