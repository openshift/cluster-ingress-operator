package gatewayclass

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
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

func profileTestClass(name string, managed bool) *gatewayapiv1.GatewayClass {
	class := &gatewayapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name)}, Spec: gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName}}
	if managed {
		class.Labels = map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}
	} else {
		class.Finalizers = []string{sailLibraryFinalizer}
	}
	return class
}

func profileTestReconciler(t *testing.T, objects ...client.Object) *reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, register := range []func(*runtime.Scheme) error{corev1.AddToScheme, networkingv1.AddToScheme, rbacv1.AddToScheme, configv1.Install, gatewayapiv1.Install, sailv1.AddToScheme, apiextensionsv1.AddToScheme} {
		require.NoError(t, register(scheme))
	}
	objects = append(objects, &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}})
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&gatewayapiv1.GatewayClass{}).WithIndex(&gatewayapiv1.GatewayClass{}, operatorcontroller.GatewayClassIndexFieldName, func(object client.Object) []string {
		return []string{string(object.(*gatewayapiv1.GatewayClass).Spec.ControllerName)}
	}).Build()
	informers := &informertest.FakeInformers{Scheme: scheme}
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	return &reconciler{client: client, cache: &testutil.FakeCache{Informers: informers, Reader: client}, modeAccessor: mode, config: Config{OperandNamespace: operatorcontroller.DefaultOperandNamespace, GatewayAPIWithoutOLMEnabled: true, IstioVersion: "v1.30.5"}, sailInstaller: &fakeSailInstaller{}, releaseReader: &fakeHelmReleaseReader{}}
}

func TestSupportedProfilePlatforms(t *testing.T) {
	t.Parallel()
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.AzurePlatformType, configv1.GCPPlatformType, configv1.IBMCloudPlatformType, configv1.PowerVSPlatformType, configv1.OpenStackPlatformType, configv1.BareMetalPlatformType, configv1.VSpherePlatformType, configv1.NonePlatformType, configv1.NutanixPlatformType, configv1.ExternalPlatformType} {
		t.Run(string(platform), func(t *testing.T) {
			t.Parallel()
			infra := &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: platform}}}
			names := supportedProfileNames(infra)
			require.Contains(t, names, operatorcontroller.OpenShiftClusterIPGatewayClassName)
			require.Contains(t, names, operatorcontroller.OpenShiftExternalGatewayClassName)
			internal := platform == configv1.AWSPlatformType || platform == configv1.AzurePlatformType || platform == configv1.GCPPlatformType || platform == configv1.IBMCloudPlatformType || platform == configv1.PowerVSPlatformType || platform == configv1.OpenStackPlatformType
			if internal {
				require.Contains(t, names, operatorcontroller.OpenShiftInternalGatewayClassName)
			} else {
				require.NotContains(t, names, operatorcontroller.OpenShiftInternalGatewayClassName)
			}
		})
	}
	require.Len(t, supportedProfileNames(nil), 2)
}

func TestProfileOverlaysPreserveDefaultAndCommonConfiguration(t *testing.T) {
	t.Parallel()
	infra := &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType}}}
	classes := withDesiredProfiles(infra, []gatewayapiv1.GatewayClass{*profileTestClass("openshift-default", false)})
	raw, err := buildGatewayClassesConfig(infra, classes)
	require.NoError(t, err)
	var configs map[string]map[string]any
	require.NoError(t, json.Unmarshal(raw, &configs))
	defaultConfig := configs["openshift-default"]
	require.NotContains(t, defaultConfig, "service")
	for _, name := range supportedProfileNames(infra) {
		require.Equal(t, defaultConfig["horizontalPodAutoscaler"], configs[name]["horizontalPodAutoscaler"])
		deployment := configs[name]["deployment"].(map[string]any)
		template := deployment["spec"].(map[string]any)["template"].(map[string]any)
		defaultTemplate := defaultConfig["deployment"].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)
		require.Equal(t, defaultTemplate["spec"], template["spec"])
		service := configs[name]["service"].(map[string]any)
		if name == operatorcontroller.OpenShiftClusterIPGatewayClassName {
			require.Equal(t, "ClusterIP", service["spec"].(map[string]any)["type"])
			require.Nil(t, service["metadata"].(map[string]any)["annotations"])
			require.NotContains(t, template, "metadata")
		} else {
			require.Equal(t, "LoadBalancer", service["spec"].(map[string]any)["type"])
			require.Equal(t, `{"gatewayTopology":{"proxyProtocol":{}}}`, template["metadata"].(map[string]any)["annotations"].(map[string]any)["proxy.istio.io/config"])
		}
	}
}

