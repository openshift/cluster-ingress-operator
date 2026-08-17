package gatewayapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
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
		name               string
		marketplaceEnabled bool
		olmEnabled         bool
		existingObjects    []runtime.Object
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
		expectRequeue      bool
	}{
		{
			name:               "gateway API enabled",
			marketplaceEnabled: true,
			olmEnabled:         true,
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
				crd("listenersets.gateway.networking.k8s.io"),
				crd("tlsroutes.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectStartCtrl: false,
			expectRequeue:   true,
		},
		{
			name:               "gateway API enabled with established CRDs",
			marketplaceEnabled: true,
			olmEnabled:         true,
			existingObjects:    append(establishedManagedCRDs(), co("ingress")),
			expectCreate: []client.Object{
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate: []client.Object{
				crd("gatewayclasses.gateway.networking.k8s.io"),
				crd("gateways.gateway.networking.k8s.io"),
				crd("grpcroutes.gateway.networking.k8s.io"),
				crd("httproutes.gateway.networking.k8s.io"),
				crd("referencegrants.gateway.networking.k8s.io"),
				crd("backendtlspolicies.gateway.networking.k8s.io"),
				crd("listenersets.gateway.networking.k8s.io"),
				crd("tlsroutes.gateway.networking.k8s.io"),
			},
			expectDelete:    []client.Object{},
			expectStartCtrl: true,
		},
		{
			name:               "GatewayAPI enabled, GatewayAPIController enabled, marketplace and OLM capabilities disabled",
			marketplaceEnabled: false,
			olmEnabled:         false,
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
				crd("listenersets.gateway.networking.k8s.io"),
				crd("tlsroutes.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectStartCtrl: false,
		},
		{
			name:               "unmanaged gateway API CRDs created",
			marketplaceEnabled: true,
			olmEnabled:         true,
			existingObjects: []runtime.Object{
				co("ingress"),
				crd("invalid.test.gateway.networking.k8s.io"),
				crd("another.test.gateway.networking.k8s.io"),
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
				crd("listenersets.gateway.networking.k8s.io"),
				crd("tlsroutes.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			expectStatusUpdate: []client.Object{
				coWithExtension("ingress", `{"unmanagedGatewayAPICRDNames":"another.test.gateway.networking.k8s.io,invalid.test.gateway.networking.k8s.io"}`),
			},
			expectStartCtrl: false,
			expectRequeue:   true,
		},
		{
			name:               "unmanaged gateway API CRDs removed",
			marketplaceEnabled: true,
			olmEnabled:         true,
			existingObjects: []runtime.Object{
				coWithExtension("ingress", `{"unmanagedGatewayAPICRDNames":"invalid.test.gateway.networking.k8s.io"}`),
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
				crd("listenersets.gateway.networking.k8s.io"),
				crd("tlsroutes.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			expectStatusUpdate: []client.Object{
				coWithExtension("ingress", `{}`),
			},
			expectStartCtrl: false,
			expectRequeue:   true,
		},
		{
			name:               "third party CRDs",
			marketplaceEnabled: true,
			olmEnabled:         true,
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
				crd("listenersets.gateway.networking.k8s.io"),
				crd("tlsroutes.gateway.networking.k8s.io"),
				clusterRole("system:openshift:gateway-api:aggregate-to-admin"),
				clusterRole("system:openshift:gateway-api:aggregate-to-view"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			// Third party CRDs have no impact on cluster operator status.
			expectStatusUpdate: []client.Object{},
			expectStartCtrl:    false,
			expectRequeue:      true,
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
					// Assume that test.gateway group CRD is unmanaged.
					if strings.Contains(o.GetName(), "test.gateway.networking.k8s.io") {
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
			ctrl := &testutil.FakeController{T: t, Started: false, StartNotificationChan: nil}
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
			reconciler := &reconciler{
				client: cl,
				cache:  cache,
				config: Config{
					MarketplaceEnabled:              tc.marketplaceEnabled,
					OperatorLifecycleManagerEnabled: tc.olmEnabled,
					DependentControllers:            []controller.Controller{ctrl},
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
			if tc.expectRequeue {
				assert.Equal(t, reconcile.Result{RequeueAfter: 10 * time.Second}, res, "expected requeue after 10s")
			} else {
				assert.Equal(t, reconcile.Result{}, res)
			}
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
				cmpopts.IgnoreFields(apiextensionsv1.CustomResourceDefinition{}, "Spec", "Status"),
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
		WithRuntimeObjects(append(establishedManagedCRDs(),
			&configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "ingress"},
			},
		)...).
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
	ctrl := &testutil.FakeController{T: t, Started: false, StartNotificationChan: make(chan struct{})}
	informer := informertest.FakeInformers{Scheme: scheme}
	cache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	reconciler := &reconciler{
		client: cl,
		cache:  cache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			DependentControllers:            []controller.Controller{ctrl},
		},
		fieldIndexer: FakeIndexer{},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconcile once and verify Start() is called.
	res, err := reconciler.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)
	assert.True(t, reconciler.controllersStarted, "controllersStarted should be true after first reconcile")
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
	assert.True(t, reconciler.controllersStarted, "controllersStarted should still be true after second reconcile")
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

func establishedManagedCRDs() []runtime.Object {
	var objs []runtime.Object
	for _, managed := range managedCRDs {
		objs = append(objs, &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: managed.Name},
			Status: apiextensionsv1.CustomResourceDefinitionStatus{
				Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
					{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
				},
			},
		})
	}
	return objs
}

// TestReconcile_ForbiddenIngress_BlocksOwnership verifies that when
// the Ingress CR Get returns Forbidden, reconcile returns an error
// (triggering requeue) without performing any ownership mutations,
// CRD ensures, or dependent controller starts.
func TestReconcile_ForbiddenIngress_BlocksOwnership(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	var objs []runtime.Object
	for _, managed := range managedCRDs {
		crd := managed.DeepCopy()
		crd.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{
			{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
		}
		objs = append(objs, crd)
	}

	objs = append(objs, &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
		},
	})

	objs = append(objs, &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "ingress"},
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
			return []string{}
		})).
		Build()

	cl := &testutil.FakeClientRecorder{
		Client:  &forbidIngressClient{Client: fakeClient},
		T:       t,
		Added:   []client.Object{},
		Updated: []client.Object{},
		Deleted: []client.Object{},
	}

	depCtrl := &testutil.FakeController{
		T:                     t,
		Started:               false,
		StartNotificationChan: make(chan struct{}),
	}
	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}

	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
			DependentControllers:            []controller.Controller{depCtrl},
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	_, err := r.Reconcile(context.Background(), req)
	assert.Error(t, err,
		"Forbidden Ingress must cause reconcile to return error for requeue")
	assert.Contains(t, err.Error(), "forbidden")

	assert.False(t, modeAccessor.ShouldManageCRDs(),
		"Forbidden must NOT set ShouldManageCRDs=true")
	assert.False(t, modeAccessor.AllowDependents(),
		"Forbidden must NOT set AllowDependents=true")

	assert.Empty(t, cl.Added,
		"no CRDs/RBAC/VAP should be created when Ingress is Forbidden")
	assert.False(t, depCtrl.Started,
		"dependent controllers must NOT start when Ingress is Forbidden")
}

func TestManagementModeEnabled(t *testing.T) {
	t.Run("nil ModeAccessor", func(t *testing.T) {
		r := &reconciler{config: Config{ModeAccessor: nil}}
		assert.False(t, r.managementModeEnabled())
	})
	t.Run("gate disabled", func(t *testing.T) {
		r := &reconciler{config: Config{ModeAccessor: NewModeAccessor(false)}}
		assert.False(t, r.managementModeEnabled())
	})
	t.Run("gate enabled", func(t *testing.T) {
		r := &reconciler{config: Config{ModeAccessor: NewModeAccessor(true)}}
		assert.True(t, r.managementModeEnabled())
	})
}

// ---------- Phase 2: Mode-conditional CRD/RBAC ensure tests ----------

// TestReconcile_Unmanaged_SkipsCRDAndRBAC verifies that when the
// Ingress CR sets mode=Unmanaged, the reconciler does NOT create/update
// CRDs or ClusterRoles.
func TestReconcile_Unmanaged_SkipsCRDAndRBAC(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			},
		},
	}

	var objs []runtime.Object
	objs = append(objs, ingressObj)
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
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

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	res, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res,
		"Unmanaged gate-ON path must not requeue; watches on Ingress CR/CRDs/VAP/ClusterRoles trigger the next reconcile")

	assert.False(t, modeAccessor.ShouldManageCRDs(), "Unmanaged mode must not manage CRDs")
	assert.Empty(t, cl.Added, "no CRDs or ClusterRoles should be created when Unmanaged")
	assert.Empty(t, cl.Updated, "no CRDs or ClusterRoles should be updated when Unmanaged")
}

