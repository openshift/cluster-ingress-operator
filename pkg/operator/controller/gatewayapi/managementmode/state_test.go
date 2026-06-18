package managementmode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSupportedProfile_matchesEmbeddedManifests(t *testing.T) {
	profile := SupportedProfile()
	assert.Equal(t, "v1.4.1", profile.BundleVersion)
	assert.Equal(t, "standard", profile.Channel)
	assert.Len(t, profile.CRDNames, 6)
	assert.Contains(t, profile.CRDNames, "gatewayclasses.gateway.networking.k8s.io")
}

func TestState_ShouldRunGatewayStack(t *testing.T) {
	assert.True(t, (State{StackReady: true}).ShouldRunGatewayStack())
	assert.False(t, (State{StackReady: false}).ShouldRunGatewayStack())
}

func TestState_ShouldManageCRDs(t *testing.T) {
	assert.True(t, (State{EffectiveMode: ModeManaged}).ShouldManageCRDs())
	assert.False(t, (State{EffectiveMode: ModeUnmanaged}).ShouldManageCRDs())
	assert.False(t, (State{EffectiveMode: ModeManaged, TransitionPhase: TransitionPhaseStoppingStack}).ShouldManageCRDs())
	assert.False(t, (State{EffectiveMode: ModeManaged, TransitionPhase: TransitionPhaseRemovingVAP}).ShouldManageCRDs())
	assert.True(t, (State{EffectiveMode: ModeManaged, TransitionPhase: TransitionPhaseInstallingCRDs}).ShouldManageCRDs())
}

func TestState_ShouldProtectCRDs(t *testing.T) {
	assert.True(t, (State{EffectiveMode: ModeManaged, Managed: true}).ShouldProtectCRDs())
	assert.False(t, (State{EffectiveMode: ModeManaged, Managed: false}).ShouldProtectCRDs())
	assert.False(t, (State{EffectiveMode: ModeManaged, Managed: true, TransitionPhase: TransitionPhaseStoppingStack}).ShouldProtectCRDs())
}

func TestDefaultManagedStackReadyState(t *testing.T) {
	disabled := DefaultManagedStackReadyState(false)
	assert.True(t, disabled.StackReady)
	assert.False(t, disabled.GatewayAPIManagementModeEnabled)

	enabled := DefaultManagedStackReadyState(true)
	assert.False(t, enabled.StackReady)
	assert.True(t, enabled.GatewayAPIManagementModeEnabled)
}
