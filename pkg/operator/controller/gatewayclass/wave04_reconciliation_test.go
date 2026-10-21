package gatewayclass

import (
	"context"
	"fmt"
	"testing"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/install"
	configv1 "github.com/openshift/api/config/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func wave04GatewayClass(name string) *gatewayapiv1.GatewayClass {
	return &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 2, Finalizers: []string{sailLibraryFinalizer}},
		Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: gatewayapiv1.GatewayController(operatorcontroller.OpenShiftGatewayClassControllerName)},
	}
}

func newWave04Reconciler(t *testing.T, objects ...client.Object) (*reconciler, client.Client, *fakeSailInstaller) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, rbacv1.AddToScheme, apiextensionsv1.AddToScheme, configv1.Install, gatewayapiv1.Install, operatorsv1alpha1.AddToScheme, sailv1.AddToScheme} {
		require.NoError(t, add(scheme))
	}
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(objects...).WithStatusSubresource(&gatewayapiv1.GatewayClass{}).
		WithIndex(&gatewayapiv1.GatewayClass{}, operatorcontroller.GatewayClassIndexFieldName, func(object client.Object) []string {
			return []string{string(object.(*gatewayapiv1.GatewayClass).Spec.ControllerName)}
		}).Build()
	installer := &fakeSailInstaller{status: install.Status{Installed: true, Version: "v1.27.9", CRDState: install.CRDManagedByCIO}}
	return &reconciler{client: client, cache: &testutil.FakeCache{Reader: client}, sailInstaller: installer,
		config: Config{GatewayAPIWithoutOLMEnabled: true, OperatorNamespace: "openshift-ingress-operator", OperandNamespace: "openshift-ingress", IstioVersion: "v1.27-latest"}}, client, installer
}

func TestWave04ReconciliationFailureStatus(t *testing.T) {
	ctx := context.Background()
	class := wave04GatewayClass("wave04")
	roles, err := istioAccessRoles()
	require.NoError(t, err)
	conflict := roles[0].DeepCopy()
	conflict.Labels = nil
	reconciler, client, installer := newWave04Reconciler(t, class, conflict)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: class.Name}}
	_, err = reconciler.Reconcile(ctx, request)
	require.ErrorContains(t, err, "conflicting ownership")
	require.Empty(t, installer.internalOpts.Version)
	require.True(t, installer.status.Installed)
	require.NoError(t, installer.status.Error)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, client.Get(ctx, request.NamespacedName, &current))
	condition := meta.FindStatusCondition(current.Status.Conditions, ControllerInstalledConditionType)
	require.NotNil(t, condition)
	require.Equal(t, metav1.ConditionFalse, condition.Status)
	require.Equal(t, "InstallFailed", condition.Reason)
	require.Equal(t, class.Generation, condition.ObservedGeneration)
	require.Contains(t, condition.Message, "conflicting ownership")
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(current.Status.Conditions, CRDsReadyConditionType).Status)
	require.NoError(t, client.Delete(ctx, conflict))
	_, err = reconciler.Reconcile(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "v1.27.9", installer.internalOpts.Version)
	require.NoError(t, client.Get(ctx, request.NamespacedName, &current))
	condition = meta.FindStatusCondition(current.Status.Conditions, ControllerInstalledConditionType)
	require.Equal(t, metav1.ConditionTrue, condition.Status)
	require.Equal(t, "Installed", condition.Reason)
	var installed rbacv1.ClusterRoleList
	require.NoError(t, client.List(ctx, &installed))
	require.Len(t, installed.Items, 3)
}

type wave04DeleteFailureClient struct {
	client.Client
}

func (failed *wave04DeleteFailureClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	return fmt.Errorf("injected ClusterRole delete failure")
}