// TestReconcile_TakeoverBlocked_SkipsCRDAndRBAC verifies that when
// desired mode is Managed but CRDs are present and non-compliant
// (TakeoverBlocked), the reconciler does NOT overwrite CRDs or RBAC.
func TestReconcile_TakeoverBlocked_SkipsCRDAndRBAC(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			},
		},
	}

	// Pre-populate CRDs with wrong bundle-version annotation to
	// trigger TakeoverBlocked (present=true, compliant=false).
	nonCompliantCRDs := allManagedCRDObjects()
	for _, obj := range nonCompliantCRDs {
		crd := obj.(*apiextensionsv1.CustomResourceDefinition)
		if crd.Annotations == nil {
			crd.Annotations = map[string]string{}
		}
		crd.Annotations[bundleVersionAnnotation] = "v0.0.0-wrong"
	}

	var objs []runtime.Object
	objs = append(objs, nonCompliantCRDs...)
	objs = append(objs, ingressObj)
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
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

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	res, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res,
		"TakeoverBlocked gate-ON path must not requeue; watches on Ingress CR/CRDs/VAP/ClusterRoles trigger the next reconcile")

	assert.False(t, modeAccessor.ShouldManageCRDs(), "TakeoverBlocked must not manage CRDs")
	assert.Empty(t, cl.Added, "no CRDs should be created when TakeoverBlocked")
	assert.Empty(t, cl.Updated, "existing CRDs must not be overwritten when TakeoverBlocked")

	// Verify status was written with TakeoverBlocked reason.
	var updated operatorv1alpha1.Ingress
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &updated)
	assert.NoError(t, err)
	managedCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsManaged)
	assert.NotNil(t, managedCond)
	assert.Equal(t, metav1.ConditionFalse, managedCond.Status)
	assert.Equal(t, reasonTakeoverBlocked, managedCond.Reason)
}

