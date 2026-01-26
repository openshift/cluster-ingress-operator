package gatewayapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
)

func Test_Reconcile(t *testing.T) {
	crd := func(name string) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
	}
	clusterRole := func(name string) *rbacv1.ClusterRole {
		return &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
	}
	co := func(name string) *configv1.ClusterOperator {
		return &configv1.ClusterOperator{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
	}
	coWithExtension := func(name, extension string) *configv1.ClusterOperator {
		co := co(name)
		co.Status = configv1.ClusterOperatorStatus{
			Extension: runtime.RawExtension{
				Raw: []byte(extension),
			},
		}
		return co
	}

	tests := []struct {
		name                        string
		gatewayAPIEnabled           bool
		gatewayAPIControllerEnabled bool
		existingObjects             []runtime.Object
		// existingStatusSubresource contains the original version of objects
		// whose status will updated by Reconcile function.
		// This field is similar to `existingObjects` but is specifically used
		// for objects where status updates are performed using `Status().Update()` call.
		existingStatusSubresource []client.Object
		expectCreate              []client.Object
		expectUpdate              []client.Object
		expectDelete              []client.Object
		// expectStatusUpdate contains the updated versions of objects
		// whose status is expected to be updated by the test.
		expectStatusUpdate []client.Object
		expectStartCtrl    bool
	}{
		{
			name:              "gateway API disabled",
			gatewayAPIEnabled: false,
			existingObjects: []runtime.Object{
				co("ingress"),
			},
			expectCreate:    []client.Object{},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectStartCtrl: false,
		},
		{
			name:                        "gateway API enabled",
			gatewayAPIEnabled:           true,
			gatewayAPIControllerEnabled: true,
			existingObjects: []runtime.Object{
				co("ingress"),
			},
			expectCreate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
				crd("grpcroutes.gateway.networking.k8s.io"),
				crd("httproutes.gateway.networking.k8s.io"),
				crd("referencegrants.gateway.networking.k8s.io"),
				crd("backendtlspolicies.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectStartCtrl: true,
		},
		{
			name:                        "gateway API enabled, gateway API controller disabled",
			gatewayAPIEnabled:           true,
			gatewayAPIControllerEnabled: false,
<<<<<<< HEAD
			marketplaceEnabled:          true,
			olmEnabled:                  true,
			existingObjects: []runtime.Object{
				co("ingress"),
			},
			expectCreate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
				crd("grpcroutes.gateway.networking.k8s.io"),
				crd("httproutes.gateway.networking.k8s.io"),
				crd("referencegrants.gateway.networking.k8s.io"),
				crd("backendtlspolicies.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectStartCtrl: false,
		},
		{
			name:                        "GatewayAPI enabled, GatewayAPIController enabled, marketplace and OLM capabilities disabled",
			gatewayAPIEnabled:           true,
			gatewayAPIControllerEnabled: true,
			marketplaceEnabled:          false,
			olmEnabled:                  false,
=======
>>>>>>> 3ab262185 (refactor(gatewayapi): replace OLM-based Istio install with Sail Library)
			existingObjects: []runtime.Object{
				co("ingress"),
			},
			expectCreate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
				crd("grpcroutes.gateway.networking.k8s.io"),
				crd("httproutes.gateway.networking.k8s.io"),
				crd("referencegrants.gateway.networking.k8s.io"),
				crd("backendtlspolicies.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectStartCtrl: false,
		},
		{
			name:                        "unmanaged gateway API CRDs created",
			gatewayAPIEnabled:           true,
			gatewayAPIControllerEnabled: true,
			existingObjects: []runtime.Object{
				co("ingress"),
				crd("listenersets.gateway.networking.x-k8s.io"),
				crd("backendtrafficpolicies.gateway.networking.x-k8s.io"),
			},
			existingStatusSubresource: []client.Object{
				co("ingress"),
			},
			expectCreate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
				crd("grpcroutes.gateway.networking.k8s.io"),
				crd("httproutes.gateway.networking.k8s.io"),
				crd("referencegrants.gateway.networking.k8s.io"),
				crd("backendtlspolicies.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			expectStatusUpdate: []client.Object{
				coWithExtension("ingress", `{"unmanagedGatewayAPICRDNames":"backendtrafficpolicies.gateway.networking.x-k8s.io,listenersets.gateway.networking.x-k8s.io"}`),
			},
			expectStartCtrl: true,
		},
		{
			name:                        "unmanaged gateway API CRDs removed",
			gatewayAPIEnabled:           true,
			gatewayAPIControllerEnabled: true,
			existingObjects: []runtime.Object{
				coWithExtension("ingress", `{"unmanagedGatewayAPICRDNames":"listenersets.gateway.networking.x-k8s.io"}`),
			},
			existingStatusSubresource: []client.Object{
				co("ingress"),
			},
			expectCreate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
				crd("grpcroutes.gateway.networking.k8s.io"),
				crd("httproutes.gateway.networking.k8s.io"),
				crd("referencegrants.gateway.networking.k8s.io"),
				crd("backendtlspolicies.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			expectStatusUpdate: []client.Object{
				coWithExtension("ingress", `{}`),
			},
			expectStartCtrl: true,
		},
		{
			name:                        "third party CRDs",
			gatewayAPIEnabled:           true,
			gatewayAPIControllerEnabled: true,
			existingObjects: []runtime.Object{
				co("ingress"),
				crd("thirdpartycrd1.openshift.io"),
				crd("thirdpartycrd2.openshift.io"),
			},
			existingStatusSubresource: []client.Object{
				co("ingress"),
			},
			expectCreate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
				crd("grpcroutes.gateway.networking.k8s.io"),
				crd("httproutes.gateway.networking.k8s.io"),
				crd("referencegrants.gateway.networking.k8s.io"),
				crd("backendtlspolicies.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			// Third party CRDs have no impact on cluster operator status.
			expectStatusUpdate: []client.Object{},
			expectStartCtrl:    true,
		},
	}

	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				WithStatusSubresource(tc.existingStatusSubresource...).
				WithIndex(&apiextensionsv1.CustomResourceDefinition{}, "gatewayAPICRD", client.IndexerFunc(func(o client.Object) []string {
					// Assume that all experimental CRDs are unmanaged.
					if strings.Contains(o.GetName(), "gateway.networking.x-k8s.io") {
						return []string{"unmanaged"}
					}
					return []string{}
				})).
				Build()
			cl := &testutil.FakeClientRecorder{
				Client:  fakeClient,
				T:       t,
				Added:   []client.Object{},
				Updated: []client.Object{},
				Deleted: []client.Object{},
				StatusWriter: &testutil.FakeStatusWriter{
					StatusWriter: fakeClient.Status(),
				},
			}
			ctrl := &testutil.FakeController{t, false, nil}
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
			reconciler := &reconciler{
				client: cl,
				cache:  cache,
				config: Config{
					GatewayAPIEnabled:           tc.gatewayAPIEnabled,
					GatewayAPIControllerEnabled: tc.gatewayAPIControllerEnabled,
					DependentControllers:        []controller.Controller{ctrl},
				},
				fieldIndexer: FakeIndexer{},
			}
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "cluster",
				},
			}
			res, err := reconciler.Reconcile(context.Background(), req)
			assert.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, res)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			select {
			case <-ctrl.StartNotificationChan:
				t.Log("Start() was called")
			case <-ctx.Done():
				t.Log(ctx.Err())
			}
			assert.Equal(t, ctrl.Started, tc.expectStartCtrl, "fake controller should have been started")
			cmpOpts := []cmp.Option{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Labels", "Annotations", "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(apiextensionsv1.CustomResourceDefinition{}, "Spec"),
				cmpopts.IgnoreFields(rbacv1.ClusterRole{}, "Rules", "AggregationRule"),
			}
			if diff := cmp.Diff(tc.expectCreate, cl.Added, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual creates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectUpdate, cl.Updated, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual updates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectDelete, cl.Deleted, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual deletes: %s", diff)
			}
			if diff := cmp.Diff(tc.expectStatusUpdate, cl.StatusWriter.Updated, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual status updates: %s", diff)
			}
		})
	}
}

