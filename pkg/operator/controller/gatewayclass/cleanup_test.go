package gatewayclass

import (
	"context"
	"fmt"
	"testing"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func teardownTestDefaults() *corev1.ConfigMap {
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      managedProfileDefaultsName(operatorcontroller.DefaultOperandNamespace, name).Name,
		Namespace: operatorcontroller.DefaultOperandNamespace, UID: "defaults-uid",
		Labels:      map[string]string{operatorcontroller.GatewayClassDefaultsLabel: name, "managed-by": "sail-library", "release": operatorcontroller.IstioName("").Name + "-istiod"},
		Annotations: map[string]string{"meta.helm.sh/release-name": operatorcontroller.IstioName("").Name + "-istiod", "meta.helm.sh/release-namespace": operatorcontroller.DefaultOperandNamespace},
	}}
}

func TestUninstallSailWithoutLibraryIsNoOp(t *testing.T) {
	r := &reconciler{}
	require.NoError(t, r.UninstallSail(context.Background()))
}

func TestManagedProfileCleanupPreservesForeignClasses(t *testing.T) {
	for _, scenario := range []string{"user-created", "other-controller"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, scenario != "user-created")
			if scenario == "other-controller" {
				managedProfile.Spec.ControllerName = "example.com/other"
			}
			r := managedProfileTestReconciler(t, managedProfile)
			pending, err := r.deleteManagedProfile(ctx)
			require.NoError(t, err)
			require.False(t, pending)
			var current gatewayapiv1.GatewayClass
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
			require.True(t, current.DeletionTimestamp.IsZero())
			require.Equal(t, managedProfile.UID, current.UID)
		})
	}
}

func TestDefaultsCleanupFailuresHoldAnchorFinalizer(t *testing.T) {
	for _, failure := range []string{"delay", "get-error", "delete-error", "foreign"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			anchor := managedProfileTestClass("openshift-default", false)
			defaults := teardownTestDefaults()
			if failure == "delay" {
				defaults.Finalizers = []string{"test/delayed-delete"}
			}
			if failure == "foreign" {
				defaults.Labels["managed-by"] = "user"
			}
			r := managedProfileTestReconciler(t, anchor, defaults)
			base := r.client.(client.WithWatch)
			fail := true
			deleted := false
			r.client = interceptor.NewClient(base, interceptor.Funcs{
				Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
					if fail && failure == "get-error" && key == client.ObjectKeyFromObject(defaults) {
						return fmt.Errorf("defaults read failed")
					}
					return delegate.Get(ctx, key, object, options...)
				},
				Delete: func(ctx context.Context, delegate client.WithWatch, object client.Object, options ...client.DeleteOption) error {
					if client.ObjectKeyFromObject(object) == client.ObjectKeyFromObject(defaults) {
						if fail && failure == "delete-error" {
							return fmt.Errorf("defaults delete failed")
						}
						deleted = true
					}
					return delegate.Delete(ctx, object, options...)
				},
			})
			require.NoError(t, base.Delete(ctx, anchor))
			request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)}
			_, err := r.Reconcile(ctx, request)
			require.Error(t, err)
			require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
			var current gatewayapiv1.GatewayClass
			require.NoError(t, base.Get(ctx, request.NamespacedName, &current))
			require.Contains(t, current.Finalizers, sailLibraryFinalizer)
			if failure == "foreign" {
				require.False(t, deleted)
				require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(defaults), &corev1.ConfigMap{}))
				return
			}
			fail = false
			if failure == "delay" {
				var pending corev1.ConfigMap
				require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(defaults), &pending))
				pending.Finalizers = nil
				require.NoError(t, base.Update(ctx, &pending))
			}
			_, err = r.Reconcile(ctx, request)
			require.NoError(t, err)
			require.True(t, apierrors.IsNotFound(base.Get(ctx, request.NamespacedName, &current)))
		})
	}
}

func TestManagedProfileClassDeleteErrorsRetry(t *testing.T) {
	ctx := context.Background()
	anchor := managedProfileTestClass("openshift-default", false)
	managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	r := managedProfileTestReconciler(t, anchor, managedProfile)
	base := r.client.(client.WithWatch)
	fail := true
	r.client = interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, delegate client.WithWatch, object client.Object, options ...client.DeleteOption) error {
		if object.GetName() == managedProfile.Name && fail {
			return fmt.Errorf("managed class deletion failed")
		}
		return delegate.Delete(ctx, object, options...)
	}})
	require.NoError(t, base.Delete(ctx, anchor))
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)}
	_, err := r.Reconcile(ctx, request)
	require.ErrorContains(t, err, "managed class deletion failed")
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, base.Get(ctx, request.NamespacedName, &current))
	require.Contains(t, current.Finalizers, sailLibraryFinalizer)
	fail = false
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(base.Get(ctx, request.NamespacedName, &current)))
}

