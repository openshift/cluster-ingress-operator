package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestListenerSetByObjectRequiresInstalledCRD(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, gatewayapiv1.AddToScheme(scheme))
	mapper := meta.NewDefaultRESTMapper(nil)
	_, err := cache.New(&rest.Config{Host: "https://unused.invalid"}, cache.Options{
		Scheme: scheme, Mapper: mapper,
		ByObject: map[client.Object]cache.ByObject{
			&gatewayapiv1.ListenerSet{}: {Namespaces: map[string]cache.Config{cache.AllNamespaces: {}}},
		},
	})
	require.True(t, meta.IsNoMatchError(err), "ByObject must resolve the absent CRD during cache construction: %v", err)
}

type listenerSetCacheManager struct {
	manager.Manager
	config   *rest.Config
	scheme   *runtime.Scheme
	mapper   meta.RESTMapper
	runnable manager.Runnable
}

func (m *listenerSetCacheManager) GetConfig() *rest.Config        { return m.config }
func (m *listenerSetCacheManager) GetScheme() *runtime.Scheme     { return m.scheme }
func (m *listenerSetCacheManager) GetRESTMapper() meta.RESTMapper { return m.mapper }
func (m *listenerSetCacheManager) Add(runnable manager.Runnable) error {
	m.runnable = runnable
	return nil
}

func TestListenerSetCacheDefersDiscoveryAndWatchesAllNamespaces(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, gatewayapiv1.AddToScheme(scheme))
	mapper := meta.NewDefaultRESTMapper(nil)
	path := "/apis/gateway.networking.k8s.io/v1/listenersets"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != path {
			t.Errorf("cache must watch all namespaces, got %s", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		listeners := &gatewayapiv1.ListenerSetList{TypeMeta: metav1.TypeMeta{APIVersion: gatewayapiv1.GroupVersion.String(), Kind: "ListenerSetList"}, ListMeta: metav1.ListMeta{ResourceVersion: "1"}}
		for _, namespace := range []string{"tenant-one", "tenant-two"} {
			listeners.Items = append(listeners.Items, gatewayapiv1.ListenerSet{TypeMeta: metav1.TypeMeta{APIVersion: gatewayapiv1.GroupVersion.String(), Kind: "ListenerSet"}, ObjectMeta: metav1.ObjectMeta{Name: "listeners", Namespace: namespace}, Spec: gatewayapiv1.ListenerSetSpec{ParentRef: gatewayapiv1.ParentGatewayReference{Name: "gateway"}}})
		}
		encoder := json.NewEncoder(writer)
		if request.URL.Query().Get("watch") == "true" {
			if request.URL.Query().Get("sendInitialEvents") == "true" {
				for index := range listeners.Items {
					if err := encoder.Encode(metav1.WatchEvent{Type: "ADDED", Object: runtime.RawExtension{Object: &listeners.Items[index]}}); err != nil {
						t.Error(err)
					}
				}
				bookmark := &gatewayapiv1.ListenerSet{TypeMeta: metav1.TypeMeta{APIVersion: gatewayapiv1.GroupVersion.String(), Kind: "ListenerSet"}, ObjectMeta: metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"k8s.io/initial-events-end": "true"}}}
				if err := encoder.Encode(metav1.WatchEvent{Type: "BOOKMARK", Object: runtime.RawExtension{Object: bookmark}}); err != nil {
					t.Error(err)
				}
			}
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
			return
		}
		if err := encoder.Encode(listeners); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	mgr := &listenerSetCacheManager{config: &rest.Config{Host: server.URL}, scheme: scheme, mapper: mapper}
	listenerSets, err := newListenerSetCache(mgr)
	require.NoError(t, err, "cache construction must not resolve the absent CRD")
	require.Same(t, listenerSets, mgr.runnable)
	_, err = listenerSets.GetInformer(t.Context(), &gatewayapiv1.ListenerSet{}, cache.BlockUntilSynced(false))
	require.True(t, meta.IsNoMatchError(err))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- listenerSets.Start(ctx) }()
	defer func() { cancel(); require.NoError(t, <-stopped) }()
	require.True(t, listenerSets.WaitForCacheSync(ctx))
	kinds, _, err := scheme.ObjectKinds(&gatewayapiv1.ListenerSet{})
	require.NoError(t, err)
	require.Len(t, kinds, 1)
	mapper.Add(kinds[0], meta.RESTScopeNamespace)
	require.NoError(t, listenerSets.IndexField(ctx, &gatewayapiv1.ListenerSet{}, operatorcontroller.ListenerSetParentGatewayIndex, operatorcontroller.IndexListenerSetParent))
	require.True(t, listenerSets.WaitForCacheSync(ctx))
	for _, namespace := range []string{"tenant-one", "tenant-two"} {
		var attached gatewayapiv1.ListenerSetList
		require.NoError(t, listenerSets.List(ctx, &attached, client.MatchingFields{operatorcontroller.ListenerSetParentGatewayIndex: namespace + "/gateway"}))
		require.Len(t, attached.Items, 1)
		require.Equal(t, namespace, attached.Items[0].Namespace)
	}
}
