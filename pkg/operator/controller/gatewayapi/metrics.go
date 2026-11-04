package gatewayapi

import (
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	ctrlruntimemetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

var (
	// gatewayAPIManagementModeMetric reports the effective Gateway API
	// management mode derived from the GatewayAPICRDsManaged status
	// condition.  The mode label is "Managed" or "Unmanaged"; the
	// effective mode is set to 1 and the other to 0, following the
	// same GaugeVec pattern as ingress_controller_conditions.
	gatewayAPIManagementModeMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ingress_controller_gateway_api_management_mode",
		Help: "Reports the effective Gateway API management mode. 1 for the effective mode, 0 for the other.",
	}, []string{"mode"})

	// gatewayAPIUnmanagedCRDsMetric reports 1 for each Gateway API-group
	// CRD found on the cluster that CIO does not manage (e.g., a
	// third-party or unexpected CRD sharing the gateway.networking.k8s.io
	// group). This is an observational signal only -- it must never be
	// used to set ClusterOperator Degraded, since a real Degraded
	// transition on "ingress" is a hard, unconditional CI failure with no
	// grace period. See the "GatewayAPIUnmanagedCRDsFound" alert instead.
	gatewayAPIUnmanagedCRDsMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ingress_controller_gateway_api_unmanaged_crds",
		Help: "Reports 1 for each Gateway API-group CRD present on the cluster that the Cluster Ingress Operator does not manage.",
	}, []string{"name"})

	gatewayAPIMetricsList = []prometheus.Collector{
		gatewayAPIManagementModeMetric,
		gatewayAPIUnmanagedCRDsMetric,
	}

	// unmanagedCRDsMetricMu protects lastUnmanagedCRDNames, used the same
	// way as lastFailingTarget above: only the CRD names that actually
	// dropped out of the set are deleted, so a steady-state list of
	// unmanaged CRDs is never briefly empty to a concurrent scrape.
	unmanagedCRDsMetricMu sync.Mutex
	lastUnmanagedCRDNames = sets.New[string]()
)

// RegisterMetrics registers the Gateway API management mode metrics
// with the controller-runtime Prometheus registry.
func RegisterMetrics() error {
	for _, metric := range gatewayAPIMetricsList {
		if err := ctrlruntimemetrics.Registry.Register(metric); err != nil {
			return fmt.Errorf("failed to register gatewayapi metric: %w", err)
		}
	}
	return nil
}

// updateManagementModeMetrics updates the management mode metric based
// on the GatewayAPICRDsManaged condition.
//
// managedCond carries the effective mode derived from the Ingress
// status.
func updateManagementModeMetrics(managedCond metav1.Condition) {
	managed := managedCond.Status == metav1.ConditionTrue

	if managed {
		gatewayAPIManagementModeMetric.WithLabelValues("Managed").Set(1)
		gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged").Set(0)
	} else {
		gatewayAPIManagementModeMetric.WithLabelValues("Managed").Set(0)
		gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged").Set(1)
	}
}

// updateUnmanagedCRDsMetric reports the set of Gateway API-group CRDs that
// CIO does not manage. It is called every reconcile with the current list
// so the metric always matches the ClusterOperator extension status
// (state.unmanagedGatewayAPICRDNames).
//
// Only CRD names that dropped out of the set are deleted (rather than
// calling Reset() on every call), so a steady-state list of unmanaged
// CRDs never goes briefly empty to a concurrent Prometheus scrape.
func updateUnmanagedCRDsMetric(names []string) {
	current := sets.New(names...)

	unmanagedCRDsMetricMu.Lock()
	defer unmanagedCRDsMetricMu.Unlock()

	for name := range lastUnmanagedCRDNames.Difference(current) {
		gatewayAPIUnmanagedCRDsMetric.DeleteLabelValues(name)
	}
	for name := range current {
		gatewayAPIUnmanagedCRDsMetric.WithLabelValues(name).Set(1)
	}
	lastUnmanagedCRDNames = current
}
