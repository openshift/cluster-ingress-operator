package gatewayclass

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/istio-ecosystem/sail-operator/pkg/install"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	retryable "github.com/openshift/cluster-ingress-operator/pkg/util/retryableerror"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// managedProfiles lists enabled GatewayClass profiles and descriptions (limited to 64 characters).
var managedProfiles = map[string]string{
	operatorcontroller.OpenShiftClusterIPGatewayClassName: "OpenShift Gateway using a cluster-internal ClusterIP Service",
}

// syncManagedProfile ensures one profile's presence without installing or uninstalling Sail.
func (r *reconciler) syncManagedProfile(ctx context.Context, name string) (reconcile.Result, error) {
	if !r.modeAccessor.AllowDependents() {
		log.Info("Management mode does not allow dependents, skipping managed GatewayClass profile", "request", name)
		return reconcile.Result{}, nil
	}

	var classes gatewayapiv1.GatewayClassList
	if err := r.cache.List(ctx, &classes, client.MatchingFields{
		operatorcontroller.GatewayClassIndexFieldName: operatorcontroller.OpenShiftGatewayClassControllerName,
	}); err != nil {
		return reconcile.Result{}, err
	}

	// Only user-created OpenShift GatewayClasses enable managed profile resources.
	wantProfile := countActiveUserCreatedGatewayClasses(&classes, "") > 0
	return r.ensureManagedProfile(ctx, name, wantProfile)
}

// ensureManagedProfile ensures one class is present or absent, and reconciles owned fields and status when wanted.
func (r *reconciler) ensureManagedProfile(ctx context.Context, name string, want bool) (reconcile.Result, error) {
	// Read the named GatewayClass; existing classes are checked in the cases below.
	have, current, err := r.currentManagedProfile(ctx, name)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Read the associated ConfigMap defaults before creating a class or updating status.
	defaultsReady := false
	if want {
		defaultsReady, err = r.managedProfileConfigMapDefaultsReady(ctx, name)
		if err != nil {
			return reconcile.Result{}, err
		}
	}
	desired := desiredManagedProfile(name)
	switch {
	case !want && !have:
		return reconcile.Result{}, nil
	case !want && have:
		return reconcile.Result{}, r.deleteManagedProfile(ctx, current)
	case want && !have:
		if defaultsReady {
			if err := r.createManagedProfile(ctx, desired); err != nil {
				return reconcile.Result{}, err
			}
		} else {
			defaultsName := managedProfileConfigMapDefaultsName(r.config.OperandNamespace, name)
			log.Info("waiting for defaults ConfigMap before creating managed GatewayClass",
				"gatewayclass", name, "configmap", defaultsName.String())
		}
	case want && have:
		if current.Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName {
			// A class created under a reserved name by another controller cannot
			// be adopted: its controllerName is immutable, so it must be deleted.
			log.Error(fmt.Errorf("conflicting controllerName %q", current.Spec.ControllerName),
				"cannot manage GatewayClass with a reserved name; delete the class to let the Ingress Operator manage the profile",
				"gatewayclass", name)
			r.recorder.Eventf(current, corev1.EventTypeWarning, "GatewayClassConflict",
				"GatewayClass %q is managed by another controller (%q); delete it to let the Ingress Operator manage the profile",
				name, current.Spec.ControllerName)
			return reconcile.Result{}, nil
		}
		if err := r.updateManagedProfile(ctx, current, desired); err != nil {
			return reconcile.Result{}, err
		}
	}

	// Update CIO conditions together without replacing other controllers' conditions.
	if err := r.syncManagedProfileStatus(ctx, name, defaultsReady); err != nil {
		// Retry a missing class through the queue, including a class just created above.
		if apierrors.IsNotFound(err) {
			return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return reconcile.Result{}, err
	}

	// Retry until the defaults ConfigMap appears.
	if !defaultsReady {
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}
	return reconcile.Result{}, nil
}

// deleteAllManagedProfiles requests deletion of managed classes, returning retryable errors for pending deletion.
func (r *reconciler) deleteAllManagedProfiles(ctx context.Context) error {
	for name := range managedProfiles {
		if _, err := r.ensureManagedProfile(ctx, name, false); err != nil {
			return err
		}
	}
	return nil
}

// markProfilesUnmanagedConfigured sets owned profiles' openshift.io/Configured
// condition to False/Unmanaged during a mode transition to Unmanaged.
//
// The gatewayapi controller calls it via UninstallSail, while the gatewayclass
// controller writes the same condition via syncManagedProfileStatus, so
// profileStatusMu keeps a late reconcile from overwriting the Unmanaged status.
func (r *reconciler) markProfilesUnmanagedConfigured(ctx context.Context) error {
	r.profileStatusMu.Lock()
	defer r.profileStatusMu.Unlock()

	var errs []error
	for name := range managedProfiles {
		defaultsName := managedProfileConfigMapDefaultsName(r.config.OperandNamespace, name)
		message := fmt.Sprintf("ConfigMap %s and its association with GatewayClass %s are not managed or guaranteed by the Ingress Operator in Unmanaged mode.", defaultsName, name)
		if err := updateManagedProfileStatus(ctx, r.client, name, metav1.ConditionFalse, "Unmanaged", message, nil); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			errs = append(errs, fmt.Errorf("cannot set GatewayClass %q openshift.io/Configured condition to False/Unmanaged: %w", name, err))
		}
	}
	return utilerrors.NewAggregate(errs)
}

