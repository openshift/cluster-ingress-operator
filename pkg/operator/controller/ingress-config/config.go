package ingressconfig

import (
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayapi/managementmode"
)

// Config holds configuration for the ingress-config controller.
type Config struct {
	// GatewayAPIManagementModeEnabled indicates whether the GatewayAPIManagementMode feature gate is enabled.
	GatewayAPIManagementModeEnabled bool
	// GatewayAPIWithoutOLMEnabled indicates whether Istio is installed via the Sail library.
	GatewayAPIWithoutOLMEnabled bool
	// IBMCloudManaged selects the VAP manifest variant without pod-binding admission rules.
	IBMCloudManaged bool
	// StateStore is the shared management mode state written for other controllers to read.
	StateStore *managementmode.Store
	// OperatorNamespace is the namespace in which the ingress operator runs.
	OperatorNamespace string
	// OperandNamespace is the namespace in which Gateway API operands run.
	OperandNamespace string
}
