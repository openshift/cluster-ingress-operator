package gatewayapi

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
)

func TestComputeManagedCondition(t *testing.T) {
	tests := []struct {
		name                    string
		desiredMode             operatorv1alpha1.GatewayAPIManagementMode
		present                 bool
		compliant               bool
		anyExistingNonCompliant bool
		wantStatus              metav1.ConditionStatus
		wantReason              string
	}{
		{
			name:                    "managed, present, compliant",
			desiredMode:             operatorv1alpha1.GatewayAPIManagementModeManaged,
			present:                 true,
			compliant:               true,
			anyExistingNonCompliant: false,
			wantStatus:              metav1.ConditionTrue,
			wantReason:              reasonManagedByIngressOperator,
		},
		{
			name:                    "managed, not present, none non-compliant (CIO installing)",
			desiredMode:             operatorv1alpha1.GatewayAPIManagementModeManaged,
			present:                 false,
			compliant:               false,
			anyExistingNonCompliant: false,
			wantStatus:              metav1.ConditionTrue,
			wantReason:              reasonManagedByIngressOperator,
		},
		{
			name:                    "managed, present, not compliant (takeover blocked)",
			desiredMode:             operatorv1alpha1.GatewayAPIManagementModeManaged,
			present:                 true,
			compliant:               false,
			anyExistingNonCompliant: true,
			wantStatus:              metav1.ConditionFalse,
			wantReason:              reasonTakeoverBlocked,
		},
		{
			name:                    "managed, partial presence, some non-compliant (takeover blocked)",
			desiredMode:             operatorv1alpha1.GatewayAPIManagementModeManaged,
			present:                 false,
			compliant:               false,
			anyExistingNonCompliant: true,
			wantStatus:              metav1.ConditionFalse,
			wantReason:              reasonTakeoverBlocked,
		},
		{
			name:                    "unmanaged",
			desiredMode:             operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			present:                 true,
			compliant:               true,
			anyExistingNonCompliant: false,
			wantStatus:              metav1.ConditionFalse,
			wantReason:              reasonUnmanaged,
		},
		{
			name:                    "empty mode defaults to managed",
			desiredMode:             "",
			present:                 true,
			compliant:               true,
			anyExistingNonCompliant: false,
			wantStatus:              metav1.ConditionTrue,
			wantReason:              reasonManagedByIngressOperator,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mode := tc.desiredMode
			if mode == "" {
				mode = operatorv1alpha1.GatewayAPIManagementModeManaged
			}
			cond := computeManagedCondition(mode, tc.present, tc.compliant, tc.anyExistingNonCompliant)
			assert.Equal(t, conditionTypeGatewayAPICRDsManaged, cond.Type)
			assert.Equal(t, tc.wantStatus, cond.Status)
			assert.Equal(t, tc.wantReason, cond.Reason)
		})
	}
}

func TestBuildPresentCondition(t *testing.T) {
	t.Run("all present", func(t *testing.T) {
		cond := buildPresentCondition(true, nil)
		assert.Equal(t, conditionTypeGatewayAPICRDsPresent, cond.Type)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, reasonCRDsFound, cond.Reason)
	})
	t.Run("some missing", func(t *testing.T) {
		cond := buildPresentCondition(false, []string{"foo.gateway.networking.k8s.io"})
		assert.Equal(t, conditionTypeGatewayAPICRDsPresent, cond.Type)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, reasonCRDsNotFound, cond.Reason)
		assert.Contains(t, cond.Message, "foo.gateway.networking.k8s.io")
	})
}

func TestBuildCompliantCondition(t *testing.T) {
	t.Run("all compliant", func(t *testing.T) {
		cond := buildCompliantCondition(true, nil, nil)
		assert.Equal(t, conditionTypeGatewayAPICRDsCompliant, cond.Type)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, reasonVersionMatch, cond.Reason)
	})
	t.Run("annotation mismatch", func(t *testing.T) {
		cond := buildCompliantCondition(false, []string{"foo (expected v1.5.1, got v1.4.0)"}, nil)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, reasonVersionMismatch, cond.Reason)
		assert.Contains(t, cond.Message, "bundle-version annotation mismatch")
		assert.Contains(t, cond.Message, "v1.5.1")
	})
	t.Run("schema mismatch", func(t *testing.T) {
		cond := buildCompliantCondition(false, nil, []string{"bar.gateway.networking.k8s.io"})
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Contains(t, cond.Message, "schema differs despite matching annotation")
		assert.Contains(t, cond.Message, "bar.gateway.networking.k8s.io")
	})
	t.Run("both annotation and schema mismatch", func(t *testing.T) {
		cond := buildCompliantCondition(false,
			[]string{"foo (expected v1.5.1, got v1.4.0)"},
			[]string{"bar.gateway.networking.k8s.io"})
		assert.Contains(t, cond.Message, "bundle-version annotation mismatch")
		assert.Contains(t, cond.Message, "schema differs")
	})
}