// withDesiredManagedProfiles includes enabled classes' ConfigMap defaults in Sail inputs before class creation.
func withDesiredManagedProfiles(classes []gatewayapiv1.GatewayClass) []gatewayapiv1.GatewayClass {
	classes = slices.Clone(classes)
	for name := range managedProfiles {
		if !slices.ContainsFunc(classes, func(class gatewayapiv1.GatewayClass) bool { return class.Name == name }) {
			classes = append(classes, *desiredManagedProfile(name))
		}
	}
	return classes
}

// isManagedProfileConfigMapDefaults limits watches to enabled classes' ConfigMap defaults in the operand namespace.
func (r *reconciler) isManagedProfileConfigMapDefaults(object client.Object) bool {
	return object.GetNamespace() == r.config.OperandNamespace &&
		isManagedProfileConfigMapDefaultsName(object.GetNamespace(), object.GetName())
}

// requestsForManagedProfileConfigMapDefaults maps defaults changes, including label removal, to their class.
func (r *reconciler) requestsForManagedProfileConfigMapDefaults(_ context.Context, object client.Object) []reconcile.Request {
	for name := range managedProfiles {
		if client.ObjectKeyFromObject(object) == managedProfileConfigMapDefaultsName(r.config.OperandNamespace, name) {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name}}}
		}
	}
	return nil
}

// syncManagedProfileStatus reports defaults readiness and installation without resetting Accepted.
func (r *reconciler) syncManagedProfileStatus(ctx context.Context, name string, defaultsReady bool) error {
	// Serialize with markProfilesUnmanagedConfigured. If the mode changed
	// while this reconcile was in flight, the transition owns the status and
	// this controller's watches are gated off, so a write now would never be
	// corrected.
	r.profileStatusMu.Lock()
	defer r.profileStatusMu.Unlock()
	if !r.modeAccessor.AllowDependents() {
		return nil
	}

	defaultsName := managedProfileConfigMapDefaultsName(r.config.OperandNamespace, name)
	status, reason := metav1.ConditionTrue, "Configured"
	message := fmt.Sprintf("ConfigMap %s is present and associated with GatewayClass %s. Inspect the ConfigMap for configuration details.", defaultsName, name)

	if !defaultsReady {
		status, reason = metav1.ConditionFalse, "ConfigurationMissing"
		message = fmt.Sprintf("ConfigMap %s is absent or is not associated with GatewayClass %s.", defaultsName, name)
	}

	return updateManagedProfileStatus(ctx, r.client, name, status, reason, message, new(r.sailInstaller.Status()))
}

// updateManagedProfileStatus reconciles openshift.io/Configured and optional installation conditions without replacing other controllers' updates.
func updateManagedProfileStatus(ctx context.Context, cl client.Client, name string, status metav1.ConditionStatus, reason, message string, installationStatus *install.Status) error {
	// Read once so newly created classes and current Accepted conditions are visible.
	var current gatewayapiv1.GatewayClass
	if err := cl.Get(ctx, types.NamespacedName{Name: name}, &current); err != nil {
		return err
	}

	// Update only a registered OpenShift profile that is not being deleted.
	if !current.DeletionTimestamp.IsZero() || !isManagedProfile(&current) {
		return nil
	}

	// Merge CIO conditions into a copy, leaving other conditions and the caller intact.
	updated := current.DeepCopy()
	changed := meta.SetStatusCondition(&updated.Status.Conditions, metav1.Condition{
		Type: "openshift.io/Configured", Status: status, Reason: reason, Message: message,
		ObservedGeneration: current.Generation, LastTransitionTime: metav1.Now(),
	})
	if installationStatus != nil {
		changed = mapStatusToConditions(*installationStatus, current.Generation, &updated.Status.Conditions) || changed
	}
	if !changed {
		return nil
	}

	// Patch owned status conditions without overwriting concurrent condition updates.
	return cl.Status().Patch(ctx, updated, client.MergeFromWithOptions(&current, client.MergeFromWithOptimisticLock{}))
}

// createManagedProfile creates the class only after its defaults are available.
func (r *reconciler) createManagedProfile(ctx context.Context, desired *gatewayapiv1.GatewayClass) error {
	if err := r.client.Create(ctx, desired); err != nil {
		return fmt.Errorf("failed to create managed GatewayClass %q: %w", desired.Name, err)
	}
	log.Info("created managed GatewayClass", "gatewayclass", desired.Name)
	return nil
}

