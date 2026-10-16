package gatewayapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
)

// TestDesiredAdmissionPolicyForTopology verifies that the desired VAP
// shape varies based on the skipPodBound flag (set for External
// topology or IBMCloud platform).
func TestDesiredAdmissionPolicyForTopology(t *testing.T) {
	tests := []struct {
		name                string
		skipPodBound        bool
		wantValidationCount int
		wantPodBound        bool
	}{
		{
			name:                "pod-bound appended when skipPodBound is false",
			skipPodBound:        false,
			wantValidationCount: 2,
			wantPodBound:        true,
		},
		{
			name:                "SA-only when skipPodBound is true",
			skipPodBound:        true,
			wantValidationCount: 1,
			wantPodBound:        false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vap := desiredAdmissionPolicyForTopology(tc.skipPodBound)

			assert.Equal(t, baseAdmissionPolicy.Name, vap.Name,
				"name must match base asset")
			assert.Len(t, vap.Spec.Validations, tc.wantValidationCount,
				"validation count mismatch")

			assert.Contains(t, vap.Spec.Validations[0].Expression,
				"request.userInfo.username",
				"first validation must be the SA check")

			if tc.wantPodBound {
				assert.Contains(t, vap.Spec.Validations[1].Expression,
					"authentication.kubernetes.io/node-name",
					"second validation must be the pod-bound check")
				assert.Contains(t, vap.Spec.Validations[1].Expression,
					"authentication.kubernetes.io/pod-name",
					"second validation must check pod-name claim")
			}
		})
	}
}

// TestDesiredAdmissionPolicyForTopology_DoesNotMutateBase verifies
// that calling desiredAdmissionPolicyForTopology does not mutate the
// package-level baseAdmissionPolicy.
func TestDesiredAdmissionPolicyForTopology_DoesNotMutateBase(t *testing.T) {
	origLen := len(baseAdmissionPolicy.Spec.Validations)

	_ = desiredAdmissionPolicyForTopology(false)
	_ = desiredAdmissionPolicyForTopology(false)

	assert.Len(t, baseAdmissionPolicy.Spec.Validations, origLen,
		"baseAdmissionPolicy must not be mutated by desiredAdmissionPolicyForTopology")
}

// TestReconcile_Managed_CreatesVAP verifies that when the mode is Managed, the
// reconciler creates the ValidatingAdmissionPolicy and its binding.
func TestReconcile_Managed_CreatesVAP(t *testing.T) {
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
	modeAccessor := operatorcontroller.NewGatewayAPIModeAccessor(true)
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

	assert.True(t, modeAccessor.ShouldManageCRDs(), "Managed mode must allow CRD management")

	var vapCreated, vapbCreated bool
	for _, obj := range cl.Added {
		switch obj.(type) {
		case *admissionregistrationv1.ValidatingAdmissionPolicy:
			vapCreated = true
		case *admissionregistrationv1.ValidatingAdmissionPolicyBinding:
			vapbCreated = true
		}
	}
	assert.True(t, vapCreated, "ValidatingAdmissionPolicy must be created in Managed mode")
	assert.True(t, vapbCreated, "ValidatingAdmissionPolicyBinding must be created in Managed mode")
}

// TestReconcile_Managed_InfraHA_CreatesPodBoundVAP verifies
// that when Infrastructure has HighlyAvailable topology, the created
// VAP includes the pod-bound validation.
func TestReconcile_Managed_InfraHA_CreatesPodBoundVAP(t *testing.T) {
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
	modeAccessor := operatorcontroller.NewGatewayAPIModeAccessor(true)
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

	var createdVAP *admissionregistrationv1.ValidatingAdmissionPolicy
	for _, obj := range cl.Added {
		if vap, ok := obj.(*admissionregistrationv1.ValidatingAdmissionPolicy); ok {
			createdVAP = vap
		}
	}
	require.NotNil(t, createdVAP, "VAP must be created")
	assert.Len(t, createdVAP.Spec.Validations, 2,
		"HA topology must include pod-bound validation")
	assert.Contains(t, createdVAP.Spec.Validations[1].Expression,
		"authentication.kubernetes.io/node-name",
		"second validation must be the pod-bound check")
}

