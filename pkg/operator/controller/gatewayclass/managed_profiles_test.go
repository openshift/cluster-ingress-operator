package gatewayclass

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	retryable "github.com/openshift/cluster-ingress-operator/pkg/util/retryableerror"

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

// managedProfileTestClass builds an operator-managed profile (labeled) or a
// user-created gatewayclass (finalized and unlabeled).
func managedProfileTestClass(name string, managed bool) *gatewayapiv1.GatewayClass {
	class := &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name)},
		Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName},
	}
	// Managed profiles carry the owned label; user-created classes carry the
	// Sail finalizer instead and are identified by name and controller.
	if managed {
		class.Labels = map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}
	} else {
		class.Finalizers = []string{sailLibraryFinalizer}
	}
	return class
}

// managedProfileTestUserClass builds the user-created openshift-default
// gatewayclass whose presence enables the openshift-clusterip profile.
func managedProfileTestUserClass() *gatewayapiv1.GatewayClass {
	return managedProfileTestClass(operatorcontroller.OpenShiftDefaultGatewayClassName, false)
}

// managedProfileTestDefaults builds the ConfigMap that Sail renders for the ClusterIP profile.
func managedProfileTestDefaults() *corev1.ConfigMap {
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	// Production only reads the name, namespace, and owning-class label.
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      managedProfileConfigMapDefaultsName(operatorcontroller.DefaultOperandNamespace, name).Name,
		Namespace: operatorcontroller.DefaultOperandNamespace, UID: "defaults-uid",
		Labels:      map[string]string{operatorcontroller.GatewayClassDefaultsLabel: name, "managed-by": "sail-library", "release": operatorcontroller.IstioName("").Name + "-istiod"},
		Annotations: map[string]string{"meta.helm.sh/release-name": operatorcontroller.IstioName("").Name + "-istiod", "meta.helm.sh/release-namespace": operatorcontroller.DefaultOperandNamespace},
	}}
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

	// The cache reads through the same fake client so seeded objects are visible.
	return &reconciler{client: client, cache: &testutil.FakeCache{Informers: informers, Reader: client}, modeAccessor: mode, config: Config{OperandNamespace: operatorcontroller.DefaultOperandNamespace, GatewayAPIWithoutOLMEnabled: true, IstioVersion: "v1.30.5"}, sailInstaller: &fakeSailInstaller{}}
}

// blockManagedProfileDependents puts a gate-enabled accessor into Unmanaged mode.
func blockManagedProfileDependents(r *reconciler) {
	r.modeAccessor = operatorcontroller.NewGatewayAPIModeAccessor(true)
	r.modeAccessor.SetCRDsEstablished(true)
	r.modeAccessor.Update(operatorv1alpha1.GatewayAPIManagementModeUnmanaged, false, true, true)
	r.modeAccessor.BlockDependents()
}

// TestReconcileWithSailLibraryDeletesProfileBeforeUninstall checks that deleting
// the last user-created openshift-default gatewayclass deletes openshift-clusterip
// before uninstalling Sail.
func TestReconcileWithSailLibraryDeletesProfileBeforeUninstall(t *testing.T) {
	ctx := context.Background()

	// Seed the last user-created class plus a profile that cannot finish deleting.
	userClass := managedProfileTestUserClass()
	profile := managedProfileTestClass(operatorcontroller.OpenShiftClusterIPGatewayClassName, true)
	profile.Labels = nil
	profile.Finalizers = []string{"example.com/profile-in-use"}
	r := managedProfileTestReconciler(t, userClass, profile)
	blockManagedProfileDependents(r)

	// Delete the only class that enables the profile and reconcile its deletion.
	require.NoError(t, r.client.Delete(ctx, userClass))
	result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(userClass)})
	require.NoError(t, err)

	// The deletion must request profile removal and defer Sail uninstall.
	require.NotZero(t, result.RequeueAfter)
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), profile))
	require.False(t, profile.DeletionTimestamp.IsZero())
}

