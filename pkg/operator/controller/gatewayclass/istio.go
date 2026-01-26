package gatewayclass

import (
	"context"
	"fmt"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/config"
	"github.com/istio-ecosystem/sail-operator/pkg/install"
	"github.com/istio-ecosystem/sail-operator/resources"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

const (
	// systemClusterCriticalPriorityClassName is the keyword to specify
	// cluster-critical priority class in a pod's spec.priorityClassName.
	systemClusterCriticalPriorityClassName = "system-cluster-critical"

	// WorkloadPartitioningManagementAnnotationKey is the annotation key for
	// workload partitioning management used by OpenShift to schedule pods
	// on management nodes.
	WorkloadPartitioningManagementAnnotationKey = "target.workload.openshift.io/management"

	// WorkloadPartitioningManagementPreferredScheduling is the value for
	// preferred scheduling on management nodes.
	WorkloadPartitioningManagementPreferredScheduling = `{"effect": "PreferredDuringScheduling"}`
)

// ensureIstio installs or updates Istio using the Sail Library.
// It returns an error if the installation fails.
func (r *reconciler) ensureIstio(ctx context.Context, gatewayclass *gatewayapiv1.GatewayClass, istioVersion string) error {
	enableInferenceExtension, err := r.inferencepoolCrdExists(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for InferencePool CRD: %w", err)
	}

	// Create the Sail Library installer
	installer, err := install.NewInstaller(install.Options{
		KubeConfig:        r.kubeConfig,
		ResourceFS:        resources.FS,
		Platform:          config.PlatformOpenShift,
		DefaultProfile:    "openshift",
		OperatorNamespace: r.config.OperatorNamespace,
	})
	if err != nil {
		return fmt.Errorf("failed to create Sail installer: %w", err)
	}

	// Create owner reference for garbage collection
	ownerRef := metav1.OwnerReference{
		APIVersion: gatewayapiv1.SchemeGroupVersion.String(),
		Kind:       "GatewayClass",
		Name:       gatewayclass.Name,
		UID:        gatewayclass.UID,
		Controller: ptr.To(true),
	}

	// Get OpenShift-specific value overrides
	values := openshiftValues(enableInferenceExtension)

	// Install using the gateway-api preset with OpenShift-specific overrides
	err = installer.InstallWithOwnerReference(ctx, install.PresetGatewayAPI, &install.Overrides{
		Namespace: r.config.OperandNamespace,
		Version:   istioVersion,
		Values:    values,
	}, &ownerRef)
	if err != nil {
		return fmt.Errorf("failed to install Istio: %w", err)
	}

	log.Info("installed/updated Istio via Sail Library", "namespace", r.config.OperandNamespace, "version", istioVersion)
	return nil
}

// inferencepoolCrdExists returns a Boolean value indicating whether the
// InferencePool CRD exists under the inference.networking.k8s.io or
// inference.networking.x-k8s.io API group.
func (r *reconciler) inferencepoolCrdExists(ctx context.Context) (bool, error) {
	if v, err := r.crdExists(ctx, inferencepoolCrdName); err != nil {
		return false, err
	} else if v {
		return true, nil
	}

	if v, err := r.crdExists(ctx, inferencepoolExperimentalCrdName); err != nil {
		return false, err
	} else if v {
		return true, nil
	}

	return false, nil
}

// crdExists returns a Boolean value indicating whether the named CRD exists.
func (r *reconciler) crdExists(ctx context.Context, crdName string) (bool, error) {
	namespacedName := types.NamespacedName{Name: crdName}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := r.cache.Get(ctx, namespacedName, &crd); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get CRD %s: %w", crdName, err)
	}
	return true, nil
}

// openshiftValues returns the OpenShift-specific value overrides for Istio.
// These values are merged on top of the gateway-api preset defaults.
func openshiftValues(enableInferenceExtension bool) *sailv1.Values {
	pilotEnv := map[string]string{
		// OpenShift-specific gatewayclass name
		"PILOT_GATEWAY_API_DEFAULT_GATEWAYCLASS_NAME": controller.OpenShiftDefaultGatewayClassName,
		// OpenShift-specific controller name
		"PILOT_GATEWAY_API_CONTROLLER_NAME": controller.OpenShiftGatewayClassControllerName,
	}
	if enableInferenceExtension {
		pilotEnv["ENABLE_GATEWAY_API_INFERENCE_EXTENSION"] = "true"
	}

	return &sailv1.Values{
		Global: &sailv1.GlobalConfig{
			DefaultPodDisruptionBudget: &sailv1.DefaultPodDisruptionBudgetConfig{
				Enabled: ptr.To(false),
			},
			IstioNamespace: ptr.To(controller.DefaultOperandNamespace),
			// Use system-cluster-critical priority for OpenShift system workloads
			PriorityClassName: ptr.To(systemClusterCriticalPriorityClassName),
			// OpenShift-specific CA trust bundle
			TrustBundleName: ptr.To(controller.OpenShiftGatewayCARootCertName),
		},
		Pilot: &sailv1.PilotConfig{
			Env: pilotEnv,
			// Workload partitioning for OpenShift management workloads
			PodAnnotations: map[string]string{
				WorkloadPartitioningManagementAnnotationKey: WorkloadPartitioningManagementPreferredScheduling,
			},
		},
	}
}
