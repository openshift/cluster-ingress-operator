package udproute_status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ctrltestutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/prometheus/client_golang/prometheus"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	informercache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/yaml"
)

type recordingGuard struct {
	controller.Controller
	sources []source.Source
	failAt  int
}

func (guard *recordingGuard) Watch(src source.Source) error {
	guard.sources = append(guard.sources, src)
	if guard.failAt == len(guard.sources) {
		return fmt.Errorf("watch registration failed")
	}
	return nil
}

type guardManager struct {
	manager.Manager
	config          *rest.Config
	guardReconciler *reconciler
	added           []manager.Runnable
	addError        error
}

func (mgr *guardManager) GetConfig() *rest.Config    { return mgr.config }
func (mgr *guardManager) GetScheme() *runtime.Scheme { return mgr.guardReconciler.client.Scheme() }
func (mgr *guardManager) GetRESTMapper() meta.RESTMapper {
	return mgr.guardReconciler.client.RESTMapper()
}
func (mgr *guardManager) GetClient() client.Client { return mgr.guardReconciler.client }
func (mgr *guardManager) GetCache() cache.Cache    { return mgr.guardReconciler.cache }
func (mgr *guardManager) Add(runnable manager.Runnable) error {
	mgr.added = append(mgr.added, runnable)
	return mgr.addError
}

func TestNewUnmanagedRegistersActualWatchesWithoutUDPKind(t *testing.T) {
	oldCache, oldController := newGatewayCache, newGuardController
	t.Cleanup(func() { newGatewayCache, newGuardController = oldCache, oldController })
	for _, gate := range []string{"nil", "off", "on"} {
		t.Run(gate, func(t *testing.T) {
			route := udpRoute("udp", "customer", []gatewayapiv1.ParentReference{{Name: "gateway", Namespace: namespacePtr("outside-main-cache")}})
			fixture := newTestReconciler(t, route, gatewayClass("ours", operatorcontroller.OpenShiftGatewayClassControllerName), gatewayClass("foreign", "example.com/foreign"))
			require.NoError(t, operatorv1alpha1.AddToScheme(fixture.client.Scheme()))
			mgr := &guardManager{config: &rest.Config{Host: "http://127.0.0.1:1"}, guardReconciler: fixture}
			guard := &recordingGuard{}
			var built *reconciler
			newGatewayCache = func(_ *rest.Config, opts cache.Options) (cache.Cache, error) {
				assert.Empty(t, opts.DefaultNamespaces, "Gateway ownership follows existing all-namespace gateway-labeler contract")
				return fixture.gatewayCache, nil
			}
			newGuardController = func(_ string, opts controller.Options) (controller.Controller, error) {
				built = opts.Reconciler.(*reconciler)
				return guard, nil
			}
			var accessor *operatorcontroller.GatewayAPIModeAccessor
			if gate != "nil" {
				accessor = operatorcontroller.NewGatewayAPIModeAccessor(gate == "on")
			}
			result, err := NewUnmanaged(mgr, accessor)
			require.NoError(t, err)
			assert.Same(t, guard, result)
			require.Len(t, mgr.added, 1)
			want := 4
			if gate == "on" {
				want++
			}
			require.Len(t, guard.sources, want)
			require.NoError(t, built.ensureUDPInformer(t.Context()))
			assert.Nil(t, built.informer)
			assert.True(t, built.resource.Empty())
			built.informer = fixture.informer
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			t.Cleanup(queue.ShutDown)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			for _, src := range guard.sources[1:] {
				require.NoError(t, src.Start(ctx, queue))
				require.NoError(t, src.(source.SyncingSource).WaitForSync(ctx))
			}
			informers := fixture.gatewayCache.(ctrltestutil.FakeCache).Informers.(*informertest.FakeInformers)
			gwInformer, err := informers.FakeInformerForKind(ctx, gatewayapiv1.SchemeGroupVersion.WithKind("Gateway"))
			require.NoError(t, err)
			classInformer, err := informers.FakeInformerForKind(ctx, gatewayapiv1.SchemeGroupVersion.WithKind("GatewayClass"))
			require.NoError(t, err)
			ours := gateway("gateway", "outside-main-cache", "ours")
			foreign := gateway("gateway", "outside-main-cache", "foreign")
			check := func(emit func(), accepted bool) {
				t.Helper()
				emit()
				if !accepted {
					assert.Equal(t, 0, queue.Len())
					return
				}
				require.Equal(t, 1, queue.Len())
				request, stopped := queue.Get()
				require.False(t, stopped)
				assert.Equal(t, requestFor(route), request)
				queue.Done(request)
				queue.Forget(request)
			}
			check(func() { gwInformer.Add(ours) }, true)
			check(func() { gwInformer.Delete(ours) }, true)
			check(func() { gwInformer.Add(foreign) }, true)
			check(func() { gwInformer.Delete(foreign) }, true)
			check(func() { gwInformer.Update(ours, ours.DeepCopy()) }, false)
			check(func() { gwInformer.Update(ours, foreign) }, true)
			check(func() { gwInformer.Update(foreign, ours) }, true)
			check(func() { gwInformer.Add(&unstructured.Unstructured{}) }, false)
			check(func() { gwInformer.Delete(&unstructured.Unstructured{}) }, false)
			check(func() { gwInformer.Update(ours, &unstructured.Unstructured{}) }, false)
			ourClass := gatewayClass("ours", operatorcontroller.OpenShiftGatewayClassControllerName)
			foreignClass := gatewayClass("ours", "example.com/foreign")
			check(func() { classInformer.Add(ourClass) }, true)
			check(func() { classInformer.Delete(ourClass) }, true)
			check(func() { classInformer.Update(ourClass, foreignClass) }, true)
			check(func() { classInformer.Update(foreignClass, ourClass) }, true)
			check(func() { classInformer.Add(foreignClass) }, false)
			check(func() { classInformer.Delete(foreignClass) }, false)
			check(func() { classInformer.Update(foreignClass, foreignClass.DeepCopy()) }, false)
			check(func() { classInformer.Add(&unstructured.Unstructured{}) }, false)
			if gate == "on" {
				modeInformer, err := informers.FakeInformerForKind(ctx, operatorv1alpha1.GroupVersion.WithKind("Ingress"))
				require.NoError(t, err)
				ingress := &operatorv1alpha1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
				check(func() { modeInformer.Add(ingress) }, true)
				check(func() { modeInformer.Update(ingress, ingress.DeepCopy()) }, false)
				updated := ingress.DeepCopy()
				updated.Spec.GatewayAPI.ManagementMode = operatorv1alpha1.GatewayAPIManagementModeUnmanaged
				check(func() { modeInformer.Update(ingress, updated) }, true)
				check(func() { modeInformer.Delete(ingress) }, true)
			}
		})
	}
}

