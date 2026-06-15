package managementmode

import (
	"fmt"
	"time"
)

const (
	ConditionGatewayAPICRDsManaged   = "GatewayAPICRDsManaged"
	ConditionGatewayAPICRDsPresent   = "GatewayAPICRDsPresent"
	ConditionGatewayAPICRDsCompliant = "GatewayAPICRDsCompliant"

	ReasonManagedByCIO    = "ManagedByCIO"
	ReasonUnmanaged       = "Unmanaged"
	ReasonCRDsFound       = "CRDsFound"
	ReasonCRDsNotFound    = "CRDsNotFound"
	ReasonVersionMatch    = "VersionMatch"
	ReasonVersionMismatch = "VersionMismatch"
	ReasonNotApplicable   = "NotApplicable"

	AnnotationBundleVersion = "gateway.networking.k8s.io/bundle-version"
	AnnotationChannel       = "gateway.networking.k8s.io/channel"

	// StackNotReadyRequeueInterval is how long gateway operand controllers wait
	// before retrying when the Gateway stack is stopped during a mode transition.
	StackNotReadyRequeueInterval = 30 * time.Second
)

// ValidateManagementModeConfig ensures management mode wiring is valid at startup.
func ValidateManagementModeConfig(gateEnabled bool, reader Reader) error {
	if gateEnabled && reader == nil {
		return fmt.Errorf("ManagementModeReader is required when GatewayAPIManagementModeEnabled is true")
	}
	return nil
}
