package gatewayclass

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func supportedProfileNames(infra *configv1.Infrastructure) []string {
	names := []string{operatorcontroller.OpenShiftExternalGatewayClassName, operatorcontroller.OpenShiftClusterIPGatewayClassName}
	if infra != nil && infra.Status.PlatformStatus != nil {
		switch infra.Status.PlatformStatus.Type {
		case configv1.AWSPlatformType, configv1.AzurePlatformType, configv1.GCPPlatformType, configv1.IBMCloudPlatformType, configv1.PowerVSPlatformType, configv1.OpenStackPlatformType:
			names = append(names, operatorcontroller.OpenShiftInternalGatewayClassName)
		}
	}
	return names
}

func isProfileName(name string) bool {
	return name == operatorcontroller.OpenShiftExternalGatewayClassName || name == operatorcontroller.OpenShiftInternalGatewayClassName || name == operatorcontroller.OpenShiftClusterIPGatewayClassName
}

func isInstallationAnchor(class *gatewayapiv1.GatewayClass) bool {
	return class.Spec.ControllerName == operatorcontroller.OpenShiftGatewayClassControllerName && class.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] != "true"
}

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

func withDesiredProfiles(infra *configv1.Infrastructure, classes []gatewayapiv1.GatewayClass) []gatewayapiv1.GatewayClass {
	supported := supportedProfileNames(infra)
	result := make([]gatewayapiv1.GatewayClass, 0, len(classes)+len(supported))
	for _, class := range classes {
		if isProfileName(class.Name) && !slices.Contains(supported, class.Name) {
			continue
		}
		result = append(result, class)
	}
	for _, name := range supported {
		found := false
		for _, class := range result {
			if class.Name == name {
				found = true
				break
			}
		}
		if !found {
			result = append(result, gatewayapiv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}},
				Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName},
			})
		}
	}
	return result
}

func (r *reconciler) removeUnsupportedInternalProfile(ctx context.Context, infra *configv1.Infrastructure) (bool, error) {
	if slices.Contains(supportedProfileNames(infra), operatorcontroller.OpenShiftInternalGatewayClassName) {
		return false, nil
	}
	var class gatewayapiv1.GatewayClass
	key := types.NamespacedName{Name: operatorcontroller.OpenShiftInternalGatewayClassName}
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
			return false, fmt.Errorf("cannot remove unsupported managed internal profile: %w", err)
		}
	}
	if err := r.client.Get(ctx, key, &class); err == nil {
		return true, nil
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	return false, nil
}

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
	sort.Slice(anchors, func(first, second int) bool {
		if anchors[first].CreationTimestamp.Equal(&anchors[second].CreationTimestamp) {
			return anchors[first].Name < anchors[second].Name
		}
		return anchors[first].CreationTimestamp.Before(&anchors[second].CreationTimestamp)
	})
	for index := range anchors {
		if anchors[index].Name == request.Name && r.config.GatewayAPIWithoutOLMEnabled {
			return &anchors[index], nil
		}
	}
	return &anchors[0], nil
}

func profileDefaultsName(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: "istio-" + operatorcontroller.IstioName("").Name + "-gatewayclass-" + name}
}

func (r *reconciler) updateCustomized(ctx context.Context, name string, status metav1.ConditionStatus, reason, message string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var current gatewayapiv1.GatewayClass
		if err := r.client.Get(ctx, types.NamespacedName{Name: name}, &current); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if !current.DeletionTimestamp.IsZero() || current.Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName || current.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] != "true" || !isProfileName(current.Name) {
			return nil
		}
		updated := current.DeepCopy()
		if !meta.SetStatusCondition(&updated.Status.Conditions, metav1.Condition{Type: "Customized", Status: status, Reason: reason, Message: message, ObservedGeneration: current.Generation, LastTransitionTime: metav1.Now()}) {
			return nil
		}
		return r.client.Status().Patch(ctx, updated, client.MergeFromWithOptions(&current, client.MergeFromWithOptimisticLock{}))
	})
}

func (r *reconciler) ensureManagedProfiles(ctx context.Context, infra *configv1.Infrastructure) (reconcile.Result, error) {
	r.sailLifecycleMu.Lock()
	defer r.sailLifecycleMu.Unlock()
	if !r.modeAccessor.AllowDependents() {
		return reconcile.Result{}, nil
	}
	anchor, err := r.installationAnchor(ctx, reconcile.Request{})
	if err != nil || anchor == nil {
		return reconcile.Result{}, err
	}
	var errs []error
	pending, err := r.removeUnsupportedInternalProfile(ctx, infra)
	if err != nil {
		errs = append(errs, err)
	}
	for _, name := range supportedProfileNames(infra) {
		var class gatewayapiv1.GatewayClass
		err := r.client.Get(ctx, types.NamespacedName{Name: name}, &class)
		missing := apierrors.IsNotFound(err)
		if err != nil && !missing {
			errs = append(errs, err)
			continue
		}
		if !missing && class.Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName {
			errs = append(errs, fmt.Errorf("cannot update GatewayClass %q: conflicting controllerName %q", name, class.Spec.ControllerName))
			continue
		}
		defaultsName := profileDefaultsName(r.config.OperandNamespace, name)
		var defaults corev1.ConfigMap
		defaultsErr := r.client.Get(ctx, defaultsName, &defaults)
		if defaultsErr != nil && !apierrors.IsNotFound(defaultsErr) {
			errs = append(errs, defaultsErr)
			continue
		}
		status, reason := metav1.ConditionTrue, "Configured"
		message := fmt.Sprintf("ConfigMap %s is present and associated with GatewayClass %s. Inspect the ConfigMap for configuration details.", defaultsName, name)
		if defaultsErr != nil || defaults.Labels[operatorcontroller.GatewayClassDefaultsLabel] != name {
			pending = true
			status, reason = metav1.ConditionFalse, "ConfigurationMissing"
			message = fmt.Sprintf("ConfigMap %s is absent or is not associated with GatewayClass %s.", defaultsName, name)
			if r.sailInstaller != nil {
				r.sailInstaller.Enqueue()
			}
		} else if missing {
			class = gatewayapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}}, Spec: gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName}}
			if err := r.client.Create(ctx, &class); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := r.updateCustomized(ctx, name, status, reason, message); err != nil {
			errs = append(errs, err)
		}
	}
	result := reconcile.Result{}
	if pending {
		result.RequeueAfter = 5 * time.Second
	}
	return result, utilerrors.NewAggregate(errs)
}