// updateManagedProfile repairs a managed profile's owned fields, including
// removing the legacy Sail finalizer from a class that is being deleted.
func (r *reconciler) updateManagedProfile(ctx context.Context, current, desired *gatewayapiv1.GatewayClass) error {
	if !isManagedProfile(current) {
		return nil
	}

	changed, updated := managedProfileChanged(current, desired)
	if !changed {
		return nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated)
	if err := r.client.Update(ctx, updated); err != nil {
		return fmt.Errorf("failed to update managed GatewayClass %q: %w", current.Name, err)
	}
	log.Info("updated managed GatewayClass profile", "gatewayclass", current.Name, "diff", diff)
	return nil
}

// managedProfileChanged reports whether the profile label, description, or Sail
// finalizer differ, and returns the updated class.
func managedProfileChanged(current, expected *gatewayapiv1.GatewayClass) (bool, *gatewayapiv1.GatewayClass) {
	if current.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] == "true" &&
		cmp.Equal(current.Spec.Description, expected.Spec.Description) &&
		!controllerutil.ContainsFinalizer(current, sailLibraryFinalizer) {
		return false, nil
	}

	updated := current.DeepCopy()
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	updated.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] = "true"
	updated.Spec.Description = expected.Spec.Description
	// A class adopted from before managed profiles existed may still carry the
	// Sail lifecycle finalizer. Profiles neither install nor uninstall Sail, so
	// that finalizer must not keep the class from being deleted.
	controllerutil.RemoveFinalizer(updated, sailLibraryFinalizer)
	return true, updated
}

// deleteManagedProfile requests UID-preconditioned deletion of a registered OpenShift profile.
// It returns a retryable error while the class is still present in the cache.
func (r *reconciler) deleteManagedProfile(ctx context.Context, current *gatewayapiv1.GatewayClass) error {
	if !isManagedProfile(current) {
		return nil
	}

	// Update paths remove the legacy Sail finalizer through managedProfileChanged;
	// deletion does not reach them, so remove it here as well.
	if controllerutil.ContainsFinalizer(current, sailLibraryFinalizer) {
		updated := current.DeepCopy()
		controllerutil.RemoveFinalizer(updated, sailLibraryFinalizer)
		if err := r.client.Update(ctx, updated); err != nil {
			return fmt.Errorf("failed to remove Sail finalizer from managed GatewayClass %q: %w", current.Name, err)
		}
		log.Info("removed Sail finalizer from managed profile", "gatewayclass", current.Name)
		current = updated
	}

	// Delete only the observed UID; completion is checked on later cache snapshots.
	if current.DeletionTimestamp.IsZero() {
		if err := r.client.Delete(ctx, current, client.Preconditions{UID: &current.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete managed GatewayClass %q: %w", current.Name, err)
		}
	}

	if have, _, err := r.currentManagedProfile(ctx, current.Name); err != nil {
		return err
	} else if have {
		return retryable.New(fmt.Errorf("waiting for managed GatewayClass %q to be deleted", current.Name), 5*time.Second)
	}
	return nil
}

// managedProfileConfigMapDefaultsReady checks the expected defaults and their class association.
func (r *reconciler) managedProfileConfigMapDefaultsReady(ctx context.Context, name string) (bool, error) {
	var defaults corev1.ConfigMap
	if err := r.cache.Get(ctx, managedProfileConfigMapDefaultsName(r.config.OperandNamespace, name), &defaults); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return defaults.Labels[operatorcontroller.GatewayClassDefaultsLabel] == name, nil
}

// desiredManagedProfile builds an enabled operator-managed GatewayClass.
func desiredManagedProfile(name string) *gatewayapiv1.GatewayClass {
	return &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				operatorcontroller.ManagedGatewayClassProfileLabel: "true",
			},
		},
		Spec: gatewayapiv1.GatewayClassSpec{
			ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName,
			Description:    new(managedProfiles[name]),
		},
	}
}

// currentManagedProfile reads the named GatewayClass from the controller cache.
func (r *reconciler) currentManagedProfile(ctx context.Context, name string) (bool, *gatewayapiv1.GatewayClass, error) {
	current := &gatewayapiv1.GatewayClass{}
	if err := r.cache.Get(ctx, types.NamespacedName{Name: name}, current); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

// isManagedProfileName identifies an enabled managed GatewayClass profile.
func isManagedProfileName(name string) bool {
	_, enabled := managedProfiles[name]
	return enabled
}

// isManagedProfile identifies profiles by registered name and controller, independently of output labels.
func isManagedProfile(class *gatewayapiv1.GatewayClass) bool {
	return isManagedProfileName(class.Name) && class.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName
}

// managedProfileConfigMapDefaultsName locates the ConfigMap defaults rendered by Sail for a managed class.
func managedProfileConfigMapDefaultsName(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: "istio-" + operatorcontroller.IstioName("").Name + "-gatewayclass-" + name}
}

// isManagedProfileConfigMapDefaultsName matches enabled classes' ConfigMap defaults independently of labels.
func isManagedProfileConfigMapDefaultsName(namespace, name string) bool {
	for profile := range managedProfiles {
		if name == managedProfileConfigMapDefaultsName(namespace, profile).Name {
			return true
		}
	}
	return false
}
