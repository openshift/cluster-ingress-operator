package controller

import (
	"context"
	"sync"
	"testing"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestModeAccessor_BlockDependents(t *testing.T) {
	t.Run("immediately makes AllowDependents false", func(t *testing.T) {
		m := NewModeAccessor(true)
		m.Update(operatorv1alpha1.GatewayAPIManagementModeManaged, true, true, true)
		require.True(t, m.AllowDependents(),
			"precondition: AllowDependents must be true after a fully Managed Update")

		m.BlockDependents()

		assert.False(t, m.AllowDependents(),
			"AllowDependents must be false immediately after BlockDependents")
	})

	t.Run("does not touch present or compliant", func(t *testing.T) {
		m := NewModeAccessor(true)
		m.Update(operatorv1alpha1.GatewayAPIManagementModeManaged, true, true, true)

		m.BlockDependents()

		m.mu.RLock()
		present, compliant := m.present, m.compliant
		m.mu.RUnlock()
		assert.True(t, present, "present must be unaffected by BlockDependents")
		assert.True(t, compliant, "compliant must be unaffected by BlockDependents")

		// Restoring managed=true should make AllowDependents true again,
		// confirming present/compliant were never actually cleared.
		m.Update(operatorv1alpha1.GatewayAPIManagementModeManaged, true, true, true)
		assert.True(t, m.AllowDependents(),
			"present/compliant must be unaffected by BlockDependents")
	})

	t.Run("no-op on gate-off accessor", func(t *testing.T) {
		m := NewModeAccessor(false)
		m.SetCRDsEstablished(true)
		require.True(t, m.AllowDependents(),
			"precondition: gate-off AllowDependents tracks crdsEstablished only")

		m.BlockDependents()

		assert.True(t, m.AllowDependents(),
			"BlockDependents must not affect the gate-off (legacy) path, which ignores managed")
	})
}

func TestIngressWakeUpMapper_ConcurrentSafety(t *testing.T) {
	// Regression test: verifies that concurrent invocations of the
	// MapFunc returned by IngressWakeUpMapper do not race on the
	// ObjectList. Prior to the factory pattern, a shared ObjectList
	// instance was reused, causing races when multiple watch events
	// fired concurrently.

	pod1 := &corev1.Pod{}
	pod1.SetName("pod1")
	pod1.SetNamespace("default")

	pod2 := &corev1.Pod{}
	pod2.SetName("pod2")
	pod2.SetNamespace("default")

	pod3 := &corev1.Pod{}
	pod3.SetName("pod3")
	pod3.SetNamespace("default")

	fakeClient := fake.NewClientBuilder().WithObjects(pod1, pod2, pod3).Build()

	mapper := IngressWakeUpMapper(fakeClient, func() client.ObjectList { return &corev1.PodList{} })

	expectedNames := map[string]bool{"pod1": true, "pod2": true, "pod3": true}

	var wg sync.WaitGroup
	var mu sync.Mutex
	const concurrency = 10
	var incompleteCount int
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				result := mapper(context.Background(), nil)
				complete := len(result) == len(expectedNames)
				if complete {
					seen := map[string]bool{}
					for _, req := range result {
						seen[req.Name] = true
					}
					for name := range expectedNames {
						if !seen[name] {
							complete = false
							break
						}
					}
				}
				if !complete {
					mu.Lock()
					incompleteCount++
					mu.Unlock()
					return
				}
			}
		}()
	}

	wg.Wait()

	// Assert from main goroutine after all workers complete
	require.Equal(t, 0, incompleteCount, "mapper returned an incomplete/nil result %d times", incompleteCount)
}