func TestManagedProfileCleanupRejectsUIDReplacement(t *testing.T) {
	for _, kind := range []string{"class", "defaults"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			anchor := managedProfileTestClass("openshift-default", false)
			var original client.Object = managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
			if kind == "defaults" {
				original = teardownTestDefaults()
			}
			r := managedProfileTestReconciler(t, anchor, original)
			base := r.client.(client.WithWatch)
			replaced := false
			r.client = interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, delegate client.WithWatch, object client.Object, options ...client.DeleteOption) error {
				if client.ObjectKeyFromObject(object) == client.ObjectKeyFromObject(original) {
					deleteOptions := (&client.DeleteOptions{}).ApplyOptions(options)
					require.NotNil(t, deleteOptions.Preconditions)
					require.NotNil(t, deleteOptions.Preconditions.UID)
					require.Equal(t, original.GetUID(), *deleteOptions.Preconditions.UID)
					require.False(t, replaced, "must not attempt to delete foreign replacement")
					require.NoError(t, delegate.Delete(ctx, original))
					replacement := original.DeepCopyObject().(client.Object)
					replacement.SetResourceVersion("")
					replacement.SetUID("foreign-replacement")
					replacement.SetLabels(nil)
					require.NoError(t, delegate.Create(ctx, replacement))
					replaced = true
					return apierrors.NewConflict(gatewayapiv1.Resource("gatewayclasses"), object.GetName(), fmt.Errorf("UID precondition failed"))
				}
				return delegate.Delete(ctx, object, options...)
			}})
			require.NoError(t, base.Delete(ctx, anchor))
			request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)}
			_, err := r.Reconcile(ctx, request)
			require.ErrorContains(t, err, "UID precondition failed")
			require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
			_, err = r.Reconcile(ctx, request)
			if kind == "defaults" {
				require.ErrorContains(t, err, "not owned")
				require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
			} else {
				require.NoError(t, err)
			}
			current := original.DeepCopyObject().(client.Object)
			require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(original), current))
			require.EqualValues(t, "foreign-replacement", current.GetUID())
			require.True(t, current.GetDeletionTimestamp().IsZero())
		})
	}
}

func TestManagedProfileCleanupCompletesOnSailUninstallSuccess(t *testing.T) {
	ctx := context.Background()
	anchor := managedProfileTestClass("openshift-default", false)
	defaults := teardownTestDefaults()
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "istiod", Namespace: operatorcontroller.DefaultOperandNamespace, Labels: map[string]string{"managed-by": "sail-library"}, Finalizers: []string{"test/delayed-release-gc"}}}
	r := managedProfileTestReconciler(t, anchor, defaults, service)
	require.NoError(t, r.client.Delete(ctx, service))
	installer := r.sailInstaller.(*fakeSailInstaller)
	installer.onUninstall = func() error {
		require.True(t, apierrors.IsNotFound(r.client.Get(ctx, client.ObjectKeyFromObject(defaults), &corev1.ConfigMap{})))
		return nil
	}
	require.NoError(t, r.client.Delete(ctx, anchor))
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)}
	_, err := r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.True(t, installer.uninstallCalled)
	require.Zero(t, installer.stopCalls)
	require.True(t, apierrors.IsNotFound(r.client.Get(ctx, request.NamespacedName, &gatewayapiv1.GatewayClass{})))
	var remaining corev1.Service
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(service), &remaining))
	require.False(t, remaining.DeletionTimestamp.IsZero())
}

func TestManagedProfileManagedRecoveryUsesApplyWithoutRestart(t *testing.T) {
	ctx := context.Background()
	anchor := managedProfileTestClass("openshift-default", false)
	managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	managedProfile.Status.Conditions = []metav1.Condition{{Type: "Customized", Status: metav1.ConditionFalse, Reason: "Unmanaged", Message: "CIO is not managing customization", LastTransitionTime: metav1.Now()}}
	r := managedProfileTestReconciler(t, anchor, managedProfile)
	installer := r.sailInstaller.(*fakeSailInstaller)
	require.NoError(t, r.UninstallSail(ctx))
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)})
	require.NoError(t, err)
	require.NotNil(t, installer.internalOpts.Values)
	require.NoError(t, r.client.Create(ctx, teardownTestDefaults()))
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)})
	require.NoError(t, err)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
	require.Equal(t, "Configured", meta.FindStatusCondition(current.Status.Conditions, "Customized").Reason)
	require.Zero(t, installer.stopCalls)
	require.Zero(t, installer.startCalls)
}

func TestEnrollsEverySailAnchorBeforeProvisioning(t *testing.T) {
	ctx := context.Background()
	first := managedProfileTestClass("openshift-default", false)
	second := managedProfileTestClass("user-class", false)
	first.Finalizers, second.Finalizers = nil, nil
	managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	r := managedProfileTestReconciler(t, first, second, managedProfile)
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(second)})
	require.NoError(t, err)
	for _, anchor := range []*gatewayapiv1.GatewayClass{first, second} {
		var current gatewayapiv1.GatewayClass
		require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(anchor), &current))
		require.Contains(t, current.Finalizers, sailLibraryFinalizer)
		require.NoError(t, r.client.Delete(ctx, &current))
	}
	var current gatewayapiv1.GatewayClass
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
	require.Empty(t, current.Finalizers)
	for _, anchor := range []*gatewayapiv1.GatewayClass{first, second} {
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)})
		require.NoError(t, err)
		require.True(t, apierrors.IsNotFound(r.client.Get(ctx, client.ObjectKeyFromObject(anchor), &current)))
	}
	require.True(t, apierrors.IsNotFound(r.client.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current)))
}