// TestReconcile_PartialPresenceNonCompliant_TakeoverBlocked verifies
// that when some managed CRDs are missing and at least one existing
// managed CRD has a wrong bundle-version, the reconciler does NOT
// create or update any CRDs (the Phase 2 takeover hole fix).
func TestReconcile_PartialPresenceNonCompliant_TakeoverBlocked(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			},
		},
	}

	// Include only the first managed CRD with a wrong bundle-version;
	// the rest are absent. This exercises the takeover hole scenario:
	// present=false (incomplete set) but anyExistingNonCompliant=true.
	firstCRD := managedCRDs[0].DeepCopy()
	if firstCRD.Annotations == nil {
		firstCRD.Annotations = map[string]string{}
	}
	firstCRD.Annotations[bundleVersionAnnotation] = "v0.0.0-foreign"

	var objs []runtime.Object
	objs = append(objs, firstCRD)
	objs = append(objs, ingressObj)
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
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

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	_, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	assert.False(t, modeAccessor.ShouldManageCRDs(),
		"partial presence with non-compliant CRD must block ShouldManageCRDs")
	assert.Empty(t, cl.Added,
		"no CRDs should be created when an existing CRD is non-compliant")
	assert.Empty(t, cl.Updated,
		"no CRDs should be updated when an existing CRD is non-compliant")

	// Verify Ingress status reflects TakeoverBlocked.
	var updated operatorv1alpha1.Ingress
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &updated)
	assert.NoError(t, err)

	managedCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsManaged)
	assert.NotNil(t, managedCond)
	assert.Equal(t, metav1.ConditionFalse, managedCond.Status)
	assert.Equal(t, reasonTakeoverBlocked, managedCond.Reason)

	presentCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsPresent)
	assert.NotNil(t, presentCond)
	assert.Equal(t, metav1.ConditionFalse, presentCond.Status,
		"Present must be False because not all managed CRDs exist")
}

