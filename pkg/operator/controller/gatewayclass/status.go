package gatewayclass

import (
	"bytes"
	"fmt"

	"github.com/istio-ecosystem/sail-operator/pkg/install"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ControllerInstalledConditionType indicates whether the Istio control plane (istiod)
	// has been successfully installed by the Sail Library.
	// Status values:
	//   - True (Installed): istiod is installed and running
	//   - False (InstallFailed): installation failed with an error
	//   - Unknown (Pending): waiting for first reconciliation
	ControllerInstalledConditionType = "ControllerInstalled"

	// CRDsReadyConditionType indicates the ownership and readiness state of Istio CRDs.
	// Status values:
	//   - True (ManagedByCIO/ManagedByOLM): CRDs are managed by CIO or OLM respectively
	//   - Unknown (NoneExist): CRDs have not yet been installed
	//   - False (MixedOwnership/UnknownManagement): CRDs have conflicting ownership or unknown management
	CRDsReadyConditionType = "CRDsReady"
)

// mapStatusToConditions translates the Sail Library Status into GatewayClass conditions.
// Returns true if any conditions were added or updated.
func mapStatusToConditions(status install.Status, generation int64, conditions *[]metav1.Condition) bool {
	installed := metav1.Condition{
		Type:               ControllerInstalledConditionType,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}
	if status.Installed {
		installed.Status = metav1.ConditionTrue
		installed.Reason = "Installed"
		if status.Error != nil {
			installed.Message = fmt.Sprintf("istiod %s installed (with warning: %v)", status.Version, status.Error)
		} else {
			installed.Message = fmt.Sprintf("istiod %s installed", status.Version)
		}
	} else if status.Error != nil {
		installed.Status = metav1.ConditionFalse
		installed.Reason = "InstallFailed"
		installed.Message = status.Error.Error()
	} else {
		installed.Status = metav1.ConditionUnknown
		installed.Reason = "Pending"
		installed.Message = "waiting for first reconciliation"
	}

	changed := meta.SetStatusCondition(conditions, installed)

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
		crd.Message = getCRDStatusMessage(status)
	default:
		crd.Status = metav1.ConditionFalse
		crd.Reason = "UnknownManagement"
		crd.Message = getCRDStatusMessage(status)
	}
	changed = meta.SetStatusCondition(conditions, crd) || changed

	return changed
}

// removeSailInstallConditions removes conditions that track Sail Library installation state.
func removeSailInstallConditions(conditions *[]metav1.Condition) {
	meta.RemoveStatusCondition(conditions, ControllerInstalledConditionType)
	meta.RemoveStatusCondition(conditions, CRDsReadyConditionType)
}

// getCRDStatusMessage formats a detailed CRD status message including the overall
// state and a bulleted list of individual CRD ownership states.
func getCRDStatusMessage(status install.Status) string {
	if status.CRDMessage == "" && len(status.CRDs) == 0 {
		return "unable to determine CRD ownership"
	}
	var message bytes.Buffer
	if _, err := message.WriteString(status.CRDMessage); err != nil {
		log.Error(err, "error writing CRD status", "status", status.CRDMessage)
	}
	for _, info := range status.CRDs {
		crdMsg := fmt.Sprintf("\n- %s: %s", info.Name, info.State)
		_, err := message.WriteString(crdMsg)
		if err != nil {
			log.Error(err, "error writing CRD message", "crd", info.Name)
		}
	}
	return message.String()
}
