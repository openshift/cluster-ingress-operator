package gatewayclass

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	operatorv1 "github.com/openshift/api/operator/v1"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	retryable "github.com/openshift/cluster-ingress-operator/pkg/util/retryableerror"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// managedProfileTestClass builds the named GatewayClass for the given controller.
func managedProfileTestClass(name string, controller gatewayapiv1.GatewayController) *gatewayapiv1.GatewayClass {
	return &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name)},
		Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: controller},
	}
}

// withSailFinalizer adds the legacy Sail GatewayClass finalizer.
func withSailFinalizer(gatewayClass *gatewayapiv1.GatewayClass) *gatewayapiv1.GatewayClass {
	gatewayClass.Finalizers = append(gatewayClass.Finalizers, sailLibraryFinalizer)
	return gatewayClass
}

// managedProfileTestDefaults builds the ClusterIP profile's defaults ConfigMap
// labeled for the given GatewayClass.
func managedProfileTestDefaults(class string) *corev1.ConfigMap {
	// Production only reads the name, namespace, and owning-GatewayClass label.
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      operatorcontroller.ManagedProfileConfigMapDefaultsName(operatorcontroller.DefaultOperandNamespace, operatorcontroller.OpenShiftClusterIPGatewayClassName).Name,
		Namespace: operatorcontroller.DefaultOperandNamespace,
		Labels:    map[string]string{operatorcontroller.GatewayClassDefaultsLabel: class},
	}}
}

// managedProfileTestReconciler shares a fake API with the cache and installs a controllable Sail client.
func managedProfileTestReconciler(t *testing.T, objects ...client.Object) *reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, register := range []func(*runtime.Scheme) error{corev1.AddToScheme, gatewayapiv1.Install} {
		require.NoError(t, register(scheme))
	}

	// Match the production controller-name index and status subresource behavior.
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&gatewayapiv1.GatewayClass{}).WithIndex(&gatewayapiv1.GatewayClass{}, operatorcontroller.GatewayClassIndexFieldName, func(object client.Object) []string {
		return []string{string(object.(*gatewayapiv1.GatewayClass).Spec.ControllerName)}
	}).Build()
	informers := &informertest.FakeInformers{Scheme: scheme}
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)

	// The cache reads through the same fake client so seeded objects are visible.
	return &reconciler{client: client, cache: &testutil.FakeCache{Informers: informers, Reader: client}, recorder: record.NewFakeRecorder(10), modeAccessor: mode, config: Config{OperandNamespace: operatorcontroller.DefaultOperandNamespace, GatewayAPIWithoutOLMEnabled: true}, sailInstaller: &fakeSailInstaller{}}
}

// blockManagedProfileDependents puts a gate-enabled accessor into Unmanaged mode.
func blockManagedProfileDependents(r *reconciler) {
	r.modeAccessor = operatorcontroller.NewGatewayAPIModeAccessor(true)
	r.modeAccessor.Update(operatorv1.GatewayAPIManagementModeUnmanaged, false, true, true)
}

// Test_syncManagedProfile checks when openshift-clusterip is created or removed
// and the mode gate.
func Test_syncManagedProfile(t *testing.T) {
	ctx := t.Context()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	// Each case seeds the GatewayClasses the profile decision depends on and states
	// whether openshift-clusterip should exist or report Configured.
	for _, tc := range []struct {
		name               string
		objects            []client.Object
		blockDependents    bool
		expectedPresent    bool
		expectedConfigured bool
	}{
		{
			name: "removes openshift-clusterip when no user-created openshift-default GatewayClass exists",
			objects: []client.Object{
				desiredManagedProfile(name),
				managedProfileTestDefaults(name),
			},
		},
		{
			name: "creates openshift-clusterip when a user-created openshift-default GatewayClass exists",
			objects: []client.Object{
				managedProfileTestClass(operatorcontroller.OpenShiftDefaultGatewayClassName, operatorcontroller.OpenShiftGatewayClassControllerName),
				managedProfileTestDefaults(name),
			},
			expectedPresent:    true,
			expectedConfigured: true,
		},
		{
			name: "leaves openshift-clusterip unchanged when the management mode disallows dependents",
			objects: []client.Object{
				desiredManagedProfile(name),
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

			// Sync the profile.
			err := r.syncManagedProfile(ctx, name)
			require.NoError(t, err)

			// A missing profile is valid when no user GatewayClass enables it.
			var profile gatewayapiv1.GatewayClass
			err = r.client.Get(ctx, types.NamespacedName{Name: name}, &profile)
			if !tc.expectedPresent {
				require.True(t, apierrors.IsNotFound(err))
				return
			}

			// A present profile reflects defaults readiness.
			require.NoError(t, err)
			require.Equal(t, tc.expectedConfigured, meta.IsStatusConditionTrue(profile.Status.Conditions, "openshift.io/Configured"))
		})
	}
}

