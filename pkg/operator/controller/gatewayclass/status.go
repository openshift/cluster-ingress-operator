package gatewayclass

import (
	"fmt"
	"strings"

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
	//   - True (Ready): all CRDs are ready and managed
	//   - False (NotReady/MixedOwnership/Error): CRDs are not ready, have mixed ownership, or errored
	//   - Unknown (NoneExist): CRDs have not yet been installed
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

	// CRD condition: reflects CRD readiness state.
	crd := metav1.Condition{
		Type:               CRDsReadyConditionType,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}
	switch status.CRDState {
	case install.CRDManagementStateReady:
		if unmanaged := getUnmanagedCRDs(status.CRDs); len(unmanaged) > 0 {
			crd.Status = metav1.ConditionFalse
			crd.Reason = "MixedOwnership"
			crd.Message = getCRDStatusMessage("CRDs have mixed ownership", status.CRDs)
		} else {
			crd.Status = metav1.ConditionTrue
			crd.Reason = "Ready"
			crd.Message = "all CRDs are ready and managed"
		}
	case install.CRDManagementStateNotReady:
		crd.Status = metav1.ConditionFalse
		crd.Reason = "NotReady"
		crd.Message = getCRDStatusMessage("not all CRDs are ready", status.CRDs)
	case install.CRDManagementStateError:
		crd.Status = metav1.ConditionFalse
		crd.Reason = "Error"
		crd.Message = getCRDStatusMessage(status.CRDMessage, status.CRDs)
	default:
		crd.Status = metav1.ConditionUnknown
		crd.Reason = "NoneExist"
		crd.Message = "CRDs not yet installed"
	}
	changed = meta.SetStatusCondition(conditions, crd) || changed

	return changed
}

// removeSailInstallConditions removes conditions that track Sail Library installation state.
func removeSailInstallConditions(conditions *[]metav1.Condition) {
	meta.RemoveStatusCondition(conditions, ControllerInstalledConditionType)
	meta.RemoveStatusCondition(conditions, CRDsReadyConditionType)
}

func getUnmanagedCRDs(crds []install.CRDInfo) []string {
	var unmanaged []string
	for _, info := range crds {
		if !info.Managed {
			unmanaged = append(unmanaged, info.Name)
		}
	}
	return unmanaged
}

// getCRDStatusMessage formats a status message with a summary line and
// a bulleted list of individual CRD states.
func getCRDStatusMessage(summary string, crds []install.CRDInfo) string {
	if summary == "" && len(crds) == 0 {
		return "unable to determine CRD ownership"
	}
	var lines []string
	if summary != "" {
		lines = append(lines, summary)
	}
	for _, info := range crds {
		lines = append(lines, fmt.Sprintf("- %s: %s", info.Name,
			crdStateString(info)))
	}
	return strings.Join(lines, "\n")
}

func crdStateString(info install.CRDInfo) string {
	switch {
	case info.Managed && info.Ready:
		return "Managed"
	case info.Managed && !info.Ready:
		return "Managed (not ready)"
	case !info.Managed && info.Ready:
		return "NotManaged"
	default:
		return "NotManaged (not ready)"
	}
}
