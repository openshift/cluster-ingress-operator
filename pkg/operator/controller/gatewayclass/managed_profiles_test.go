package gatewayclass

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	retryable "github.com/openshift/cluster-ingress-operator/pkg/util/retryableerror"
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
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// managedProfileTestClass builds a labeled profile or a finalized, user-created OpenShift class.
func managedProfileTestClass(name string, managed bool) *gatewayapiv1.GatewayClass {
	class := &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name)},
		Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName},
	}
	if managed {
		class.Labels = map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}
	} else {
		class.Finalizers = []string{sailLibraryFinalizer}
	}
	return class
}

// TestManagedProfileEnsureCases checks desired presence independently from defaults readiness.
func TestManagedProfileEnsureCases(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		want, have      bool
		defaults        bool
		expectedPresent bool
	}{
		{name: "absent unwanted"},
		{name: "present unwanted", have: true},
		{name: "absent wanted", want: true, defaults: true, expectedPresent: true},
		{name: "present wanted", want: true, have: true, defaults: true, expectedPresent: true},
		{name: "absent wanted without defaults", want: true},
		{name: "present wanted without defaults", want: true, have: true, expectedPresent: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// Seed only the resources present in this have/want case.
			ctx := context.Background()
			name := operatorcontroller.OpenShiftClusterIPGatewayClassName
			var objects []client.Object
			if scenario.have {
				objects = append(objects, managedProfileTestClass(name, true))
			}
			if scenario.defaults {
				objects = append(objects, teardownTestConfigMapDefaults())
			}
			r := managedProfileTestReconciler(t, objects...)

			// Check desired presence and defaults readiness independently.
			result, err := r.ensureManagedProfile(ctx, name, scenario.want)
			require.NoError(t, err)
			require.Equal(t, scenario.want && !scenario.defaults, result.RequeueAfter > 0)
			have, profile, err := r.currentManagedProfile(ctx, name)
			require.NoError(t, err)
			require.Equal(t, scenario.expectedPresent, have)
			if have {
				condition := meta.FindStatusCondition(profile.Status.Conditions, "openshift.io/Configured")
				require.NotNil(t, condition)
				require.Equal(t, scenario.defaults, condition.Status == metav1.ConditionTrue)
			}
		})
	}
}

// TestManagedProfileCachedLists keeps eligibility decisions on the controller cache, not direct Lists.
func TestManagedProfileCachedLists(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleting=%t", deleting), func(t *testing.T) {
			ctx := context.Background()
			user := managedProfileTestClass("user", false)
			r := managedProfileTestReconciler(t, user, teardownTestConfigMapDefaults())
			base := r.client.(client.WithWatch)
			r.client = interceptor.NewClient(base, interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				t.Fatal("eligibility must use the cache, not a direct List")
				return nil
			}})
			request := reconcile.Request{NamespacedName: types.NamespacedName{Name: operatorcontroller.OpenShiftClusterIPGatewayClassName}}

			// Both profile eligibility and user-created GatewayClass deletion must use the cached list.
			if deleting {
				require.NoError(t, base.Delete(ctx, user))
				request.NamespacedName = client.ObjectKeyFromObject(user)
			}
			_, err := r.Reconcile(ctx, request)
			require.NoError(t, err)
			require.Equal(t, deleting, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
		})
	}
}

// TestManagedProfileCachedObservations retries deletion until the cache observes absence.
func TestManagedProfileCachedObservations(t *testing.T) {
	for _, kind := range []string{"class", "defaults"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
			defaults := teardownTestConfigMapDefaults()
			r := managedProfileTestReconciler(t, profile, defaults)
			base := r.client
			snapshot := fake.NewClientBuilder().WithScheme(base.Scheme()).WithObjects(profile.DeepCopy(), defaults.DeepCopy()).Build()
			r.cache = &testutil.FakeCache{Reader: snapshot}
			remove := func() error {
				if kind == "class" {
					_, err := r.ensureManagedProfile(ctx, profile.Name, false)
					return err
				}
				return r.deleteManagedProfileConfigMapDefaults(ctx, profile.Name)
			}

			// Repeated controller passes remain pending until the cache catches up.
			for range 2 {
				var pending retryable.Error
				require.ErrorAs(t, remove(), &pending)
				require.Equal(t, 5*time.Second, pending.After())
			}
			r.cache = &testutil.FakeCache{Reader: base}
			require.NoError(t, remove())
		})
	}
}

// TestManagedProfileCreationBeforeCacheVisibility initializes status without a cached class yet.
func TestManagedProfileCreationBeforeCacheVisibility(t *testing.T) {
	ctx := context.Background()
	defaults := teardownTestConfigMapDefaults()
	r := managedProfileTestReconciler(t, defaults)
	base := r.client.(client.WithWatch)
	r.cache = &testutil.FakeCache{Reader: fake.NewClientBuilder().WithScheme(base.Scheme()).WithObjects(defaults.DeepCopy()).Build()}

	// Initial status uses one current read rather than waiting for cache visibility.
	statusReads := 0
	r.client = interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
		_, isClass := object.(*gatewayapiv1.GatewayClass)
		require.True(t, isClass, "ordinary defaults reads must use the cache")
		statusReads++
		return delegate.Get(ctx, key, object, options...)
	}})
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	_, err := r.ensureManagedProfile(ctx, name, true)
	require.NoError(t, err)
	require.Equal(t, 1, statusReads)
	var profile gatewayapiv1.GatewayClass
	require.NoError(t, base.Get(ctx, types.NamespacedName{Name: name}, &profile))
	require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))
}

// TestManagedProfileCreationStatusReadRequeues retries a transient read miss through the controller queue.
func TestManagedProfileCreationStatusReadRequeues(t *testing.T) {
	// The cache can observe the create while the first status read transiently misses it.
	r := managedProfileTestReconciler(t, teardownTestConfigMapDefaults())
	base := r.client.(client.WithWatch)
	statusReads := 0
	r.client = interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
		statusReads++
		if statusReads == 1 {
			return apierrors.NewNotFound(gatewayapiv1.Resource("gatewayclasses"), key.Name)
		}
		return delegate.Get(ctx, key, object, options...)
	}})

	// Creation succeeds, but missing initial status explicitly requests another pass.
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	result, err := r.ensureManagedProfile(t.Context(), name, true)
	require.NoError(t, err)
	require.Positive(t, result.RequeueAfter)
	require.Equal(t, 1, statusReads)
	var profile gatewayapiv1.GatewayClass
	require.NoError(t, base.Get(t.Context(), types.NamespacedName{Name: name}, &profile))
	require.Empty(t, profile.Status.Conditions)

	// The next invocation initializes status instead of treating the read miss as success.
	_, err = r.ensureManagedProfile(t.Context(), name, true)
	require.NoError(t, err)
	require.Equal(t, 2, statusReads)
	require.NoError(t, base.Get(t.Context(), types.NamespacedName{Name: name}, &profile))
	require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))
}

// TestManagedProfileCreationAlreadyExistsRequeues tolerates an earlier create missing from the cache.
func TestManagedProfileCreationAlreadyExistsRequeues(t *testing.T) {
	// The API already has the profile, but the cache initially contains only defaults.
	defaults := teardownTestConfigMapDefaults()
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	r := managedProfileTestReconciler(t, profile, defaults)
	freshCache := r.cache
	r.cache = &testutil.FakeCache{Reader: fake.NewClientBuilder().WithScheme(r.client.Scheme()).WithObjects(defaults.DeepCopy()).Build()}
	_, err := r.ensureManagedProfile(t.Context(), profile.Name, true)
	require.True(t, apierrors.IsAlreadyExists(err))

	// After outer requeue and cache catch-up, initialize status on that same class.
	r.cache = freshCache
	_, err = r.ensureManagedProfile(t.Context(), profile.Name, true)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(t.Context(), client.ObjectKeyFromObject(profile), profile))
	require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))
}

