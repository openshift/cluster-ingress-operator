package gatewayapi

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
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
