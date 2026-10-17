package gatewayclass

import (
	"context"
	"fmt"
	"strings"
	"testing"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/require"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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

func teardownTestRelease(defaults *corev1.ConfigMap) *releasev1.Release {
	return &releasev1.Release{Name: operatorcontroller.IstioName("").Name + "-istiod", Namespace: operatorcontroller.DefaultOperandNamespace,
		Manifest: fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n", defaults.Name)}
}

func teardownTestMapper(r *reconciler) {
	mapper := r.client.RESTMapper().(*meta.DefaultRESTMapper)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("ConfigMap"), meta.RESTScopeNamespace)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Service"), meta.RESTScopeNamespace)
}

func TestUninstallSailWithoutLibraryIsNoOp(t *testing.T) {
	r := &reconciler{}
	require.NoError(t, r.UninstallSail(context.Background()))
	require.NoError(t, r.uninstallIstio(context.Background()))
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

func TestSailTeardownWaitsForReleaseInventoryAcrossRestart(t *testing.T) {
	ctx := context.Background()
	anchor := managedProfileTestClass("openshift-default", false)
	defaults := teardownTestDefaults()
	service := &corev1.Service{ObjectMeta: *defaults.ObjectMeta.DeepCopy()}
	service.Name, service.UID = "istiod-openshift-gateway", "service-uid"
	service.Finalizers = []string{"test/delayed-delete"}
	r := managedProfileTestReconciler(t, anchor, defaults, service)
	teardownTestMapper(r)
	reader := &fakeHelmReleaseReader{current: teardownTestRelease(defaults)}
	reader.current.(*releasev1.Release).Manifest += fmt.Sprintf("---\napiVersion: v1\nkind: Service\nmetadata:\n  name: %s\n", service.Name)
	r.releaseReader = reader
	installer := r.sailInstaller.(*fakeSailInstaller)
	installer.onUninstall = func() error {
		require.Positive(t, installer.stopCalls)
		require.NoError(t, r.client.Get(ctx, r.teardownInventoryName(), &corev1.ConfigMap{}))
		reader.current = nil
		if err := r.client.Delete(ctx, defaults); err != nil {
			return err
		}
		return r.client.Delete(ctx, service)
	}
	require.NoError(t, r.client.Delete(ctx, anchor))
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)}
	_, err := r.Reconcile(ctx, request)
	require.ErrorContains(t, err, "waiting for CIO Istio release resource Service")
	require.True(t, apierrors.IsNotFound(r.client.Get(ctx, client.ObjectKeyFromObject(defaults), &corev1.ConfigMap{})))
	var current gatewayapiv1.GatewayClass
	require.NoError(t, r.client.Get(ctx, request.NamespacedName, &current))
	require.Contains(t, current.Finalizers, sailLibraryFinalizer)
	restarted := &reconciler{config: r.config, client: r.client, cache: r.cache, modeAccessor: r.modeAccessor, sailInstaller: &fakeSailInstaller{}, releaseReader: reader}
	_, err = restarted.Reconcile(ctx, request)
	require.ErrorContains(t, err, "waiting for CIO Istio release resource Service")
	var pending corev1.Service
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(service), &pending))
	pending.Finalizers = nil
	require.NoError(t, r.client.Update(ctx, &pending))
	_, err = restarted.Reconcile(ctx, request)
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(r.client.Get(ctx, request.NamespacedName, &current)))
	require.True(t, apierrors.IsNotFound(r.client.Get(ctx, r.teardownInventoryName(), &corev1.ConfigMap{})))
}

func TestManagedReturnFinishesPendingSailTeardownBeforeApply(t *testing.T) {
	ctx := context.Background()
	anchor := managedProfileTestClass("openshift-default", false)
	defaults := teardownTestDefaults()
	defaults.Finalizers = []string{"test/pending"}
	r := managedProfileTestReconciler(t, anchor, defaults)
	teardownTestMapper(r)
	reader := &fakeHelmReleaseReader{current: teardownTestRelease(defaults)}
	r.releaseReader = reader
	installer := r.sailInstaller.(*fakeSailInstaller)
	installer.onUninstall = func() error {
		reader.current = nil
		return r.client.Delete(ctx, defaults)
	}
	require.ErrorContains(t, r.uninstallIstio(ctx), "waiting for CIO Istio release resource")
	installer.onUninstall = nil
	require.ErrorContains(t, r.ensureIstio(ctx, "v1.30.5", []gatewayapiv1.GatewayClass{*anchor}, nil), "waiting for CIO Istio release resource")
	require.Zero(t, installer.startCalls)
	var current corev1.ConfigMap
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(defaults), &current))
	current.Finalizers = nil
	require.NoError(t, r.client.Update(ctx, &current))
	require.NoError(t, r.ensureIstio(ctx, "v1.30.5", []gatewayapiv1.GatewayClass{*anchor}, nil))
	require.Equal(t, 1, installer.startCalls)
	require.True(t, installer.status.Installed)
	require.False(t, r.sailStopped)
}