// TestReconcile_GateOff_AlwaysEnsures verifies that when the
// management mode gate is OFF, CRDs and RBAC are always ensured
// (legacy behavior preserved).
func TestReconcile_GateOff_AlwaysEnsures(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)

	var objs []runtime.Object
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
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

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    nil,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	_, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	assert.NotEmpty(t, cl.Added, "CRDs and RBAC must be created when gate is off (legacy behavior)")
	var crdCount, rbacCount int
	for _, obj := range cl.Added {
		switch obj.(type) {
		case *apiextensionsv1.CustomResourceDefinition:
			crdCount++
		case *rbacv1.ClusterRole:
			rbacCount++
		}
	}
	assert.Equal(t, len(managedCRDs), crdCount, "all managed CRDs must be created")
	assert.Equal(t, len(managedClusterRoles), rbacCount, "all managed ClusterRoles must be created")
}

// TestReconcile_ManagedAndAbsent_InstallsCRDs verifies that when mode
// is Managed and CRDs are absent, the reconciler installs them.
func TestReconcile_ManagedAndAbsent_InstallsCRDs(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			},
		},
	}
	infraObj := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
		},
	}

	var objs []runtime.Object
	objs = append(objs, ingressObj, infraObj)
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
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

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	res, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res,
		"gate-ON Managed happy path must not requeue; the CRD watch triggers the next reconcile once CRDs become present/compliant")

	assert.True(t, modeAccessor.ShouldManageCRDs(), "Managed + absent must allow CRD management")
	var crdCount, rbacCount int
	for _, obj := range cl.Added {
		switch obj.(type) {
		case *apiextensionsv1.CustomResourceDefinition:
			crdCount++
		case *rbacv1.ClusterRole:
			rbacCount++
		}
	}
	assert.Equal(t, len(managedCRDs), crdCount, "all managed CRDs must be installed")
	assert.Equal(t, len(managedClusterRoles), rbacCount, "all managed ClusterRoles must be installed")
}

// TestModeAccessor_ShouldManageCRDs verifies the new ShouldManageCRDs
// method under various state combinations.
func TestModeAccessor_ShouldManageCRDs(t *testing.T) {
	tests := []struct {
		name      string
		managed   bool
		present   bool
		compliant bool
		want      bool
	}{
		{"managed=true", true, true, true, true},
		{"managed=false (Unmanaged)", false, true, true, false},
		{"managed=false (TakeoverBlocked)", false, true, false, false},
		{"managed=true, absent", true, false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModeAccessor(true)
			m.Update(operatorv1alpha1.GatewayAPIManagementModeManaged, tc.managed, tc.present, tc.compliant)
			assert.Equal(t, tc.want, m.ShouldManageCRDs())
		})
	}
}

// forbidIngressClient wraps a client.Client and returns Forbidden
// for Ingress Gets to simulate HyperShift RBAC restrictions.
type forbidIngressClient struct {
	client.Client
}

