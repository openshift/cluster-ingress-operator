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
	classes := withDesiredManagedProfile([]gatewayapiv1.GatewayClass{*managedProfileTestClass("openshift-default", false)})
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
	require.Len(t, withDesiredManagedProfile(classes), 2)
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
		_, err := r.ensureManagedProfile(context.Background())
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