// Test_ensureManagedProfile checks the want/have/defaults matrix and conflict handling.
func Test_ensureManagedProfile(t *testing.T) {
	ctx := t.Context()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	// Each case provides the GatewayClass this reconcile sees, whether the profile
	// is wanted, and whether the defaults ConfigMap is present.
	for _, tc := range []struct {
		name            string
		gatewayClass    *gatewayapiv1.GatewayClass
		want            bool
		defaults        bool
		expectedPresent bool
		expectedRequeue bool
	}{
		{
			name:            "openshift-clusterip absent and unwanted",
			expectedPresent: false,
			expectedRequeue: false,
		},
		{
			name:         "openshift-clusterip present and unwanted",
			gatewayClass: managedProfileTestClass(name, operatorcontroller.OpenShiftGatewayClassControllerName),
		},
		{
			name:            "openshift-clusterip absent and wanted with defaults",
			want:            true,
			defaults:        true,
			expectedPresent: true,
		},
		{
			name:            "openshift-clusterip present and wanted with defaults",
			gatewayClass:    managedProfileTestClass(name, operatorcontroller.OpenShiftGatewayClassControllerName),
			want:            true,
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
			gatewayClass:    managedProfileTestClass(name, operatorcontroller.OpenShiftGatewayClassControllerName),
			want:            true,
			expectedPresent: true,
			expectedRequeue: true,
		},
		// A GatewayClass created before managed profiles existed is owned by another
		// controller and cannot be adopted; report it and leave it alone until
		// it is deleted.
		{
			name:            "refuses to adopt an openshift-clusterip with a conflicting controller",
			gatewayClass:    managedProfileTestClass(name, "example.com/other"),
			want:            true,
			defaults:        true,
			expectedPresent: true,
		},
		// A GatewayClass created before managed profiles existed may carry the
		// legacy Sail lifecycle finalizer.
		{
			name:         "deletes an unwanted openshift-clusterip with the legacy Sail finalizer",
			gatewayClass: withSailFinalizer(managedProfileTestClass(name, operatorcontroller.OpenShiftGatewayClassControllerName)),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Seed the GatewayClass and defaults ConfigMap this case provides.
			var objects []client.Object
			if tc.gatewayClass != nil {
				objects = append(objects, tc.gatewayClass)
			}
			if tc.defaults {
				objects = append(objects, managedProfileTestDefaults(name))
			}
			r := managedProfileTestReconciler(t, objects...)

			// Sync toward tc.want and check whether a requeue is expected.
			err := r.ensureManagedProfile(ctx, name, tc.want)
			if tc.expectedRequeue {
				var retry retryable.Error
				require.ErrorAs(t, err, &retry)
			} else {
				require.NoError(t, err)
			}

			// Absent is a valid outcome when the profile is not wanted.
			have, current, err := r.currentManagedProfile(ctx, name)
			require.NoError(t, err)
			require.Equal(t, tc.expectedPresent, have)
			if !have {
				return
			}

			if tc.gatewayClass != nil && tc.gatewayClass.Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName {
				// A foreign GatewayClass is reported but never adopted.
				require.Nil(t, meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured"))
				select {
				case event := <-r.recorder.(*record.FakeRecorder).Events:
					require.Contains(t, event, "GatewayClassConflict")
				default:
					t.Error("expected a GatewayClassConflict event")
				}
				return
			}

			// A wanted profile reports defaults readiness.
			condition := meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured")
			require.NotNil(t, condition)
			require.Equal(t, tc.defaults, condition.Status == metav1.ConditionTrue)
		})
	}
}