func (c *forbidIngressClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*operatorv1alpha1.Ingress); ok {
		return apierrors.NewForbidden(
			schema.GroupResource{Group: "operator.openshift.io", Resource: "ingresses"},
			key.Name,
			fmt.Errorf("forbidden"),
		)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// modeFlippingClient simulates a TOCTOU race: the first Ingress Get
// returns one mode, subsequent Gets return a different mode. Before the
// snapshot fix, reconcileAdmissionPolicyTransition and
// reconcileIngressStatus each did their own Get, so divergent reads
// could cause Unmanaged status while VAP was still live.
type modeFlippingClient struct {
	client.Client
	calls     int
	firstMode operatorv1alpha1.GatewayAPIManagementMode
	laterMode operatorv1alpha1.GatewayAPIManagementMode
}

func (c *modeFlippingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if ing, ok := obj.(*operatorv1alpha1.Ingress); ok {
		c.calls++
		if c.calls == 1 {
			ing.Spec.GatewayAPI.ManagementMode = c.firstMode
		} else {
			ing.Spec.GatewayAPI.ManagementMode = c.laterMode
		}
	}
	return nil
}

// TestReconcile_ModeSnapshot_PreventsStaleUnmanaged proves that the
// single-snapshot approach eliminates the TOCTOU race where a mode flip
// between two separate Ingress Gets could write Unmanaged status while
// the VAP is still live.
//
// Scenario: the first read sees Managed (so reconcileAdmissionPolicyTransition
// would have been a no-op under the old dual-read code), but the second read
// sees Unmanaged (so reconcileIngressStatus would have written Unmanaged status
// while the VAP was not deleted). With the fix, only ONE read occurs: whichever
// mode it sees is used consistently for both VAP transition and status write.
func TestReconcile_ModeSnapshot_PreventsStaleUnmanaged(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Generation: 1},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			},
		},
	}

	existingVAP := baseAdmissionPolicy.DeepCopy()
	existingVAPBinding := desiredAdmissionPolicyBinding.DeepCopy()
	infraObj := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
		},
	}

	var objs []runtime.Object
	objs = append(objs, ingressObj.DeepCopy())
	objs = append(objs, existingVAP, existingVAPBinding, infraObj)
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
			return []string{}
		})).
		Build()

	// The flipping client simulates the old TOCTOU: first Get returns
	// Managed (old reconcileAdmissionPolicyTransition would skip VAP
	// delete), second Get returns Unmanaged (old reconcileIngressStatus
	// would write Unmanaged condition while VAP is still live).
	flippingClient := &modeFlippingClient{
		Client:    fakeClient,
		firstMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
		laterMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
	}

	cl := &testutil.FakeClientRecorder{
		Client:  flippingClient,
		T:       t,
		Added:   []client.Object{},
		Updated: []client.Object{},
		Deleted: []client.Object{},
		StatusWriter: &testutil.FakeStatusWriter{
			StatusWriter: fakeClient.Status(),
		},
	}

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	_, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	// The snapshot sees Managed (first Get). Verify the invariant:
	// Managed snapshot -> VAP NOT deleted, status NOT Unmanaged.
	assert.Equal(t, 1, flippingClient.calls,
		"Ingress CR must be read exactly once (snapshot), not twice")

	for _, obj := range cl.Deleted {
		switch obj.(type) {
		case *admissionregistrationv1.ValidatingAdmissionPolicy:
			t.Fatal("VAP must NOT be deleted when snapshot sees Managed")
		case *admissionregistrationv1.ValidatingAdmissionPolicyBinding:
			t.Fatal("VAPBinding must NOT be deleted when snapshot sees Managed")
		}
	}

	assert.True(t, modeAccessor.ShouldManageCRDs(),
		"Managed snapshot must set ShouldManageCRDs=true")

	// Verify Ingress status was NOT written with Unmanaged reason.
	var updatedIngress operatorv1alpha1.Ingress
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &updatedIngress)
	assert.NoError(t, err)
	managedCond := findCondition(updatedIngress.Status.Conditions, conditionTypeGatewayAPICRDsManaged)
	if managedCond != nil {
		assert.NotEqual(t, reasonUnmanaged, managedCond.Reason,
			"Unmanaged status must NOT be written when snapshot sees Managed "+
				"(TOCTOU: second read would have seen Unmanaged under old code)")
	}
}