func TestProfileDefaultsPrecedeCreationAndRepairStatus(t *testing.T) {
	anchor := profileTestClass("openshift-default", false)
	r := profileTestReconciler(t, anchor)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: anchor.Name}}
	result, err := r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NotZero(t, result.RequeueAfter)
	var values map[string]any
	require.NoError(t, json.Unmarshal(r.sailInstaller.(*fakeSailInstaller).internalOpts.Values.GatewayClasses, &values))
	for _, name := range supportedProfileNames(nil) {
		require.Contains(t, values, name)
		require.True(t, apierrors.IsNotFound(r.client.Get(context.Background(), types.NamespacedName{Name: name}, &gatewayapiv1.GatewayClass{})))
		defaults := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: profileDefaultsName(r.config.OperandNamespace, name).Name, Namespace: r.config.OperandNamespace, Labels: map[string]string{operatorcontroller.GatewayClassDefaultsLabel: name}}}
		require.NoError(t, r.client.Create(context.Background(), defaults))
	}
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	var profile gatewayapiv1.GatewayClass
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &profile))
	require.Empty(t, profile.Finalizers)
	require.Equal(t, "true", profile.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
	require.Equal(t, "Configured", meta.FindStatusCondition(profile.Status.Conditions, "Customized").Reason)
	profile.Status.Conditions = append(profile.Status.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()})
	meta.SetStatusCondition(&profile.Status.Conditions, metav1.Condition{Type: "Customized", Status: metav1.ConditionFalse, Reason: "Unmanaged", Message: "CIO is not managing customization", LastTransitionTime: metav1.Now()})
	require.NoError(t, r.client.Status().Update(context.Background(), &profile))
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &profile))
	require.Equal(t, "Configured", meta.FindStatusCondition(profile.Status.Conditions, "Customized").Reason)
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(profile.Status.Conditions, "Accepted").Status)
	var defaults corev1.ConfigMap
	require.NoError(t, r.client.Get(context.Background(), profileDefaultsName(r.config.OperandNamespace, name), &defaults))
	defaults.Labels = nil
	require.NoError(t, r.client.Update(context.Background(), &defaults))
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &profile))
	require.Equal(t, "ConfigurationMissing", meta.FindStatusCondition(profile.Status.Conditions, "Customized").Reason)
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(profile.Status.Conditions, "Accepted").Status)
	defaults.Labels = map[string]string{operatorcontroller.GatewayClassDefaultsLabel: name}
	require.NoError(t, r.client.Update(context.Background(), &defaults))
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &profile))
	require.Equal(t, "Configured", meta.FindStatusCondition(profile.Status.Conditions, "Customized").Reason)
}

func TestProfilesNeverEnableInstallationOrTakeOverConflicts(t *testing.T) {
	t.Run("profile only", func(t *testing.T) {
		profile := profileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
		r := profileTestReconciler(t, profile)
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: profile.Name}})
		require.NoError(t, err)
		require.Nil(t, r.sailInstaller.(*fakeSailInstaller).internalOpts.Values)
	})
	t.Run("wrong controller", func(t *testing.T) {
		anchor := profileTestClass("openshift-default", false)
		conflict := profileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
		conflict.Spec.ControllerName = "example.com/other"
		r := profileTestReconciler(t, anchor, conflict)
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: anchor.Name}})
		require.ErrorContains(t, err, "cannot update GatewayClass")
		var current gatewayapiv1.GatewayClass
		require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: conflict.Name}, &current))
		require.Equal(t, conflict.Spec, current.Spec)
	})
	t.Run("pre-existing user profile", func(t *testing.T) {
		anchor := profileTestClass("openshift-default", false)
		profile := profileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, false)
		r := profileTestReconciler(t, anchor, profile)
		_, err := r.ensureManagedProfiles(context.Background(), nil)
		require.NoError(t, err)
		var current gatewayapiv1.GatewayClass
		require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: profile.Name}, &current))
		require.Empty(t, current.Labels)
		require.Empty(t, current.Status.Conditions)
	})
}