func TestNewUnmanagedErrors(t *testing.T) {
	oldCache, oldController := newGatewayCache, newGuardController
	t.Cleanup(func() { newGatewayCache, newGuardController = oldCache, oldController })
	fixture := newTestReconciler(t)
	mgr := &guardManager{config: &rest.Config{Host: "://invalid"}, guardReconciler: fixture}
	_, err := NewUnmanaged(mgr, nil)
	require.Error(t, err)
	mgr.config.Host = "http://127.0.0.1:1"
	newGatewayCache = func(_ *rest.Config, _ cache.Options) (cache.Cache, error) {
		return nil, fmt.Errorf("cache setup failed")
	}
	_, err = NewUnmanaged(mgr, nil)
	require.ErrorContains(t, err, "cache setup failed")
	newGatewayCache = func(_ *rest.Config, _ cache.Options) (cache.Cache, error) { return fixture.gatewayCache, nil }
	mgr.addError = fmt.Errorf("manager add failed")
	_, err = NewUnmanaged(mgr, nil)
	require.ErrorContains(t, err, "manager add failed")
	mgr.addError = nil
	newGuardController = func(_ string, _ controller.Options) (controller.Controller, error) {
		return nil, fmt.Errorf("controller setup failed")
	}
	_, err = NewUnmanaged(mgr, nil)
	require.ErrorContains(t, err, "controller setup failed")
	for failAt := 1; failAt <= 5; failAt++ {
		newGuardController = func(_ string, _ controller.Options) (controller.Controller, error) {
			return &recordingGuard{failAt: failAt}, nil
		}
		_, err = NewUnmanaged(mgr, operatorcontroller.NewGatewayAPIModeAccessor(true))
		require.ErrorContains(t, err, "watch registration failed")
	}
}

