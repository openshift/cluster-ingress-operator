package gatewayapi

import (
	"context"
	"fmt"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestManagedProfileUnmanagedTransitionRetriesRestartsAndStopsObserving(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, register := range []func(*runtime.Scheme) error{configv1.Install, operatorv1alpha1.Install, gatewayapiv1.Install, admissionv1.AddToScheme, rbacv1.AddToScheme, apiextensionsv1.AddToScheme} {
		require.NoError(t, register(scheme))
	}
	ingress := &operatorv1alpha1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: operatorv1alpha1.IngressSpec{GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{ManagementMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged}}}
	ingress.Status.Conditions = []metav1.Condition{{Type: conditionTypeGatewayAPICRDsManaged, Status: metav1.ConditionTrue, Reason: reasonManagedByIngressOperator, LastTransitionTime: metav1.Now()}}
	var objects []client.Object
	objects = append(objects, ingress, &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}, &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}})
	for _, crd := range managedCRDs {
		current := crd.DeepCopy()
		current.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue}}
		objects = append(objects, current)
	}
	names := []string{operatorcontroller.OpenShiftClusterIPGatewayClassName, operatorcontroller.OpenShiftDefaultGatewayClassName}
	for _, name := range names {
		managedProfile := &gatewayapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName}}
		if name != operatorcontroller.OpenShiftDefaultGatewayClassName {
			managedProfile.Labels = map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}
		}
		managedProfile.Status.Conditions = []metav1.Condition{{Type: "Customized", Status: metav1.ConditionTrue, Reason: "Configured", LastTransitionTime: metav1.Now()}, {Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()}}
		objects = append(objects, managedProfile)
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&operatorv1alpha1.Ingress{}, &gatewayapiv1.GatewayClass{}).WithIndex(&apiextensionsv1.CustomResourceDefinition{}, gatewayAPICRDIndexFieldName, func(object client.Object) []string {
		return []string{}
	}).Build()
	failStatus := true
	managedProfileWrites, managedProfileLists := 0, 0
	wrapped := interceptor.NewClient(base, interceptor.Funcs{
		List: func(ctx context.Context, delegate client.WithWatch, list client.ObjectList, options ...client.ListOption) error {
			if _, managedProfiles := list.(*gatewayapiv1.GatewayClassList); managedProfiles {
				managedProfileLists++
			}
			return delegate.List(ctx, list, options...)
		},
		SubResourcePatch: func(ctx context.Context, delegate client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
			if _, managedProfile := object.(*gatewayapiv1.GatewayClass); managedProfile {
				managedProfileWrites++
				if failStatus && object.GetName() == operatorcontroller.OpenShiftClusterIPGatewayClassName {
					return fmt.Errorf("transient managed-profile status failure")
				}
			}
			return delegate.SubResource(subresource).Patch(ctx, object, patch, options...)
		},
	})
	uninstallCalls := 0
	uninstaller := &fakeSailUninstaller{onUninstall: func() { uninstallCalls++ }}
	newReconciler := func() *reconciler {
		mode := operatorcontroller.NewGatewayAPIModeAccessor(true)
		informers := &informertest.FakeInformers{Scheme: scheme}
		return &reconciler{client: wrapped, cache: &testutil.FakeCache{Informers: informers, Reader: wrapped}, config: Config{ModeAccessor: mode, SailUninstaller: uninstaller}}
	}
	r := newReconciler()
	uninstaller.err = fmt.Errorf("teardown pending")
	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.ErrorContains(t, err, "teardown pending")
	require.Zero(t, managedProfileWrites)
	uninstaller.err = nil
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.ErrorContains(t, err, "transient managed-profile status failure")
	require.Nil(t, r.config.ModeAccessor.GetLastAppliedMode())
	var current operatorv1alpha1.Ingress
	require.NoError(t, base.Get(ctx, types.NamespacedName{Name: "cluster"}, &current))
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(current.Status.Conditions, conditionTypeGatewayAPICRDsManaged).Status)
	failStatus = false
	r = newReconciler()
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.Equal(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged, *r.config.ModeAccessor.GetLastAppliedMode())
	for _, name := range names {
		var managedProfile gatewayapiv1.GatewayClass
		require.NoError(t, base.Get(ctx, types.NamespacedName{Name: name}, &managedProfile))
		require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(managedProfile.Status.Conditions, "Accepted").Status)
		condition := meta.FindStatusCondition(managedProfile.Status.Conditions, "Customized")
		if name == operatorcontroller.OpenShiftDefaultGatewayClassName {
			require.Equal(t, "Configured", condition.Reason)
		} else {
			require.Equal(t, metav1.ConditionFalse, condition.Status)
			require.Equal(t, "Unmanaged", condition.Reason)
		}
	}
	priorWrites, priorLists, priorUninstalls := managedProfileWrites, managedProfileLists, uninstallCalls
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.Equal(t, priorWrites, managedProfileWrites)
	require.Equal(t, priorLists, managedProfileLists)
	require.Equal(t, priorUninstalls, uninstallCalls)
	r = newReconciler()
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.Equal(t, priorWrites, managedProfileWrites)
	require.Greater(t, uninstallCalls, priorUninstalls)
	require.NoError(t, base.Get(ctx, types.NamespacedName{Name: "cluster"}, &current))
	current.Spec.GatewayAPI.ManagementMode = operatorv1alpha1.GatewayAPIManagementModeManaged
	require.NoError(t, base.Update(ctx, &current))
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.True(t, r.config.ModeAccessor.AllowDependents())
}

func TestManagedProfileUnmanagedSkipsAbsentDeletingAndUnownedClasses(t *testing.T) {
	for _, scenario := range []string{"absent", "deleting", "other-controller", "user-created"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			require.NoError(t, gatewayapiv1.Install(scheme))
			managedProfile := &gatewayapiv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: operatorcontroller.OpenShiftClusterIPGatewayClassName, Labels: map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}},
				Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName},
				Status:     gatewayapiv1.GatewayClassStatus{Conditions: []metav1.Condition{{Type: "Customized", Status: metav1.ConditionTrue, Reason: "Configured", LastTransitionTime: metav1.Now()}}},
			}
			switch scenario {
			case "deleting":
				managedProfile.DeletionTimestamp = new(metav1.Now())
				managedProfile.Finalizers = []string{"example.com/finalizer"}
			case "other-controller":
				managedProfile.Spec.ControllerName = "example.com/other"
			case "user-created":
				managedProfile.Labels = nil
			}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&gatewayapiv1.GatewayClass{})
			if scenario != "absent" {
				builder = builder.WithObjects(managedProfile)
			}
			base := builder.Build()
			writes := 0
			wrapped := interceptor.NewClient(base, interceptor.Funcs{SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
				writes++
				return fmt.Errorf("unexpected status write")
			}})
			r := &reconciler{client: wrapped}
			require.NoError(t, r.markManagedProfileUnmanaged(ctx))
			require.Zero(t, writes)
			if scenario != "absent" {
				var current gatewayapiv1.GatewayClass
				require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
				require.Equal(t, "Configured", meta.FindStatusCondition(current.Status.Conditions, "Customized").Reason)
			}
		})
	}
}
