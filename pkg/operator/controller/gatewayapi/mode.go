package gatewayapi

import (
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
)

// ModeAccessor is an alias for the shared type in the parent controller
// package. It is kept here so that existing references within this
// package (e.g. Config.ModeAccessor) continue to compile without a
// package-wide rename.
type ModeAccessor = operatorcontroller.ModeAccessor

// NewModeAccessor delegates to the canonical constructor.
func NewModeAccessor(gateEnabled bool) *ModeAccessor {
	return operatorcontroller.NewModeAccessor(gateEnabled)
}
