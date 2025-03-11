package gatewayapi

import (
	"context"
	"testing"
	"time"

	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

func Test_Reconcile(t *testing.T) {
	crd := func(name string) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
	}
	tests := []struct {
		name                        string
		gatewayAPIEnabled           bool
		gatewayAPIControllerEnabled bool
		existingObjects             []runtime.Object
		expectCreate                []client.Object
		expectUpdate                []client.Object
		expectDelete                []client.Object
		expectStartCtrl             bool
	}{
		{
			name:              "gateway API disabled",
			gatewayAPIEnabled: false,
			expectCreate:      []client.Object{},
			expectUpdate:      []client.Object{},
			expectDelete:      []client.Object{},
			expectStartCtrl:   false,
		},
		{
			name:                        "gateway API enabled",
			gatewayAPIEnabled:           true,
			gatewayAPIControllerEnabled: true,
			expectCreate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
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
			expectCreate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
				crd("httproutes.gateway.networking.k8s.io"),
				crd("referencegrants.gateway.networking.k8s.io"),
			},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectStartCtrl: false,
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
				Build()
			cl := &fakeClientRecorder{fakeClient, t, []client.Object{}, []client.Object{}, []client.Object{}}
			ctrl := &fakeController{t, false, nil}
			reconciler := &reconciler{
				client: cl,
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
			case <-ctrl.startNotificationChan:
				t.Log("Start() was called")
			case <-ctx.Done():
				t.Log(ctx.Err())
			}
			assert.Equal(t, ctrl.started, tc.expectStartCtrl, "fake controller should have been started")
			cmpOpts := []cmp.Option{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Annotations", "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(apiextensionsv1.CustomResourceDefinition{}, "Spec"),
			}
			if diff := cmp.Diff(tc.expectCreate, cl.added, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual creates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectUpdate, cl.updated, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual updates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectDelete, cl.deleted, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual deletes: %s", diff)
			}
		})
	}
}

func TestReconcileOnlyStartsControllerOnce(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects().Build()
	cl := &fakeClientRecorder{fakeClient, t, []client.Object{}, []client.Object{}, []client.Object{}}
	ctrl := &fakeController{t, false, make(chan struct{})}
	reconciler := &reconciler{
		client: cl,
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
	case <-ctrl.startNotificationChan:
		t.Log("Start() was called for the first reconcile request")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assert.True(t, ctrl.started, "fake controller should have been started")

	// Reconcile again and verify Start() isn't called again.
	res, err = reconciler.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)
	select {
	case <-ctrl.startNotificationChan:
		t.Error("Start() was called again for the second reconcile request")
	case <-ctx.Done():
		t.Log(ctx.Err())
	}
	assert.True(t, ctrl.started, "fake controller should have been started")
}

type fakeCache struct {
	cache.Informers
	client.Reader
}

type fakeClientRecorder struct {
	client.Client
	*testing.T

	added   []client.Object
	updated []client.Object
	deleted []client.Object
}

func (c *fakeClientRecorder) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *fakeClientRecorder) List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error {
	return c.Client.List(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Scheme() *runtime.Scheme {
	return c.Client.Scheme()
}

func (c *fakeClientRecorder) RESTMapper() meta.RESTMapper {
	return c.Client.RESTMapper()
}

func (c *fakeClientRecorder) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.added = append(c.added, obj)
	return c.Client.Create(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.deleted = append(c.deleted, obj)
	return c.Client.Delete(ctx, obj, opts...)
}

func (c *fakeClientRecorder) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return c.Client.DeleteAllOf(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.updated = append(c.updated, obj)
	return c.Client.Update(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c *fakeClientRecorder) Status() client.StatusWriter {
	return c.Client.Status()
}

type fakeController struct {
	*testing.T
	// started indicates whether Start() has been called.
	started bool
	// startNotificationChan is an optional channel by which a test can
	// receive a notification when Start() is called.
	startNotificationChan chan struct{}
}

func (_ *fakeController) Reconcile(context.Context, reconcile.Request) (reconcile.Result, error) {
	return reconcile.Result{}, nil
}

func (_ *fakeController) Watch(_ source.Source) error {
	return nil
}

func (c *fakeController) Start(_ context.Context) error {
	if c.started {
		c.T.Fatal("controller was started twice!")
	}
	c.started = true
	if c.startNotificationChan != nil {
		c.startNotificationChan <- struct{}{}
	}
	return nil
}

func (_ *fakeController) GetLogger() logr.Logger {
	return logf.Logger
}
