package gatewayclass

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

func managedProfileTestClass(name string, managed bool) *gatewayapiv1.GatewayClass {
	class := &gatewayapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name)}, Spec: gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName}}
	if managed {
		class.Labels = map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}
	} else {
		class.Finalizers = []string{sailLibraryFinalizer}
	}
	return class
}

func managedProfileTestReconciler(t *testing.T, objects ...client.Object) *reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, register := range []func(*runtime.Scheme) error{corev1.AddToScheme, networkingv1.AddToScheme, rbacv1.AddToScheme, configv1.Install, gatewayapiv1.Install, apiextensionsv1.AddToScheme} {
		require.NoError(t, register(scheme))
	}
	objects = append(objects, &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}})
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&gatewayapiv1.GatewayClass{}).WithIndex(&gatewayapiv1.GatewayClass{}, operatorcontroller.GatewayClassIndexFieldName, func(object client.Object) []string {
		return []string{string(object.(*gatewayapiv1.GatewayClass).Spec.ControllerName)}
	}).Build()
	informers := &informertest.FakeInformers{Scheme: scheme}
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	return &reconciler{client: client, cache: &testutil.FakeCache{Informers: informers, Reader: client}, modeAccessor: mode, config: Config{OperandNamespace: operatorcontroller.DefaultOperandNamespace, GatewayAPIWithoutOLMEnabled: true, IstioVersion: "v1.30.5"}, sailInstaller: &fakeSailInstaller{}}
}

func TestManagedProfileOverlaysPreserveDefaultAndCommonConfiguration(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{operatorcontroller.OpenShiftClusterIPGatewayClassName}, managedProfileNames)
	classes := withDesiredManagedProfiles([]gatewayapiv1.GatewayClass{*managedProfileTestClass("openshift-default", false)})
	raw, err := buildGatewayClassesConfig(nil, classes)
	require.NoError(t, err)
	var configs map[string]map[string]any
	require.NoError(t, json.Unmarshal(raw, &configs))
	require.Len(t, configs, 2)
	defaultConfig := configs["openshift-default"]
	require.NotContains(t, defaultConfig, "service")
	managedConfig := configs[operatorcontroller.OpenShiftClusterIPGatewayClassName]
	require.Equal(t, defaultConfig["horizontalPodAutoscaler"], managedConfig["horizontalPodAutoscaler"])
	require.Equal(t, defaultConfig["deployment"], managedConfig["deployment"])
	require.Equal(t, map[string]any{"spec": map[string]any{"type": "ClusterIP"}}, managedConfig["service"])
	require.Len(t, withDesiredManagedProfiles(classes), 2)
}

func TestManagedProfileDefaultsPrecedeCreationAndRepairStatus(t *testing.T) {
	anchor := managedProfileTestClass("openshift-default", false)
	r := managedProfileTestReconciler(t, anchor)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: anchor.Name}}
	result, err := r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NotZero(t, result.RequeueAfter)
	var values map[string]any
	require.NoError(t, json.Unmarshal(r.sailInstaller.(*fakeSailInstaller).internalOpts.Values.GatewayClasses, &values))
	for _, name := range []string{operatorcontroller.OpenShiftClusterIPGatewayClassName} {
		require.Contains(t, values, name)
		require.True(t, apierrors.IsNotFound(r.client.Get(context.Background(), types.NamespacedName{Name: name}, &gatewayapiv1.GatewayClass{})))
		defaults := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: managedProfileDefaultsName(r.config.OperandNamespace, name).Name, Namespace: r.config.OperandNamespace, Labels: map[string]string{operatorcontroller.GatewayClassDefaultsLabel: name}}}
		require.NoError(t, r.client.Create(context.Background(), defaults))
	}
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	var managedProfile gatewayapiv1.GatewayClass
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &managedProfile))
	require.Empty(t, managedProfile.Finalizers)
	require.Equal(t, "true", managedProfile.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
	require.Equal(t, "Configured", meta.FindStatusCondition(managedProfile.Status.Conditions, "Customized").Reason)
	managedProfile.Status.Conditions = append(managedProfile.Status.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()})
	meta.SetStatusCondition(&managedProfile.Status.Conditions, metav1.Condition{Type: "Customized", Status: metav1.ConditionFalse, Reason: "Unmanaged", Message: "CIO is not managing customization", LastTransitionTime: metav1.Now()})
	require.NoError(t, r.client.Status().Update(context.Background(), &managedProfile))
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &managedProfile))
	require.Equal(t, "Configured", meta.FindStatusCondition(managedProfile.Status.Conditions, "Customized").Reason)
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(managedProfile.Status.Conditions, "Accepted").Status)
	var defaults corev1.ConfigMap
	require.NoError(t, r.client.Get(context.Background(), managedProfileDefaultsName(r.config.OperandNamespace, name), &defaults))
	defaults.Labels = nil
	require.NoError(t, r.client.Update(context.Background(), &defaults))
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &managedProfile))
	require.Equal(t, "ConfigurationMissing", meta.FindStatusCondition(managedProfile.Status.Conditions, "Customized").Reason)
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(managedProfile.Status.Conditions, "Accepted").Status)
	defaults.Labels = map[string]string{operatorcontroller.GatewayClassDefaultsLabel: name}
	require.NoError(t, r.client.Update(context.Background(), &defaults))
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &managedProfile))
	require.Equal(t, "Configured", meta.FindStatusCondition(managedProfile.Status.Conditions, "Customized").Reason)
}

