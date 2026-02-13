package gatewayclass

import (
	"context"
	"fmt"

	"github.com/istio-ecosystem/sail-operator/pkg/install"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	ControllerInstalledConditionType = "ControllerInstalled"
	CRDsReadyConditionType           = "CRDsReady"
)

// ensureIstio installs or updates Istio using the Sail Library.
// It returns an error if the installation fails.
func (r *reconciler) ensureIstio(ctx context.Context, gatewayclass *gatewayapiv1.GatewayClass, istioVersion string) error {

	// TODO: check if anything is null here, to avoid panics
	sailInstaller := r.config.SailOperatorReconciler.Installer

	enableInferenceExtension, err := r.inferencepoolCrdExists(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for InferencePool CRD: %w", err)
	}

	// Build options from current state
	opts, err := r.buildInstallerOptions(enableInferenceExtension, istioVersion)
	if err != nil {
		return err
	}

	// Enqueue the request to reconcile. This does not return
	// any error or status. We validate the status again next, and set
	// the right conditions on the GatewayClass for it
	sailInstaller.Apply(opts)

	// Istio adds its own Accepted condition: https://github.com/istio/istio/blob/24eab8800c50b999d01d2dd6ec589bbd59d01726/pilot/pkg/config/kube/gateway/gatewayclass.go#L114
	// We must generate a specific Openshift condition for it
	// TODO: merge the conditions, remove conditions in case OLM is being used instead of this
	status := sailInstaller.Status()

	mapStatusToConditions(status, gatewayclass.Generation, &gatewayclass.Status.Conditions)

	return nil
}

func (r *reconciler) buildInstallerOptions(enableInferenceExtension bool, istioVersion string) (install.Options, error) {
	// Start with Gateway API defaults
	values := install.GatewayAPIDefaults()

	// Apply OpenShift-specific overrides
	openshiftOverrides := openshiftValues(enableInferenceExtension)
	values = install.MergeValues(values, openshiftOverrides)

	return install.Options{
		Namespace:      controller.DefaultOperandNamespace,
		Revision:       controller.IstioName("").Name,
		Values:         values,
		Version:        istioVersion,
		ManageCRDs:     ptr.To(true),
		IncludeAllCRDs: ptr.To(true),
	}, nil
}

// mapStatusToConditions translates the library Status into GatewayClass conditions.
func mapStatusToConditions(status install.Status, generation int64, conditions *[]metav1.Condition) {
	installed := metav1.Condition{
		Type:               ControllerInstalledConditionType,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}
	if status.Installed {
		installed.Status = metav1.ConditionTrue
		installed.Reason = "Installed"
		installed.Message = fmt.Sprintf("istiod %s installed", status.Version)
	} else if status.Error != nil {
		installed.Status = metav1.ConditionFalse
		installed.Reason = "InstallFailed"
		installed.Message = status.Error.Error()
	} else {
		installed.Status = metav1.ConditionUnknown
		installed.Reason = "Pending"
		installed.Message = "waiting for first reconciliation"
	}

	meta.SetStatusCondition(conditions, installed)

	// CRD condition: reflects CRD ownership state.
	crd := metav1.Condition{
		Type:               CRDsReadyConditionType,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}
	switch status.CRDState {
	case install.CRDManagedByCIO:
		crd.Status = metav1.ConditionTrue
		crd.Reason = "ManagedByCIO"
		crd.Message = status.CRDMessage
	case install.CRDManagedByOLM:
		crd.Status = metav1.ConditionTrue
		crd.Reason = "ManagedByOLM"
		crd.Message = status.CRDMessage
	case install.CRDNoneExist:
		crd.Status = metav1.ConditionUnknown
		crd.Reason = "NoneExist"
		crd.Message = "CRDs not yet installed"
	case install.CRDMixedOwnership:
		crd.Status = metav1.ConditionFalse
		crd.Reason = "MixedOwnership"
		crd.Message = status.CRDMessage
	default:
		crd.Status = metav1.ConditionFalse
		crd.Reason = "UnknownManagement"
		crd.Message = status.CRDMessage
	}
	meta.SetStatusCondition(conditions, crd)
}

func removeSailOperatorConditions(conditions *[]metav1.Condition) {
	meta.RemoveStatusCondition(conditions, ControllerInstalledConditionType)
	meta.RemoveStatusCondition(conditions, CRDsReadyConditionType)
}