// Test_updateManagedProfile checks operator-owned field repair, preservation of
// other fields, skip rules, and retry behavior.
func Test_updateManagedProfile(t *testing.T) {
	ctx := t.Context()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	desired := desiredManagedProfile(name)

	// Each case applies a different drift and states the expected update count.
	for _, tc := range []struct {
		name            string
		mutate          func(*gatewayapiv1.GatewayClass)
		conflict        bool
		expectedUpdates int
		verify          func(*testing.T, *gatewayapiv1.GatewayClass)
	}{
		{
			name: "repairs operator-owned fields and preserves other fields",
			mutate: func(gatewayClass *gatewayapiv1.GatewayClass) {
				gatewayClass.Labels["example.com/label"] = "preserved"
				gatewayClass.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] = "false"
				gatewayClass.Spec.Description = new("stale description")
			},
			expectedUpdates: 1,
			verify: func(t *testing.T, current *gatewayapiv1.GatewayClass) {
				require.Equal(t, map[string]string{
					"example.com/label": "preserved",
					operatorcontroller.ManagedGatewayClassProfileLabel: "true",
				}, current.Labels)
				require.Equal(t, desired.Spec.Description, current.Spec.Description)
			},
		},
		{
			name:   "does not update an already-current profile",
			mutate: func(_ *gatewayapiv1.GatewayClass) {},
		},
		{
			name: "removes the legacy Sail finalizer",
			mutate: func(gatewayClass *gatewayapiv1.GatewayClass) {
				gatewayClass.Finalizers = []string{sailLibraryFinalizer, "example.com/finalizer"}
			},
			expectedUpdates: 1,
			verify: func(t *testing.T, current *gatewayapiv1.GatewayClass) {
				// Only our finalizer is removed; others are preserved.
				require.Equal(t, []string{"example.com/finalizer"}, current.Finalizers)
			},
		},
		{
			name: "returns update conflicts for retry",
			mutate: func(gatewayClass *gatewayapiv1.GatewayClass) {
				gatewayClass.Labels = nil
			},
			conflict:        true,
			expectedUpdates: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Start from a managed profile and apply this case's drift.
			profile := desiredManagedProfile(name)
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

			// The caller's object must never be mutated, and extra checks must pass.
			require.Equal(t, before, profile, "must not mutate the caller's object")
			if tc.verify != nil {
				tc.verify(t, &current)
			}
		})
	}
}

// Test_deleteManagedProfile checks ownership boundaries, finalizer deferral, and
// that the delete carries the observed UID.
func Test_deleteManagedProfile(t *testing.T) {
	ctx := t.Context()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	for _, tc := range []struct {
		description      string
		gatewayClassName string
		controller       string
		finalizer        string
		expectedDeletes  int
		expectedRetry    bool
	}{
		{
			description:      "deletes the managed profile",
			gatewayClassName: name,
			controller:       operatorcontroller.OpenShiftGatewayClassControllerName,
			expectedDeletes:  1,
		},
		{
			description:      "preserves an unregistered name",
			gatewayClassName: "openshift-not-registered",
			controller:       operatorcontroller.OpenShiftGatewayClassControllerName,
		},
		{
			description:      "preserves a GatewayClass owned by another controller",
			gatewayClassName: name,
			controller:       "example.com/other",
		},
		{
			description:      "waits while a finalizer keeps it terminating",
			gatewayClassName: name,
			controller:       operatorcontroller.OpenShiftGatewayClassControllerName,
			finalizer:        "example.com/finalizer",
			expectedDeletes:  1,
			expectedRetry:    true,
		},
	} {
		t.Run(tc.description, func(t *testing.T) {
			// Build the GatewayClass and record the delete request this case makes.
			gatewayClass := managedProfileTestClass(tc.gatewayClassName, gatewayapiv1.GatewayController(tc.controller))
			if tc.finalizer != "" {
				gatewayClass.Finalizers = []string{tc.finalizer}
			}
			r := managedProfileTestReconciler(t, gatewayClass)
			base := r.client.(client.WithWatch)
			var deletes int
			var preconditions *metav1.Preconditions
			r.client = interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, delegate client.WithWatch, object client.Object, options ...client.DeleteOption) error {
				deletes++
				preconditions = (&client.DeleteOptions{}).ApplyOptions(options).Preconditions
				return delegate.Delete(ctx, object, options...)
			}})

			// A delete that cannot finish yet requeues instead of failing.
			err := r.deleteManagedProfile(ctx, gatewayClass)
			if tc.expectedRetry {
				var pending retryable.Error
				require.ErrorAs(t, err, &pending)
				require.Equal(t, 5*time.Second, pending.After())
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.expectedDeletes, deletes)

			// A delete only ever carries the UID this reconcile observed.
			if deletes > 0 {
				require.NotNil(t, preconditions)
				require.Equal(t, &gatewayClass.UID, preconditions.UID)
			}

			// Only a completed deletion removes the GatewayClass.
			have, _, err := r.currentManagedProfile(ctx, tc.gatewayClassName)
			require.NoError(t, err)
			require.Equal(t, tc.expectedRetry || tc.expectedDeletes == 0, have)
		})
	}
}