func TestCrdSpecCompliant(t *testing.T) {
	base := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "test.gateway.networking.k8s.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "gateway.networking.k8s.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:   "Test",
				Plural: "tests",
			},
		},
	}

	t.Run("identical specs", func(t *testing.T) {
		current := base.DeepCopy()
		desired := base.DeepCopy()
		assert.True(t, crdSpecCompliant(current, desired))
	})

	t.Run("conversion field is ignored", func(t *testing.T) {
		current := base.DeepCopy()
		current.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{
			Strategy: apiextensionsv1.WebhookConverter,
		}
		desired := base.DeepCopy()
		assert.True(t, crdSpecCompliant(current, desired))
	})

	t.Run("spec differs", func(t *testing.T) {
		current := base.DeepCopy()
		current.Spec.Names.Kind = "Different"
		desired := base.DeepCopy()
		assert.False(t, crdSpecCompliant(current, desired))
	})

	t.Run("nil vs empty slices equated", func(t *testing.T) {
		current := base.DeepCopy()
		current.Spec.Versions = nil
		desired := base.DeepCopy()
		desired.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{}
		assert.True(t, crdSpecCompliant(current, desired),
			"nil vs empty slice should be equal (matches crdChanged semantics)")
	})
}

func TestReconcileIngressStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	operatorv1alpha1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)

	ingressCR := func(mode operatorv1alpha1.GatewayAPIManagementMode) *operatorv1alpha1.Ingress {
		return &operatorv1alpha1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: operatorv1alpha1.IngressSpec{
				GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
					ManagementMode: mode,
				},
			},
		}
	}

	tests := []struct {
		name              string
		ingressCR         *operatorv1alpha1.Ingress
		existingCRDs      []runtime.Object
		wantManaged       bool
		wantPresent       bool
		wantCompliant     bool
		wantAllowDeps     bool
		wantManagedReason string
		wantStatusUpdate  bool
	}{
		{
			name:              "managed mode, all CRDs compliant",
			ingressCR:         ingressCR(operatorv1alpha1.GatewayAPIManagementModeManaged),
			existingCRDs:      allManagedCRDObjects(),
			wantManaged:       true,
			wantPresent:       true,
			wantCompliant:     true,
			wantAllowDeps:     true,
			wantManagedReason: reasonManagedByIngressOperator,
			wantStatusUpdate:  true,
		},
		{
			name:              "managed mode, no CRDs present",
			ingressCR:         ingressCR(operatorv1alpha1.GatewayAPIManagementModeManaged),
			existingCRDs:      nil,
			wantManaged:       true,
			wantPresent:       false,
			wantCompliant:     false,
			wantAllowDeps:     false,
			wantManagedReason: reasonManagedByIngressOperator,
			wantStatusUpdate:  true,
		},
		{
			name:              "unmanaged mode",
			ingressCR:         ingressCR(operatorv1alpha1.GatewayAPIManagementModeUnmanaged),
			existingCRDs:      allManagedCRDObjects(),
			wantManaged:       false,
			wantPresent:       true,
			wantCompliant:     true,
			wantAllowDeps:     false,
			wantManagedReason: reasonUnmanaged,
			wantStatusUpdate:  true,
		},
		{
			name:              "no Ingress CR (HyperShift)",
			ingressCR:         nil,
			existingCRDs:      allManagedCRDObjects(),
			wantManaged:       true,
			wantPresent:       true,
			wantCompliant:     true,
			wantAllowDeps:     true,
			wantManagedReason: reasonManagedByIngressOperator,
			wantStatusUpdate:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var objs []runtime.Object
			objs = append(objs, tc.existingCRDs...)
			if tc.ingressCR != nil {
				objs = append(objs, tc.ingressCR)
			}
			var statusSubresources []client.Object
			if tc.ingressCR != nil {
				statusSubresources = append(statusSubresources, tc.ingressCR.DeepCopy())
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(statusSubresources...).
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
			modeAccessor := NewModeAccessor(true)
			r := &reconciler{
				client: cl,
				config: Config{
					ModeAccessor: modeAccessor,
				},
			}

			// Build the snapshot as resolveIngressModeSnapshot would.
			var snapshot ingressModeSnapshot
			if tc.ingressCR != nil {
				mode := tc.ingressCR.Spec.GatewayAPI.ManagementMode
				if mode == "" {
					mode = operatorv1alpha1.GatewayAPIManagementModeManaged
				}
				snapshot = ingressModeSnapshot{
					desiredMode: mode,
					ingress:     tc.ingressCR,
					found:       true,
				}
			} else {
				snapshot = ingressModeSnapshot{
					desiredMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
					ingress:     nil,
					found:       false,
				}
			}

			err := r.reconcileIngressStatus(context.Background(), snapshot)
			require.NoError(t, err)

			assert.Equal(t, tc.wantAllowDeps, modeAccessor.AllowDependents(), "AllowDependents mismatch")

			if tc.wantStatusUpdate {
				assert.NotEmpty(t, cl.StatusWriter.Updated, "expected status update")

				var updated operatorv1alpha1.Ingress
				err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &updated)
				require.NoError(t, err)

				managedCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsManaged)
				require.NotNil(t, managedCond, "GatewayAPICRDsManaged condition missing")
				assert.Equal(t, tc.wantManagedReason, managedCond.Reason)
				if tc.wantManaged {
					assert.Equal(t, metav1.ConditionTrue, managedCond.Status)
				} else {
					assert.Equal(t, metav1.ConditionFalse, managedCond.Status)
				}

				presentCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsPresent)
				require.NotNil(t, presentCond, "GatewayAPICRDsPresent condition missing")
				if tc.wantPresent {
					assert.Equal(t, metav1.ConditionTrue, presentCond.Status)
				} else {
					assert.Equal(t, metav1.ConditionFalse, presentCond.Status)
				}

				compliantCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsCompliant)
				require.NotNil(t, compliantCond, "GatewayAPICRDsCompliant condition missing")
				if tc.wantCompliant {
					assert.Equal(t, metav1.ConditionTrue, compliantCond.Status)
				} else {
					assert.Equal(t, metav1.ConditionFalse, compliantCond.Status)
				}
			} else {
				assert.Empty(t, cl.StatusWriter.Updated, "expected no status update")
			}
		})
	}
}