// Test_syncManagedProfile checks when openshift-clusterip is created or removed,
// defaults gating, and installation isolation.
func Test_syncManagedProfile(t *testing.T) {
	ctx := context.Background()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	// Each case seeds the classes the profile decision depends on and states
	// whether openshift-clusterip should exist, requeue, or report Configured.
	for _, tc := range []struct {
		name               string
		objects            []client.Object
		blockDependents    bool
		expectedPresent    bool
		expectedRequeue    bool
		expectedConfigured bool
	}{
		{
			name: "removes openshift-clusterip when no user-created openshift-default gatewayclass exists",
			objects: []client.Object{
				managedProfileTestClass(name, true),
				managedProfileTestDefaults(),
			},
		},
		{
			name: "creates openshift-clusterip when a user-created openshift-default gatewayclass exists",
			objects: []client.Object{
				managedProfileTestUserClass(),
				managedProfileTestDefaults(),
			},
			expectedPresent:    true,
			expectedConfigured: true,
		},
		{
			name: "waits for the defaults ConfigMap before creating openshift-clusterip",
			objects: []client.Object{
				managedProfileTestUserClass(),
			},
			expectedRequeue: true,
		},
		{
			name: "leaves openshift-clusterip unchanged when the management mode disallows dependents",
			objects: []client.Object{
				managedProfileTestClass(name, true),
			},
			blockDependents: true,
			expectedPresent: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Seed the reconciler and optionally block dependent resources.
			r := managedProfileTestReconciler(t, tc.objects...)
			if tc.blockDependents {
				blockManagedProfileDependents(r)
			}

			// Sync the profile; this function must never install or uninstall Sail.
			result, err := r.syncManagedProfile(ctx, name)
			require.NoError(t, err)
			require.Equal(t, tc.expectedRequeue, result.RequeueAfter > 0)
			require.Nil(t, r.sailInstaller.(*fakeSailInstaller).internalOpts.Values)
			require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)

			// A missing profile is valid when no user class enables it.
			var profile gatewayapiv1.GatewayClass
			err = r.client.Get(ctx, types.NamespacedName{Name: name}, &profile)
			if !tc.expectedPresent {
				require.True(t, apierrors.IsNotFound(err))
				return
			}

			// A present profile carries the owned label and reflects defaults readiness.
			require.NoError(t, err)
			require.Equal(t, "true", profile.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
			require.Equal(t, tc.expectedConfigured, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))
		})
	}
}

// Test_ensureManagedProfile checks the want/have/defaults matrix and conflict handling.
func Test_ensureManagedProfile(t *testing.T) {
	ctx := context.Background()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	// Each case varies whether the class exists, whether it is wanted, and
	// whether the defaults ConfigMap is present.
	for _, tc := range []struct {
		name            string
		want, have      bool
		defaults        bool
		mutate          func(*gatewayapiv1.GatewayClass)
		expectedError   string
		expectedPresent bool
		expectedRequeue bool
	}{
		{
			name: "openshift-clusterip absent and unwanted",
		},
		{
			name: "openshift-clusterip present and unwanted",
			have: true,
		},
		{
			name:            "openshift-clusterip absent and wanted with defaults",
			want:            true,
			defaults:        true,
			expectedPresent: true,
		},
		{
			name:            "openshift-clusterip present and wanted with defaults",
			want:            true,
			have:            true,
			defaults:        true,
			expectedPresent: true,
		},
		{
			name:            "openshift-clusterip absent and wanted without defaults",
			want:            true,
			expectedRequeue: true,
		},
		{
			name:            "openshift-clusterip present and wanted without defaults",
			want:            true,
			have:            true,
			expectedPresent: true,
			expectedRequeue: true,
		},
		// A class owned by another controller must not be adopted.
		{
			name:          "refuses to adopt openshift-clusterip with a conflicting controller",
			want:          true,
			have:          true,
			defaults:      true,
			expectedError: "conflicting controllerName",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.Spec.ControllerName = "example.com/other"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Build the class and defaults ConfigMap this case needs.
			var objects []client.Object
			if tc.have {
				class := managedProfileTestClass(name, true)
				if tc.mutate != nil {
					tc.mutate(class)
				}
				objects = append(objects, class)
			}
			// Add the defaults ConfigMap only when the case provides one.
			if tc.defaults {
				objects = append(objects, managedProfileTestDefaults())
			}
			r := managedProfileTestReconciler(t, objects...)

			// Sync toward tc.want and check whether a requeue is expected.
			result, err := r.ensureManagedProfile(ctx, name, tc.want)
			if tc.expectedError != "" {
				// Conflicting controllers must fail rather than be adopted.
				require.ErrorContains(t, err, tc.expectedError)
				require.Zero(t, result.RequeueAfter)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expectedRequeue, result.RequeueAfter > 0)

			// Absent is a valid outcome when the profile is not wanted.
			have, profile, err := r.currentManagedProfile(ctx, name)
			require.NoError(t, err)
			require.Equal(t, tc.expectedPresent, have)
			if !have {
				return
			}

			// Presence includes the owned label, description, and current defaults readiness.
			require.Equal(t, "true", profile.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
			require.Equal(t, managedProfiles[name], *profile.Spec.Description)
			condition := meta.FindStatusCondition(profile.Status.Conditions, "openshift.io/Configured")
			require.NotNil(t, condition)
			require.Equal(t, tc.defaults, condition.Status == metav1.ConditionTrue)
		})
	}
}