func namespacePtr(value string) *gatewayapiv1.Namespace {
	namespace := gatewayapiv1.Namespace(value)
	return &namespace
}

type namespaceRestrictedCache struct{ cache.Cache }

func (reader namespaceRestrictedCache) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if _, gatewayObject := object.(*gatewayapiv1.Gateway); gatewayObject && key.Namespace != operatorcontroller.DefaultOperandNamespace {
		return fmt.Errorf("unknown namespace for the cache: %s", key.Namespace)
	}
	return reader.Cache.Get(ctx, key, object, opts...)
}

func TestGatewayLookupAndWatchesShareAllNamespaceOwnership(t *testing.T) {
	udpRouteOnManagedGatewayMetric.Reset()
	t.Cleanup(udpRouteOnManagedGatewayMetric.Reset)
	parents := []gatewayapiv1.ParentReference{{Name: "gateway", Namespace: namespacePtr(operatorcontroller.DefaultOperandNamespace)}, {Name: "gateway", Namespace: namespacePtr("foreign-ns")}, {Name: "missing", Namespace: namespacePtr("missing-ns")}}
	route := udpRoute("udp", "customer", parents)
	foreign := map[string]interface{}{"controllerName": "example.com/foreign", "parentRef": map[string]interface{}{"name": "gateway"}, "future": map[string]interface{}{"keep": "unchanged"}}
	require.NoError(t, unstructured.SetNestedSlice(route.Object, []interface{}{foreign}, "status", "parents"))
	guardReconciler := newTestReconciler(t, route, gateway("gateway", operatorcontroller.DefaultOperandNamespace, "ours"), gateway("gateway", "foreign-ns", "foreign"), gateway("gateway", "another-managed-ns", "ours"), gatewayClass("ours", operatorcontroller.OpenShiftGatewayClassControllerName), gatewayClass("foreign", "example.com/foreign"))
	guardReconciler.cache = namespaceRestrictedCache{Cache: guardReconciler.cache}
	_, err := guardReconciler.Reconcile(t.Context(), requestFor(route))
	require.NoError(t, err)
	current := newUDPRoute("v1")
	require.NoError(t, guardReconciler.client.Get(t.Context(), client.ObjectKeyFromObject(route), current))
	assert.Len(t, routeParents(t, current), 2)
	assert.Equal(t, float64(1), promtestutil.ToFloat64(udpRouteOnManagedGatewayMetric.WithLabelValues("customer", "udp")))
	setRouteParentRefs(t, current, []gatewayapiv1.ParentReference{{Name: "gateway", Namespace: namespacePtr("another-managed-ns")}})
	require.NoError(t, guardReconciler.client.Update(t.Context(), current))
	require.NoError(t, guardReconciler.informer.GetIndexer().Update(current))
	assert.Empty(t, guardReconciler.gatewayToUDPRoutes(t.Context(), gateway("gateway", operatorcontroller.DefaultOperandNamespace, "ours")))
	assert.Len(t, guardReconciler.gatewayToUDPRoutes(t.Context(), gateway("gateway", "another-managed-ns", "ours")), 1)
	_, err = guardReconciler.Reconcile(t.Context(), requestFor(route))
	require.NoError(t, err)
	require.NoError(t, guardReconciler.client.Get(t.Context(), client.ObjectKeyFromObject(route), current))
	assert.Equal(t, namespacePtr("another-managed-ns"), routeParents(t, current)[1].ParentRef.Namespace)
	setRouteParentRefs(t, current, parents[1:])
	require.NoError(t, guardReconciler.client.Update(t.Context(), current))
	_, err = guardReconciler.Reconcile(t.Context(), requestFor(route))
	require.NoError(t, err)
	require.NoError(t, guardReconciler.client.Get(t.Context(), client.ObjectKeyFromObject(route), current))
	values, _, err := unstructured.NestedSlice(current.Object, "status", "parents")
	require.NoError(t, err)
	assert.Equal(t, []interface{}{foreign}, values)
	assert.Zero(t, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
}

type routeCallClient struct {
	client.Client
	gets, statuses int
}

func (guardClient *routeCallClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if _, ok := object.(*unstructured.Unstructured); ok {
		guardClient.gets++
	}
	return guardClient.Client.Get(ctx, key, object, opts...)
}
func (guardClient *routeCallClient) Status() client.StatusWriter {
	guardClient.statuses++
	return guardClient.Client.Status()
}

func TestInactiveDiscoveryAndFailedReplacementIgnoreQueuedRoutes(t *testing.T) {
	for _, failure := range []string{"delete", "index", "handler", "stopped"} {
		t.Run(failure, func(t *testing.T) {
			udpRouteOnManagedGatewayMetric.Reset()
			t.Cleanup(udpRouteOnManagedGatewayMetric.Reset)
			crd := udpCRD("initial", "v1")
			route := udpRoute("udp", "ns", []gatewayapiv1.ParentReference{{Name: "gateway"}})
			guardReconciler := newTestReconciler(t, crd, route)
			guardReconciler.informer, guardReconciler.resource = nil, schema.GroupVersionResource{}
			guardReconciler.events = make(chan event.GenericEvent, 1)
			attempts := 0
			guardReconciler.newInformer = func(resource schema.GroupVersionResource) informercache.SharedIndexInformer {
				attempts++
				informer := &lifecycleInformer{SharedIndexInformer: informercache.NewSharedIndexInformer(&informercache.ListWatch{}, newUDPRoute(resource.Version), 0, informercache.Indexers{})}
				if attempts == 2 && failure == "index" {
					informer.indexError = fmt.Errorf("index failure")
				}
				if attempts == 2 && failure == "handler" {
					informer.handlerError = fmt.Errorf("handler failure")
				}
				return informer
			}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			require.NoError(t, guardReconciler.ensureUDPInformer(ctx))
			oldDone := guardReconciler.done
			if failure == "stopped" {
				cancel()
				select {
				case <-oldDone:
				case <-time.After(5 * time.Second):
					t.Fatal("informer failed to stop")
				}
				require.NoError(t, guardReconciler.ensureUDPInformer(t.Context()))
				assert.Equal(t, 2, attempts, "same UID/GVR must restart a stopped informer")
				guardReconciler.cancel()
				<-guardReconciler.done
				return
			}
			udpRouteOnManagedGatewayMetric.WithLabelValues("ns", "udp").Set(1)
			if failure == "delete" {
				require.NoError(t, guardReconciler.client.Delete(t.Context(), crd))
			} else {
				crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1alpha2", Served: true}}
				require.NoError(t, guardReconciler.client.Update(t.Context(), crd))
			}
			err := guardReconciler.ensureUDPInformer(ctx)
			if failure == "delete" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.True(t, guardReconciler.resource.Empty())
			assert.Empty(t, guardReconciler.crdUID)
			select {
			case <-oldDone:
			default:
				t.Fatal("old informer is still running")
			}
			spy := &routeCallClient{Client: guardReconciler.client}
			guardReconciler.client = spy
			_, err = guardReconciler.Reconcile(t.Context(), requestFor(route))
			require.NoError(t, err)
			assert.Zero(t, spy.gets)
			assert.Zero(t, spy.statuses)
			assert.Zero(t, promtestutil.CollectAndCount(udpRouteOnManagedGatewayMetric))
			if failure != "delete" {
				require.NoError(t, guardReconciler.ensureUDPInformer(ctx))
				assert.Equal(t, "v1alpha2", guardReconciler.resource.Version)
				guardReconciler.cancel()
				<-guardReconciler.done
			}
		})
	}
}

func TestRegisterMetricsAndDuplicateRegistration(t *testing.T) {
	oldRegistry := ctrlmetrics.Registry
	ctrlmetrics.Registry = prometheus.NewRegistry()
	t.Cleanup(func() { ctrlmetrics.Registry = oldRegistry; udpRouteOnManagedGatewayMetric.Reset() })
	require.NoError(t, RegisterMetrics())
	udpRouteOnManagedGatewayMetric.WithLabelValues("namespace", "name").Set(1)
	families, err := ctrlmetrics.Registry.Gather()
	require.NoError(t, err)
	require.Len(t, families, 1)
	assert.Equal(t, "ingress_operator_udproute_on_managed_gateway", families[0].GetName())
	labels := families[0].Metric[0].Label
	require.Len(t, labels, 2)
	assert.Equal(t, "udproute_name", labels[0].GetName())
	assert.Equal(t, "udproute_namespace", labels[1].GetName())
	var duplicate prometheus.AlreadyRegisteredError
	assert.True(t, errors.As(RegisterMetrics(), &duplicate))
}

func TestUDPManifestPermissionsAndAlert(t *testing.T) {
	data, err := os.ReadFile("../../../../manifests/00-cluster-role.yaml")
	require.NoError(t, err)
	var role struct {
		Rules []struct{ APIGroups, Resources, Verbs []string }
	}
	require.NoError(t, yaml.Unmarshal(data, &role))
	permissions := map[string][]string{}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if resource == "udproutes" || resource == "udproutes/status" {
				permissions[resource] = append(permissions[resource], rule.Verbs...)
				assert.Equal(t, []string{gatewayapiv1.GroupName}, rule.APIGroups)
			}
			assert.NotEqual(t, "tcproutes", resource)
		}
	}
	assert.ElementsMatch(t, []string{"get", "list", "watch"}, permissions["udproutes"])
	assert.ElementsMatch(t, []string{"patch", "update"}, permissions["udproutes/status"])
	data, err = os.ReadFile("../../../../manifests/0000_90_ingress-operator_03_prometheusrules.yaml")
	require.NoError(t, err)
	var rules struct {
		Spec struct {
			Groups []struct {
				Rules []struct {
					Alert, Expr, For    string
					Labels, Annotations map[string]string
				}
			}
		}
	}
	require.NoError(t, yaml.Unmarshal(data, &rules))
	found := false
	for _, group := range rules.Spec.Groups {
		for _, rule := range group.Rules {
			if rule.Alert != "UDPRouteOnManagedGateway" {
				continue
			}
			found = true
			assert.Equal(t, "ingress_operator_udproute_on_managed_gateway == 1", rule.Expr)
			assert.Equal(t, "5m", rule.For)
			assert.Equal(t, "info", rule.Labels["severity"])
			assert.NotEmpty(t, rule.Annotations["summary"])
			assert.NotEmpty(t, rule.Annotations["description"])
			for _, label := range []string{"udproute_namespace", "udproute_name"} {
				assert.Contains(t, rule.Annotations["message"], "$labels."+label)
			}
		}
	}
	assert.True(t, found)
}