// TestManagedProfileStatusReadFailureReturnsToQueue preserves real API failures without local retries.
func TestManagedProfileStatusReadFailureReturnsToQueue(t *testing.T) {
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	r := managedProfileTestReconciler(t, profile, teardownTestConfigMapDefaults())
	reads := 0
	r.client = interceptor.NewClient(r.client.(client.WithWatch), interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		reads++
		return apierrors.NewServiceUnavailable("status read unavailable")
	}})

	// Cached resource reads succeed; the single fresh status read returns its real error.
	_, err := r.ensureManagedProfile(t.Context(), profile.Name, true)
	require.True(t, apierrors.IsServiceUnavailable(err))
	require.Equal(t, 1, reads)
}

// TestManagedProfileStatusConflictReturnsToQueue makes one status attempt per invocation.
func TestManagedProfileStatusConflictReturnsToQueue(t *testing.T) {
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	r := managedProfileTestReconciler(t, profile)
	patches := 0
	wrapped := interceptor.NewClient(r.client.(client.WithWatch), interceptor.Funcs{SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
		patches++
		return apierrors.NewConflict(gatewayapiv1.Resource("gatewayclasses"), profile.Name, fmt.Errorf("persistent status conflict"))
	}})
	err := updateManagedProfileStatus(t.Context(), wrapped, profile.Name, metav1.ConditionTrue, "Configured", "defaults ready", nil)
	require.True(t, apierrors.IsConflict(err))
	require.Equal(t, 1, patches)
	require.Empty(t, profile.Status.Conditions)
}

// TestManagedProfileDefaultsLabelErrors identifies each unexpected or missing ownership label.
func TestManagedProfileDefaultsLabelErrors(t *testing.T) {
	for _, label := range []string{operatorcontroller.GatewayClassDefaultsLabel, "managed-by", "release"} {
		for _, value := range []string{"", "unexpected"} {
			t.Run(label+"="+value, func(t *testing.T) {
				defaults := teardownTestConfigMapDefaults()
				expected := defaults.Labels[label]
				if value == "" {
					delete(defaults.Labels, label)
				} else {
					defaults.Labels[label] = value
				}
				r := managedProfileTestReconciler(t, defaults)
				err := r.deleteManagedProfileConfigMapDefaults(t.Context(), operatorcontroller.OpenShiftClusterIPGatewayClassName)
				require.ErrorContains(t, err, fmt.Sprintf("label %q is %q, expected %q", label, value, expected))
				require.ErrorContains(t, err, client.ObjectKeyFromObject(defaults).String())
				require.NoError(t, r.client.Get(t.Context(), client.ObjectKeyFromObject(defaults), &corev1.ConfigMap{}))
			})
		}
	}
}

// TestManagedProfileSharedRequestsIncludeMissingDefaults checks readiness wakeups without extra ConfigMap reads.
func TestManagedProfileSharedRequestsIncludeMissingDefaults(t *testing.T) {
	ctx := context.Background()
	r := managedProfileTestReconciler(t)
	profileRequest := reconcile.Request{NamespacedName: types.NamespacedName{Name: operatorcontroller.OpenShiftClusterIPGatewayClassName}}
	require.Equal(t, []reconcile.Request{profileRequest}, r.requestsForAllManagedGatewayClasses(ctx, nil))
	_, err := r.Reconcile(ctx, profileRequest)
	require.NoError(t, err)
	require.Nil(t, r.sailInstaller.(*fakeSailInstaller).internalOpts.Values)
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
	r.config.GatewayAPIWithoutOLMEnabled = false
	require.Empty(t, r.requestsForAllManagedGatewayClasses(ctx, nil))
}

// TestManagedProfileDescriptions repairs descriptions for matching identities regardless of their output label.
func TestManagedProfileDescriptions(t *testing.T) {
	for _, labeled := range []bool{false, true} {
		t.Run(fmt.Sprintf("labeled=%t", labeled), func(t *testing.T) {
			ctx := context.Background()
			profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
			if !labeled {
				profile.Labels = nil
			}
			r := managedProfileTestReconciler(t, profile, teardownTestConfigMapDefaults())

			_, err := r.ensureManagedProfile(ctx, profile.Name, true)
			require.NoError(t, err)
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), profile))
			require.NotNil(t, profile.Spec.Description)
			require.Equal(t, managedProfiles[profile.Name], *profile.Spec.Description)
			require.Equal(t, "true", profile.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
		})
	}
}

// TestManagedProfileConfigMapDefaultsWatch checks exact-name watches, including label removal.
func TestManagedProfileConfigMapDefaultsWatch(t *testing.T) {
	r := managedProfileTestReconciler(t)
	watch := predicate.NewPredicateFuncs(r.isManagedProfileConfigMapDefaults)
	defaults := teardownTestConfigMapDefaults()
	require.True(t, watch.Create(event.CreateEvent{Object: defaults}))
	require.True(t, watch.Delete(event.DeleteEvent{Object: defaults}))

	withoutLabels := defaults.DeepCopy()

	// Association-label removal must still enqueue the exact profile key.
	withoutLabels.Labels = nil
	require.True(t, watch.Update(event.UpdateEvent{ObjectOld: defaults, ObjectNew: withoutLabels}))
	require.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Name: operatorcontroller.OpenShiftClusterIPGatewayClassName}}}, r.requestsForManagedProfileConfigMapDefaults(context.Background(), withoutLabels))

	for _, key := range []types.NamespacedName{
		{Namespace: "other", Name: defaults.Name},
		{Namespace: defaults.Namespace, Name: "unrelated"},
		{Namespace: defaults.Namespace, Name: "istio-openshift-gatewayclass-openshift-default"},
	} {
		require.False(t, watch.Create(event.CreateEvent{Object: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, Labels: defaults.Labels}}}))
	}
}

// TestManagedProfileRequestsBootstrapMissingClasses checks defaults-driven creation, startup replay, and Managed wakeups.
func TestManagedProfileRequestsBootstrapMissingClasses(t *testing.T) {
	for _, scenario := range []string{"new defaults", "existing defaults", "Managed wakeup"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			user := managedProfileTestClass("user", false)
			r := managedProfileTestReconciler(t, user)
			profileRequest := reconcile.Request{NamespacedName: types.NamespacedName{Name: operatorcontroller.OpenShiftClusterIPGatewayClassName}}
			defaults := teardownTestConfigMapDefaults()
			if scenario != "new defaults" {
				require.NoError(t, r.client.Create(ctx, defaults))
			}
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer queue.ShutDown()

			// User events enqueue only the user; its Apply includes the profile defaults.
			r.enqueueRequestForSomeGatewayClass().Create(ctx, event.CreateEvent{Object: user}, queue)
			require.Equal(t, 1, queue.Len())
			request, _ := queue.Get()
			require.Equal(t, user.Name, request.Name)
			_, err := r.Reconcile(ctx, request)
			require.NoError(t, err)
			queue.Done(request)
			require.NotNil(t, r.sailInstaller.(*fakeSailInstaller).internalOpts.Values)
			if scenario == "new defaults" {
				require.Contains(t, r.requestsForAllManagedGatewayClasses(ctx, nil), profileRequest)
				result, err := r.Reconcile(ctx, profileRequest)
				require.NoError(t, err)
				require.NotZero(t, result.RequeueAfter)
				require.True(t, apierrors.IsNotFound(r.client.Get(ctx, profileRequest.NamespacedName, &gatewayapiv1.GatewayClass{})))
				require.NoError(t, r.client.Create(ctx, defaults))
			}

			// ConfigMap watch replay also covers pre-existing defaults with a missing class.
			mapper := handler.EnqueueRequestsFromMapFunc(r.requestsForManagedProfileConfigMapDefaults)
			if scenario == "Managed wakeup" {
				r.modeAccessor = operatorcontroller.NewGatewayAPIModeAccessor(true)
				r.modeAccessor.SetCRDsEstablished(true)
				r.modeAccessor.BlockDependents()
				require.False(t, r.modeAccessor.DependentPredicate().Create(event.CreateEvent{Object: defaults}))
				_, err := r.Reconcile(ctx, profileRequest)
				require.NoError(t, err)
				r.modeAccessor.Update(operatorv1alpha1.GatewayAPIManagementModeManaged, true, true, true)
				require.Contains(t, r.requestsForAllManagedGatewayClasses(ctx, nil), profileRequest)
			}
			mapper.Create(ctx, event.CreateEvent{Object: defaults}, queue)
			require.Equal(t, 1, queue.Len())
			request, _ = queue.Get()
			require.Equal(t, profileRequest, request)
			_, err = r.Reconcile(ctx, request)
			require.NoError(t, err)
			queue.Done(request)
			var profile gatewayapiv1.GatewayClass
			require.NoError(t, r.client.Get(ctx, profileRequest.NamespacedName, &profile))
			require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))
			require.Empty(t, profile.Finalizers)

			// Manual deletion queues that same key and recreates the profile without Apply.
			require.NoError(t, r.client.Delete(ctx, &profile))
			r.enqueueRequestForSomeGatewayClass().Delete(ctx, event.DeleteEvent{Object: &profile}, queue)
			require.Equal(t, 1, queue.Len())
			request, _ = queue.Get()
			require.Equal(t, profileRequest, request)
			_, err = r.Reconcile(ctx, request)
			require.NoError(t, err)
			queue.Done(request)
			require.NoError(t, r.client.Get(ctx, profileRequest.NamespacedName, &profile))
			require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))

			profile.Spec.ControllerName = "example.com/other"
			r.enqueueRequestForSomeGatewayClass().Delete(ctx, event.DeleteEvent{Object: &profile}, queue)
			require.Equal(t, 1, queue.Len())
			request, _ = queue.Get()
			require.Equal(t, profileRequest, request)
			queue.Done(request)
		})
	}
}