// Test_updateManagedProfile checks operator-owned field repair, preservation of
// other fields, skip rules, and retry behavior.
func Test_updateManagedProfile(t *testing.T) {
	ctx := context.Background()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	desired := desiredManagedProfile(name)

	// Each case applies a different drift and states the expected update count.
	for _, tc := range []struct {
		name            string
		mutate          func(*gatewayapiv1.GatewayClass)
		deleting        bool
		conflict        bool
		expectedUpdates int
		expectedLabel   string
		verify          func(*testing.T, *gatewayapiv1.GatewayClass)
	}{
		{
			name: "repairs operator-owned fields and preserves other fields",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.Labels = map[string]string{
					"example.com/label": "preserved",
					operatorcontroller.ManagedGatewayClassProfileLabel: "false",
				}
				class.Annotations = map[string]string{"example.com/annotation": "preserved"}
				class.Spec.ParametersRef = &gatewayapiv1.ParametersReference{Group: "example.com", Kind: "Config", Name: "preserved"}
				class.Spec.Description = new("stale description")
				class.Status.Conditions = []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()}}
			},
			expectedUpdates: 1,
			expectedLabel:   "true",
			verify: func(t *testing.T, current *gatewayapiv1.GatewayClass) {
				require.Equal(t, "preserved", current.Labels["example.com/label"])
				require.Equal(t, map[string]string{"example.com/annotation": "preserved"}, current.Annotations)
				require.Equal(t, &gatewayapiv1.ParametersReference{Group: "example.com", Kind: "Config", Name: "preserved"}, current.Spec.ParametersRef)
				require.Equal(t, desired.Spec.Description, current.Spec.Description)
				require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, "Accepted"))
			},
		},
		{
			name: "does not update an already-current profile",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.Spec.Description = desired.Spec.Description
			},
			expectedLabel: "true",
		},
		{
			name: "skips openshift-clusterip while it is being deleted",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.Labels = map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "false"}
				class.Finalizers = []string{"example.com/finalizer"}
			},
			deleting:      true,
			expectedLabel: "false",
		},
		{
			name: "returns update conflicts for retry",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.Labels = nil
			},
			conflict:        true,
			expectedUpdates: 2,
			expectedLabel:   "true",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Start from a managed profile and apply this case's drift.
			profile := managedProfileTestClass(name, true)
			tc.mutate(profile)
			r := managedProfileTestReconciler(t, profile)
			base := r.client.(client.WithWatch)

			// Count updates and fail the first one when the case asks for a conflict.
			updates := 0
			r.client = interceptor.NewClient(base, interceptor.Funcs{Update: func(ctx context.Context, delegate client.WithWatch, object client.Object, options ...client.UpdateOption) error {
				updates++
				if tc.conflict && updates == 1 {
					return apierrors.NewConflict(gatewayapiv1.Resource("gatewayclasses"), object.GetName(), fmt.Errorf("metadata changed"))
				}
				return delegate.Update(ctx, object, options...)
			}})

			// Put the class into deletion before updating when required.
			if tc.deleting {
				require.NoError(t, r.client.Delete(ctx, profile))
				require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), profile))
			}
			before := profile.DeepCopy()

			// A conflict must be retried; other cases update at most once.
			if tc.conflict {
				require.True(t, apierrors.IsConflict(r.updateManagedProfile(ctx, profile, desired)))
				require.NoError(t, r.updateManagedProfile(ctx, profile, desired))
			} else {
				require.NoError(t, r.updateManagedProfile(ctx, profile, desired))
			}
			require.Equal(t, tc.expectedUpdates, updates)

			// Read back through the unwrapped client and check the owned fields.
			var current gatewayapiv1.GatewayClass
			require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(profile), &current))
			require.Equal(t, tc.expectedLabel, current.Labels[operatorcontroller.ManagedGatewayClassProfileLabel])
			require.Equal(t, tc.deleting, !current.DeletionTimestamp.IsZero())

			// The caller's object must never be mutated, and extra checks must pass.
			require.Equal(t, before, profile, "must not mutate the caller's object")
			if tc.verify != nil {
				tc.verify(t, &current)
			}
		})
	}
}

