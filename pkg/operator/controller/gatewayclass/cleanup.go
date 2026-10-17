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
		pending, err := r.deleteManagedProfile(ctx)
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
		if err := r.deleteManagedProfileDefaults(ctx); err != nil {
			return reconcile.Result{}, err
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