// Test_updateManagedProfileStatus checks condition merging, idempotence, and conflict reporting.
func Test_updateManagedProfileStatus(t *testing.T) {
	ctx := t.Context()
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
			// A second call must be idempotent.
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
			profile := desiredManagedProfile(name)
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

			// Repeated calls must be idempotent.
			require.Equal(t, tc.expectedPatches, patches)

			// Read back through the unwrapped client to inspect merged conditions.
			var current gatewayapiv1.GatewayClass
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(profile), &current))
			if tc.expectedConfigured {
				configured := meta.FindStatusCondition(current.Status.Conditions, "openshift.io/Configured")
				require.NotNil(t, configured)
				require.Equal(t, metav1.ConditionTrue, configured.Status)
			}

			// Every pre-existing condition must survive the update.
			for _, condition := range tc.conditions {
				require.True(t, meta.IsStatusConditionTrue(current.Status.Conditions, condition.Type))
			}
		})
	}
}

// Test_managedProfileConfigMapDefaultsReady checks presence and GatewayClass association of the defaults ConfigMap.
func Test_managedProfileConfigMapDefaultsReady(t *testing.T) {
	ctx := t.Context()
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName

	// Each case omits, mislabels, or correctly associates the ConfigMap.
	for _, tc := range []struct {
		name     string
		defaults *corev1.ConfigMap
		ready    bool
	}{
		{
			name: "defaults ConfigMap missing",
		},
		{
			name:     "defaults ConfigMap labeled for a different GatewayClass",
			defaults: managedProfileTestDefaults("another-class"),
		},
		{
			name:     "defaults ConfigMap labeled for openshift-clusterip",
			defaults: managedProfileTestDefaults(name),
			ready:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Seed the defaults ConfigMap unless this case omits it.
			var objects []client.Object
			if tc.defaults != nil {
				objects = append(objects, tc.defaults)
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
			description: "if an unrelated label changes",
			mutate: func(gatewayClass *gatewayapiv1.GatewayClass) {
				gatewayClass.Labels["example.com/other"] = "true"
			},
			expect: false,
		},
		// Changes to the owned label or description must trigger an update.
		{
			description: "if the managed profile label changes",
			mutate: func(gatewayClass *gatewayapiv1.GatewayClass) {
				gatewayClass.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] = "false"
			},
			expect: true,
		},
		{
			description: "if .spec.description changes",
			mutate: func(gatewayClass *gatewayapiv1.GatewayClass) {
				gatewayClass.Spec.Description = new("other description")
			},
			expect: true,
		},
		// A GatewayClass adopted from before managed profiles existed carries the Sail
		// lifecycle finalizer, which must be removed on the next update.
		{
			description: "if the legacy Sail finalizer is present",
			mutate: func(gatewayClass *gatewayapiv1.GatewayClass) {
				gatewayClass.Finalizers = append(gatewayClass.Finalizers, sailLibraryFinalizer)
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			// Start from the desired GatewayClass and apply the case's mutation.
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