// TestManagedProfileClassLookup distinguishes missing classes from cached API failures.
func TestManagedProfileClassLookup(t *testing.T) {
	for _, scenario := range []struct {
		name, className string
		failRead        bool
	}{
		{name: "missing profile", className: operatorcontroller.OpenShiftClusterIPGatewayClassName},
		{name: "missing user-created class", className: "missing-user"},
		{name: "profile read failure", className: operatorcontroller.OpenShiftClusterIPGatewayClassName, failRead: true},
		{name: "user-created class read failure", className: "missing-user", failRead: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// An existing user-created class enables profile creation, not arbitrary missing names.
			r := managedProfileTestReconciler(t, managedProfileTestClass("openshift-default", false), teardownTestConfigMapDefaults())
			base := r.client.(client.WithWatch)
			cached := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
				if scenario.failRead && key.Name == scenario.className {
					return apierrors.NewServiceUnavailable("class lookup unavailable")
				}
				return delegate.Get(ctx, key, object, options...)
			}})
			r.cache = &testutil.FakeCache{Reader: cached}

			// Only a registered missing profile reaches creation; real read errors reach the outer queue.
			_, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Name: scenario.className}})
			if scenario.failRead {
				require.True(t, apierrors.IsServiceUnavailable(err))
			} else {
				require.NoError(t, err)
			}
			var profile gatewayapiv1.GatewayClass
			err = base.Get(t.Context(), types.NamespacedName{Name: scenario.className}, &profile)
			if !scenario.failRead && isManagedProfileName(scenario.className) {
				require.NoError(t, err)
				require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))
			} else {
				require.True(t, apierrors.IsNotFound(err))
			}
			require.Nil(t, r.sailInstaller.(*fakeSailInstaller).internalOpts.Values)
			require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
		})
	}
}

// TestManagedProfileMissingKeyCannotInstall checks synthetic requests without any user class.
func TestManagedProfileMissingKeyCannotInstall(t *testing.T) {
	r := managedProfileTestReconciler(t, teardownTestConfigMapDefaults())
	for _, request := range r.requestsForAllManagedGatewayClasses(context.Background(), nil) {
		_, err := r.Reconcile(context.Background(), request)
		require.NoError(t, err)
	}
	require.Nil(t, r.sailInstaller.(*fakeSailInstaller).internalOpts.Values)
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
	var classes gatewayapiv1.GatewayClassList
	require.NoError(t, r.client.List(context.Background(), &classes))
	require.Empty(t, classes.Items)
}

// TestManagedProfileCleanupStopsSailBeforeDefaultsDeletion checks repair, retries, and finalizer ordering.
func TestManagedProfileCleanupStopsSailBeforeDefaultsDeletion(t *testing.T) {
	ctx := context.Background()
	user := managedProfileTestClass("user", false)
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	defaults := teardownTestConfigMapDefaults()
	r := managedProfileTestReconciler(t, user, profile, defaults)
	base := r.client.(client.WithWatch)
	enabled := true
	failUninstall := true
	installer := r.sailInstaller.(*fakeSailInstaller)

	// Sail can replace defaults during teardown, but must stop before defaults cleanup.
	installer.onUninstall = func() error {
		require.True(t, apierrors.IsNotFound(base.Get(ctx, client.ObjectKeyFromObject(profile), &gatewayapiv1.GatewayClass{})))
		require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(defaults), &corev1.ConfigMap{}))
		if failUninstall {
			return fmt.Errorf("uninstall failed")
		}
		enabled = false
		require.NoError(t, base.Delete(ctx, defaults))
		replacement := teardownTestConfigMapDefaults()
		replacement.UID = "post-uninstall-replacement"
		return base.Create(ctx, replacement)
	}
	deletedDefaults := false

	// Model Sail's repair behavior while it still owns a running installation.
	r.client = interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, delegate client.WithWatch, object client.Object, options ...client.DeleteOption) error {
		if _, ok := object.(*corev1.ConfigMap); !ok {
			return delegate.Delete(ctx, object, options...)
		}
		if err := delegate.Delete(ctx, object, options...); err != nil {
			return err
		}
		if enabled {
			replacement := teardownTestConfigMapDefaults()
			replacement.UID = "sail-repair"
			return delegate.Create(ctx, replacement)
		}
		require.EqualValues(t, "post-uninstall-replacement", object.GetUID())
		deletedDefaults = true
		return nil
	}})

	// Deleting defaults alone cannot finish while Sail is still repairing them.
	require.ErrorContains(t, r.deleteManagedProfileConfigMapDefaults(ctx, profile.Name), "waiting for ConfigMap defaults")
	require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(defaults), &corev1.ConfigMap{}))
	require.NoError(t, base.Delete(ctx, user))
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(user)}
	_, err := r.Reconcile(ctx, request)
	require.ErrorContains(t, err, "uninstall failed")
	require.False(t, deletedDefaults)
	require.NoError(t, base.Get(ctx, request.NamespacedName, user))
	require.Contains(t, user.Finalizers, sailLibraryFinalizer)

	// A later successful uninstall allows defaults removal and releases the user.
	failUninstall = false
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.True(t, deletedDefaults)
	require.True(t, apierrors.IsNotFound(base.Get(ctx, request.NamespacedName, &gatewayapiv1.GatewayClass{})))
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
}

