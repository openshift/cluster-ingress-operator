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
	co := func(name string) *configv1.ClusterOperator {
		return &configv1.ClusterOperator{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
	}
	tests := []struct {
		name                        string
		gatewayAPIEnabled           bool
		gatewayAPIControllerEnabled bool
		existingObjects             []runtime.Object
		existingStatusSubresource   []client.Object
		expectCreate                []client.Object
		expectUpdate                []client.Object
		expectDelete                []client.Object
		expectStatusUpdate          []client.Object
		expectStartCtrl             bool
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
			},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectStartCtrl: true,
		},
		{
			name:                        "gateway API enabled, gateway API controller disabled",
			gatewayAPIEnabled:           true,
			gatewayAPIControllerEnabled: false,
			existingObjects: []runtime.Object{
				co("ingress"),
			},
			expectCreate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
				crd("grpcroutes.gateway.networking.k8s.io"),
				crd("httproutes.gateway.networking.k8s.io"),
				crd("referencegrants.gateway.networking.k8s.io"),
			},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectStartCtrl: false,
		},
		{
			name:                        "unmanaged gateway API CRDs",
			gatewayAPIEnabled:           true,
			gatewayAPIControllerEnabled: true,
			existingObjects: []runtime.Object{
				co("ingress"),
				crd("listenersets.gateway.networking.x-k8s.io"),
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
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			expectStatusUpdate: []client.Object{
				co("ingress"),
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
			},
			expectUpdate:       []client.Object{},
			expectDelete:       []client.Object{},
			expectStatusUpdate: []client.Object{},
			expectStartCtrl:    true,
		},
	}

	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				WithStatusSubresource(tc.existingStatusSubresource...).
				WithIndex(&apiextensionsv1.CustomResourceDefinition{}, "crdAPIGroup", client.IndexerFunc(func(o client.Object) []string {
					if strings.Contains(o.GetName(), "gateway.networking") {
						return []string{"gateway"}
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
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Annotations", "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(apiextensionsv1.CustomResourceDefinition{}, "Spec"),
				cmpopts.IgnoreFields(configv1.ClusterOperator{}, "Status"),
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
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(
			&configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "ingress"},
			}).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, "crdAPIGroup", client.IndexerFunc(func(o client.Object) []string {
			return []string{"gateway"} // all crds are gateway api ones
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