// Test_deleteManagedProfile checks ownership boundaries, UID preconditions, and cache-lag retries.
func Test_deleteManagedProfile(t *testing.T) {
	ctx := context.Background()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	t.Run("preserves gatewayclasses it does not manage", func(t *testing.T) {
		// Only a registered name with the OpenShift controller is deletable.
		for _, tc := range []struct {
			description string
			className   string
			controller  string
		}{
			{
				description: "unregistered name",
				className:   "openshift-not-registered",
				controller:  operatorcontroller.OpenShiftGatewayClassControllerName,
			},
			{
				description: "different controller",
				className:   name,
				controller:  "example.com/other",
			},
		} {
			t.Run(tc.description, func(t *testing.T) {
				// Attempt deletion and verify the class was left alone.
				class := managedProfileTestClass(tc.className, true)
				class.Spec.ControllerName = gatewayapiv1.GatewayController(tc.controller)
				r := managedProfileTestReconciler(t, class)
				require.NoError(t, r.deleteManagedProfile(ctx, class))
				var current gatewayapiv1.GatewayClass
				require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(class), &current))
				require.True(t, current.DeletionTimestamp.IsZero())
			})
		}
	})

	t.Run("retries until the cache observes deletion", func(t *testing.T) {
		// Freeze the cache on a snapshot so the class still looks present.
		profile := managedProfileTestClass(name, true)
		r := managedProfileTestReconciler(t, profile)
		base := r.client
		snapshot := fake.NewClientBuilder().WithScheme(base.Scheme()).WithObjects(profile.DeepCopy()).Build()
		r.cache = &testutil.FakeCache{Reader: snapshot}

		// Every pass stays retryable at 5s while the cache is stale.
		for range 2 {
			var pending retryable.Error
			require.ErrorAs(t, r.deleteManagedProfile(ctx, profile), &pending)
			require.Equal(t, 5*time.Second, pending.After())
		}

		// Once the cache catches up, deletion completes.
		r.cache = &testutil.FakeCache{Reader: base}
		require.NoError(t, r.deleteManagedProfile(ctx, profile))
	})

	t.Run("deletes only the observed UID", func(t *testing.T) {
		original := managedProfileTestClass(name, true)
		r := managedProfileTestReconciler(t, original)
		base := r.client.(client.WithWatch)
		replaced := false

		// The first delete is intercepted: drop the original and swap in a foreign class.
		r.client = interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, delegate client.WithWatch, object client.Object, options ...client.DeleteOption) error {
			if !replaced {
				// Confirm the delete carries the observed UID before removing it.
				deleteOptions := (&client.DeleteOptions{}).ApplyOptions(options)
				require.NotNil(t, deleteOptions.Preconditions)
				require.NotNil(t, deleteOptions.Preconditions.UID)
				require.Equal(t, original.UID, *deleteOptions.Preconditions.UID)
				require.NoError(t, delegate.Delete(ctx, original))

				// Swap in a foreign class under the same name.
				replacement := original.DeepCopy()
				replacement.ResourceVersion = ""
				replacement.UID = "foreign-replacement"
				replacement.Labels = nil
				replacement.Spec.ControllerName = "example.com/other"
				require.NoError(t, delegate.Create(ctx, replacement))
				replaced = true

				// Fail the first delete as if the UID precondition lost the race.
				return apierrors.NewConflict(gatewayapiv1.Resource("gatewayclasses"), object.GetName(), fmt.Errorf("UID precondition failed"))
			}
			return delegate.Delete(ctx, object, options...)
		}})

		// A stale-UID delete must fail, and deleting again must leave the replacement.
		require.ErrorContains(t, r.deleteManagedProfile(ctx, original), "UID precondition failed")
		var current gatewayapiv1.GatewayClass
		require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(original), &current))
		require.NoError(t, r.deleteManagedProfile(ctx, &current))
		require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(original), &current))
		require.EqualValues(t, "foreign-replacement", current.UID)
		require.True(t, current.DeletionTimestamp.IsZero())
	})
}