// TestManagedProfileUnmanagedDeletion completes explicit user deletion while profile updates stay gated.
func TestManagedProfileUnmanagedDeletion(t *testing.T) {
	ctx := context.Background()
	user := managedProfileTestClass("user", false)
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	profile.Labels = nil
	profile.Finalizers = []string{"example.com/profile-in-use"}
	r := managedProfileTestReconciler(t, user, profile, teardownTestConfigMapDefaults())
	r.modeAccessor = operatorcontroller.NewGatewayAPIModeAccessor(true)
	r.modeAccessor.SetCRDsEstablished(true)
	r.modeAccessor.Update(operatorv1alpha1.GatewayAPIManagementModeUnmanaged, false, true, true)
	r.modeAccessor.BlockDependents()

	// Operational profile requests do not repair metadata or enable installation.
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)})
	require.NoError(t, err)
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), profile))
	require.Empty(t, profile.Labels)

	// Explicit user-created GatewayClass deletion requests profile cleanup before the mode gate.
	require.NoError(t, r.client.Delete(ctx, user))
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(user)}
	result, err := r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.NotZero(t, result.RequeueAfter)
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), profile))
	require.False(t, profile.DeletionTimestamp.IsZero())
	require.Empty(t, profile.Labels)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(user), user))
	require.Contains(t, user.Finalizers, sailLibraryFinalizer)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(teardownTestConfigMapDefaults()), &corev1.ConfigMap{}))

	// An uninstall failure still retains the user finalizer and defaults.
	profile.Finalizers = nil
	require.NoError(t, r.client.Update(ctx, profile))
	installer := r.sailInstaller.(*fakeSailInstaller)
	installer.uninstallError = fmt.Errorf("uninstall unavailable")
	_, err = r.Reconcile(ctx, request)
	require.ErrorContains(t, err, "uninstall unavailable")
	require.NoError(t, r.client.Get(ctx, request.NamespacedName, user))
	require.Contains(t, user.Finalizers, sailLibraryFinalizer)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(teardownTestConfigMapDefaults()), &corev1.ConfigMap{}))

	// A later reconciliation completes class, Sail and defaults cleanup.
	installer.uninstallError = nil
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.True(t, installer.uninstallCalled)
	require.True(t, apierrors.IsNotFound(r.client.Get(ctx, request.NamespacedName, user)))
	require.True(t, apierrors.IsNotFound(r.client.Get(ctx, client.ObjectKeyFromObject(teardownTestConfigMapDefaults()), &corev1.ConfigMap{})))
}

// managedProfileTestReconciler shares a fake API with the cache and installs a controllable Sail client.
func managedProfileTestReconciler(t *testing.T, objects ...client.Object) *reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, register := range []func(*runtime.Scheme) error{corev1.AddToScheme, networkingv1.AddToScheme, rbacv1.AddToScheme, configv1.Install, gatewayapiv1.Install, apiextensionsv1.AddToScheme} {
		require.NoError(t, register(scheme))
	}
	objects = append(objects, &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}})

	// Match the production controller-name index and status subresource behavior.
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&gatewayapiv1.GatewayClass{}).WithIndex(&gatewayapiv1.GatewayClass{}, operatorcontroller.GatewayClassIndexFieldName, func(object client.Object) []string {
		return []string{string(object.(*gatewayapiv1.GatewayClass).Spec.ControllerName)}
	}).Build()
	informers := &informertest.FakeInformers{Scheme: scheme}
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	return &reconciler{client: client, cache: &testutil.FakeCache{Informers: informers, Reader: client}, modeAccessor: mode, config: Config{OperandNamespace: operatorcontroller.DefaultOperandNamespace, GatewayAPIWithoutOLMEnabled: true, IstioVersion: "v1.30.5"}, sailInstaller: &fakeSailInstaller{}}
}

// TestManagedProfileOverlaysPreserveDefaultAndCommonConfiguration checks ClusterIP-only overlays.
func TestManagedProfileOverlaysPreserveDefaultAndCommonConfiguration(t *testing.T) {
	t.Parallel()
	require.Equal(t, map[string]string{
		operatorcontroller.OpenShiftClusterIPGatewayClassName: "OpenShift Gateway using a cluster-internal ClusterIP Service",
	}, managedProfiles)
	classes := withDesiredManagedProfiles([]gatewayapiv1.GatewayClass{*managedProfileTestClass("openshift-default", false)})

	// The profile adds only its Service overlay to common deployment and HPA defaults.
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

// TestManagedProfileConfigMapDefaultsPrecedeCreationAndRepairStatus checks creation ordering and missing/misassociated defaults.
func TestManagedProfileConfigMapDefaultsPrecedeCreationAndRepairStatus(t *testing.T) {
	anchor := managedProfileTestClass("openshift-default", false)
	r := managedProfileTestReconciler(t, anchor)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: anchor.Name}}
	result, err := r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	request.Name = operatorcontroller.OpenShiftClusterIPGatewayClassName
	result, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NotZero(t, result.RequeueAfter)

	// Sail receives defaults for the desired class before the class can be created.
	var values map[string]any
	require.NoError(t, json.Unmarshal(r.sailInstaller.(*fakeSailInstaller).internalOpts.Values.GatewayClasses, &values))
	for _, name := range []string{operatorcontroller.OpenShiftClusterIPGatewayClassName} {
		require.Contains(t, values, name)
		require.True(t, apierrors.IsNotFound(r.client.Get(context.Background(), types.NamespacedName{Name: name}, &gatewayapiv1.GatewayClass{})))
		defaults := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name:      managedProfileConfigMapDefaultsName(r.config.OperandNamespace, name).Name,
			Namespace: r.config.OperandNamespace,
			Labels:    map[string]string{operatorcontroller.GatewayClassDefaultsLabel: name},
		}}
		require.NoError(t, r.client.Create(context.Background(), defaults))
	}

	// Once defaults exist, create the class with its description and openshift.io/Configured condition.
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	var managedProfile gatewayapiv1.GatewayClass
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &managedProfile))
	require.Empty(t, managedProfile.Finalizers)
	require.Equal(t, "true", managedProfile.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
	require.NotNil(t, managedProfile.Spec.Description)
	require.Equal(t, managedProfiles[name], *managedProfile.Spec.Description)
	require.LessOrEqual(t, len(*managedProfile.Spec.Description), 64)
	require.Equal(t, "Configured", meta.FindStatusCondition(managedProfile.Status.Conditions, "openshift.io/Configured").Reason)
	configured := meta.FindStatusCondition(managedProfile.Status.Conditions, "openshift.io/Configured")
	require.Equal(t, metav1.ConditionTrue, configured.Status)
	require.Contains(t, configured.Message, managedProfileConfigMapDefaultsName(r.config.OperandNamespace, name).String())
	require.Contains(t, configured.Message, "associated with GatewayClass "+name)

	// Repair CIO status without changing the installer's Accepted condition.
	managedProfile.Status.Conditions = append(managedProfile.Status.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()})
	meta.SetStatusCondition(&managedProfile.Status.Conditions, metav1.Condition{Type: "openshift.io/Configured", Status: metav1.ConditionFalse, Reason: "Unmanaged", Message: "CIO is not managing customization", LastTransitionTime: metav1.Now()})
	require.NoError(t, r.client.Status().Update(context.Background(), &managedProfile))
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &managedProfile))
	require.Equal(t, "Configured", meta.FindStatusCondition(managedProfile.Status.Conditions, "openshift.io/Configured").Reason)
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(managedProfile.Status.Conditions, "Accepted").Status)

	// Association loss changes openshift.io/Configured without deleting the existing class.
	var defaults corev1.ConfigMap
	require.NoError(t, r.client.Get(context.Background(), managedProfileConfigMapDefaultsName(r.config.OperandNamespace, name), &defaults))
	defaults.Labels = nil
	require.NoError(t, r.client.Update(context.Background(), &defaults))
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &managedProfile))
	require.Equal(t, "ConfigurationMissing", meta.FindStatusCondition(managedProfile.Status.Conditions, "openshift.io/Configured").Reason)
	missing := meta.FindStatusCondition(managedProfile.Status.Conditions, "openshift.io/Configured")
	require.Equal(t, metav1.ConditionFalse, missing.Status)
	require.Contains(t, missing.Message, managedProfileConfigMapDefaultsName(r.config.OperandNamespace, name).String())
	require.Contains(t, missing.Message, "not associated with GatewayClass "+name)
	require.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(managedProfile.Status.Conditions, "Accepted").Status)

	// Restoring the selection label makes the same profile configured again.
	defaults.Labels = map[string]string{operatorcontroller.GatewayClassDefaultsLabel: name}
	require.NoError(t, r.client.Update(context.Background(), &defaults))
	_, err = r.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: name}, &managedProfile))
	require.Equal(t, "Configured", meta.FindStatusCondition(managedProfile.Status.Conditions, "openshift.io/Configured").Reason)
}

