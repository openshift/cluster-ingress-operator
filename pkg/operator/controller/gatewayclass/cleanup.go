package gatewayclass

import (
	"context"
	"fmt"
	"time"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

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
		if defaults.Labels[operatorcontroller.GatewayClassDefaultsLabel] != name || (managedBy != "sail-library" && managedBy != "sail-operator") || defaults.Labels["release"] != operatorcontroller.IstioName("").Name+"-istiod" {
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

func (r *reconciler) uninstallIstio(ctx context.Context) error {
	if r.sailInstaller != nil {
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
	key := operatorcontroller.IstioName("")
	var istio sailv1.Istio
	err := r.client.Get(ctx, key, &istio)
	if err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return err
	}
	if err == nil {
		owned := false
		for _, owner := range istio.OwnerReferences {
			if owner.Kind != "GatewayClass" || owner.APIVersion != gatewayapiv1.SchemeGroupVersion.String() {
				continue
			}
			var class gatewayapiv1.GatewayClass
			if err := r.client.Get(ctx, types.NamespacedName{Name: owner.Name}, &class); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return err
			}
			owned = owned || (class.UID == owner.UID && isInstallationAnchor(&class))
		}
		if !owned || istio.Spec.Namespace != r.config.OperandNamespace {
			return fmt.Errorf("cannot uninstall Istio %q: resource is not owned by a CIO GatewayClass anchor", istio.Name)
		}
		checkpoint, inventory, err := r.teardownInventory(ctx)
		if err != nil {
			return err
		}
		inventory = append(inventory, teardownResource{APIVersion: sailv1.GroupVersion.String(), Kind: "Istio", Name: istio.Name, UID: istio.UID})
		var revision sailv1.IstioRevision
		if err := r.client.Get(ctx, key, &revision); err == nil {
			for _, owner := range revision.OwnerReferences {
				if owner.APIVersion == sailv1.GroupVersion.String() && owner.Kind == "Istio" && owner.Name == istio.Name && owner.UID == istio.UID && revision.Spec.Namespace == r.config.OperandNamespace {
					inventory = append(inventory, teardownResource{APIVersion: sailv1.GroupVersion.String(), Kind: "IstioRevision", Name: revision.Name, UID: revision.UID})
					break
				}
			}
		} else if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return err
		}
		if err := r.saveTeardownInventory(ctx, checkpoint, inventory); err != nil {
			return err
		}
		if istio.DeletionTimestamp.IsZero() {
			if err := r.client.Delete(ctx, &istio, client.Preconditions{UID: &istio.UID}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		if err := r.client.Get(ctx, key, &sailv1.Istio{}); err == nil {
			return fmt.Errorf("waiting for CIO-managed Istio %q teardown", key.Name)
		} else if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return err
		}
	}
	return r.finishSailTeardown(ctx)
}