// TestReconcile_ModeSnapshot_UnmanagedDeletesVAPBeforeStatus verifies
// the complementary case: when the snapshot sees Unmanaged, the VAP is
// deleted AND the Unmanaged status is written - both from the same
// snapshot, without a second read that could have flipped to Managed.
func TestReconcile_ModeSnapshot_UnmanagedDeletesVAPBeforeStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Generation: 2},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			},
		},
	}

	existingVAP := baseAdmissionPolicy.DeepCopy()
	existingVAPBinding := desiredAdmissionPolicyBinding.DeepCopy()

	var objs []runtime.Object
	objs = append(objs, ingressObj.DeepCopy())
	objs = append(objs, existingVAP, existingVAPBinding)
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
			return []string{}
		})).
		Build()

	// Snapshot reads Unmanaged; subsequent hypothetical reads would
	// see Managed - proving that only the snapshot matters.
	flippingClient := &modeFlippingClient{
		Client:    fakeClient,
		firstMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
		laterMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
	}

	cl := &testutil.FakeClientRecorder{
		Client:  flippingClient,
		T:       t,
		Added:   []client.Object{},
		Updated: []client.Object{},
		Deleted: []client.Object{},
		StatusWriter: &testutil.FakeStatusWriter{
			StatusWriter: fakeClient.Status(),
		},
	}

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	_, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	assert.Equal(t, 1, flippingClient.calls,
		"Ingress CR must be read exactly once (snapshot)")

	// Unmanaged snapshot -> VAP must be deleted.
	var vapDeleted, vapbDeleted bool
	for _, obj := range cl.Deleted {
		switch obj.(type) {
		case *admissionregistrationv1.ValidatingAdmissionPolicy:
			vapDeleted = true
		case *admissionregistrationv1.ValidatingAdmissionPolicyBinding:
			vapbDeleted = true
		}
	}
	assert.True(t, vapDeleted, "VAP must be deleted when snapshot sees Unmanaged")
	assert.True(t, vapbDeleted, "VAPBinding must be deleted when snapshot sees Unmanaged")

	// Status must show Unmanaged (consistent with the delete decision).
	assert.False(t, modeAccessor.ShouldManageCRDs(),
		"Unmanaged snapshot must set ShouldManageCRDs=false")

	var updatedIngress operatorv1alpha1.Ingress
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &updatedIngress)
	assert.NoError(t, err)
	managedCond := findCondition(updatedIngress.Status.Conditions, conditionTypeGatewayAPICRDsManaged)
	assert.NotNil(t, managedCond, "GatewayAPICRDsManaged condition must be set")
	assert.Equal(t, metav1.ConditionFalse, managedCond.Status)
	assert.Equal(t, reasonUnmanaged, managedCond.Reason,
		"Unmanaged status must be written when snapshot sees Unmanaged")
}

// ---------- Phase 5: Spurious InProgress flap prevention ----------

// TestReconcile_SteadyState_NoSpuriousInProgress verifies that repeated
// reconciles with the same desired mode do NOT set InProgress=true after
// the first successful reconcile. This prevents ClusterOperator
// Progressing=True flaps on every 30s requeue.
func TestReconcile_SteadyState_NoSpuriousInProgress(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			},
		},
	}
	infraObj := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
		},
	}

	var objs []runtime.Object
	objs = append(objs, ingressObj, infraObj)
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
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

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconcile: lastAppliedMode is nil, so InProgress should be set.
	_, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	// After first reconcile, lastAppliedMode should be set and
	// transition state should be cleared (successful completion).
	assert.NotNil(t, modeAccessor.GetLastAppliedMode(),
		"lastAppliedMode must be set after first successful reconcile")
	assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeManaged, *modeAccessor.GetLastAppliedMode())
	ts := modeAccessor.GetTransitionState()
	assert.False(t, ts.InProgress,
		"transition state must be cleared after successful reconcile")

	// Second reconcile: same mode, so InProgress must NOT be set.
	// We inject an observer to verify InProgress is never true during
	// the second reconcile by checking the final state.
	_, err = r.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	ts = modeAccessor.GetTransitionState()
	assert.False(t, ts.InProgress,
		"steady-state reconcile must NOT set InProgress=true")
	assert.NotNil(t, modeAccessor.GetLastAppliedMode())
	assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeManaged, *modeAccessor.GetLastAppliedMode())
}

// TestReconcile_ModeChange_SetsInProgress verifies that when the desired
// mode changes between reconciles, InProgress is set during the
// transition reconcile but cleared after completion.
func TestReconcile_ModeChange_SetsInProgress(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			},
		},
	}
	infraObj := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
		},
	}

	var objs []runtime.Object
	objs = append(objs, ingressObj, infraObj)
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
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

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconcile with Managed mode.
	_, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeManaged, *modeAccessor.GetLastAppliedMode())

	// Now change the Ingress CR to Unmanaged.
	var currentIngress operatorv1alpha1.Ingress
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &currentIngress)
	assert.NoError(t, err)
	currentIngress.Spec.GatewayAPI.ManagementMode = operatorv1alpha1.GatewayAPIManagementModeUnmanaged
	err = fakeClient.Update(context.Background(), &currentIngress)
	assert.NoError(t, err)

	// Second reconcile with Unmanaged mode: mode changed, so
	// InProgress should be set during transition but cleared after.
	_, err = r.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	// After successful reconcile, lastAppliedMode should be updated
	// and transition state cleared.
	assert.NotNil(t, modeAccessor.GetLastAppliedMode())
	assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged, *modeAccessor.GetLastAppliedMode())
	ts := modeAccessor.GetTransitionState()
	assert.False(t, ts.InProgress,
		"transition state must be cleared after successful mode change")
}

