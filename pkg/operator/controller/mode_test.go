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

	var wg sync.WaitGroup
	var mu sync.Mutex
	const concurrency = 10
	var nilCount int
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				result := mapper(context.Background(), nil)
				if result == nil {
					mu.Lock()
					nilCount++
					mu.Unlock()
					return
				}
			}
		}()
	}

	wg.Wait()

	// Assert from main goroutine after all workers complete
	require.Equal(t, 0, nilCount, "mapper returned nil %d times", nilCount)
}
