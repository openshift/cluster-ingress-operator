package gatewayclass

import (
	"context"
	"fmt"
	"time"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// cleanupInstallation orders profile, Sail release and defaults deletion before anchor release.
func (r *reconciler) cleanupInstallation(ctx context.Context) (reconcile.Result, error) {
	if r.modeAccessor.AllowDependents() {
		pending, err := r.deleteManagedProfiles(ctx)
		if err != nil {
			return reconcile.Result{}, err
		}
		if pending {
			return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}
	if err := r.uninstallIstio(ctx); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to uninstall Istio: %w", err)
	}
	if r.modeAccessor.AllowDependents() {
		if err := r.deleteProfileDefaults(ctx); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

// deleteManagedProfiles deletes CIO's profile classes and reports pending deletion.
func (r *reconciler) deleteManagedProfiles(ctx context.Context) (bool, error) {
	var classes gatewayapiv1.GatewayClassList
	if err := r.client.List(ctx, &classes); err != nil {
		return false, err
	}
	pending := false
	for _, class := range classes.Items {
		if class.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] != "true" || !isProfileName(class.Name) || class.Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName {
			continue
		}
		if class.DeletionTimestamp.IsZero() {
			if err := r.client.Delete(ctx, &class, client.Preconditions{UID: &class.UID}); err != nil && !apierrors.IsNotFound(err) {
				return false, err
			}
		}
		var current gatewayapiv1.GatewayClass
		if err := r.client.Get(ctx, types.NamespacedName{Name: class.Name}, &current); err == nil {
			pending = true
		} else if !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return pending, nil
}

// deleteProfileDefaults waits for owned Sail profile defaults to disappear.
func (r *reconciler) deleteProfileDefaults(ctx context.Context) error {
	for _, name := range []string{operatorcontroller.OpenShiftExternalGatewayClassName, operatorcontroller.OpenShiftInternalGatewayClassName, operatorcontroller.OpenShiftClusterIPGatewayClassName} {
		var defaults corev1.ConfigMap
		key := profileDefaultsName(r.config.OperandNamespace, name)
		if err := r.client.Get(ctx, key, &defaults); err != nil {
			if apierrors.IsNotFound(err) {
				continue
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
	}
	return nil
}

// uninstallIstio stops Sail and waits for its recorded release resources to disappear.
func (r *reconciler) uninstallIstio(ctx context.Context) error {
	if r.sailInstaller == nil {
		return nil
	}
	r.sailInstaller.Stop()
	r.sailStopped = true
	if err := r.captureSailTeardown(ctx); err != nil {
		return err
	}
	if err := r.sailInstaller.Uninstall(ctx, r.config.OperandNamespace, operatorcontroller.IstioName("").Name); err != nil {
		return err
	}
	return r.finishSailTeardown(ctx)
}