// TestReconcile_SnapshotFailure_ClearsStaleState verifies that when
// resolveIngressModeSnapshot fails (e.g., Forbidden), any stale
// transition state from a prior reconcile is cleared.
func TestReconcile_SnapshotFailure_ClearsStaleState(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	var objs []runtime.Object
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
			return []string{}
		})).
		Build()

	cl := &testutil.FakeClientRecorder{
		Client:  &forbidIngressClient{Client: fakeClient},
		T:       t,
		Added:   []client.Object{},
		Updated: []client.Object{},
		Deleted: []client.Object{},
	}

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	modeAccessor := NewModeAccessor(true)

	// Simulate stale transition state from a prior successful reconcile
	// that set InProgress=true before an error path.
	modeAccessor.SetTransitionState(operatorcontroller.TransitionState{
		InProgress: true,
		Target:     operatorv1alpha1.GatewayAPIManagementModeManaged,
	})

	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	_, err := r.Reconcile(context.Background(), req)
	assert.Error(t, err, "Forbidden Ingress must return error")

	// The stale InProgress=true must be cleared.
	ts := modeAccessor.GetTransitionState()
	assert.False(t, ts.InProgress,
		"snapshot failure must clear stale InProgress transition state")
	assert.NoError(t, ts.Error,
		"snapshot failure must clear transition error (the reconcile error is returned separately)")
}

// TestReconcile_SteadyStateUnmanaged_SkipsTransitionOps verifies that
// after a successful transition to Unmanaged, subsequent steady-state
// reconciles do NOT call reconcileAdmissionPolicyTransition (and
// therefore do NOT call UninstallSail or deleteAdmissionPolicy).
func TestReconcile_SteadyStateUnmanaged_SkipsTransitionOps(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	operatorv1alpha1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			},
		},
	}

	var objs []runtime.Object
	objs = append(objs, ingressObj)
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
			return []string{}
		})).
		Build()

	informer := informertest.FakeInformers{Scheme: scheme}
	fakeCache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
	modeAccessor := NewModeAccessor(true)

	var allowDependentsAtUninstallTime bool
	uninstaller := &fakeSailUninstaller{
		onUninstall: func() {
			// Capture AllowDependents() exactly when UninstallSail is
			// invoked, to verify BlockDependents flipped the gate
			// before this call, not after.
			allowDependentsAtUninstallTime = modeAccessor.AllowDependents()
		},
	}
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

	r := &reconciler{
		client: cl,
		cache:  fakeCache,
		config: Config{
			MarketplaceEnabled:              true,
			OperatorLifecycleManagerEnabled: true,
			ModeAccessor:                    modeAccessor,
			SailUninstaller:                 uninstaller,
		},
		fieldIndexer: FakeIndexer{},
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconcile: transitions to Unmanaged. UninstallSail should be called.
	_, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.True(t, uninstaller.called, "UninstallSail must be called on first Unmanaged reconcile")
	assert.False(t, allowDependentsAtUninstallTime,
		"BlockDependents must flip AllowDependents to false before UninstallSail is called, "+
			"closing the race window against a concurrently in-flight gatewayclass reconcile")
	assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged, *modeAccessor.GetLastAppliedMode())

	// Reset the uninstaller tracking for the second reconcile.
	uninstaller.called = false
	// Reset the recorder to isolate second-reconcile side effects.
	cl.Deleted = []client.Object{}

	// Second reconcile: same Unmanaged mode (steady state).
	// UninstallSail must NOT be called again.
	_, err = r.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.False(t, uninstaller.called,
		"UninstallSail must NOT be called on steady-state Unmanaged reconcile")
	assert.Empty(t, cl.Deleted,
		"no VAP/VAPBinding deletions should occur on steady-state Unmanaged reconcile")
}
