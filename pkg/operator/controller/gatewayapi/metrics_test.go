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

func TestUpdateManagementModeMetrics_ManagedAndCompliant(t *testing.T) {
	managedCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsManaged,
		Status: metav1.ConditionTrue,
		Reason: reasonManagedByIngressOperator,
	}
	compliantCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsCompliant,
		Status: metav1.ConditionTrue,
		Reason: reasonVersionMatch,
	}

	updateManagementModeMetrics(managedCond, compliantCond, "v1.5.1", "v3.4.1")

	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Managed")),
		"Managed gauge should be 1 when managed")
	assert.Equal(t, float64(0), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged")),
		"Unmanaged gauge should be 0 when managed")
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIInfoMetric.WithLabelValues("v1.5.1", "v3.4.1")),
		"Info metric should be 1 when managed and compliant")
}

func TestUpdateManagementModeMetrics_Unmanaged(t *testing.T) {
	// Start by setting managed+compliant so info metric is set...
	updateManagementModeMetrics(
		metav1.Condition{Type: conditionTypeGatewayAPICRDsManaged, Status: metav1.ConditionTrue},
		metav1.Condition{Type: conditionTypeGatewayAPICRDsCompliant, Status: metav1.ConditionTrue},
		"v1.5.1", "v3.4.1",
	)
	// ...then transition to unmanaged.
	managedCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsManaged,
		Status: metav1.ConditionFalse,
		Reason: reasonUnmanaged,
	}
	compliantCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsCompliant,
		Status: metav1.ConditionTrue,
		Reason: reasonVersionMatch,
	}

	updateManagementModeMetrics(managedCond, compliantCond, "v1.5.1", "v3.4.1")

	assert.Equal(t, float64(0), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Managed")),
		"Managed gauge should be 0 when unmanaged")
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged")),
		"Unmanaged gauge should be 1 when unmanaged")

	// Info metric should be cleared (reset) when not managed.
	assert.Equal(t, 0, collectGaugeVecCount(gatewayAPIInfoMetric),
		"Info metric should have no series when unmanaged")
}

func TestUpdateManagementModeMetrics_ManagedButNotCompliant(t *testing.T) {
	// First set managed+compliant to populate info metric...
	updateManagementModeMetrics(
		metav1.Condition{Type: conditionTypeGatewayAPICRDsManaged, Status: metav1.ConditionTrue},
		metav1.Condition{Type: conditionTypeGatewayAPICRDsCompliant, Status: metav1.ConditionTrue},
		"v1.5.1", "v3.4.1",
	)
	// ...then transition to managed but non-compliant.
	managedCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsManaged,
		Status: metav1.ConditionTrue,
		Reason: reasonManagedByIngressOperator,
	}
	compliantCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsCompliant,
		Status: metav1.ConditionFalse,
		Reason: reasonVersionMismatch,
	}

	updateManagementModeMetrics(managedCond, compliantCond, "v1.5.1", "v3.4.1")

	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Managed")),
		"Managed gauge should be 1")
	assert.Equal(t, float64(0), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged")),
		"Unmanaged gauge should be 0")

	// Info metric should NOT be present when non-compliant.
	assert.Equal(t, 0, collectGaugeVecCount(gatewayAPIInfoMetric),
		"Info metric should have no series when non-compliant")
}

func TestUpdateManagementModeMetrics_TakeoverBlocked(t *testing.T) {
	managedCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsManaged,
		Status: metav1.ConditionFalse,
		Reason: reasonTakeoverBlocked,
	}
	compliantCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsCompliant,
		Status: metav1.ConditionFalse,
		Reason: reasonVersionMismatch,
	}

	updateManagementModeMetrics(managedCond, compliantCond, "v1.5.1", "v3.4.1")

	assert.Equal(t, float64(0), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Managed")),
		"Managed gauge should be 0 when takeover blocked")
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged")),
		"Unmanaged gauge should be 1 when takeover blocked")

	// Info metric should NOT be present when takeover blocked.
	assert.Equal(t, 0, collectGaugeVecCount(gatewayAPIInfoMetric),
		"Info metric should have no series when takeover blocked")
}

func TestUpdateManagementModeMetrics_VersionUpgradeClearsStaleLabels(t *testing.T) {
	// Simulate managed+compliant with OSSM v3.4.1...
	updateManagementModeMetrics(
		metav1.Condition{Type: conditionTypeGatewayAPICRDsManaged, Status: metav1.ConditionTrue, Reason: reasonManagedByIngressOperator},
		metav1.Condition{Type: conditionTypeGatewayAPICRDsCompliant, Status: metav1.ConditionTrue, Reason: reasonVersionMatch},
		"v1.5.1", "v3.4.1",
	)
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIInfoMetric.WithLabelValues("v1.5.1", "v3.4.1")),
		"Info metric should be 1 for old version")

	// ...then upgrade to OSSM v3.5.0.
	updateManagementModeMetrics(
		metav1.Condition{Type: conditionTypeGatewayAPICRDsManaged, Status: metav1.ConditionTrue, Reason: reasonManagedByIngressOperator},
		metav1.Condition{Type: conditionTypeGatewayAPICRDsCompliant, Status: metav1.ConditionTrue, Reason: reasonVersionMatch},
		"v1.5.1", "v3.5.0",
	)

	// The new version should be present.
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIInfoMetric.WithLabelValues("v1.5.1", "v3.5.0")),
		"Info metric should be 1 for new version")

	// The old version series must be gone (Reset clears it).
	assert.Equal(t, 1, collectGaugeVecCount(gatewayAPIInfoMetric),
		"Only one info metric series should exist after version upgrade")
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

func TestParseOSSMVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard CSV name",
			input: "servicemeshoperator3.v3.4.1",
			want:  "v3.4.1",
		},
		{
			name:  "pre-release CSV",
			input: "servicemeshoperator3.v3.5.0-rc1",
			want:  "v3.5.0-rc1",
		},
		{
			name:  "plain version",
			input: "v3.4.1",
			want:  "v3.4.1",
		},
		{
			name:  "no version marker",
			input: "some-string",
			want:  "some-string",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseOSSMVersion(tt.input))
		})
	}
}

func TestEmbeddedGatewayAPIVersion(t *testing.T) {
	v := embeddedGatewayAPIVersion()
	assert.NotEmpty(t, v, "Embedded Gateway API version should not be empty")
	assert.NotEqual(t, "unknown", v, "Embedded Gateway API version should not be 'unknown'")
	assert.Contains(t, v, "v", "Version should contain a 'v' prefix")
}

// TestUpdateManagementModeMetrics_SteadyStateNoReset verifies that
// calling updateManagementModeMetrics repeatedly with the same
// managed+compliant state and identical version labels does NOT
// call Reset() on the info metric after the first call.
func TestUpdateManagementModeMetrics_SteadyStateNoReset(t *testing.T) {
	// Clear the cached last-seen labels to isolate this test.
	infoMetricMu.Lock()
	lastInfoGatewayAPIVersion = ""
	lastInfoOSSMVersion = ""
	infoMetricMu.Unlock()

	managedCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsManaged,
		Status: metav1.ConditionTrue,
		Reason: reasonManagedByIngressOperator,
	}
	compliantCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsCompliant,
		Status: metav1.ConditionTrue,
		Reason: reasonVersionMatch,
	}

	// First call: sets the metric and caches version labels.
	updateManagementModeMetrics(managedCond, compliantCond, "v1.5.1", "v3.4.1")
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIInfoMetric.WithLabelValues("v1.5.1", "v3.4.1")),
		"Info metric should be 1 after first call")

	// Inject a second label combination to detect whether Reset()
	// is called on the second invocation. If Reset() fires, this
	// injected series will disappear.
	gatewayAPIInfoMetric.WithLabelValues("canary", "canary").Set(42)
	assert.Equal(t, float64(42), gaugeValue(t, gatewayAPIInfoMetric.WithLabelValues("canary", "canary")),
		"Canary metric must be present before second call")

	// Second call with identical versions: must NOT call Reset().
	updateManagementModeMetrics(managedCond, compliantCond, "v1.5.1", "v3.4.1")

	// The canary series must survive because Reset() was skipped.
	assert.Equal(t, float64(42), gaugeValue(t, gatewayAPIInfoMetric.WithLabelValues("canary", "canary")),
		"Canary metric must survive steady-state call (Reset must NOT be called)")
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIInfoMetric.WithLabelValues("v1.5.1", "v3.4.1")),
		"Info metric must still be 1 after steady-state call")

	// Clean up the canary series for other tests.
	gatewayAPIInfoMetric.Reset()
	infoMetricMu.Lock()
	lastInfoGatewayAPIVersion = ""
	lastInfoOSSMVersion = ""
	infoMetricMu.Unlock()
}

// TestUpdateManagementModeMetrics_VersionChangeResetsStale verifies
// that when version labels change, Reset() IS called to remove the
// old label combination.
func TestUpdateManagementModeMetrics_VersionChangeResetsStale(t *testing.T) {
	// Clear the cached last-seen labels to isolate this test.
	infoMetricMu.Lock()
	lastInfoGatewayAPIVersion = ""
	lastInfoOSSMVersion = ""
	infoMetricMu.Unlock()

	managedCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsManaged,
		Status: metav1.ConditionTrue,
		Reason: reasonManagedByIngressOperator,
	}
	compliantCond := metav1.Condition{
		Type:   conditionTypeGatewayAPICRDsCompliant,
		Status: metav1.ConditionTrue,
		Reason: reasonVersionMatch,
	}

	// Set initial version.
	updateManagementModeMetrics(managedCond, compliantCond, "v1.5.1", "v3.4.1")
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIInfoMetric.WithLabelValues("v1.5.1", "v3.4.1")))

	// Change version: Reset() must fire to clear old series.
	updateManagementModeMetrics(managedCond, compliantCond, "v1.5.1", "v3.5.0")
	assert.Equal(t, float64(1), gaugeValue(t, gatewayAPIInfoMetric.WithLabelValues("v1.5.1", "v3.5.0")),
		"New version metric must be set")
	assert.Equal(t, 1, collectGaugeVecCount(gatewayAPIInfoMetric),
		"Old version series must be cleaned up by Reset()")

	// Clean up.
	gatewayAPIInfoMetric.Reset()
	infoMetricMu.Lock()
	lastInfoGatewayAPIVersion = ""
	lastInfoOSSMVersion = ""
	infoMetricMu.Unlock()
}
