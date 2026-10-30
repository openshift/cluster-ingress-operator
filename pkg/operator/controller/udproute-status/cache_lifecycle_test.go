package udproute_status

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type lifecycleManager struct {
	*guardManager
	mapper meta.RESTMapper
}

func (mgr *lifecycleManager) GetRESTMapper() meta.RESTMapper { return mgr.mapper }

type observableGatewayCache struct {
	cache.Cache
	gatewayInformers atomic.Int32
	classInformers   atomic.Int32
	reads            atomic.Int32
	backgroundReads  atomic.Int32
}

func (reader *observableGatewayCache) GetInformer(ctx context.Context, object client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	switch object.(type) {
	case *gatewayapiv1.Gateway:
		reader.gatewayInformers.Add(1)
	case *gatewayapiv1.GatewayClass:
		reader.classInformers.Add(1)
	}
	return reader.Cache.GetInformer(ctx, object, opts...)
}

func (reader *observableGatewayCache) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	reader.reads.Add(1)
	if ctx.Done() == nil {
		reader.backgroundReads.Add(1)
	}
	return reader.Cache.Get(ctx, key, object, opts...)
}

func TestConstructorRealGatewayCacheLifecycle(t *testing.T) {
	oldCache, oldController := newGatewayCache, newGuardController
	t.Cleanup(func() { newGatewayCache, newGuardController = oldCache, oldController })
	for _, recoverClass := range []bool{false, true} {
		name := "cancel-unsynced"
		if recoverClass {
			name = "recover-sync-and-reconcile"
		}
		t.Run(name, func(t *testing.T) {
			udpRouteOnManagedGatewayMetric.Reset()
			t.Cleanup(udpRouteOnManagedGatewayMetric.Reset)
			var classesAvailable atomic.Bool
			classFailure := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				classPath := "/apis/gateway.networking.k8s.io/v1/gatewayclasses"
				gatewayPath := "/apis/gateway.networking.k8s.io/v1/gateways"
				if request.URL.Path != classPath && request.URL.Path != gatewayPath {
					http.NotFound(response, request)
					return
				}
				if request.URL.Query().Get("watch") == "true" && request.URL.Query().Get("sendInitialEvents") == "true" {
					http.Error(response, "watch-list unsupported", http.StatusBadRequest)
					return
				}
				if request.URL.Path == classPath && !classesAvailable.Load() {
					select {
					case classFailure <- struct{}{}:
					default:
					}
					http.Error(response, "class LIST unavailable", http.StatusServiceUnavailable)
					return
				}
				if request.URL.Query().Get("watch") == "true" {
					response.WriteHeader(http.StatusOK)
					response.(http.Flusher).Flush()
					<-request.Context().Done()
					return
				}
				var list interface{}
				if request.URL.Path == classPath {
					item := gatewayClass("ours", operatorcontroller.OpenShiftGatewayClassControllerName)
					item.APIVersion, item.Kind = gatewayapiv1.SchemeGroupVersion.String(), "GatewayClass"
					item.ResourceVersion = "1"
					list = &gatewayapiv1.GatewayClassList{TypeMeta: metav1.TypeMeta{APIVersion: gatewayapiv1.SchemeGroupVersion.String(), Kind: "GatewayClassList"}, ListMeta: metav1.ListMeta{ResourceVersion: "1"}, Items: []gatewayapiv1.GatewayClass{*item}}
				} else {
					item := gateway("gateway", "outside-main-cache", "ours")
					item.APIVersion, item.Kind = gatewayapiv1.SchemeGroupVersion.String(), "Gateway"
					item.ResourceVersion, item.UID = "1", "gateway-uid"
					list = &gatewayapiv1.GatewayList{TypeMeta: metav1.TypeMeta{APIVersion: gatewayapiv1.SchemeGroupVersion.String(), Kind: "GatewayList"}, ListMeta: metav1.ListMeta{ResourceVersion: "1"}, Items: []gatewayapiv1.Gateway{*item}}
				}
				if err := json.NewEncoder(response).Encode(list); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			route := udpRoute("udp", "customer", []gatewayapiv1.ParentReference{{Name: "gateway", Namespace: namespacePtr("outside-main-cache")}})
			fixture := newTestReconciler(t, route)
			fixture.client.Scheme().AddKnownTypeWithName(route.GroupVersionKind(), newUDPRoute("v1"))
			fixture.cache = &namespaceRestrictedCache{Cache: fixture.cache}
			mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gatewayapiv1.SchemeGroupVersion})
			mapper.AddSpecific(gatewayapiv1.SchemeGroupVersion.WithKind("Gateway"), gatewayapiv1.SchemeGroupVersion.WithResource("gateways"), gatewayapiv1.SchemeGroupVersion.WithResource("gateway"), meta.RESTScopeNamespace)
			mapper.AddSpecific(gatewayapiv1.SchemeGroupVersion.WithKind("GatewayClass"), gatewayapiv1.SchemeGroupVersion.WithResource("gatewayclasses"), gatewayapiv1.SchemeGroupVersion.WithResource("gatewayclass"), meta.RESTScopeRoot)
			mgr := &lifecycleManager{guardManager: &guardManager{config: &rest.Config{Host: server.URL}, guardReconciler: fixture}, mapper: mapper}
			var wider *observableGatewayCache
			newGatewayCache = func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
				assert.Empty(t, opts.DefaultNamespaces)
				actual, err := cache.New(config, opts)
				wider = &observableGatewayCache{Cache: actual}
				return wider, err
			}
			guard := &recordingGuard{}
			var built *reconciler
			newGuardController = func(_ string, opts controller.Options) (controller.Controller, error) {
				assert.Zero(t, opts.ReconciliationTimeout, "UDP informer lifetime currently assumes no reconcile timeout")
				built = opts.Reconciler.(*reconciler)
				return guard, nil
			}
			_, err := NewUnmanaged(mgr, nil)
			require.NoError(t, err)
			require.Len(t, mgr.added, 1)
			assert.Same(t, wider, mgr.added[0])
			assert.Same(t, wider, built.gatewayCache)
			assert.Same(t, fixture.cache, built.cache)
			assert.NotSame(t, fixture.cache, built.gatewayCache)
			built.informer = fixture.informer
			built.resource = fixture.resource
			built.modeAccessor = fixture.modeAccessor
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			cacheDone := make(chan error, 1)
			go func() { cacheDone <- mgr.added[0].Start(ctx) }()
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			t.Cleanup(queue.ShutDown)
			for _, src := range guard.sources[2:4] {
				require.NoError(t, src.Start(ctx, queue))
			}
			select {
			case <-classFailure:
			case <-time.After(5 * time.Second):
				t.Fatal("class informer did not encounter the initial LIST failure")
			}
			require.Eventually(t, func() bool { return queue.Len() == 1 }, 5*time.Second, 10*time.Millisecond, "initial Gateway callback must enqueue despite unsynced class cache")
			assert.Equal(t, int32(0), wider.reads.Load(), "Gateway predicates must not read the class cache")
			request := getQueuedRequest(t, queue)
			assert.Equal(t, requestFor(route), request)
			queue.Done(request)
			queue.Forget(request)
			if recoverClass {
				classesAvailable.Store(true)
				syncCtx, stopSync := context.WithTimeout(ctx, 5*time.Second)
				defer stopSync()
				for _, src := range guard.sources[2:4] {
					require.NoError(t, src.(source.SyncingSource).WaitForSync(syncCtx))
				}
				_, err := built.Reconcile(ctx, requestFor(route))
				require.NoError(t, err)
				assert.Equal(t, int32(2), wider.reads.Load(), "Gateway and class reads must use the added wider cache")
				current := newUDPRoute("v1")
				require.NoError(t, fixture.client.Get(ctx, client.ObjectKeyFromObject(route), current))
				require.Len(t, routeParents(t, current), 1)
				assert.Equal(t, gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName), routeParents(t, current)[0].ControllerName)
			}
			assert.Equal(t, int32(1), wider.gatewayInformers.Load())
			assert.Equal(t, int32(1), wider.classInformers.Load())
			assert.Zero(t, wider.backgroundReads.Load())
			cancel()
			select {
			case err := <-cacheDone:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("actual cache must stop after cancellation without manually releasing a Get")
			}
			for _, src := range guard.sources[2:4] {
				finished := make(chan struct{})
				go func(src source.Source) {
					src.(source.SyncingSource).WaitForSync(ctx)
					close(finished)
				}(src)
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Fatal("cancelled source synchronization did not finish")
				}
			}
		})
	}
}
