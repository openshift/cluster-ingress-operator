package gatewayapi

import (
	"context"
	"testing"

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
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func Test_Reconcile(t *testing.T) {
	crd := func(name string) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
	}
	tests := []struct {
		name            string
		existingObjects []runtime.Object
		expectCreate    []client.Object
		expectUpdate    []client.Object
		expectDelete    []client.Object
	}{
		{
			name:            "featuregates.config.openshift.io/cluster doesn't exist",
			existingObjects: []runtime.Object{},
			expectCreate:    []client.Object{},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
		},
		{
			name: "featuregates.config.openshift.io/cluster specifies CustomNoUpgrade but doesn't specify GatewayAPI",
			existingObjects: []runtime.Object{
				&configv1.FeatureGate{
					ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
					Spec: configv1.FeatureGateSpec{
						FeatureGateSelection: configv1.FeatureGateSelection{
							FeatureSet: configv1.CustomNoUpgrade,
							CustomNoUpgrade: &configv1.CustomFeatureGates{
								Enabled: []string{"Foo", "Bar", "Baz"},
							},
						},
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "featuregates.config.openshift.io/cluster specifies CustomNoUpgrade but disables GatewayAPI",
			existingObjects: []runtime.Object{
				&configv1.FeatureGate{
					ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
					Spec: configv1.FeatureGateSpec{
						FeatureGateSelection: configv1.FeatureGateSelection{
							FeatureSet: configv1.CustomNoUpgrade,
							CustomNoUpgrade: &configv1.CustomFeatureGates{
								Disabled: []string{"GatewayAPI"},
							},
						},
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "featuregates.config.openshift.io/cluster specifies CustomNoUpgrade and enables GatewayAPI",
			existingObjects: []runtime.Object{
				&configv1.FeatureGate{
					ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
					Spec: configv1.FeatureGateSpec{
						FeatureGateSelection: configv1.FeatureGateSelection{
							FeatureSet: configv1.CustomNoUpgrade,
							CustomNoUpgrade: &configv1.CustomFeatureGates{
								Enabled: []string{"GatewayAPI"},
							},
						},
					},
				},
			},
			expectCreate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
				crd("httproutes.gateway.networking.k8s.io"),
				crd("referencegrants.gateway.networking.k8s.io"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "featuregates.config.openshift.io/cluster specifies TechPreviewNoUpgrade",
			existingObjects: []runtime.Object{
				&configv1.FeatureGate{
					ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
					Spec: configv1.FeatureGateSpec{
						FeatureGateSelection: configv1.FeatureGateSelection{
							FeatureSet: configv1.TechPreviewNoUpgrade,
						},
					},
				},
			},
			// TODO Update expectCreate and expectStartCtrl when
			// "GatewayAPI" is added to
			// configv1.FeatureSets["TechPreviewNoUpgrade"].Enabled.
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "featuregates.config.openshift.io/bogus enables GatewayAPI",
			existingObjects: []runtime.Object{
				&configv1.FeatureGate{
					ObjectMeta: metav1.ObjectMeta{Name: "bogus"},
					Spec: configv1.FeatureGateSpec{
						FeatureGateSelection: configv1.FeatureGateSelection{
							FeatureSet: configv1.CustomNoUpgrade,
							CustomNoUpgrade: &configv1.CustomFeatureGates{
								Enabled: []string{"GatewayAPI"},
							},
						},
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
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
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := fakeCache{Informers: &informer, Reader: cl}
			reconciler := &reconciler{
				cache:  cache,
				client: cl,
			}
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "cluster",
				},
			}
			res, err := reconciler.Reconcile(context.Background(), req)
			assert.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, res)
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