// Test_SetManagedProfilesUnmanagedConfiguredStatus checks transition status updates and eligibility.
func Test_SetManagedProfilesUnmanagedConfiguredStatus(t *testing.T) {
	ctx := context.Background()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	defaultsNamespace := operatorcontroller.DefaultOperandNamespace

	t.Run("marks openshift-clusterip unmanaged and preserves other conditions", func(t *testing.T) {
		// Seed an unlabeled profile whose conditions belong to other controllers.
		profile := managedProfileTestClass(name, true)
		profile.Labels = nil
		profile.Status.Conditions = []metav1.Condition{
			{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()},
			{Type: "Customized", Status: metav1.ConditionUnknown, Reason: "OtherOwner", LastTransitionTime: metav1.Now()},
		}
		r := managedProfileTestReconciler(t, profile)

		// Mark the profile Unmanaged and read back the merged conditions.
		require.NoError(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, r.client, defaultsNamespace))
		var current gatewayapiv1.GatewayClass
		require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), &current))
		require.Empty(t, current.Labels)

		// The owned condition must point at the defaults ConfigMap and class.
		configured := meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured")
		require.NotNil(t, configured)
		require.Equal(t, metav1.ConditionFalse, configured.Status)
		require.Equal(t, "Unmanaged", configured.Reason)
		require.Contains(t, configured.Message, managedProfileConfigMapDefaultsName(defaultsNamespace, name).String())
		require.Contains(t, configured.Message, "association with GatewayClass "+name)

		// Other controllers' conditions must survive unchanged.
		require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, "Accepted"))
		require.Equal(t, "OtherOwner", meta.FindStatusCondition(current.Status.Conditions, "Customized").Reason)
	})

	t.Run("skips gatewayclasses that do not qualify", func(t *testing.T) {
		for _, scenario := range []string{
			"absent",
			"being deleted",
			"different controller",
			"unregistered name",
		} {
			t.Run(scenario, func(t *testing.T) {
				// Build the variant that must be ignored and seed it unless absent.
				profile := managedProfileTestClass(name, true)

				// Break exactly the property this scenario targets.
				switch scenario {
				case "being deleted":
					profile.Finalizers = []string{"example.com/finalizer"}
					profile.DeletionTimestamp = new(metav1.Now())
				case "different controller":
					profile.Spec.ControllerName = "example.com/other"
				case "unregistered name":
					profile.Name = "openshift-not-registered"
				}

				// Only the absent case omits the class.
				var objects []client.Object
				if scenario != "absent" {
					objects = append(objects, profile)
				}
				r := managedProfileTestReconciler(t, objects...)
				// Any status write is a failure for these cases.
				writes := 0
				r.client = interceptor.NewClient(r.client.(client.WithWatch), interceptor.Funcs{SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
					writes++
					return fmt.Errorf("unexpected status write")
				}})

				// The transition must succeed without writing.
				require.NoError(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, r.client, defaultsNamespace))
				require.Zero(t, writes)
			})
		}
	})

	t.Run("returns conflicts for retry and does not rewrite unchanged status", func(t *testing.T) {
		profile := managedProfileTestClass(name, true)
		r := managedProfileTestReconciler(t, profile)
		base := r.client.(client.WithWatch)
		patches := 0
		wrapped := interceptor.NewClient(base, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, delegate client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
			patches++

			// The first patch races a concurrent Accepted update and conflicts.
			if patches == 1 {
				var current gatewayapiv1.GatewayClass
				require.NoError(t, delegate.Get(ctx, client.ObjectKeyFromObject(profile), &current))
				meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()})
				require.NoError(t, delegate.Status().Update(ctx, &current))
				return apierrors.NewConflict(gatewayapiv1.Resource("gatewayclasses"), object.GetName(), fmt.Errorf("concurrent Accepted update"))
			}
			return delegate.SubResource(subresource).Patch(ctx, object, patch, options...)
		}})

		// The transition completes only after the conflict is retried.
		require.Error(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, wrapped, defaultsNamespace))
		require.Equal(t, 1, patches)
		require.NoError(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, wrapped, defaultsNamespace))
		require.Equal(t, 2, patches)

		// After the retry, Accepted survives and Configured reports Unmanaged.
		var current gatewayapiv1.GatewayClass
		require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(profile), &current))
		require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, "Accepted"))
		require.Equal(t, "Unmanaged", meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured").Reason)

		// An unchanged Configured status must not be written again.
		require.NoError(t, SetManagedProfilesUnmanagedConfiguredStatus(ctx, wrapped, defaultsNamespace))
		require.Equal(t, 2, patches)
	})
}