func TestMalformedParentsStatusAndUIDPredicate(t *testing.T) {
	for _, invalid := range []interface{}{"invalid", []interface{}{"invalid"}, []interface{}{map[string]interface{}{"name": int64(3)}}} {
		route := newUDPRoute("v1")
		route.Object["spec"] = map[string]interface{}{"parentRefs": invalid}
		_, err := parentReferences(route)
		require.Error(t, err)
		_, err = parentGatewayIndexKeys(route)
		require.Error(t, err)
		assert.Empty(t, ParentGatewayIndexKeys(route))
	}
	route := udpRoute("route", "ns", nil)
	changed := route.DeepCopy()
	changed.SetUID(types.UID("new"))
	assert.True(t, udpRouteEventPredicate().Update(event.UpdateEvent{ObjectOld: route, ObjectNew: changed}))
	assert.False(t, udpRouteEventPredicate().Update(event.UpdateEvent{ObjectOld: route, ObjectNew: gateway("gateway", "ns", "class")}))
	guardReconciler := newTestReconciler(t)
	for _, invalid := range []interface{}{"invalid", []interface{}{"invalid"}} {
		malformed := route.DeepCopy()
		malformed.Object["status"] = map[string]interface{}{"parents": invalid}
		require.Error(t, guardReconciler.updateStatus(t.Context(), malformed, []gatewayapiv1.ParentReference{{Name: "gateway"}}))
	}
	assert.False(t, isOurClass(route))
}