// TestManagedProfileKeyedReconciliationRestoresMissingResources checks missing class and ConfigMap event repair.
func TestManagedProfileKeyedReconciliationRestoresMissingResources(t *testing.T) {
	ctx := context.Background()
	anchor := managedProfileTestClass("openshift-default", false)
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	profile.Annotations = map[string]string{"example.com/user-note": "not a desired profile output"}
	defaults := teardownTestConfigMapDefaults()
	r := managedProfileTestReconciler(t, anchor, profile, defaults)
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)}

	// Recreate only the desired profile outputs, never unrelated annotations.
	require.NoError(t, r.client.Delete(ctx, profile))
	_, err := r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), profile))
	require.Empty(t, profile.Annotations)
	require.Equal(t, "true", profile.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
	require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))

	// A defaults event can report missing configuration, then recover after replacement.
	defaults.Labels = nil
	require.NoError(t, r.client.Update(ctx, defaults))
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), profile))
	require.Equal(t, "ConfigurationMissing", meta.FindStatusCondition(profile.Status.Conditions, "openshift.io/Configured").Reason)
	require.NoError(t, r.client.Delete(ctx, defaults))
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
	defaults.ResourceVersion = ""
	defaults.Labels = map[string]string{operatorcontroller.GatewayClassDefaultsLabel: profile.Name}
	require.NoError(t, r.client.Create(ctx, defaults))
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), profile))
	require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))
}

// TestManagedProfilesNeverEnableInstallationOrTakeOverConflicts checks profile-only and preexisting-class safety.
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
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: conflict.Name}})
		require.ErrorContains(t, err, "cannot update GatewayClass")
		var current gatewayapiv1.GatewayClass
		require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: conflict.Name}, &current))
		require.Equal(t, conflict.Spec, current.Spec)
	})
	t.Run("pre-existing unlabeled matching profile", func(t *testing.T) {
		anchor := managedProfileTestClass("openshift-default", false)
		managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
		managedProfile.Labels = nil
		r := managedProfileTestReconciler(t, anchor, managedProfile)
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(managedProfile)})
		require.NoError(t, err)
		var current gatewayapiv1.GatewayClass
		require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Name: managedProfile.Name}, &current))
		require.Equal(t, "true", current.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
		require.True(t, meta.IsStatusConditionFalse(current.Status.Conditions, "openshift.io/Configured"))
	})
}

// TestFinalAnchorWaitsForManagedProfileAndIstioCleanup holds the user's finalizer through profile and Sail teardown.
func TestFinalAnchorWaitsForManagedProfileAndIstioCleanup(t *testing.T) {
	ctx := context.Background()
	anchor := managedProfileTestClass("openshift-default", false)
	managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	managedProfile.Finalizers = []string{"gateway.networking.k8s.io/gateway-exists-finalizer"}
	managedProfile.Labels = nil
	r := managedProfileTestReconciler(t, anchor, managedProfile)
	require.NoError(t, r.client.Delete(ctx, anchor))
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: anchor.Name}}

	// A terminating profile keeps the user-created GatewayClass finalizer and defers Sail uninstall.
	var pending retryable.Error
	require.ErrorAs(t, r.ensureGatewayClassDeleted(ctx, anchor), &pending)
	require.Equal(t, 5*time.Second, pending.After())
	result, err := r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.NotZero(t, result.RequeueAfter)
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, r.client.Get(ctx, request.NamespacedName, &current))
	require.Contains(t, current.Finalizers, sailLibraryFinalizer)
	require.NoError(t, r.client.Get(ctx, types.NamespacedName{Name: managedProfile.Name}, &current))

	// Once the profile is absent, uninstall failures must still retain the user.
	current.Finalizers = nil
	require.NoError(t, r.client.Update(ctx, &current))
	r.sailInstaller.(*fakeSailInstaller).uninstallError = fmt.Errorf("teardown failed")
	_, err = r.Reconcile(ctx, request)
	require.ErrorContains(t, err, "teardown failed")
	require.NoError(t, r.client.Get(ctx, request.NamespacedName, &current))
	require.Contains(t, current.Finalizers, sailLibraryFinalizer)
	r.sailInstaller.(*fakeSailInstaller).uninstallError = nil

	// Only a successful later pass releases the user-created GatewayClass finalizer.
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(r.client.Get(ctx, request.NamespacedName, &current)))
}

// teardownTestConfigMapDefaults builds ConfigMap defaults labeled for the CIO Sail release.
func teardownTestConfigMapDefaults() *corev1.ConfigMap {
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      managedProfileConfigMapDefaultsName(operatorcontroller.DefaultOperandNamespace, name).Name,
		Namespace: operatorcontroller.DefaultOperandNamespace, UID: "defaults-uid",
		Labels:      map[string]string{operatorcontroller.GatewayClassDefaultsLabel: name, "managed-by": "sail-library", "release": operatorcontroller.IstioName("").Name + "-istiod"},
		Annotations: map[string]string{"meta.helm.sh/release-name": operatorcontroller.IstioName("").Name + "-istiod", "meta.helm.sh/release-namespace": operatorcontroller.DefaultOperandNamespace},
	}}
}

// TestUninstallSailWithoutLibraryIsNoOp preserves the legacy OLM path.
func TestUninstallSailWithoutLibraryIsNoOp(t *testing.T) {
	r := &reconciler{}
	require.NoError(t, r.UninstallSail(context.Background()))
}

// TestManagedProfileCleanupPreservesForeignClasses checks registered name and controller boundaries.
func TestManagedProfileCleanupPreservesForeignClasses(t *testing.T) {
	for _, scenario := range []string{"nonregistered", "other-controller"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
			if scenario == "nonregistered" {
				managedProfile.Name = "openshift-not-registered"
			}
			if scenario == "other-controller" {
				managedProfile.Spec.ControllerName = "example.com/other"
			}
			r := managedProfileTestReconciler(t, managedProfile)

			// Neither a nonregistered name nor a foreign controller is a deletion target.
			result, err := r.ensureManagedProfile(ctx, managedProfile.Name, false)
			require.NoError(t, err)
			require.Zero(t, result.RequeueAfter)
			var current gatewayapiv1.GatewayClass
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
			require.True(t, current.DeletionTimestamp.IsZero())
			require.Equal(t, managedProfile.UID, current.UID)
		})
	}
}

// TestConfigMapDefaultsCleanupFailuresHoldAnchorFinalizer checks pending deletion, API failures, and foreign defaults.
func TestConfigMapDefaultsCleanupFailuresHoldAnchorFinalizer(t *testing.T) {
	for _, failure := range []string{"delay", "get-error", "delete-error", "foreign"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			anchor := managedProfileTestClass("openshift-default", false)
			defaults := teardownTestConfigMapDefaults()
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

			// Inject failures at each defaults cleanup operation, not at unrelated reads.
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
			r.cache = &testutil.FakeCache{Reader: r.client}

			// Pending and failed cleanup both keep the user-created GatewayClass finalizer.
			require.NoError(t, base.Delete(ctx, anchor))
			request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)}
			result, err := r.Reconcile(ctx, request)
			if failure == "delay" {
				require.NoError(t, err)
				require.Equal(t, 5*time.Second, result.RequeueAfter)
			} else {
				require.Error(t, err)
			}
			require.True(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
			var current gatewayapiv1.GatewayClass
			require.NoError(t, base.Get(ctx, request.NamespacedName, &current))
			require.Contains(t, current.Finalizers, sailLibraryFinalizer)
			if failure == "foreign" {
				require.False(t, deleted)
				require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(defaults), &corev1.ConfigMap{}))
				return
			}

			// A later pass can finish only after the transient failure or finalizer clears.
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

