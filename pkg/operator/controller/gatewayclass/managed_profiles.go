package gatewayclass

import (
	"context"
	"fmt"
	"time"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// isManagedProfileName identifies the managed ClusterIP GatewayClass.
func isManagedProfileName(name string) bool {
	return name == operatorcontroller.OpenShiftClusterIPGatewayClassName
}

// isInstallationAnchor reports whether a user class can retain the Sail installation.
func isInstallationAnchor(class *gatewayapiv1.GatewayClass) bool {
	return class.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName && class.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] != "true"
}

// enrollInstallationAnchors protects eligible user classes until Sail cleanup completes.
func (r *reconciler) enrollInstallationAnchors(ctx context.Context) error {
	var classes gatewayapiv1.GatewayClassList
	if err := r.client.List(ctx, &classes); err != nil {
		return err
	}
	for _, class := range classes.Items {
		if !isInstallationAnchor(&class) || !class.DeletionTimestamp.IsZero() {
			continue
		}
		if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			var current gatewayapiv1.GatewayClass
			if err := r.client.Get(ctx, client.ObjectKeyFromObject(&class), &current); err != nil {
				if apierrors.IsNotFound(err) {
					return nil
				}
				return err
			}
			if !isInstallationAnchor(&current) || !current.DeletionTimestamp.IsZero() {
				return nil
			}
			updated := current.DeepCopy()
			if !controllerutil.AddFinalizer(updated, sailLibraryFinalizer) {
				return nil
			}
			return r.client.Patch(ctx, updated, client.MergeFromWithOptions(&current, client.MergeFromWithOptimisticLock{}))
		}); err != nil {
			return fmt.Errorf("failed to enroll GatewayClass %q for lifecycle cleanup: %w", class.Name, err)
		}
	}
	return nil
}

// withDesiredManagedProfile includes ClusterIP defaults in Sail inputs before class creation.
func withDesiredManagedProfile(classes []gatewayapiv1.GatewayClass) []gatewayapiv1.GatewayClass {
	for _, class := range classes {
		if isManagedProfileName(class.Name) {
			return classes
		}
	}
	return append(append([]gatewayapiv1.GatewayClass(nil), classes...), gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: operatorcontroller.OpenShiftClusterIPGatewayClassName, Labels: map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}},
		Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName},
	})
}

// installationAnchor selects a live user class, preferring the triggering class.
func (r *reconciler) installationAnchor(ctx context.Context, request reconcile.Request) (*gatewayapiv1.GatewayClass, error) {
	var classes gatewayapiv1.GatewayClassList
	if err := r.client.List(ctx, &classes); err != nil {
		return nil, err
	}
	var anchors []gatewayapiv1.GatewayClass
	for _, class := range classes.Items {
		if isInstallationAnchor(&class) && class.DeletionTimestamp.IsZero() {
			anchors = append(anchors, class)
		}
	}
	if len(anchors) == 0 {
		return nil, nil
	}
	for index := range anchors {
		if anchors[index].Name == request.Name {
			return &anchors[index], nil
		}
	}
	return &anchors[0], nil
}

// managedProfileDefaultsName locates the defaults ConfigMap rendered by Sail for a managed profile.
func managedProfileDefaultsName(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: "istio-" + operatorcontroller.IstioName("").Name + "-gatewayclass-" + name}
}

// updateManagedProfileCustomized changes only CIO's condition on a live managed profile.
func (r *reconciler) updateManagedProfileCustomized(ctx context.Context, name string, status metav1.ConditionStatus, reason, message string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var current gatewayapiv1.GatewayClass
		if err := r.client.Get(ctx, types.NamespacedName{Name: name}, &current); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if !current.DeletionTimestamp.IsZero() || current.Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName || current.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] != "true" || !isManagedProfileName(current.Name) {
			return nil
		}
		updated := current.DeepCopy()
		if !meta.SetStatusCondition(&updated.Status.Conditions, metav1.Condition{Type: "Customized", Status: status, Reason: reason, Message: message, ObservedGeneration: current.Generation, LastTransitionTime: metav1.Now()}) {
			return nil
		}
		return r.client.Status().Patch(ctx, updated, client.MergeFromWithOptions(&current, client.MergeFromWithOptimisticLock{}))
	})
}