func TestFinalAnchorWaitsForProfilesAndIstioCleanup(t *testing.T) {
	ctx := context.Background()
	anchor := profileTestClass("openshift-default", false)
	profile := profileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	profile.Finalizers = []string{"gateway.networking.k8s.io/gateway-exists-finalizer"}
	r := profileTestReconciler(t, anchor, profile)
	require.NoError(t, r.client.Delete(ctx, anchor))
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: anchor.Name}}
	result, err := r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.NotZero(t, result.RequeueAfter)
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, r.client.Get(ctx, request.NamespacedName, &current))
	require.Contains(t, current.Finalizers, sailLibraryFinalizer)
	require.NoError(t, r.client.Get(ctx, types.NamespacedName{Name: profile.Name}, &current))
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

func TestOLMTeardownDeletesOnlyOwnedIstioAndWaits(t *testing.T) {
	ctx := context.Background()
	for _, owned := range []bool{true, false} {
		t.Run(fmt.Sprintf("owned=%t", owned), func(t *testing.T) {
			anchor := profileTestClass("openshift-default", false)
			istio := &sailv1.Istio{ObjectMeta: metav1.ObjectMeta{Name: operatorcontroller.IstioName("").Name, UID: "istio-uid", Finalizers: []string{"sail-finalizer"}}, Spec: sailv1.IstioSpec{Namespace: operatorcontroller.DefaultOperandNamespace}}
			if owned {
				istio.OwnerReferences = []metav1.OwnerReference{{APIVersion: gatewayapiv1.SchemeGroupVersion.String(), Kind: "GatewayClass", Name: anchor.Name, UID: anchor.UID}}
			}
			revision := &sailv1.IstioRevision{ObjectMeta: metav1.ObjectMeta{Name: istio.Name, UID: "revision-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: sailv1.GroupVersion.String(), Kind: "Istio", Name: istio.Name, UID: istio.UID}}}, Spec: sailv1.IstioRevisionSpec{Namespace: operatorcontroller.DefaultOperandNamespace}}
			r := profileTestReconciler(t, anchor, istio, revision)
			r.sailInstaller = nil
			deletions := 0
			baseClient := r.client.(client.WithWatch)
			r.client = interceptor.NewClient(baseClient, interceptor.Funcs{Delete: func(ctx context.Context, delegate client.WithWatch, object client.Object, options ...client.DeleteOption) error {
				if _, isIstio := object.(*sailv1.Istio); isIstio {
					deletions++
				} else {
					require.IsType(t, &corev1.ConfigMap{}, object)
					require.Equal(t, r.teardownInventoryName(), client.ObjectKeyFromObject(object))
				}
				return delegate.Delete(ctx, object, options...)
			}})
			err := r.uninstallIstio(ctx)
			if !owned {
				require.ErrorContains(t, err, "not owned")
				require.Zero(t, deletions)
				return
			}
			require.ErrorContains(t, err, "waiting for CIO-managed Istio")
			require.Equal(t, 1, deletions)
			var current sailv1.Istio
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(istio), &current))
			require.False(t, current.DeletionTimestamp.IsZero())
			require.Error(t, r.uninstallIstio(ctx))
			require.Equal(t, 1, deletions)
			current.Finalizers = nil
			require.NoError(t, r.client.Update(ctx, &current))
			require.ErrorContains(t, r.uninstallIstio(ctx), "IstioRevision")
			require.NoError(t, baseClient.Delete(ctx, revision))
			require.NoError(t, r.uninstallIstio(ctx))
		})
	}
}