// TestManagedProfileClassDeleteErrorsRetry prevents uninstall until a failed profile deletion succeeds.
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

	// A failed class delete must not advance to uninstall or release the user.
	require.NoError(t, base.Delete(ctx, anchor))
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)}
	_, err := r.Reconcile(ctx, request)
	require.ErrorContains(t, err, "managed class deletion failed")
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, base.Get(ctx, request.NamespacedName, &current))
	require.Contains(t, current.Finalizers, sailLibraryFinalizer)

	// Retry through a separate reconciliation, not a loop inside the helper.
	fail = false
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(base.Get(ctx, request.NamespacedName, &current)))
}

// TestManagedProfileCleanupRejectsUIDReplacement checks delete preconditions and foreign replacements.
func TestManagedProfileCleanupRejectsUIDReplacement(t *testing.T) {
	for _, kind := range []string{"class", "defaults"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			anchor := managedProfileTestClass("openshift-default", false)
			var original client.Object = managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
			if kind == "defaults" {
				original = teardownTestConfigMapDefaults()
			}
			r := managedProfileTestReconciler(t, anchor, original)
			base := r.client.(client.WithWatch)
			replaced := false

			// Replace the cached UID just before deletion and reject that stale attempt.
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
					if class, ok := replacement.(*gatewayapiv1.GatewayClass); ok {
						class.Spec.ControllerName = "example.com/other"
					}
					require.NoError(t, delegate.Create(ctx, replacement))
					replaced = true
					return apierrors.NewConflict(gatewayapiv1.Resource("gatewayclasses"), object.GetName(), fmt.Errorf("UID precondition failed"))
				}
				return delegate.Delete(ctx, object, options...)
			}})

			// The next pass must preserve the replacement's foreign identity or labels.
			require.NoError(t, base.Delete(ctx, anchor))
			request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)}
			_, err := r.Reconcile(ctx, request)
			require.ErrorContains(t, err, "UID precondition failed")
			require.Equal(t, kind == "defaults", r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
			_, err = r.Reconcile(ctx, request)
			if kind == "defaults" {
				require.ErrorContains(t, err, "label")
				require.True(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
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

// TestManagedProfileCleanupCompletesOnSailUninstallSuccess does not wait for unrelated release garbage collection.
func TestManagedProfileCleanupCompletesOnSailUninstallSuccess(t *testing.T) {
	ctx := context.Background()
	anchor := managedProfileTestClass("openshift-default", false)
	defaults := teardownTestConfigMapDefaults()
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "istiod", Namespace: operatorcontroller.DefaultOperandNamespace, Labels: map[string]string{"managed-by": "sail-library"}, Finalizers: []string{"test/delayed-release-gc"}}}
	r := managedProfileTestReconciler(t, anchor, defaults, service)
	require.NoError(t, r.client.Delete(ctx, service))
	installer := r.sailInstaller.(*fakeSailInstaller)
	installer.onUninstall = func() error {
		require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(defaults), &corev1.ConfigMap{}))
		return nil
	}

	// Unrelated release garbage collection must not hold the user's finalizer.
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

// TestManagedProfileManagedRecoveryUsesApplyWithoutRestart restores Managed mode through normal Sail Apply.
func TestManagedProfileManagedRecoveryUsesApplyWithoutRestart(t *testing.T) {
	ctx := context.Background()
	anchor := managedProfileTestClass("openshift-default", false)
	managedProfile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	managedProfile.Status.Conditions = []metav1.Condition{{Type: "openshift.io/Configured", Status: metav1.ConditionFalse, Reason: "Unmanaged", Message: "CIO is not managing customization", LastTransitionTime: metav1.Now()}}
	r := managedProfileTestReconciler(t, anchor, managedProfile)
	installer := r.sailInstaller.(*fakeSailInstaller)
	require.NoError(t, r.UninstallSail(ctx))

	// Normal Apply after returning to Managed recreates defaults without restarting Sail.
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(anchor)})
	require.NoError(t, err)
	require.NotNil(t, installer.internalOpts.Values)
	require.NoError(t, r.client.Create(ctx, teardownTestConfigMapDefaults()))
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(managedProfile)})
	require.NoError(t, err)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
	require.Equal(t, "Configured", meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured").Reason)
	require.Zero(t, installer.stopCalls)
	require.Zero(t, installer.startCalls)
}

// TestManagedProfilePerClassFinalizerAndReconciliation checks per-user enrollment and profile status without installation.
func TestManagedProfilePerClassFinalizerAndReconciliation(t *testing.T) {
	ctx := context.Background()
	user := managedProfileTestClass("user-class", false)
	user.Labels = map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}
	user.Finalizers = nil
	other := managedProfileTestClass("other-user", false)
	other.Finalizers = nil
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	r := managedProfileTestReconciler(t, user, other, profile)
	installer := r.sailInstaller.(*fakeSailInstaller)

	// Only the reconciled user class gets its finalizer, before any Apply.
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(user)})
	require.NoError(t, err)
	require.Nil(t, installer.internalOpts.Values)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(user), user))
	require.Contains(t, user.Finalizers, sailLibraryFinalizer)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(other), other))
	require.Empty(t, other.Finalizers)

	// Profile-only reconciliation reports status without installation or a Sail finalizer.
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)})
	require.NoError(t, err)
	require.Nil(t, installer.internalOpts.Values)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), profile))
	require.Empty(t, profile.Finalizers)
	require.NotNil(t, meta.FindStatusCondition(profile.Status.Conditions, "ControllerInstalled"))
}

// TestManagedProfileCachedEventsConvergeWithOrdinaryDeletion permits a stale cache before ordered teardown.
func TestManagedProfileCachedEventsConvergeWithOrdinaryDeletion(t *testing.T) {
	ctx := context.Background()
	user := managedProfileTestClass("user-class", false)
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	r := managedProfileTestReconciler(t, user, profile)
	installer := r.sailInstaller.(*fakeSailInstaller)
	freshCache := r.cache
	r.cache = managedProfileTestReconciler(t, user.DeepCopy(), profile.DeepCopy()).cache
	require.NoError(t, r.client.Delete(ctx, user))

	// A queued profile event uses its cached snapshot, which can briefly retain the profile.
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)})
	require.NoError(t, err)
	require.Nil(t, installer.internalOpts.Values)
	require.False(t, installer.uninstallCalled)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), &gatewayapiv1.GatewayClass{}))

	// Once the cache catches up, user-created GatewayClass deletion owns the global teardown sequence.
	r.cache = freshCache
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(user)})
	require.NoError(t, err)
	require.True(t, installer.uninstallCalled)
	require.True(t, apierrors.IsNotFound(r.client.Get(ctx, client.ObjectKeyFromObject(user), &gatewayapiv1.GatewayClass{})))
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)})
	require.NoError(t, err)
	require.Nil(t, installer.internalOpts.Values)
}