// TestReconcile_Managed_InfraExternal_CreatesSAOnlyVAP verifies
// that when Infrastructure has External topology, the created VAP
// includes only the SA validation (no pod-bound).
func TestReconcile_Managed_InfraExternal_CreatesSAOnlyVAP(t *testing.T) {
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
			ControlPlaneTopology: configv1.ExternalTopologyMode,
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
	modeAccessor := operatorcontroller.NewGatewayAPIModeAccessor(true)
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

	var createdVAP *admissionregistrationv1.ValidatingAdmissionPolicy
	for _, obj := range cl.Added {
		if vap, ok := obj.(*admissionregistrationv1.ValidatingAdmissionPolicy); ok {
			createdVAP = vap
		}
	}
	require.NotNil(t, createdVAP, "VAP must be created")
	assert.Len(t, createdVAP.Spec.Validations, 1,
		"External topology must use SA-only (no pod-bound)")
}

// TestReconcile_Managed_InfraIBMCloud_CreatesSAOnlyVAP verifies
// that when Infrastructure has IBMCloud platform type, the created VAP
// includes only the SA validation (no pod-bound), regardless of
// control-plane topology.
func TestReconcile_Managed_InfraIBMCloud_CreatesSAOnlyVAP(t *testing.T) {
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
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.IBMCloudPlatformType,
			},
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
	modeAccessor := operatorcontroller.NewGatewayAPIModeAccessor(true)
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

	var createdVAP *admissionregistrationv1.ValidatingAdmissionPolicy
	for _, obj := range cl.Added {
		if vap, ok := obj.(*admissionregistrationv1.ValidatingAdmissionPolicy); ok {
			createdVAP = vap
		}
	}
	require.NotNil(t, createdVAP, "VAP must be created")
	assert.Len(t, createdVAP.Spec.Validations, 1,
		"IBMCloud platform must use SA-only VAP (no pod-bound)")
}

// TestReconcile_Unmanaged_DeletesVAP verifies that when
// transitioning to Unmanaged mode, the VAP and binding are deleted
// before the Managed=False/Unmanaged status condition is written.
func TestReconcile_Unmanaged_DeletesVAP(t *testing.T) {
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

	existingVAP := baseAdmissionPolicy.DeepCopy()
	existingVAPBinding := desiredAdmissionPolicyBinding.DeepCopy()

	var objs []runtime.Object
	objs = append(objs, ingressObj)
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
	modeAccessor := operatorcontroller.NewGatewayAPIModeAccessor(true)
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

	// Verify VAP was deleted.
	var vapDeleted, vapbDeleted bool
	for _, obj := range cl.Deleted {
		switch obj.(type) {
		case *admissionregistrationv1.ValidatingAdmissionPolicy:
			vapDeleted = true
		case *admissionregistrationv1.ValidatingAdmissionPolicyBinding:
			vapbDeleted = true
		}
	}
	assert.True(t, vapDeleted, "ValidatingAdmissionPolicy must be deleted when transitioning to Unmanaged")
	assert.True(t, vapbDeleted, "ValidatingAdmissionPolicyBinding must be deleted when transitioning to Unmanaged")

	// No new VAP should have been created.
	for _, obj := range cl.Added {
		switch obj.(type) {
		case *admissionregistrationv1.ValidatingAdmissionPolicy:
			t.Fatal("VAP must NOT be created when mode is Unmanaged")
		case *admissionregistrationv1.ValidatingAdmissionPolicyBinding:
			t.Fatal("VAPBinding must NOT be created when mode is Unmanaged")
		}
	}

	// Verify Ingress status shows Unmanaged (transition complete since delete succeeded).
	var updated operatorv1alpha1.Ingress
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &updated)
	require.NoError(t, err)
	managedCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsManaged)
	require.NotNil(t, managedCond, "GatewayAPICRDsManaged condition must be set")
	assert.Equal(t, metav1.ConditionFalse, managedCond.Status)
	assert.Equal(t, reasonUnmanaged, managedCond.Reason)
}