type failureCache struct {
	cache.Cache
	target, name string
	failure      error
}

func (reader *failureCache) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if fmt.Sprintf("%T", object) == reader.target && (reader.name == "" || reader.name == key.Name) {
		return reader.failure
	}
	return reader.Cache.Get(ctx, key, object, opts...)
}

type failureClient struct {
	client.Client
	getFailure, patchFailure error
}

func (guardClient *failureClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if guardClient.getFailure != nil {
		return guardClient.getFailure
	}
	return guardClient.Client.Get(ctx, key, object, opts...)
}
func (guardClient *failureClient) Status() client.StatusWriter {
	return &failureStatusWriter{StatusWriter: guardClient.Client.Status(), failure: guardClient.patchFailure}
}

type failureStatusWriter struct {
	client.StatusWriter
	failure error
}

func (writer *failureStatusWriter) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if writer.failure != nil {
		return writer.failure
	}
	return writer.StatusWriter.Patch(ctx, object, patch, opts...)
}

func TestReaderAndPatchFailuresPreserveStateAndRetry(t *testing.T) {
	for _, problem := range []string{"discovery", "route", "gateway-transport", "class-forbidden", "status"} {
		t.Run(problem, func(t *testing.T) {
			udpRouteOnManagedGatewayMetric.Reset()
			t.Cleanup(udpRouteOnManagedGatewayMetric.Reset)
			route := udpRoute("udp", "ns", []gatewayapiv1.ParentReference{{Name: "first"}, {Name: "second"}})
			foreign := map[string]interface{}{"controllerName": "example.com/foreign", "future": map[string]interface{}{"preserve": true}}
			require.NoError(t, unstructured.SetNestedSlice(route.Object, []interface{}{foreign}, "status", "parents"))
			guardReconciler := newTestReconciler(t, route, gateway("first", "ns", "first-class"), gateway("second", "ns", "second-class"), gatewayClass("first-class", operatorcontroller.OpenShiftGatewayClassControllerName), gatewayClass("second-class", operatorcontroller.OpenShiftGatewayClassControllerName))
			originalClient, originalCache, originalGatewayCache := guardReconciler.client, guardReconciler.cache, guardReconciler.gatewayCache
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(func() {
				cancel()
				if guardReconciler.done != nil {
					select {
					case <-guardReconciler.done:
					case <-time.After(5 * time.Second):
						t.Error("discovery retry informer did not stop")
					}
				}
			})
			if problem == "discovery" {
				require.NoError(t, originalClient.Create(ctx, udpCRD("discovery-retry", "v1")))
				guardReconciler.informer = nil
				guardReconciler.resource = schema.GroupVersionResource{}
				guardReconciler.events = make(chan event.GenericEvent, 16)
				guardReconciler.newInformer = func(resource schema.GroupVersionResource) informercache.SharedIndexInformer {
					return informercache.NewSharedIndexInformer(&informercache.ListWatch{
						ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
							return &unstructured.UnstructuredList{Object: map[string]interface{}{"apiVersion": gatewayapiv1.GroupName + "/" + resource.Version, "kind": "UDPRouteList", "metadata": map[string]interface{}{"resourceVersion": "1"}}, Items: []unstructured.Unstructured{*route.DeepCopy()}}, nil
						},
						WatchFuncWithContext: func(_ context.Context, options metav1.ListOptions) (watch.Interface, error) {
							if options.SendInitialEvents != nil && *options.SendInitialEvents {
								return nil, fmt.Errorf("watch-list unsupported by fixture")
							}
							return watch.NewRaceFreeFake(), nil
						},
					}, newUDPRoute(resource.Version), 0, informercache.Indexers{})
				}
			}
			failure := fmt.Errorf("transport unavailable")
			if problem == "class-forbidden" {
				failure = apierrors.NewForbidden(schema.GroupResource{Resource: "gatewayclasses"}, "second-class", fmt.Errorf("denied"))
			}
			switch problem {
			case "discovery":
				guardReconciler.cache = &failureCache{Cache: guardReconciler.cache, target: "*v1.CustomResourceDefinition", failure: failure}
			case "route":
				guardReconciler.client = &failureClient{Client: guardReconciler.client, getFailure: failure}
			case "gateway-transport":
				guardReconciler.gatewayCache = &failureCache{Cache: guardReconciler.gatewayCache, target: "*v1.Gateway", name: "second", failure: failure}
			case "class-forbidden":
				guardReconciler.gatewayCache = &failureCache{Cache: guardReconciler.gatewayCache, target: "*v1.GatewayClass", name: "second-class", failure: failure}
			case "status":
				guardReconciler.client = &failureClient{Client: guardReconciler.client, patchFailure: failure}
			}
			udpRouteOnManagedGatewayMetric.WithLabelValues("ns", "udp").Set(1)
			var err error
			if problem == "discovery" {
				err = guardReconciler.ensureUDPInformer(t.Context())
			} else {
				_, err = guardReconciler.Reconcile(t.Context(), requestFor(route))
			}
			require.ErrorIs(t, err, failure)
			assert.Equal(t, float64(1), promtestutil.ToFloat64(udpRouteOnManagedGatewayMetric.WithLabelValues("ns", "udp")))
			current := newUDPRoute("v1")
			require.NoError(t, originalClient.Get(t.Context(), client.ObjectKeyFromObject(route), current))
			parents, _, err := unstructured.NestedSlice(current.Object, "status", "parents")
			require.NoError(t, err)
			assert.Equal(t, []interface{}{foreign}, parents, "failure must not publish partial ownership or destroy foreign state")
			guardReconciler.client, guardReconciler.cache, guardReconciler.gatewayCache = originalClient, originalCache, originalGatewayCache
			if problem == "discovery" {
				_, err = guardReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: UDPRouteCRDName}})
				require.NoError(t, err)
				assert.Equal(t, types.UID("discovery-retry"), guardReconciler.crdUID)
				assert.Equal(t, schema.GroupVersionResource{Group: gatewayapiv1.GroupName, Version: "v1", Resource: "udproutes"}, guardReconciler.resource)
				select {
				case emitted := <-guardReconciler.events:
					assert.Equal(t, requestFor(route).NamespacedName, client.ObjectKeyFromObject(emitted.Object))
					assert.Equal(t, newUDPRoute("v1").GroupVersionKind(), emitted.Object.GetObjectKind().GroupVersionKind())
				case <-time.After(5 * time.Second):
					t.Fatal("successful discovery retry did not emit the listed route")
				}
			}
			_, err = guardReconciler.Reconcile(t.Context(), requestFor(route))
			require.NoError(t, err)
			require.NoError(t, originalClient.Get(t.Context(), client.ObjectKeyFromObject(route), current))
			assert.Len(t, routeParents(t, current), 3)
		})
	}
}

