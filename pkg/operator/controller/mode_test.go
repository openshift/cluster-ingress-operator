package controller

import (
	"testing"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/stretchr/testify/assert"
)

func TestModeAccessor_LastAppliedMode(t *testing.T) {
	t.Run("nil before first reconcile", func(t *testing.T) {
		m := NewModeAccessor(true)
		assert.Nil(t, m.GetLastAppliedMode(),
			"lastAppliedMode must be nil before any reconcile")
	})

	t.Run("set and get", func(t *testing.T) {
		m := NewModeAccessor(true)
		m.SetLastAppliedMode(operatorv1alpha1.GatewayAPIManagementModeManaged)
		got := m.GetLastAppliedMode()
		assert.NotNil(t, got)
		assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeManaged, *got)
	})

	t.Run("overwrite with different mode", func(t *testing.T) {
		m := NewModeAccessor(true)
		m.SetLastAppliedMode(operatorv1alpha1.GatewayAPIManagementModeManaged)
		m.SetLastAppliedMode(operatorv1alpha1.GatewayAPIManagementModeUnmanaged)
		got := m.GetLastAppliedMode()
		assert.NotNil(t, got)
		assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged, *got)
	})

	t.Run("independent of transition state", func(t *testing.T) {
		m := NewModeAccessor(true)
		m.SetLastAppliedMode(operatorv1alpha1.GatewayAPIManagementModeManaged)
		m.SetTransitionState(TransitionState{})
		got := m.GetLastAppliedMode()
		assert.NotNil(t, got,
			"clearing transition state must not affect lastAppliedMode")
		assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeManaged, *got)
	})
}