// TestReconcile_Unmanaged_VAPDeleteFails_ReturnsError verifies
// that when the VAP delete fails during Unmanaged transition, the
// reconciler returns an error (triggering retry) and does NOT write
// the Unmanaged condition.
func TestReconcile_Unmanaged_VAPDeleteFails_ReturnsError(t *testing.T) {
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

	existingVAP := baseAdmissionPolicy.DeepCopy()

	var objs []runtime.Object
	objs = append(objs, ingressObj, existingVAP)
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
		WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, client.IndexerFunc(func(o client.Object) []string {
			return []string{}
		})).
		Build()

	// Wrap with a client that fails on VAP deletion.
	errClient := &vapDeleteErrorClient{Client: fakeClient}
	cl := &testutil.FakeClientRecorder{
		Client:  errClient,
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
	modeAccessor := operatorcontroller.NewGatewayAPIModeAccessor(true)
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
	assert.Error(t, err, "reconcile must return error when VAP delete fails during Unmanaged transition")
	assert.Contains(t, err.Error(), "cannot complete transition to Unmanaged")

	// Verify that the Ingress status was NOT written with Unmanaged
	// (reconcileIngressStatus was never reached).
	var ingress operatorv1alpha1.Ingress
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &ingress)
	require.NoError(t, err)
	managedCond := findCondition(ingress.Status.Conditions, conditionTypeGatewayAPICRDsManaged)
	if managedCond != nil {
		assert.NotEqual(t, reasonUnmanaged, managedCond.Reason,
			"Unmanaged condition must NOT be written until VAP delete succeeds")
	}
}

// TestReconcile_AdoptsExistingVAP verifies that reconciliation
// updates an existing policy in place without deleting the policy or binding.
func TestReconcile_AdoptsExistingVAP(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	var objs []runtime.Object
	objs = append(objs, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})
	objs = append(objs, baseAdmissionPolicy.DeepCopy(), desiredAdmissionPolicyBinding.DeepCopy())
	objs = append(objs, &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}})

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
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	_, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	for _, obj := range cl.Added {
		switch obj.(type) {
		case *admissionregistrationv1.ValidatingAdmissionPolicy:
			t.Fatal("existing VAP must not be recreated")
		case *admissionregistrationv1.ValidatingAdmissionPolicyBinding:
			t.Fatal("existing VAPBinding must not be recreated")
		}
	}
	for _, obj := range cl.Deleted {
		switch obj.(type) {
		case *admissionregistrationv1.ValidatingAdmissionPolicy:
			t.Fatal("existing VAP must not be deleted")
		case *admissionregistrationv1.ValidatingAdmissionPolicyBinding:
			t.Fatal("existing VAPBinding must not be deleted")
		}
	}
	assert.Len(t, cl.Updated, 1, "existing policy must be updated in place")
	assert.IsType(t, &admissionregistrationv1.ValidatingAdmissionPolicy{}, cl.Updated[0])
	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	assert.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: baseAdmissionPolicy.Name}, policy))
	assert.Equal(t, desiredAdmissionPolicyForTopology(false).Spec, policy.Spec)
}