func TestRealInformerBackpressureReplacementAndStoppedRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	crd := udpCRD("first", "v1")
	guardReconciler := newTestReconciler(t, crd)
	guardReconciler.informer, guardReconciler.resource = nil, schema.GroupVersionResource{}
	guardReconciler.events = make(chan event.GenericEvent, 1)
	guardReconciler.events <- event.GenericEvent{Object: newUDPRoute("v1")}
	versions := make(chan string, 8)
	recoveredWatches := make(chan string, 2)
	notifyVersion := func(ctx context.Context, version string) {
		select {
		case versions <- version:
		case <-ctx.Done():
		}
	}
	guardReconciler.newInformer = func(resource schema.GroupVersionResource) informercache.SharedIndexInformer {
		watcher := watch.NewRaceFreeFake()
		var lists, watches atomic.Int32
		route := newUDPRoute(resource.Version)
		route.SetName("route")
		route.SetNamespace("ns")
		return informercache.NewSharedIndexInformer(&informercache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, _ metav1.ListOptions) (runtime.Object, error) {
				notifyVersion(ctx, resource.Version)
				if lists.Add(1) == 1 {
					return nil, fmt.Errorf("transient list transport failure")
				}
				return &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*route}}, nil
			},
			WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				notifyVersion(ctx, resource.Version)
				if options.SendInitialEvents != nil && *options.SendInitialEvents {
					return nil, apierrors.NewBadRequest("watch-list unsupported by test server")
				}
				if watches.Add(1) == 1 {
					return nil, fmt.Errorf("transient watch transport failure")
				}
				select {
				case recoveredWatches <- resource.Version:
				case <-ctx.Done():
				}
				return watcher, nil
			},
		}, newUDPRoute(resource.Version), 0, informercache.Indexers{})
	}
	require.NoError(t, guardReconciler.ensureUDPInformer(ctx))
	select {
	case version := <-versions:
		assert.Equal(t, "v1", version)
	case <-time.After(5 * time.Second):
		t.Fatal("old informer never requested its version")
	}
	require.Eventually(t, func() bool {
		return guardReconciler.informer.GetIndexer().ListKeys() != nil && len(guardReconciler.informer.GetIndexer().List()) == 1
	}, 5*time.Second, time.Millisecond)
	oldDone := guardReconciler.done
	select {
	case version := <-recoveredWatches:
		assert.Equal(t, "v1", version)
	case <-time.After(5 * time.Second):
		t.Fatal("old informer did not recover a transient watch failure")
	}
	for len(versions) > 0 {
		<-versions
	}
	crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1alpha2", Served: true}}
	require.NoError(t, guardReconciler.client.Update(ctx, crd))
	finished := make(chan error, 1)
	go func() { finished <- guardReconciler.ensureUDPInformer(ctx) }()
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("saturated old event callback blocked replacement")
	}
	select {
	case <-oldDone:
	default:
		t.Fatal("old informer did not finish")
	}
	<-guardReconciler.events
	select {
	case event := <-guardReconciler.events:
		assert.Equal(t, "gateway.networking.k8s.io/v1alpha2", event.Object.(*unstructured.Unstructured).GetAPIVersion())
	case <-time.After(5 * time.Second):
		t.Fatal("replacement informer failed to enqueue its own version")
	}
	done := guardReconciler.done
	select {
	case version := <-recoveredWatches:
		assert.Equal(t, "v1alpha2", version)
	case <-time.After(5 * time.Second):
		t.Fatal("replacement informer did not recover a transient watch failure")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement informer failed to stop")
	}
}