func TestReconcileOnlyStartsControllerOnce(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(
			&configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "ingress"},
			}).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, "gatewayAPICRD", client.IndexerFunc(func(o client.Object) []string {
			// Assume that there are no unmanaged CRDs.
			return []string{}
		})).
		Build()
	cl := &testutil.FakeClientRecorder{
		Client:  fakeClient,
		T:       t,
		Added:   []client.Object{},
		Updated: []client.Object{},
		Deleted: []client.Object{},
	}
	ctrl := &testutil.FakeController{t, false, make(chan struct{})}
	informer := informertest.FakeInformers{Scheme: scheme}
	cache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	reconciler := &reconciler{
		client: cl,
		cache:  cache,
		config: Config{
			GatewayAPIEnabled:           true,
			GatewayAPIControllerEnabled: true,
			DependentControllers:        []controller.Controller{ctrl},
		},
		fieldIndexer: FakeIndexer{},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconcile once and verify Start() is called.
	res, err := reconciler.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-ctrl.StartNotificationChan:
		t.Log("Start() was called for the first reconcile request")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assert.True(t, ctrl.Started, "fake controller should have been started")

	// Reconcile again and verify Start() isn't called again.
	res, err = reconciler.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)
	select {
	case <-ctrl.StartNotificationChan:
		t.Error("Start() was called again for the second reconcile request")
	case <-ctx.Done():
		t.Log(ctx.Err())
	}
	assert.True(t, ctrl.Started, "fake controller should have been started")
}

type FakeIndexer struct{}

func (indexer FakeIndexer) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	return nil
}