func TestReconcileIngressStatus_AnnotationMismatch(t *testing.T) {
	scheme := runtime.NewScheme()
	operatorv1alpha1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)

	crds := allManagedCRDObjects()
	firstCRD := crds[0].(*apiextensionsv1.CustomResourceDefinition)
	firstCRD.Annotations[bundleVersionAnnotation] = "v0.0.0-wrong"

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			},
		},
	}

	var objs []runtime.Object
	objs = append(objs, crds...)
	objs = append(objs, ingressObj)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
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
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		config: Config{
			ModeAccessor: modeAccessor,
		},
	}

	snapshot := ingressModeSnapshot{
		desiredMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
		ingress:     ingressObj,
		found:       true,
	}
	err := r.reconcileIngressStatus(context.Background(), snapshot)
	require.NoError(t, err)

	assert.False(t, modeAccessor.AllowDependents(), "should not allow dependents when CRDs non-compliant")

	var updated operatorv1alpha1.Ingress
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &updated)
	require.NoError(t, err)

	managedCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsManaged)
	require.NotNil(t, managedCond)
	assert.Equal(t, metav1.ConditionFalse, managedCond.Status)
	assert.Equal(t, reasonTakeoverBlocked, managedCond.Reason)

	compliantCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsCompliant)
	require.NotNil(t, compliantCond)
	assert.Equal(t, metav1.ConditionFalse, compliantCond.Status)
	assert.Equal(t, reasonVersionMismatch, compliantCond.Reason)
	assert.Contains(t, compliantCond.Message, "bundle-version annotation mismatch")
}

