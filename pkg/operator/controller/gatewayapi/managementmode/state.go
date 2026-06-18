package managementmode

// Mode is the Gateway API CRD management mode.
type Mode string

const (
	ModeManaged   Mode = "Managed"
	ModeUnmanaged Mode = "Unmanaged"
)

// TransitionPhase tracks in-progress mode transitions.
type TransitionPhase string

const (
	TransitionPhaseNone          TransitionPhase = ""
	TransitionPhaseStoppingStack TransitionPhase = "StoppingStack"
	TransitionPhaseRemovingVAP   TransitionPhase = "RemovingVAP"
	TransitionPhaseAssessingCRDs TransitionPhase = "AssessingCRDs"
	TransitionPhaseInstallingCRDs TransitionPhase = "InstallingCRDs"
	TransitionPhaseDeployingVAP  TransitionPhase = "DeployingVAP"
)

// State is a snapshot of Gateway API management mode used by controllers.
type State struct {
	SpecMode            Mode
	EffectiveMode       Mode
	TransitionPhase     TransitionPhase
	ObservedGeneration  int64
	Managed             bool
	Present             bool
	Compliant           bool
	StackReady          bool
	GatewayAPIManagementModeEnabled bool
}

// DefaultManagedStackReadyState returns the management mode state used when the
// GatewayAPIManagementMode feature gate is disabled or before ingress-config
// has reconciled. When the gate is off, the operator behaves as fully Managed
// with StackReady=true.
func DefaultManagedStackReadyState(gatewayAPIManagementModeEnabled bool) State {
	if !gatewayAPIManagementModeEnabled {
		return State{
			SpecMode:                        ModeManaged,
			EffectiveMode:                   ModeManaged,
			Managed:                         true,
			Present:                         true,
			Compliant:                       true,
			StackReady:                      true,
			GatewayAPIManagementModeEnabled: false,
		}
	}
	return State{
		SpecMode:                        ModeManaged,
		EffectiveMode:                   ModeManaged,
		GatewayAPIManagementModeEnabled: true,
	}
}

// isMidUnmanagedTransition reports whether a transition from Managed to
// Unmanaged is in progress and CRD management must stop.
func (s State) isMidUnmanagedTransition() bool {
	return s.TransitionPhase == TransitionPhaseStoppingStack ||
		s.TransitionPhase == TransitionPhaseRemovingVAP
}

// ShouldManageCRDs reports whether the gatewayapi controller should install or
// update Gateway API CRDs on the cluster.
func (s State) ShouldManageCRDs() bool {
	return s.EffectiveMode == ModeManaged && !s.isMidUnmanagedTransition()
}

// ShouldProtectCRDs reports whether the Gateway API CRD ValidatingAdmissionPolicy
// should be present (Managed mode with compliant CRDs, not mid-transition).
func (s State) ShouldProtectCRDs() bool {
	return s.EffectiveMode == ModeManaged && s.Managed && !s.isMidUnmanagedTransition()
}

// ShouldRunGatewayStack reports whether dependent Gateway controllers (Istio,
// labeler, DNS, status, network policy) should actively reconcile.
func (s State) ShouldRunGatewayStack() bool {
	return s.StackReady
}

// IsTransitioning reports whether a Managed/Unmanaged mode change is in progress.
func (s State) IsTransitioning() bool {
	return s.TransitionPhase != TransitionPhaseNone
}