func TestManagedProfileLabelIsOutput(t *testing.T) {
	for _, label := range []string{"", "false", "true", "unexpected"} {
		for _, defaultsReady := range []bool{false, true} {
			t.Run(fmt.Sprintf("label=%q/defaults=%t", label, defaultsReady), func(t *testing.T) {
				// Seed label drift alongside user fields and another controller's condition.
				ctx := context.Background()
				profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
				profile.Labels = map[string]string{"example.com/label": "preserved"}
				if label != "" {
					profile.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] = label
				}
				profile.Annotations = map[string]string{"example.com/annotation": "preserved"}
				profile.Spec.ParametersRef = &gatewayapiv1.ParametersReference{Group: "example.com", Kind: "Config", Name: "preserved"}
				profile.Status.Conditions = []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()}}
				objects := []client.Object{profile, managedProfileTestClass("openshift-default", false)}
				if defaultsReady {
					objects = append(objects, teardownTestConfigMapDefaults())
				}
				r := managedProfileTestReconciler(t, objects...)
				before := profile.DeepCopy()

				// Repair owned outputs even when defaults are missing, preserving everything else.
				_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)})
				require.NoError(t, err)
				var current gatewayapiv1.GatewayClass
				require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), &current))
				require.Equal(t, "true", current.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
				require.Equal(t, "preserved", current.Labels["example.com/label"])
				require.Equal(t, profile.Annotations, current.Annotations)
				require.Equal(t, profile.Spec.ParametersRef, current.Spec.ParametersRef)
				require.Equal(t, desiredManagedProfile(profile.Name).Spec.Description, current.Spec.Description)
				require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, "Accepted"))
				require.Equal(t, defaultsReady, meta.IsStatusConditionTrue(current.Status.Conditions, "openshift.io/Configured"))
				require.Empty(t, current.Finalizers)
				require.Equal(t, before, profile)
				require.Nil(t, r.sailInstaller.(*fakeSailInstaller).internalOpts.Values)
			})
		}
	}
}

func TestManagedProfileIdentityIgnoresLabel(t *testing.T) {
	for _, name := range []string{operatorcontroller.OpenShiftClusterIPGatewayClassName, "openshift-default", "openshift-not-registered", "ordinary"} {
		for _, label := range []string{"", "false", "true", "unexpected"} {
			t.Run(name+"/"+label, func(t *testing.T) {
				class := managedProfileTestClass(name, true)
				class.Labels = map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: label}
				managed := name == operatorcontroller.OpenShiftClusterIPGatewayClassName

				// Only registered identity affects installation eligibility; label values do not.
				require.Equal(t, managed, isManagedProfile(class))
				classes := &gatewayapiv1.GatewayClassList{Items: []gatewayapiv1.GatewayClass{*class}}
				expectedCount := 1
				if managed {
					expectedCount = 0
				}
				require.Equal(t, expectedCount, countActiveGatewayClasses(classes, ""))
				class.Spec.ControllerName = "example.com/other"
				require.False(t, isManagedProfile(class))
			})
		}
	}
}

func TestManagedProfileSpoofedLabelRemainsOrdinary(t *testing.T) {
	for _, name := range []string{"openshift-default", "openshift-not-registered"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			ordinary := managedProfileTestClass(name, false)
			ordinary.Finalizers = nil
			ordinary.Labels = map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}
			r := managedProfileTestReconciler(t, ordinary)
			request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(ordinary)}

			// A spoofed output label cannot bypass user-created GatewayClass finalizer-before-Apply behavior.
			_, err := r.Reconcile(ctx, request)
			require.NoError(t, err)
			require.NoError(t, r.client.Get(ctx, request.NamespacedName, ordinary))
			require.Contains(t, ordinary.Finalizers, sailLibraryFinalizer)
			_, err = r.Reconcile(ctx, request)
			require.NoError(t, err)
			require.NotNil(t, r.sailInstaller.(*fakeSailInstaller).internalOpts.Values)
			require.NoError(t, r.client.Get(ctx, request.NamespacedName, ordinary))
			require.Nil(t, ordinary.Spec.Description)
			require.Nil(t, meta.FindStatusCondition(ordinary.Status.Conditions, "openshift.io/Configured"))
		})
	}
}

func TestManagedProfileWrongControllerDriftIsNotRepaired(t *testing.T) {
	for _, label := range []string{"", "false", "true"} {
		t.Run(label, func(t *testing.T) {
			ctx := context.Background()
			profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
			profile.Spec.ControllerName = "example.com/other"
			profile.Labels = map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: label}
			r := managedProfileTestReconciler(t, profile)
			before := profile.DeepCopy()

			// Wanted conflict, unwanted cleanup, and mode status all preserve a foreign class.
			_, err := r.ensureManagedProfile(ctx, profile.Name, true)
			require.ErrorContains(t, err, "conflicting controllerName")
			_, err = r.ensureManagedProfile(ctx, profile.Name, false)
			require.NoError(t, err)
			require.NoError(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, r.client))
			var current gatewayapiv1.GatewayClass
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), &current))
			require.Equal(t, before.Labels, current.Labels)
			require.Equal(t, before.Spec, current.Spec)
			require.Empty(t, current.Status.Conditions)
			require.True(t, current.DeletionTimestamp.IsZero())
		})
	}
}

func TestManagedProfileUnlabeledModeTransition(t *testing.T) {
	ctx := context.Background()
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	profile.Labels = nil
	profile.Status.Conditions = []metav1.Condition{
		{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()},
		{Type: "Customized", Status: metav1.ConditionUnknown, Reason: "OtherOwner", LastTransitionTime: metav1.Now()},
	}
	r := managedProfileTestReconciler(t, profile, managedProfileTestClass("openshift-default", false), teardownTestConfigMapDefaults())

	// The mode transition changes openshift.io/Configured only, even without the output label.
	require.NoError(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, r.client))
	var current gatewayapiv1.GatewayClass
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), &current))
	require.Empty(t, current.Labels)
	require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, "Accepted"))
	require.Equal(t, "Unmanaged", meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured").Reason)
	unmanaged := meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured")
	require.Equal(t, metav1.ConditionFalse, unmanaged.Status)
	require.Contains(t, unmanaged.Message, managedProfileConfigMapDefaultsName(operatorcontroller.DefaultOperandNamespace, profile.Name).String())
	require.Contains(t, unmanaged.Message, "association with GatewayClass "+profile.Name)

	// Resuming Managed reconciliation repairs both metadata and configuration status.
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)})
	require.NoError(t, err)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), &current))
	require.Equal(t, "true", current.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
	require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, "openshift.io/Configured"))
	require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, "Accepted"))
	require.Equal(t, "OtherOwner", meta.FindStatusCondition(current.Status.Conditions, "Customized").Reason)
}

func TestManagedProfileLabelRepairRetriesAndSkipsTerminating(t *testing.T) {
	ctx := context.Background()
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	profile.Labels = nil
	r := managedProfileTestReconciler(t, profile)
	base := r.client.(client.WithWatch)
	patches := 0
	r.client = interceptor.NewClient(base, interceptor.Funcs{Patch: func(ctx context.Context, delegate client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
		patches++
		if patches == 1 {
			return apierrors.NewConflict(gatewayapiv1.Resource("gatewayclasses"), object.GetName(), fmt.Errorf("metadata changed"))
		}
		return delegate.Patch(ctx, object, patch, options...)
	}})

	// Metadata conflicts escape to the outer queue and succeed on its next invocation.
	_, err := r.ensureManagedProfile(ctx, profile.Name, true)
	require.True(t, apierrors.IsConflict(err))
	_, err = r.ensureManagedProfile(ctx, profile.Name, true)
	require.NoError(t, err)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(profile), &current))
	require.Equal(t, "true", current.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
	require.True(t, meta.IsStatusConditionFalse(current.Status.Conditions, "openshift.io/Configured"))

	// Terminating profiles must not have their output label restored.
	current.Labels = nil
	current.Finalizers = []string{"example.com/finalizer"}
	require.NoError(t, base.Update(ctx, &current))
	require.NoError(t, base.Delete(ctx, &current))
	_, err = r.ensureManagedProfile(ctx, profile.Name, true)
	require.NoError(t, err)
	require.Equal(t, 2, patches)
	require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(profile), &current))
	require.Empty(t, current.Labels)
}