func TestConstructorDynamicClientUsesExactDiscoveredGVR(t *testing.T) {
	oldCache, oldController := newGatewayCache, newGuardController
	t.Cleanup(func() { newGatewayCache, newGuardController = oldCache, oldController })
	for _, version := range []string{"v1", "v1alpha2"} {
		t.Run(version, func(t *testing.T) {
			requests := make(chan string, 16)
			route := newUDPRoute(version)
			route.SetName("udp")
			route.SetNamespace("outside-manager-cache")
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/apis/gateway.networking.k8s.io/"+version+"/udproutes" {
					http.Error(response, "wrong discovered resource", http.StatusNotFound)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				if request.URL.Query().Get("watch") == "true" {
					if request.URL.Query().Get("sendInitialEvents") == "true" {
						http.Error(response, "watch-list not enabled in fixture", http.StatusBadRequest)
						return
					}
					select {
					case requests <- "watch:" + request.URL.Path:
					case <-request.Context().Done():
						return
					}
					response.WriteHeader(http.StatusOK)
					response.(http.Flusher).Flush()
					<-request.Context().Done()
					return
				}
				select {
				case requests <- "list:" + request.URL.Path:
				case <-request.Context().Done():
					return
				}
				list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*route}}
				list.SetAPIVersion(gatewayapiv1.GroupName + "/" + version)
				list.SetKind("UDPRouteList")
				list.SetResourceVersion("1")
				if err := json.NewEncoder(response).Encode(list); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			fixture := newTestReconciler(t, udpCRD("discovered", version))
			newGatewayCache = func(_ *rest.Config, _ cache.Options) (cache.Cache, error) { return fixture.gatewayCache, nil }
			var built *reconciler
			newGuardController = func(_ string, opts controller.Options) (controller.Controller, error) {
				built = opts.Reconciler.(*reconciler)
				return &recordingGuard{}, nil
			}
			_, err := NewUnmanaged(&guardManager{config: &rest.Config{Host: server.URL}, guardReconciler: fixture}, nil)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			require.NoError(t, built.ensureUDPInformer(ctx))
			for _, method := range []string{"list:", "watch:"} {
				select {
				case request := <-requests:
					assert.Equal(t, method+"/apis/gateway.networking.k8s.io/"+version+"/udproutes", request)
				case <-time.After(5 * time.Second):
					t.Fatal("dynamic client did not use discovered GVR")
				}
			}
			select {
			case event := <-built.events:
				assert.Equal(t, route.GroupVersionKind(), event.Object.GetObjectKind().GroupVersionKind())
			case <-time.After(5 * time.Second):
				t.Fatal("dynamic informer failed to emit discovered route")
			}
			cancel()
			select {
			case <-built.done:
			case <-time.After(5 * time.Second):
				t.Fatal("dynamic informer did not stop")
			}
		})
	}
}