func TestWave04OLMRollbackRoleCleanup(t *testing.T) {
	for _, scenario := range []string{"owned", "multiple-classes", "ownership-conflict", "delete-error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			class := wave04GatewayClass("wave04")
			objects := []client.Object{class}
			if scenario == "multiple-classes" {
				objects = append(objects, wave04GatewayClass("another"))
			}
			reconciler, originalClient, _ := newWave04Reconciler(t, objects...)
			require.NoError(t, reconciler.reconcileIstioAccess(ctx, false))
			reconciler.config.GatewayAPIWithoutOLMEnabled = false
			reconciler.sailInstaller = nil
			var conflict rbacv1.ClusterRole
			if scenario == "ownership-conflict" {
				require.NoError(t, originalClient.Get(ctx, types.NamespacedName{Name: istioAccessRolePrefix + "admin"}, &conflict))
				delete(conflict.Labels, istioAccessOwnerLabel)
				require.NoError(t, originalClient.Update(ctx, &conflict))
			}
			if scenario == "delete-error" {
				reconciler.client = &wave04DeleteFailureClient{Client: originalClient}
			}
			request := reconcile.Request{NamespacedName: types.NamespacedName{Name: class.Name}}
			_, err := reconciler.Reconcile(ctx, request)
			if scenario == "ownership-conflict" || scenario == "delete-error" {
				require.ErrorContains(t, err, "failed to clean up Istio access during OLM rollback")
				var retryable gatewayapiv1.GatewayClass
				require.NoError(t, originalClient.Get(ctx, request.NamespacedName, &retryable))
				require.Contains(t, retryable.Finalizers, sailLibraryFinalizer)
				if scenario == "ownership-conflict" {
					require.NoError(t, originalClient.Delete(ctx, &conflict))
				}
				reconciler.client = originalClient
				_, err = reconciler.Reconcile(ctx, request)
			}
			require.NoError(t, err)
			var current gatewayapiv1.GatewayClass
			require.NoError(t, originalClient.Get(ctx, request.NamespacedName, &current))
			require.NotContains(t, current.Finalizers, sailLibraryFinalizer)
			if scenario == "multiple-classes" {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "another"}})
				require.NoError(t, err)
			}
			var roles rbacv1.ClusterRoleList
			require.NoError(t, originalClient.List(ctx, &roles))
			require.Empty(t, roles.Items)
		})
	}
}

func TestWave04SharedRoleDeletion(t *testing.T) {
	for _, scenario := range []string{"last", "another-class", "uninstall-error", "ownership-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			class := wave04GatewayClass("wave04")
			objects := []client.Object{class}
			if scenario == "another-class" {
				objects = append(objects, wave04GatewayClass("another"))
			}
			reconciler, client, installer := newWave04Reconciler(t, objects...)
			require.NoError(t, reconciler.reconcileIstioAccess(ctx, false))
			if scenario == "uninstall-error" {
				installer.uninstallError = fmt.Errorf("uninstall denied")
			}
			if scenario == "ownership-conflict" {
				var conflict rbacv1.ClusterRole
				require.NoError(t, client.Get(ctx, types.NamespacedName{Name: istioAccessRolePrefix + "admin"}, &conflict))
				delete(conflict.Labels, istioAccessOwnerLabel)
				require.NoError(t, client.Update(ctx, &conflict))
			}
			require.NoError(t, client.Delete(ctx, class))
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: class.Name}})
			if scenario == "uninstall-error" || scenario == "ownership-conflict" {
				require.Error(t, err)
				var current gatewayapiv1.GatewayClass
				require.NoError(t, client.Get(ctx, types.NamespacedName{Name: class.Name}, &current))
				require.Contains(t, current.Finalizers, sailLibraryFinalizer)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, scenario != "another-class", installer.uninstallCalled)
			var remaining rbacv1.ClusterRoleList
			require.NoError(t, client.List(ctx, &remaining))
			if scenario == "last" {
				require.Empty(t, remaining.Items)
			} else {
				require.Len(t, remaining.Items, 3)
			}
		})
	}
}