// ensureManagedProfile creates the managed ClusterIP class after verifying its defaults association.
func (r *reconciler) ensureManagedProfile(ctx context.Context) (reconcile.Result, error) {
	r.sailLifecycleMu.Lock()
	defer r.sailLifecycleMu.Unlock()
	if !r.modeAccessor.AllowDependents() {
		return reconcile.Result{}, nil
	}
	anchor, err := r.installationAnchor(ctx, reconcile.Request{})
	if err != nil || anchor == nil {
		return reconcile.Result{}, err
	}
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	var class gatewayapiv1.GatewayClass
	err = r.client.Get(ctx, types.NamespacedName{Name: name}, &class)
	missing := apierrors.IsNotFound(err)
	if err != nil && !missing {
		return reconcile.Result{}, err
	}
	if !missing && class.Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName {
		return reconcile.Result{}, fmt.Errorf("cannot update GatewayClass %q: conflicting controllerName %q", name, class.Spec.ControllerName)
	}
	defaultsName := managedProfileDefaultsName(r.config.OperandNamespace, name)
	var defaults corev1.ConfigMap
	defaultsErr := r.client.Get(ctx, defaultsName, &defaults)
	if defaultsErr != nil && !apierrors.IsNotFound(defaultsErr) {
		return reconcile.Result{}, defaultsErr
	}
	result := reconcile.Result{}
	status, reason := metav1.ConditionTrue, "Configured"
	message := fmt.Sprintf("ConfigMap %s is present and associated with GatewayClass %s. Inspect the ConfigMap for configuration details.", defaultsName, name)
	if defaultsErr != nil || defaults.Labels[operatorcontroller.GatewayClassDefaultsLabel] != name {
		result.RequeueAfter = 5 * time.Second
		status, reason = metav1.ConditionFalse, "ConfigurationMissing"
		message = fmt.Sprintf("ConfigMap %s is absent or is not associated with GatewayClass %s.", defaultsName, name)
		if r.sailInstaller != nil {
			r.sailInstaller.Enqueue()
		}
	} else if missing {
		class = gatewayapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}}, Spec: gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName}}
		if err := r.client.Create(ctx, &class); err != nil {
			return result, err
		}
	}
	return result, r.updateManagedProfileCustomized(ctx, name, status, reason, message)
}

// cleanupInstallation waits for managed resources, then delegates completion to Sail uninstall.
func (r *reconciler) cleanupInstallation(ctx context.Context) (reconcile.Result, error) {
	if r.modeAccessor.AllowDependents() {
		pending, err := r.deleteManagedProfile(ctx)
		if err != nil {
			return reconcile.Result{}, err
		}
		if pending {
			return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if err := r.deleteManagedProfileDefaults(ctx); err != nil {
			return reconcile.Result{}, err
		}
	}
	if r.sailInstaller != nil {
		if err := r.sailInstaller.Uninstall(ctx, r.config.OperandNamespace, operatorcontroller.IstioName("").Name); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to uninstall Istio: %w", err)
		}
	}
	return reconcile.Result{}, nil
}

// deleteManagedProfile deletes CIO's ClusterIP class and reports pending deletion.
func (r *reconciler) deleteManagedProfile(ctx context.Context) (bool, error) {
	var class gatewayapiv1.GatewayClass
	key := types.NamespacedName{Name: operatorcontroller.OpenShiftClusterIPGatewayClassName}
	if err := r.client.Get(ctx, key, &class); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if class.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] != "true" || class.Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName {
		return false, nil
	}
	if class.DeletionTimestamp.IsZero() {
		if err := r.client.Delete(ctx, &class, client.Preconditions{UID: &class.UID}); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	if err := r.client.Get(ctx, key, &class); err == nil {
		return true, nil
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	return false, nil
}

// deleteManagedProfileDefaults waits for owned Sail profile defaults to disappear.
func (r *reconciler) deleteManagedProfileDefaults(ctx context.Context) error {
	name := operatorcontroller.OpenShiftClusterIPGatewayClassName
	var defaults corev1.ConfigMap
	key := managedProfileDefaultsName(r.config.OperandNamespace, name)
	if err := r.client.Get(ctx, key, &defaults); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	managedBy := defaults.Labels["managed-by"]
	if defaults.Labels[operatorcontroller.GatewayClassDefaultsLabel] != name || managedBy != "sail-library" || defaults.Labels["release"] != operatorcontroller.IstioName("").Name+"-istiod" {
		return fmt.Errorf("cannot delete profile defaults %s: ConfigMap is not owned by the CIO Istio release", key)
	}
	if err := r.client.Delete(ctx, &defaults, client.Preconditions{UID: &defaults.UID}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := r.client.Get(ctx, key, &corev1.ConfigMap{}); err == nil {
		return fmt.Errorf("waiting for profile defaults %s to be deleted", key)
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