// TestReconcileIngressStatus_PartialPresenceNonCompliant verifies that
// when some CRDs are missing and at least one existing CRD is
// non-compliant, TakeoverBlocked is triggered (the Phase 2 takeover hole).
func TestReconcileIngressStatus_PartialPresenceNonCompliant(t *testing.T) {
	scheme := runtime.NewScheme()
	operatorv1alpha1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)

	// Only include a subset of managed CRDs — one with wrong bundle-version.
	crds := allManagedCRDObjects()
	// Keep only the first CRD but make it non-compliant; skip the rest.
	nonCompliantCRD := crds[0].(*apiextensionsv1.CustomResourceDefinition)
	nonCompliantCRD.Annotations[bundleVersionAnnotation] = "v0.0.0-foreign"

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			},
		},
	}

	var objs []runtime.Object
	objs = append(objs, nonCompliantCRD)
	objs = append(objs, ingressObj)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
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
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		config: Config{
			ModeAccessor: modeAccessor,
		},
	}

	snapshot := ingressModeSnapshot{
		desiredMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
		ingress:     ingressObj,
		found:       true,
	}
	err := r.reconcileIngressStatus(context.Background(), snapshot)
	require.NoError(t, err)

	assert.False(t, modeAccessor.ShouldManageCRDs(), "partial presence with non-compliant CRD must block management")
	assert.False(t, modeAccessor.AllowDependents(), "must not allow dependents when takeover is blocked")

	var updated operatorv1alpha1.Ingress
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &updated)
	require.NoError(t, err)

	managedCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsManaged)
	require.NotNil(t, managedCond)
	assert.Equal(t, metav1.ConditionFalse, managedCond.Status)
	assert.Equal(t, reasonTakeoverBlocked, managedCond.Reason)

	presentCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsPresent)
	require.NotNil(t, presentCond)
	assert.Equal(t, metav1.ConditionFalse, presentCond.Status, "Present must be False when not all CRDs exist")

	compliantCond := findCondition(updated.Status.Conditions, conditionTypeGatewayAPICRDsCompliant)
	require.NotNil(t, compliantCond)
	assert.Equal(t, metav1.ConditionFalse, compliantCond.Status)
	assert.Contains(t, compliantCond.Message, "Not all Gateway API CRDs are present")
	assert.Contains(t, compliantCond.Message, "non-compliant")
}

func TestComputeCRDConditions_ReadError(t *testing.T) {
	scheme := runtime.NewScheme()
	apiextensionsv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()
	errClient := &crdGetErrorClient{Client: fakeClient}

	r := &reconciler{client: errClient}

	_, _, _, err := r.computeCRDConditions(context.Background())
	require.Error(t, err, "non-NotFound CRD Get error must propagate")
	assert.Contains(t, err.Error(), "failed to get CRD")
}

func TestReconcileIngressStatus_ObservedGeneration(t *testing.T) {
	scheme := runtime.NewScheme()
	operatorv1alpha1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)

	ingressObj := &operatorv1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "cluster",
			Generation: 5,
		},
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
			},
		},
	}

	var objs []runtime.Object
	objs = append(objs, allManagedCRDObjects()...)
	objs = append(objs, ingressObj)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(ingressObj.DeepCopy()).
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
	modeAccessor := NewModeAccessor(true)
	r := &reconciler{
		client: cl,
		config: Config{
			ModeAccessor: modeAccessor,
		},
	}

	snapshot := ingressModeSnapshot{
		desiredMode: operatorv1alpha1.GatewayAPIManagementModeManaged,
		ingress:     ingressObj,
		found:       true,
	}
	err := r.reconcileIngressStatus(context.Background(), snapshot)
	require.NoError(t, err)

	var updated operatorv1alpha1.Ingress
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &updated)
	require.NoError(t, err)

	assert.Equal(t, int64(5), updated.Status.ObservedGeneration, "status.observedGeneration")

	for _, condType := range []string{
		conditionTypeGatewayAPICRDsManaged,
		conditionTypeGatewayAPICRDsPresent,
		conditionTypeGatewayAPICRDsCompliant,
	} {
		cond := findCondition(updated.Status.Conditions, condType)
		require.NotNil(t, cond, "condition %s missing", condType)
		assert.Equal(t, int64(5), cond.ObservedGeneration,
			"condition %s ObservedGeneration should match Ingress generation", condType)
	}
}

// crdGetErrorClient wraps a client.Client and returns a transient
// error for any CRD Get to verify computeCRDConditions error handling.
type crdGetErrorClient struct {
	client.Client
}

func (c *crdGetErrorClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok {
		return fmt.Errorf("simulated API server error")
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func allManagedCRDObjects() []runtime.Object {
	var objs []runtime.Object
	for _, crd := range managedCRDs {
		objs = append(objs, crd.DeepCopy())
	}
	return objs
}