// TestEnsureAdmissionPolicy verifies the ensure path creates both VAP
// and binding when they don't exist, and does not update when up-to-date.
func TestEnsureAdmissionPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	t.Run("fails when Infrastructure is absent", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		err := r.ensureAdmissionPolicy(context.Background())
		assert.Error(t, err, "must fail when Infrastructure/cluster is unavailable")
		assert.Contains(t, err.Error(), "Infrastructure/cluster")
		assert.Empty(t, cl.Added, "nothing should be created when infra get fails")
	})

	t.Run("creates both when absent, External infra means SA-only", func(t *testing.T) {
		infraObj := &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.ExternalTopologyMode,
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(infraObj).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		err := r.ensureAdmissionPolicy(context.Background())
		assert.NoError(t, err)
		assert.Len(t, cl.Added, 2, "both VAP and binding should be created")

		for _, obj := range cl.Added {
			if vap, ok := obj.(*admissionregistrationv1.ValidatingAdmissionPolicy); ok {
				assert.Len(t, vap.Spec.Validations, 1,
					"External infra must produce SA-only VAP")
			}
		}
	})

	t.Run("creates SA-only when IBMCloud platform with HA topology", func(t *testing.T) {
		infraObj := &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.IBMCloudPlatformType,
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(infraObj).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		err := r.ensureAdmissionPolicy(context.Background())
		assert.NoError(t, err)
		assert.Len(t, cl.Added, 2, "both VAP and binding should be created")

		for _, obj := range cl.Added {
			if vap, ok := obj.(*admissionregistrationv1.ValidatingAdmissionPolicy); ok {
				assert.Len(t, vap.Spec.Validations, 1,
					"IBMCloud platform must produce SA-only VAP")
			}
		}
	})

	t.Run("creates with pod-bound when HA infra present", func(t *testing.T) {
		infraObj := &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(infraObj).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		err := r.ensureAdmissionPolicy(context.Background())
		assert.NoError(t, err)
		assert.Len(t, cl.Added, 2, "both VAP and binding should be created")

		for _, obj := range cl.Added {
			if vap, ok := obj.(*admissionregistrationv1.ValidatingAdmissionPolicy); ok {
				assert.Len(t, vap.Spec.Validations, 2,
					"HA infra must produce VAP with pod-bound validation")
			}
		}
	})

	t.Run("no-op when up-to-date SA-only (External infra)", func(t *testing.T) {
		existingVAP := baseAdmissionPolicy.DeepCopy()
		existingBinding := desiredAdmissionPolicyBinding.DeepCopy()
		infraObj := &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.ExternalTopologyMode,
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(existingVAP, existingBinding, infraObj).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		err := r.ensureAdmissionPolicy(context.Background())
		assert.NoError(t, err)
		assert.Empty(t, cl.Added, "nothing should be created when up-to-date")
		assert.Empty(t, cl.Updated, "nothing should be updated when up-to-date")
	})

	t.Run("no-op when up-to-date with pod-bound (HA infra)", func(t *testing.T) {
		existingVAP := desiredAdmissionPolicyForTopology(false)
		existingBinding := desiredAdmissionPolicyBinding.DeepCopy()
		infraObj := &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(existingVAP, existingBinding, infraObj).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		err := r.ensureAdmissionPolicy(context.Background())
		assert.NoError(t, err)
		assert.Empty(t, cl.Added, "nothing should be created when up-to-date")
		assert.Empty(t, cl.Updated, "nothing should be updated when up-to-date")
	})

	t.Run("updates VAP when topology changes from External to HA", func(t *testing.T) {
		existingVAP := baseAdmissionPolicy.DeepCopy()
		existingBinding := desiredAdmissionPolicyBinding.DeepCopy()
		infraObj := &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(existingVAP, existingBinding, infraObj).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		err := r.ensureAdmissionPolicy(context.Background())
		assert.NoError(t, err)
		assert.Empty(t, cl.Added, "should not create when already exists")
		assert.Len(t, cl.Updated, 1, "should update VAP when topology changed")

		updatedVAP, ok := cl.Updated[0].(*admissionregistrationv1.ValidatingAdmissionPolicy)
		require.True(t, ok, "updated object must be a ValidatingAdmissionPolicy")
		assert.Len(t, updatedVAP.Spec.Validations, 2,
			"updated VAP must include pod-bound validation for HA")
	})

	t.Run("CVO metadata cleanup", func(t *testing.T) {
		core, observed := observer.New(zap.InfoLevel)
		previousLog := log
		log = zapr.NewLogger(zap.New(core))
		t.Cleanup(func() { log = previousLog })
		for _, tc := range []struct {
			name          string
			absent        bool
			metadataDrift bool
			ownerDrift    bool
			emptyOwners   bool
			specDrift     bool
			failKind      string
		}{
			{name: "create", absent: true},
			{name: "unrelated metadata is unchanged"},
			{name: "empty owner references are unchanged", emptyOwners: true},
			{name: "owner references only", ownerDrift: true},
			{name: "spec only", specDrift: true},
			{name: "metadata only", metadataDrift: true},
			{name: "spec and metadata", metadataDrift: true, specDrift: true},
			{name: "policy update fails", metadataDrift: true, failKind: "ValidatingAdmissionPolicy"},
			{name: "binding update fails", metadataDrift: true, failKind: "ValidatingAdmissionPolicyBinding"},
			{name: "policy owner cleanup fails", ownerDrift: true, failKind: "ValidatingAdmissionPolicy"},
			{name: "binding owner cleanup fails", ownerDrift: true, failKind: "ValidatingAdmissionPolicyBinding"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				observed.TakeAll()
				policy := baseAdmissionPolicy.DeepCopy()
				binding := desiredAdmissionPolicyBinding.DeepCopy()
				objects := []client.Object{policy, binding}
				retainedMetadata := metav1.ObjectMeta{
					Name: policy.Name,
					Annotations: map[string]string{
						"example.com/retain":                         "value",
						"release.openshift.io/feature-set-extra":     "retain",
						"include.release.openshift.io/other-profile": "retain",
					},
					Labels:     map[string]string{"example.com/retain": "label"},
					Finalizers: []string{"example.com/retain"},
					ManagedFields: []metav1.ManagedFieldsEntry{{
						Manager: "other", Operation: metav1.ManagedFieldsOperationUpdate,
						APIVersion: "admissionregistration.k8s.io/v1", FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{}`)},
					}},
					CreationTimestamp: metav1.NewTime(time.Unix(1, 0)),
				}
				originalMetadata := make([]metav1.ObjectMeta, len(objects))
				for index := range objects {
					metadata := retainedMetadata.DeepCopy()
					metadata.UID = types.UID(fmt.Sprintf("original-%d", index))
					if tc.emptyOwners {
						metadata.OwnerReferences = []metav1.OwnerReference{}
					}
					if tc.ownerDrift || tc.metadataDrift {
						metadata.OwnerReferences = []metav1.OwnerReference{
							{APIVersion: "config.openshift.io/v1", Kind: "ClusterVersion", Name: "other", UID: "other-version"},
							{APIVersion: "other.example.com/v1", Kind: "ClusterVersion", Name: "version", UID: "other-group"},
							{APIVersion: "config.openshift.io/v1", Kind: "Other", Name: "version", UID: "other-kind"},
						}
					}
					if tc.metadataDrift {
						for _, key := range []string{
							"capability.openshift.io/name",
							"include.release.openshift.io/ibm-cloud-managed",
							"include.release.openshift.io/self-managed-high-availability",
							"include.release.openshift.io/single-node-developer",
							"release.openshift.io/feature-set",
						} {
							metadata.Annotations[key] = "payload"
						}
						metadata.OwnerReferences = append(metadata.OwnerReferences, metav1.OwnerReference{
							APIVersion: "config.openshift.io/v1", Kind: "ClusterVersion", Name: "version", UID: "old-cvo",
						})
					}
					originalMetadata[index] = *metadata
				}
				policy.ObjectMeta = originalMetadata[0]
				binding.ObjectMeta = originalMetadata[1]
				if tc.specDrift {
					policy.Spec.Validations = nil
					binding.Spec.ValidationActions = []admissionregistrationv1.ValidationAction{admissionregistrationv1.Warn}
				}
				infra := &configv1.Infrastructure{
					ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
					Status:     configv1.InfrastructureStatus{ControlPlaneTopology: configv1.ExternalTopologyMode},
				}
				builder := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(infra).WithReturnManagedFields()
				if !tc.absent {
					builder.WithObjects(objects...)
				}
				fakeClient := builder.WithInterceptorFuncs(interceptor.Funcs{
					Update: func(ctx context.Context, inner client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						before := obj.DeepCopyObject().(client.Object)
						require.NoError(t, inner.Get(ctx, client.ObjectKeyFromObject(obj), before))
						assert.Equal(t, before.GetManagedFields(), obj.GetManagedFields(), "must preserve managedFields in the update request")
						kind := "ValidatingAdmissionPolicy"
						if _, binding := obj.(*admissionregistrationv1.ValidatingAdmissionPolicyBinding); binding {
							kind = "ValidatingAdmissionPolicyBinding"
						}
						assert.Empty(t, observed.FilterMessage("transitioned "+kind+" from CVO to CIO management").All(), "must not log before the update succeeds")
						if tc.failKind == kind {
							return fmt.Errorf("injected %s update failure", kind)
						}
						return inner.Update(ctx, obj, opts...)
					},
				}).Build()
				cl := &testutil.FakeClientRecorder{Client: fakeClient, T: t}
				r := &reconciler{client: cl}
				err := r.ensureAdmissionPolicy(context.Background())
				if tc.failKind != "" {
					require.ErrorContains(t, err, "injected "+tc.failKind+" update failure")
				} else {
					require.NoError(t, err)
				}
				wantUpdates := 0
				if !tc.absent && (tc.metadataDrift || tc.ownerDrift || tc.specDrift) {
					wantUpdates = 2
					if tc.failKind == "ValidatingAdmissionPolicy" {
						wantUpdates = 1
					}
				}
				assert.Len(t, cl.Updated, wantUpdates, "combine spec and metadata into one update per object")
				assert.Empty(t, cl.Deleted)
				if tc.absent {
					assert.Len(t, cl.Added, 2)
				} else {
					assert.Empty(t, cl.Added)
				}
				name := types.NamespacedName{Name: policy.Name}
				currentPolicy := &admissionregistrationv1.ValidatingAdmissionPolicy{}
				currentBinding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
				require.NoError(t, cl.Get(context.Background(), name, currentPolicy))
				require.NoError(t, cl.Get(context.Background(), name, currentBinding))
				for index, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
					succeeded := tc.failKind == "" || (index == 0 && tc.failKind == "ValidatingAdmissionPolicyBinding")
					entries := observed.FilterMessage("transitioned " + kind + " from CVO to CIO management").All()
					if (tc.metadataDrift || tc.ownerDrift) && succeeded {
						require.Len(t, entries, 1)
						require.Len(t, entries[0].Context, 1)
						assert.Equal(t, "name", entries[0].Context[0].Key)
						assert.Equal(t, name.MarshalLog(), entries[0].Context[0].Interface)
					} else {
						assert.Empty(t, entries)
					}
					metadata := currentPolicy.ObjectMeta
					if index == 1 {
						metadata = currentBinding.ObjectMeta
					}
					if !tc.absent {
						want := originalMetadata[index].DeepCopy()
						if succeeded {
							want = retainedMetadata.DeepCopy()
							want.UID = originalMetadata[index].UID
							want.Generation = originalMetadata[index].Generation
						}
						want.ResourceVersion = metadata.ResourceVersion
						want.ManagedFields = metadata.ManagedFields
						assert.Equal(t, *want, metadata, "preserve identity and all unrelated metadata")
					}
				}
				if tc.failKind == "" {
					assert.Equal(t, baseAdmissionPolicy.Spec, currentPolicy.Spec)
					assert.Equal(t, desiredAdmissionPolicyBinding.Spec, currentBinding.Spec)
					cl.Updated = nil
					cl.Added = nil
					observed.TakeAll()
					require.NoError(t, r.ensureAdmissionPolicy(context.Background()))
					assert.Empty(t, cl.Updated, "repeat ensure must be a no-op")
					assert.Empty(t, cl.Added)
					assert.Empty(t, observed.All())
				}
			})
		}
	})
}

// TestDeleteAdmissionPolicy verifies the delete path removes both VAP
// and binding, and is a no-op when they don't exist.
func TestDeleteAdmissionPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	admissionregistrationv1.AddToScheme(scheme)

	t.Run("deletes both when present", func(t *testing.T) {
		existingVAP := baseAdmissionPolicy.DeepCopy()
		existingBinding := desiredAdmissionPolicyBinding.DeepCopy()
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(existingVAP, existingBinding).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		err := r.deleteAdmissionPolicy(context.Background())
		assert.NoError(t, err)
		assert.Len(t, cl.Deleted, 2, "both VAP and binding should be deleted")
	})

	t.Run("no-op when absent", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		err := r.deleteAdmissionPolicy(context.Background())
		assert.NoError(t, err, "must succeed when resources are already absent")
	})

}

// TestReconcileAdmissionPolicyTransition verifies the pre-reconcile
// transition step that gates Unmanaged status on VAP deletion.
func TestReconcileAdmissionPolicyTransition(t *testing.T) {
	scheme := runtime.NewScheme()
	operatorv1alpha1.Install(scheme)
	admissionregistrationv1.AddToScheme(scheme)

	t.Run("Managed mode does nothing", func(t *testing.T) {
		ingressObj := &operatorv1alpha1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: operatorv1alpha1.IngressSpec{
				GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
					ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(ingressObj).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		snapshot := ingressModeSnapshot{
			desiredMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			ingress:     ingressObj,
			found:       true,
		}
		err := r.reconcileAdmissionPolicyTransition(context.Background(), snapshot)
		assert.NoError(t, err)
		assert.Empty(t, cl.Deleted, "Managed mode must not delete anything")
	})

	t.Run("Unmanaged mode deletes VAP", func(t *testing.T) {
		ingressObj := &operatorv1alpha1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: operatorv1alpha1.IngressSpec{
				GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
					ManagementMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
				},
			},
		}
		existingVAP := baseAdmissionPolicy.DeepCopy()
		existingBinding := desiredAdmissionPolicyBinding.DeepCopy()
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(ingressObj, existingVAP, existingBinding).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		snapshot := ingressModeSnapshot{
			desiredMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			ingress:     ingressObj,
			found:       true,
		}
		err := r.reconcileAdmissionPolicyTransition(context.Background(), snapshot)
		assert.NoError(t, err)
		assert.Len(t, cl.Deleted, 2, "both VAP and binding should be deleted")
	})

	t.Run("Ingress CR not found is no-op (defaults to Managed)", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		snapshot := ingressModeSnapshot{
			desiredMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			ingress:     nil,
			found:       false,
		}
		err := r.reconcileAdmissionPolicyTransition(context.Background(), snapshot)
		assert.NoError(t, err)
		assert.Empty(t, cl.Deleted)
	})

	t.Run("Unmanaged mode calls SailUninstaller before deleting VAP", func(t *testing.T) {
		ingressObj := &operatorv1alpha1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: operatorv1alpha1.IngressSpec{
				GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
					ManagementMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
				},
			},
		}
		existingVAP := baseAdmissionPolicy.DeepCopy()
		existingBinding := desiredAdmissionPolicyBinding.DeepCopy()
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(ingressObj, existingVAP, existingBinding).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		uninstaller := &fakeSailUninstaller{}
		r := &reconciler{
			client: cl,
			config: Config{SailUninstaller: uninstaller},
		}
		snapshot := ingressModeSnapshot{
			desiredMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			ingress:     ingressObj,
			found:       true,
		}
		err := r.reconcileAdmissionPolicyTransition(context.Background(), snapshot)
		assert.NoError(t, err)
		assert.True(t, uninstaller.called, "SailUninstaller.UninstallSail must be called")
		assert.Len(t, cl.Deleted, 2, "both VAP and binding should be deleted after Sail uninstall")
	})

	t.Run("Unmanaged mode propagates SailUninstaller error and skips VAP deletion", func(t *testing.T) {
		ingressObj := &operatorv1alpha1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: operatorv1alpha1.IngressSpec{
				GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
					ManagementMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
				},
			},
		}
		existingVAP := baseAdmissionPolicy.DeepCopy()
		existingBinding := desiredAdmissionPolicyBinding.DeepCopy()
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(ingressObj, existingVAP, existingBinding).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		uninstaller := &fakeSailUninstaller{err: fmt.Errorf("simulated uninstall failure")}
		r := &reconciler{
			client: cl,
			config: Config{SailUninstaller: uninstaller},
		}
		snapshot := ingressModeSnapshot{
			desiredMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			ingress:     ingressObj,
			found:       true,
		}
		err := r.reconcileAdmissionPolicyTransition(context.Background(), snapshot)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Sail uninstall failed")
		assert.True(t, uninstaller.called, "SailUninstaller.UninstallSail must be called even when it fails")
		assert.Empty(t, cl.Deleted, "VAP must not be deleted when Sail uninstall fails")
	})

	t.Run("Unmanaged mode without SailUninstaller still deletes VAP", func(t *testing.T) {
		ingressObj := &operatorv1alpha1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: operatorv1alpha1.IngressSpec{
				GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
					ManagementMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
				},
			},
		}
		existingVAP := baseAdmissionPolicy.DeepCopy()
		existingBinding := desiredAdmissionPolicyBinding.DeepCopy()
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(ingressObj, existingVAP, existingBinding).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		r := &reconciler{client: cl}
		snapshot := ingressModeSnapshot{
			desiredMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			ingress:     ingressObj,
			found:       true,
		}
		err := r.reconcileAdmissionPolicyTransition(context.Background(), snapshot)
		assert.NoError(t, err)
		assert.Len(t, cl.Deleted, 2, "VAP deletion must proceed when SailUninstaller is nil")
	})

	t.Run("Managed mode does not call SailUninstaller", func(t *testing.T) {
		ingressObj := &operatorv1alpha1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: operatorv1alpha1.IngressSpec{
				GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
					ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(ingressObj).
			Build()
		cl := &testutil.FakeClientRecorder{
			Client:  fakeClient,
			T:       t,
			Added:   []client.Object{},
			Updated: []client.Object{},
			Deleted: []client.Object{},
		}
		uninstaller := &fakeSailUninstaller{}
		r := &reconciler{
			client: cl,
			config: Config{SailUninstaller: uninstaller},
		}
		snapshot := ingressModeSnapshot{
			desiredMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			ingress:     ingressObj,
			found:       true,
		}
		err := r.reconcileAdmissionPolicyTransition(context.Background(), snapshot)
		assert.NoError(t, err)
		assert.False(t, uninstaller.called, "SailUninstaller must not be called in Managed mode")
	})
}

// fakeSailUninstaller is a test double for SailUninstaller.
type fakeSailUninstaller struct {
	called bool
	err    error
	// onUninstall, if set, is invoked synchronously before UninstallSail
	// returns, letting tests observe state (e.g. ModeAccessor) exactly
	// at the moment the uninstall happens.
	onUninstall func()
}

func (f *fakeSailUninstaller) UninstallSail(_ context.Context) error {
	f.called = true
	if f.onUninstall != nil {
		f.onUninstall()
	}
	return f.err
}

// TestAdmissionPolicyUpToDate verifies spec comparison logic.
func TestAdmissionPolicyUpToDate(t *testing.T) {
	t.Run("identical SA-only is up-to-date", func(t *testing.T) {
		a := baseAdmissionPolicy.DeepCopy()
		b := baseAdmissionPolicy.DeepCopy()
		assert.True(t, admissionPolicyUpToDate(a, b))
	})

	t.Run("identical with pod-bound is up-to-date", func(t *testing.T) {
		a := desiredAdmissionPolicyForTopology(false)
		b := desiredAdmissionPolicyForTopology(false)
		assert.True(t, admissionPolicyUpToDate(a, b))
	})

	t.Run("different validation expression is not up-to-date", func(t *testing.T) {
		a := baseAdmissionPolicy.DeepCopy()
		b := baseAdmissionPolicy.DeepCopy()
		a.Spec.Validations[0].Expression = "false"
		assert.False(t, admissionPolicyUpToDate(a, b))
	})

	t.Run("different validation count is not up-to-date", func(t *testing.T) {
		a := baseAdmissionPolicy.DeepCopy()
		b := desiredAdmissionPolicyForTopology(false)
		assert.False(t, admissionPolicyUpToDate(a, b),
			"SA-only vs pod-bound must detect count mismatch")
	})

	t.Run("different match condition is not up-to-date", func(t *testing.T) {
		a := baseAdmissionPolicy.DeepCopy()
		b := baseAdmissionPolicy.DeepCopy()
		a.Spec.MatchConditions[0].Expression = "true"
		assert.False(t, admissionPolicyUpToDate(a, b))
	})
}

// vapDeleteErrorClient wraps a client.Client and returns an error
// when deleting ValidatingAdmissionPolicy resources.
type vapDeleteErrorClient struct {
	client.Client
}

func (c *vapDeleteErrorClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*admissionregistrationv1.ValidatingAdmissionPolicyBinding); ok {
		return fmt.Errorf("simulated VAP binding delete failure")
	}
	if _, ok := obj.(*admissionregistrationv1.ValidatingAdmissionPolicy); ok {
		return fmt.Errorf("simulated VAP delete failure")
	}
	return c.Client.Delete(ctx, obj, opts...)
}
