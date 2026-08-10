package gatewayapi

import (
	"fmt"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	ctrlruntimemetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// gatewayAPIInfoMetric is an info-style GaugeVec that reports the
	// Gateway API and OSSM versions shipped by CIO.  It is only
	// reported when in Managed mode AND GatewayAPICRDsCompliant is
	// True; otherwise the metric is removed entirely.
	gatewayAPIInfoMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ingress_controller_gateway_api_info",
		Help: "Reports Gateway API and OSSM version info. Only present when managed and compliant.",
	}, []string{"gateway_api_version", "ossm_version"})

	gatewayAPIMetricsList = []prometheus.Collector{
		gatewayAPIManagementModeMetric,
		gatewayAPIInfoMetric,
	}

	// infoMetricMu protects the last-seen version labels used to
	// avoid redundant Reset() calls on the info metric in
	// steady-state reconciles.
	infoMetricMu             sync.Mutex
	lastInfoGatewayAPIVersion string
	lastInfoOSSMVersion       string
)

// RegisterMetrics registers the Gateway API management mode and info
// metrics with the controller-runtime Prometheus registry.
func RegisterMetrics() error {
	for _, metric := range gatewayAPIMetricsList {
		if err := ctrlruntimemetrics.Registry.Register(metric); err != nil {
			return fmt.Errorf("failed to register gatewayapi metric: %w", err)
		}
	}
	return nil
}

// updateManagementModeMetrics updates the management mode and info
// metrics based on the GatewayAPICRDsManaged and
// GatewayAPICRDsCompliant conditions.
//
// managedCond carries the effective mode derived from the Ingress
// status; compliantCond carries the CRD compliance state.
//
// gatewayAPIVersion and ossmVersion are the versions CIO ships;
// they are used as label values on the info metric.
func updateManagementModeMetrics(managedCond, compliantCond metav1.Condition, gatewayAPIVersion, ossmVersion string) {
	managed := managedCond.Status == metav1.ConditionTrue
	compliant := compliantCond.Status == metav1.ConditionTrue

	if managed {
		gatewayAPIManagementModeMetric.WithLabelValues("Managed").Set(1)
		gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged").Set(0)
	} else {
		gatewayAPIManagementModeMetric.WithLabelValues("Managed").Set(0)
		gatewayAPIManagementModeMetric.WithLabelValues("Unmanaged").Set(1)
	}

	if managed && compliant {
		// Only reset when the version labels actually change so that
		// steady-state reconciles (every 30s) do not churn the metric.
		// Without the version-change guard, Reset()+Set() on every
		// reconcile creates unnecessary GC pressure and brief metric
		// gaps visible to Prometheus scrapes.
		infoMetricMu.Lock()
		versionChanged := lastInfoGatewayAPIVersion != gatewayAPIVersion || lastInfoOSSMVersion != ossmVersion
		if versionChanged {
			gatewayAPIInfoMetric.Reset()
			lastInfoGatewayAPIVersion = gatewayAPIVersion
			lastInfoOSSMVersion = ossmVersion
		}
		infoMetricMu.Unlock()
		gatewayAPIInfoMetric.WithLabelValues(gatewayAPIVersion, ossmVersion).Set(1)
	} else {
		// Remove the info metric entirely when not managed+compliant
		// so that stale label combinations are not left behind.
		gatewayAPIInfoMetric.Reset()
		infoMetricMu.Lock()
		lastInfoGatewayAPIVersion = ""
		lastInfoOSSMVersion = ""
		infoMetricMu.Unlock()
	}
}

// embeddedGatewayAPIVersion returns the bundle-version annotation from
// the first managed CRD shipped by CIO. All managed CRDs carry the
// same version, so reading any one is sufficient.
func embeddedGatewayAPIVersion() string {
	if len(managedCRDs) == 0 {
		return "unknown"
	}
	v, ok := managedCRDs[0].Annotations[bundleVersionAnnotation]
	if !ok || v == "" {
		return "unknown"
	}
	return v
}

// parseOSSMVersion extracts a semver-style version from an OLM CSV
// name such as "servicemeshoperator3.v3.4.1". It looks for the first
// occurrence of ".v" and returns the version substring (e.g. "v3.4.1").
// If the input does not match the expected format, it is returned
// as-is so that the metric label always has a value.
func parseOSSMVersion(csvName string) string {
	if idx := strings.Index(csvName, ".v"); idx >= 0 {
		return csvName[idx+1:]
	}
	return csvName
}
