package gatewayapi

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
)

// gaugeValue extracts the float64 value of a prometheus.Gauge.
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	m := &dto.Metric{}
	require.NoError(t, g.Write(m))
	return m.GetGauge().GetValue()
}

// collectGaugeVecCount returns the number of metrics in a GaugeVec.
func collectGaugeVecCount(vec *prometheus.GaugeVec) int {
	ch := make(chan prometheus.Metric, 16)
	go func() {
		vec.Collect(ch)
		close(ch)
	}()
	count := 0
	for range ch {
		count++
	}
	return count
}

func TestUpdateManagementModeMetrics_Managed(t *testing.T) {
	managedCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsManaged,
		Status: metav1.ConditionTrue,
		Reason: reasonManagedByIngressOperator,
	}

	updateManagementModeMetrics(managedCond)

	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Managed")),
		"Managed gauge should be 1 when managed")
	assert.Equal(t, float64(0), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged")),
		"Unmanaged gauge should be 0 when managed")
}

func TestUpdateManagementModeMetrics_Unmanaged(t *testing.T) {
	managedCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsManaged,
		Status: metav1.ConditionFalse,
		Reason: reasonUnmanaged,
	}

	updateManagementModeMetrics(managedCond)

	assert.Equal(t, float64(0), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Managed")),
		"Managed gauge should be 0 when unmanaged")
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged")),
		"Unmanaged gauge should be 1 when unmanaged")
}

func TestUpdateManagementModeMetrics_TakeoverBlocked(t *testing.T) {
	managedCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsManaged,
		Status: metav1.ConditionFalse,
		Reason: reasonTakeoverBlocked,
	}

	updateManagementModeMetrics(managedCond)

	assert.Equal(t, float64(0), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Managed")),
		"Managed gauge should be 0 when takeover blocked")
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged")),
		"Unmanaged gauge should be 1 when takeover blocked")
}

func TestUpdateModeTransitionFailedMetric(t *testing.T) {
	// Start from a clean slate.
	gatewayAPIModeTransitionFailedMetric.Reset()
	modeTransitionFailedMetricMu.Lock()
	lastFailingTarget = ""
	modeTransitionFailedMetricMu.Unlock()

	updateModeTransitionFailedMetric(operatorcontroller.TransitionState{})
	assert.Equal(t, 0, collectGaugeVecCount(gatewayAPIModeTransitionFailedMetric),
		"metric should be absent when no transition is in progress")

	updateModeTransitionFailedMetric(operatorcontroller.TransitionState{
		InProgress: true,
		Target:     operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
	})
	assert.Equal(t, 0, collectGaugeVecCount(gatewayAPIModeTransitionFailedMetric),
		"metric should be absent when transition is in progress without an error")

	updateModeTransitionFailedMetric(operatorcontroller.TransitionState{
		InProgress: true,
		Target:     operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
		Error:      fmt.Errorf("Sail uninstall failed"),
	})
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIModeTransitionFailedMetric.WithLabelValues("Unmanaged")),
		"metric should be 1 for the failing target")
	assert.Equal(t, 1, collectGaugeVecCount(gatewayAPIModeTransitionFailedMetric),
		"only one series should be present")

	// Flip the failing target: the old label must disappear.
	updateModeTransitionFailedMetric(operatorcontroller.TransitionState{
		InProgress: true,
		Target:     operatorv1alpha1.GatewayAPIManagementModeManaged,
		Error:      fmt.Errorf("failed to create ValidatingAdmissionPolicy"),
	})
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIModeTransitionFailedMetric.WithLabelValues("Managed")),
		"metric should be 1 for the new failing target")
	assert.Equal(t, 1, collectGaugeVecCount(gatewayAPIModeTransitionFailedMetric),
		"only the new target series should be present")

	// The error clears: the metric must be removed entirely.
	updateModeTransitionFailedMetric(operatorcontroller.TransitionState{})
	assert.Equal(t, 0, collectGaugeVecCount(gatewayAPIModeTransitionFailedMetric),
		"metric should be absent once the transition succeeds")
}

// TestUpdateModeTransitionFailedMetric_RepeatedFailureNoGap verifies that
// repeating the same failing target across retries never calls a blanket
// Reset(): the series must remain continuously present (never zero
// collected series), since that would be observable as a false "not
// failing" reading by a concurrent Prometheus scrape.
func TestUpdateModeTransitionFailedMetric_RepeatedFailureNoGap(t *testing.T) {
	gatewayAPIModeTransitionFailedMetric.Reset()
	modeTransitionFailedMetricMu.Lock()
	lastFailingTarget = ""
	modeTransitionFailedMetricMu.Unlock()

	for i := 0; i < 5; i++ {
		updateModeTransitionFailedMetric(operatorcontroller.TransitionState{
			InProgress: true,
			Target:     operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			Error:      fmt.Errorf("retry %d failed", i),
		})
		assert.Equal(t, 1, collectGaugeVecCount(gatewayAPIModeTransitionFailedMetric),
			"series must remain present across repeated failures of the same target")
		assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIModeTransitionFailedMetric.WithLabelValues("Unmanaged")))
	}
}

func TestUpdateUnmanagedCRDsMetric(t *testing.T) {
	gatewayAPIUnmanagedCRDsMetric.Reset()
	unmanagedCRDsMetricMu.Lock()
	lastUnmanagedCRDNames = sets.New[string]()
	unmanagedCRDsMetricMu.Unlock()

	updateUnmanagedCRDsMetric(nil)
	assert.Equal(t, 0, collectGaugeVecCount(gatewayAPIUnmanagedCRDsMetric),
		"metric should be absent when there are no unmanaged CRDs")

	updateUnmanagedCRDsMetric([]string{"invalids.gateway.networking.k8s.io"})
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIUnmanagedCRDsMetric.WithLabelValues("invalids.gateway.networking.k8s.io")))
	assert.Equal(t, 1, collectGaugeVecCount(gatewayAPIUnmanagedCRDsMetric))

	// A second unmanaged CRD appears; both series must be present.
	updateUnmanagedCRDsMetric([]string{"invalids.gateway.networking.k8s.io", "other.gateway.networking.k8s.io"})
	assert.Equal(t, 2, collectGaugeVecCount(gatewayAPIUnmanagedCRDsMetric))

	// The first one is resolved: only the remaining one must be present.
	updateUnmanagedCRDsMetric([]string{"other.gateway.networking.k8s.io"})
	assert.Equal(t, 1, collectGaugeVecCount(gatewayAPIUnmanagedCRDsMetric))
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIUnmanagedCRDsMetric.WithLabelValues("other.gateway.networking.k8s.io")))

	// All resolved: the metric must be empty again.
	updateUnmanagedCRDsMetric(nil)
	assert.Equal(t, 0, collectGaugeVecCount(gatewayAPIUnmanagedCRDsMetric))
}
