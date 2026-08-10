package gatewayapi

import (
	"fmt"
	"sync"
	"testing"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/assert"

	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestModeAccessor_AllowDependents_GateOff(t *testing.T) {
	m := NewModeAccessor(false)

	assert.False(t, m.AllowDependents(), "gate-off: should be false before CRDs established")

	m.SetCRDsEstablished(true)
	assert.True(t, m.AllowDependents(), "gate-off: should be true after CRDs established")

	m.SetCRDsEstablished(false)
	assert.False(t, m.AllowDependents(), "gate-off: should be false after CRDs un-established")
}

func TestModeAccessor_AllowDependents_GateOn(t *testing.T) {
	tests := []struct {
		name      string
		managed   bool
		present   bool
		compliant bool
		want      bool
	}{
		{"all true", true, true, true, true},
		{"managed false", false, true, true, false},
		{"present false", true, false, true, false},
		{"compliant false", true, true, false, false},
		{"all false", false, false, false, false},
		{"managed+present only", true, true, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModeAccessor(true)
			m.Update(operatorv1alpha1.GatewayAPIManagementModeManaged, tc.managed, tc.present, tc.compliant)
			assert.Equal(t, tc.want, m.AllowDependents())
		})
	}
}

func TestModeAccessor_DesiredMode(t *testing.T) {
	m := NewModeAccessor(true)
	assert.Equal(t, operatorv1alpha1.GatewayAPIManagementMode(""), m.DesiredMode(), "initial mode should be empty until first Update")

	m.Update(operatorv1alpha1.GatewayAPIManagementModeUnmanaged, false, true, true)
	assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged, m.DesiredMode())

	m.Update(operatorv1alpha1.GatewayAPIManagementModeManaged, true, true, true)
	assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeManaged, m.DesiredMode())
}

func TestModeAccessor_GateEnabled(t *testing.T) {
	assert.True(t, NewModeAccessor(true).GateEnabled())
	assert.False(t, NewModeAccessor(false).GateEnabled())
}

func TestModeAccessor_DependentPredicate(t *testing.T) {
	m := NewModeAccessor(true)
	p := m.DependentPredicate()

	m.Update(operatorv1alpha1.GatewayAPIManagementModeManaged, false, false, false)
	assert.False(t, p.Generic(event.GenericEvent{}), "predicate should deny when AllowDependents=false")

	m.Update(operatorv1alpha1.GatewayAPIManagementModeManaged, true, true, true)
	assert.True(t, p.Generic(event.GenericEvent{}), "predicate should allow when AllowDependents=true")
}

func TestModeAccessor_TransitionState(t *testing.T) {
	m := NewModeAccessor(true)

	// Default: no transition in progress.
	state := m.GetTransitionState()
	assert.False(t, state.InProgress, "default transition should not be in progress")
	assert.Nil(t, state.Error, "default transition should have no error")

	// Set transition in progress.
	m.SetTransitionState(operatorcontroller.TransitionState{
		InProgress: true,
		Target:     operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
	})
	state = m.GetTransitionState()
	assert.True(t, state.InProgress)
	assert.Equal(t, operatorv1alpha1.GatewayAPIManagementModeUnmanaged, state.Target)
	assert.Nil(t, state.Error)

	// Set transition error.
	transitionErr := fmt.Errorf("VAP delete failed")
	m.SetTransitionState(operatorcontroller.TransitionState{
		InProgress: true,
		Target:     operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
		Error:      transitionErr,
	})
	state = m.GetTransitionState()
	assert.True(t, state.InProgress)
	assert.Equal(t, transitionErr, state.Error)

	// Clear transition state.
	m.SetTransitionState(operatorcontroller.TransitionState{})
	state = m.GetTransitionState()
	assert.False(t, state.InProgress)
	assert.Nil(t, state.Error)
}

func TestModeAccessor_ConcurrentAccess(t *testing.T) {
	m := NewModeAccessor(true)
	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			m.Update(operatorv1alpha1.GatewayAPIManagementModeManaged, true, true, true)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = m.AllowDependents()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = m.DesiredMode()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			m.SetTransitionState(operatorcontroller.TransitionState{
				InProgress: true,
				Target:     operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			})
			_ = m.GetTransitionState()
		}
	}()

	wg.Wait()
}