func TestManagedProfilesNeverEnableInstallationOrTakeOverConflicts(t *testing.T) {
	t.Run("managedProfile only", func(t *testing.T) {
		managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
		r := managedProfileTestReconciler(t, managedProfile)
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: managedProfile.Name}})
		require.NoError(t, err)
		require.Nil(t, r.sailInstaller.(*fakeSailInstaller).internalOpts.Values)
	})
	t.Run("wrong controller", func(t *testing.T) {
		anchor := managedProfileTestClass("openshift-default", false)
		conflict := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
		conflict.Spec.ControllerName = "example.com/other"
		r := managedProfileTestReconciler(t, anchor, conflict)
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: anchor.Name}})
		require.ErrorContains(t, err, "cannot update GatewayClass")
		var current gatewayapiv1.GatewayClass
		require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: conflict.Name}, &current))
		require.Equal(t, conflict.Spec, current.Spec)
	})
	t.Run("pre-existing user managedProfile", func(t *testing.T) {
		anchor := managedProfileTestClass("openshift-default", false)
		managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, false)
		r := managedProfileTestReconciler(t, anchor, managedProfile)
		_, err := r.ensureManagedProfiles(context.Background())
		require.NoError(t, err)
		var current gatewayapiv1.GatewayClass
		require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: managedProfile.Name}, &current))
		require.Empty(t, current.Labels)
		require.Empty(t, current.Status.Conditions)
	})
}

func TestFinalAnchorWaitsForManagedProfileAndIstioCleanup(t *testing.T) {
	ctx := context.Background()
	anchor := managedProfileTestClass("openshift-default", false)
	managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	managedProfile.Finalizers = []string{"gateway.networking.k8s.io/gateway-exists-finalizer"}
	r := managedProfileTestReconciler(t, anchor, managedProfile)
	require.NoError(t, r.client.Delete(ctx, anchor))
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: anchor.Name}}
	result, err := r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.NotZero(t, result.RequeueAfter)
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, r.client.Get(ctx, request.NamespacedName, &current))
	require.Contains(t, current.Finalizers, sailLibraryFinalizer)
	require.NoError(t, r.client.Get(ctx, types.NamespacedName{Name: managedProfile.Name}, &current))
	current.Finalizers = nil
	require.NoError(t, r.client.Update(ctx, &current))
	r.sailInstaller.(*fakeSailInstaller).uninstallError = fmt.Errorf("teardown failed")
	_, err = r.Reconcile(ctx, request)
	require.ErrorContains(t, err, "teardown failed")
	require.NoError(t, r.client.Get(ctx, request.NamespacedName, &current))
	require.Contains(t, current.Finalizers, sailLibraryFinalizer)
	r.sailInstaller.(*fakeSailInstaller).uninstallError = nil
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(r.client.Get(ctx, request.NamespacedName, &current)))
}

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
			pending, err := r.deleteManagedProfile(ctx, managedProfile.Name)
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

func TestManagedProfileCustomizedStatusRetriesPreservingAccepted(t *testing.T) {
	ctx := context.Background()
	managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	r := managedProfileTestReconciler(t, managedProfile)
	base := r.client.(client.WithWatch)
	patches := 0
	wrapped := interceptor.NewClient(base, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, delegate client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
		patches++
		if patches == 1 {
			var current gatewayapiv1.GatewayClass
			require.NoError(t, delegate.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
			meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()})
			require.NoError(t, delegate.Status().Update(ctx, &current))
			return apierrors.NewConflict(gatewayapiv1.Resource("gatewayclasses"), object.GetName(), fmt.Errorf("concurrent Accepted update"))
		}
		return delegate.SubResource(subresource).Patch(ctx, object, patch, options...)
	}})
	require.NoError(t, SetManagedProfilesUnmanagedCustomizedStatus(ctx, wrapped))
	require.Equal(t, 2, patches)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
	require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, "Accepted"))
	condition := meta.FindStatusCondition(current.Status.Conditions, "Customized")
	require.NotNil(t, condition)
	require.Equal(t, metav1.ConditionFalse, condition.Status)
	require.Equal(t, "Unmanaged", condition.Reason)
	require.NoError(t, SetManagedProfilesUnmanagedCustomizedStatus(ctx, wrapped))
	require.Equal(t, 2, patches, "unchanged CustomizedStatus must not be written again")
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
			require.NoError(t, SetManagedProfilesUnmanagedCustomizedStatus(ctx, wrapped))
			require.Zero(t, writes)
			if scenario != "absent" {
				var current gatewayapiv1.GatewayClass
				require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
				require.Equal(t, "Configured", meta.FindStatusCondition(current.Status.Conditions, "Customized").Reason)
			}
		})
	}
}