// Test_updateManagedProfileStatus checks condition merging, idempotence, and conflict reporting.
func Test_updateManagedProfileStatus(t *testing.T) {
	ctx := context.Background()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	// Each case states the initial conditions, call count, and expected patches.
	for _, tc := range []struct {
		name               string
		conditions         []metav1.Condition
		conflict           bool
		calls              int
		expectedPatches    int
		expectedConfigured bool
	}{
		{
			name: "sets openshift.io/Configured and preserves other conditions",
			conditions: []metav1.Condition{
				{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: metav1.Now()},
			},
			calls:              1,
			expectedPatches:    1,
			expectedConfigured: true,
		},
		{
			name:               "does not rewrite unchanged status",
			calls:              2,
			expectedPatches:    1,
			expectedConfigured: true,
		},
		{
			name:            "returns conflicts for requeue",
			conflict:        true,
			calls:           1,
			expectedPatches: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Seed the conditions this case starts with.
			profile := managedProfileTestClass(name, true)
			profile.Status.Conditions = tc.conditions
			r := managedProfileTestReconciler(t, profile)

			// Count patches and fail them all when the case asks for a conflict.
			patches := 0
			wrapped := interceptor.NewClient(r.client.(client.WithWatch), interceptor.Funcs{SubResourcePatch: func(ctx context.Context, delegate client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
				patches++
				if tc.conflict {
					return apierrors.NewConflict(gatewayapiv1.Resource("gatewayclasses"), name, fmt.Errorf("persistent status conflict"))
				}
				return delegate.SubResource(subresource).Patch(ctx, object, patch, options...)
			}})

			// Call the function as many times as the case expects.
			var err error
			for range tc.calls {
				err = updateManagedProfileStatus(ctx, wrapped, name, metav1.ConditionTrue, "Configured", "defaults ready", nil)
			}
			if tc.conflict {
				require.True(t, apierrors.IsConflict(err))
			} else {
				require.NoError(t, err)
			}

			// Repeated calls must be idempotent, and the caller must be untouched.
			require.Equal(t, tc.expectedPatches, patches)
			require.Equal(t, tc.conditions, profile.Status.Conditions, "must not mutate the caller's object")

			// Read back through the unwrapped client to inspect merged conditions.
			var current gatewayapiv1.GatewayClass
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), &current))
			if tc.expectedConfigured {
				configured := meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured")
				require.NotNil(t, configured)
				require.Equal(t, metav1.ConditionTrue, configured.Status)
				require.Equal(t, "Configured", configured.Reason)
			}

			// Every pre-existing condition must survive the update.
			for _, condition := range tc.conditions {
				require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, condition.Type))
			}
		})
	}
}