func TestManagedProfileUnlabeledReplacementIsOwned(t *testing.T) {
	ctx := context.Background()
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	r := managedProfileTestReconciler(t, profile)
	base := r.client.(client.WithWatch)
	replaced := false

	// A replacement with matching identity is owned, but the old UID cannot delete it blindly.
	r.client = interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, delegate client.WithWatch, object client.Object, options ...client.DeleteOption) error {
		deleteOptions := (&client.DeleteOptions{}).ApplyOptions(options)
		require.Equal(t, object.GetUID(), *deleteOptions.Preconditions.UID)
		if !replaced {
			require.NoError(t, delegate.Delete(ctx, profile))
			replacement := profile.DeepCopy()
			replacement.ResourceVersion = ""
			replacement.UID = "new-matching-profile"
			replacement.Labels = nil
			require.NoError(t, delegate.Create(ctx, replacement))
			replaced = true
			return apierrors.NewConflict(gatewayapiv1.Resource("gatewayclasses"), object.GetName(), fmt.Errorf("UID precondition failed"))
		}
		require.EqualValues(t, "new-matching-profile", object.GetUID())
		return delegate.Delete(ctx, object, options...)
	}})

	// The first stale attempt conflicts; a later pass deliberately deletes the new owned UID.
	_, err := r.ensureManagedProfile(ctx, profile.Name, false)
	require.ErrorContains(t, err, "UID precondition failed")
	var current gatewayapiv1.GatewayClass
	require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(profile), &current))
	require.EqualValues(t, "new-matching-profile", current.UID)
	_, err = r.ensureManagedProfile(ctx, profile.Name, false)
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(base.Get(ctx, client.ObjectKeyFromObject(profile), &current)))
}

// TestManagedProfileCountExcludesProfilesAndDeletingClasses counts active user-created OpenShift GatewayClasses.
func TestManagedProfileCountExcludesProfilesAndDeletingClasses(t *testing.T) {
	user := managedProfileTestClass("user", false)
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	deleting := managedProfileTestClass("deleting", false)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	classes := &gatewayapiv1.GatewayClassList{Items: []gatewayapiv1.GatewayClass{*user, *profile, *deleting}}
	require.Equal(t, 1, countActiveGatewayClasses(classes, ""))
	require.Zero(t, countActiveGatewayClasses(classes, user.Name))
}

// TestManagedProfileInstallationStatusRequeuesPreservingAccepted checks separate reconciliation attempts.
func TestManagedProfileInstallationStatusRequeuesPreservingAccepted(t *testing.T) {
	ctx := context.Background()
	user := managedProfileTestClass("user", false)
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	r := managedProfileTestReconciler(t, user, profile, teardownTestConfigMapDefaults())
	base := r.client.(client.WithWatch)
	injected := false
	patches := 0
	r.client = interceptor.NewClient(base, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, delegate client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
		patches++
		updated := object.(*gatewayapiv1.GatewayClass)
		if !injected && meta.FindStatusCondition(updated.Status.Conditions, ControllerInstalledConditionType) != nil {
			// Change Accepted after the read so the optimistic patch genuinely conflicts.
			data, err := patch.Data(object)
			require.NoError(t, err)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(data, &payload))
			require.Contains(t, payload["metadata"], "resourceVersion")
			var current gatewayapiv1.GatewayClass
			require.NoError(t, delegate.Get(ctx, client.ObjectKeyFromObject(profile), &current))
			meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()})
			require.NoError(t, delegate.Status().Update(ctx, &current))
			injected = true
		}
		return delegate.SubResource(subresource).Patch(ctx, object, patch, options...)
	}})

	// One invocation makes one patch attempt, returning its conflict to Reconcile.
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(profile)}
	_, err := r.Reconcile(ctx, request)
	require.True(t, apierrors.IsConflict(err))
	require.True(t, injected)
	require.Equal(t, 1, patches)

	// The next controller pass reads the new Accepted condition before patching.
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.Equal(t, 2, patches)
	require.NoError(t, base.Get(ctx, request.NamespacedName, profile))
	require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "Accepted"))
	require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))
	require.NotNil(t, meta.FindStatusCondition(profile.Status.Conditions, ControllerInstalledConditionType))
}

// TestManagedProfileCreationUpdatesStatusOnce checks that a new class receives all CIO conditions in one patch.
func TestManagedProfileCreationUpdatesStatusOnce(t *testing.T) {
	ctx := context.Background()
	user := managedProfileTestClass("user", false)
	r := managedProfileTestReconciler(t, user, teardownTestConfigMapDefaults())
	patches := 0
	r.client = interceptor.NewClient(r.client.(client.WithWatch), interceptor.Funcs{SubResourcePatch: func(ctx context.Context, delegate client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
		patches++
		return delegate.SubResource(subresource).Patch(ctx, object, patch, options...)
	}})

	_, err := r.ensureManagedProfile(ctx, operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	require.NoError(t, err)
	require.Equal(t, 1, patches)
	var profile gatewayapiv1.GatewayClass
	require.NoError(t, r.client.Get(ctx, types.NamespacedName{Name: operatorcontroller.OpenShiftClusterIPGatewayClassName}, &profile))
	require.True(t, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))
	require.NotNil(t, meta.FindStatusCondition(profile.Status.Conditions, ControllerInstalledConditionType))
	require.Empty(t, user.Status.Conditions)
}

// TestManagedProfileConfiguredStatusRequeuesPreservingAccepted lets the mode controller retry conflicts.
func TestManagedProfileConfiguredStatusRequeuesPreservingAccepted(t *testing.T) {
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

	// Transition completion must be deferred after the first conflicting status attempt.
	require.Error(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, wrapped))
	require.Equal(t, 1, patches)

	// A later mode reconciliation merges the concurrent Accepted condition.
	require.NoError(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, wrapped))
	require.Equal(t, 2, patches)
	var current gatewayapiv1.GatewayClass
	require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
	require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, "Accepted"))
	condition := meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured")
	require.NotNil(t, condition)
	require.Equal(t, metav1.ConditionFalse, condition.Status)
	require.Equal(t, "Unmanaged", condition.Reason)
	require.NoError(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, wrapped))
	require.Equal(t, 2, patches, "unchanged ConfiguredStatus must not be written again")
}

// TestManagedProfileUnmanagedSkipsAbsentDeletingAndUnownedClasses limits transition status updates to eligible profiles.
func TestManagedProfileUnmanagedSkipsAbsentDeletingAndUnownedClasses(t *testing.T) {
	for _, scenario := range []string{"absent", "deleting", "other-controller", "nonregistered"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			require.NoError(t, gatewayapiv1.Install(scheme))
			managedProfile := &gatewayapiv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: operatorcontroller.OpenShiftClusterIPGatewayClassName, Labels: map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}},
				Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName},
				Status:     gatewayapiv1.GatewayClassStatus{Conditions: []metav1.Condition{{Type: "openshift.io/Configured", Status: metav1.ConditionTrue, Reason: "Configured", LastTransitionTime: metav1.Now()}}},
			}
			switch scenario {
			case "deleting":
				managedProfile.DeletionTimestamp = new(metav1.Now())
				managedProfile.Finalizers = []string{"example.com/finalizer"}
			case "other-controller":
				managedProfile.Spec.ControllerName = "example.com/other"
			case "nonregistered":
				managedProfile.Name = "openshift-not-registered"
			}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&gatewayapiv1.GatewayClass{})

			// None of these cases permits an openshift.io/Configured write during a mode transition.
			if scenario != "absent" {
				builder = builder.WithObjects(managedProfile)
			}
			base := builder.Build()
			writes := 0
			wrapped := interceptor.NewClient(base, interceptor.Funcs{SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
				writes++
				return fmt.Errorf("unexpected status write")
			}})
			require.NoError(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, wrapped))
			require.Zero(t, writes)
			if scenario != "absent" {
				var current gatewayapiv1.GatewayClass
				require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(managedProfile), &current))
				require.Equal(t, "Configured", meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured").Reason)
			}
		})
	}
}