func TestSailTeardownPreflightErrorsDoNotUninstall(t *testing.T) {
	for _, failure := range []string{"release-read", "checkpoint-write", "foreign-release-object", "foreign-aggregation-role"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			defaults := teardownTestDefaults()
			r := managedProfileTestReconciler(t, defaults)
			teardownTestMapper(r)
			reader := &fakeHelmReleaseReader{current: teardownTestRelease(defaults)}
			r.releaseReader = reader
			switch failure {
			case "release-read":
				reader.err = fmt.Errorf("release unavailable")
			case "checkpoint-write":
				r.client = interceptor.NewClient(r.client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, delegate client.WithWatch, object client.Object, options ...client.CreateOption) error {
					return fmt.Errorf("checkpoint unavailable")
				}})
			case "foreign-release-object":
				defaults.Labels["managed-by"] = "user"
				require.NoError(t, r.client.Update(ctx, defaults))
			case "foreign-aggregation-role":
				object := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "istio-crd-view-" + operatorcontroller.IstioName("").Name, UID: "user-role"}}
				require.NoError(t, r.client.Create(ctx, object))
			}
			require.Error(t, r.uninstallIstio(ctx))
			require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(defaults), &corev1.ConfigMap{}))
		})
	}
}

func TestSailTeardownDoesNotWaitForReplacementOrUnrelatedResources(t *testing.T) {
	ctx := context.Background()
	defaults := teardownTestDefaults()
	other := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "user-settings", Namespace: defaults.Namespace, Finalizers: []string{"test/user-finalizer"}}}
	r := managedProfileTestReconciler(t, defaults, other)
	teardownTestMapper(r)
	r.releaseReader = &fakeHelmReleaseReader{current: teardownTestRelease(defaults)}
	require.NoError(t, r.captureSailTeardown(ctx))
	require.NoError(t, r.client.Delete(ctx, defaults))
	replacement := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: defaults.Name, Namespace: defaults.Namespace, UID: "user-replacement", Finalizers: []string{"test/user-finalizer"}}}
	require.NoError(t, r.client.Create(ctx, replacement))
	require.NoError(t, r.finishSailTeardown(ctx))
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(replacement), &corev1.ConfigMap{}))
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(other), &corev1.ConfigMap{}))
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

func TestSailTeardownManifestSkipsRetainedResources(t *testing.T) {
	ctx := context.Background()
	r := managedProfileTestReconciler(t)
	teardownTestMapper(r)
	retained := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "retained", Namespace: operatorcontroller.DefaultOperandNamespace}}
	require.NoError(t, r.client.Create(ctx, retained))
	reader := &fakeHelmReleaseReader{current: &releasev1.Release{Name: operatorcontroller.IstioName("").Name + "-istiod", Namespace: operatorcontroller.DefaultOperandNamespace, Manifest: strings.Join([]string{
		"apiVersion: v1", "kind: ConfigMap", "metadata:", "  name: retained", "  annotations:", "    helm.sh/resource-policy: keep", "---", "apiVersion: v1", "kind: ConfigMap", "metadata:", "  name: absent",
	}, "\n")}}
	r.releaseReader = reader
	require.NoError(t, r.uninstallIstio(ctx))
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(retained), &corev1.ConfigMap{}))
}

func TestSailTeardownWaitsForActualAggregationRole(t *testing.T) {
	ctx := context.Background()
	role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "istio-crd-view-" + operatorcontroller.IstioName("").Name, UID: "owned-role", Labels: map[string]string{"managed-by": "sail-library"}, Finalizers: []string{"test/role-delay"}}}
	r := managedProfileTestReconciler(t, role)
	installer := r.sailInstaller.(*fakeSailInstaller)
	installer.onUninstall = func() error { return r.client.Delete(ctx, role) }
	require.ErrorContains(t, r.uninstallIstio(ctx), "waiting for CIO Istio release resource ClusterRole")
	restarted := &reconciler{config: r.config, client: r.client, releaseReader: &fakeHelmReleaseReader{}, sailInstaller: &fakeSailInstaller{}}
	require.ErrorContains(t, restarted.uninstallIstio(ctx), "waiting for CIO Istio release resource ClusterRole")
	var current rbacv1.ClusterRole
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(role), &current))
	current.Finalizers = nil
	require.NoError(t, r.client.Update(ctx, &current))
	require.NoError(t, restarted.uninstallIstio(ctx))
}