// Test_managedProfileConfigMapDefaultsReady checks presence and class association of the defaults ConfigMap.
func Test_managedProfileConfigMapDefaultsReady(t *testing.T) {
	ctx := context.Background()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	// Each case omits, mislabels, or correctly associates the ConfigMap.
	for _, tc := range []struct {
		name     string
		defaults bool
		mutate   func(*corev1.ConfigMap)
		ready    bool
	}{
		{
			name: "defaults ConfigMap missing",
		},
		{
			name:     "defaults ConfigMap labeled for a different gatewayclass",
			defaults: true,
			mutate: func(defaults *corev1.ConfigMap) {
				defaults.Labels[operatorcontroller.GatewayClassDefaultsLabel] = "another-class"
			},
		},
		{
			name:     "defaults ConfigMap labeled for openshift-clusterip",
			defaults: true,
			ready:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Seed the defaults ConfigMap unless this case omits it.
			var objects []client.Object
			if tc.defaults {
				defaults := managedProfileTestDefaults()
				if tc.mutate != nil {
					tc.mutate(defaults)
				}
				objects = append(objects, defaults)
			}
			r := managedProfileTestReconciler(t, objects...)

			// A missing or mislabeled ConfigMap is not ready.
			ready, err := r.managedProfileConfigMapDefaultsReady(ctx, name)
			require.NoError(t, err)
			require.Equal(t, tc.ready, ready)
		})
	}
}

// Test_managedProfileChanged verifies that managedProfileChanged detects drift
// in the owned label and description and ignores unrelated fields.
func Test_managedProfileChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*gatewayapiv1.GatewayClass)
		expect      bool
	}{
		// Changes outside the owned label and description must not trigger an update.
		{
			description: "if nothing changes",
			mutate:      func(_ *gatewayapiv1.GatewayClass) {},
			expect:      false,
		},
		{
			description: "if .metadata.uid changes",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.UID = "2"
			},
			expect: false,
		},
		{
			description: "if an unrelated label changes",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.Labels["example.com/other"] = "true"
			},
			expect: false,
		},
		{
			description: "if .spec.controllerName changes",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.Spec.ControllerName = "example.com/other"
			},
			expect: false,
		},
		// Changes to the owned label or description must trigger an update.
		{
			description: "if the managed profile label changes",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] = "false"
			},
			expect: true,
		},
		{
			description: "if the managed profile label is removed",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				delete(class.Labels, operatorcontroller.ManagedGatewayClassProfileLabel)
			},
			expect: true,
		},
		{
			description: "if .spec.description changes",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.Spec.Description = new("other description")
			},
			expect: true,
		},
		{
			description: "if .spec.description is removed",
			mutate: func(class *gatewayapiv1.GatewayClass) {
				class.Spec.Description = nil
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			// Start from the desired class and apply the case's mutation.
			desired := desiredManagedProfile(operatorcontroller.OpenShiftClusterIPGatewayClassName)
			current := desired.DeepCopy()
			tc.mutate(current)

			// Compare the detected drift with the expected result.
			if changed, updated := managedProfileChanged(current, desired); changed != tc.expect {
				t.Errorf("expected managedProfileChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				// A changed result must be a fixed point on the next pass.
				if changedAgain, _ := managedProfileChanged(updated, desired); changedAgain {
					t.Error("managedProfileChanged does not behave as a fixed-point function")
				}
			}
		})
	}
}

// TestEnsureGatewayClassDeletedWaitsForOpenshiftClusterIPDeletion checks that
// finalizer release defers uninstall until the managed profile is gone.
func TestEnsureGatewayClassDeletedWaitsForOpenshiftClusterIPDeletion(t *testing.T) {
	ctx := context.Background()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	// Seed the user-created class plus a profile that is stuck terminating.
	anchor := managedProfileTestUserClass()
	profile := managedProfileTestClass(name, true)
	profile.Finalizers = []string{"gateway.networking.k8s.io/gateway-exists-finalizer"}
	profile.Labels = nil
	r := managedProfileTestReconciler(t, anchor, profile)

	// Delete the user-created class and leave it terminating.
	require.NoError(t, r.client.Delete(ctx, anchor))
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(anchor), anchor))

	// An openshift-clusterip that is still terminating must defer uninstall.
	var pending retryable.Error
	_, err := r.ensureGatewayClassDeleted(ctx, anchor)
	require.ErrorAs(t, err, &pending)
	require.Equal(t, 5*time.Second, pending.After())
	require.False(t, r.sailInstaller.(*fakeSailInstaller).uninstallCalled)

	// The user-created class keeps its finalizer until the profile is gone.
	var current gatewayapiv1.GatewayClass
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), &current))
	require.False(t, current.DeletionTimestamp.IsZero())
}
