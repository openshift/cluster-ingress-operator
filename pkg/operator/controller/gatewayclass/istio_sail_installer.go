package gatewayclass

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/install"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// subscriptionPrefix is the OLM label prefix indicating subscription ownership.
	subscriptionPrefix = "operators.coreos.com/"
)

// overwriteOLMManagedCRDFunc is used as a predicate function by the Sail Library to determine
// whether to take ownership of a CRD. Returns true if the owning OLM subscription
// no longer exists (allowing CIO to take ownership), false otherwise.
func (r *reconciler) overwriteOLMManagedCRDFunc(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) bool {
	// defensive measure
	if ctx == nil || crd == nil {
		return false
	}

	logOverwrite := log.WithValues("crd", crd.GetName())

	labels := crd.GetLabels()
	if labels == nil {
		// no labels, we can override
		return true
	}

	// This is not a OLM managed CRD
	if val, ok := labels["olm.managed"]; !ok || val != "true" {
		return true
	}

	// Look for OLM subscription labels (format: "operators.coreos.com/<name>.<namespace>")
	// Example: "operators.coreos.com/servicemeshoperator.openshift-operators"
	// Note: Multiple labels may exist; check all to avoid non-deterministic behavior from map iteration
	type subscriptionInfo struct {
		name, namespace, label string
	}
	var subscriptions []subscriptionInfo

	for label := range labels {
		if after, ok := strings.CutPrefix(label, subscriptionPrefix); ok {
			// Split on last dot since subscription names can contain dots
			lastDot := strings.LastIndex(after, ".")
			if lastDot == -1 {
				logOverwrite.Info("ignoring invalid OLM label", "label", label)
				continue
			}
			subscriptions = append(subscriptions, subscriptionInfo{
				name:      after[:lastDot],
				namespace: after[lastDot+1:],
				label:     label,
			})
		}
	}

	if len(subscriptions) == 0 {
		logOverwrite.Info("no subscription label found")
		return true
	}

	// Check each subscription we found - if ANY have active resources, do not overwrite
	for _, sub := range subscriptions {
		// Check for InstallPlan, which effectively installs CRDs. Even if invalid it
		// may become valid at some point and overwrite CRDs.
		installPlanList := operatorsv1alpha1.InstallPlanList{}
		if err := r.cache.List(ctx, &installPlanList, client.HasLabels([]string{sub.label})); err != nil {
			log.Error(err, "error trying to list InstallPlans")
			return false
		}
		if len(installPlanList.Items) > 0 {
			logOverwrite.Info("CRD has valid InstallPlans, not overwriting")
			return false
		}

		// Next check for subscriptions.
		subscription := operatorsv1alpha1.Subscription{}
		subscription.SetNamespace(sub.namespace)
		subscription.SetName(sub.name)
		err := r.cache.Get(ctx, client.ObjectKeyFromObject(&subscription), &subscription)
		if err != nil {
			if errors.IsNotFound(err) {
				// No subscription was found, continue to check other subscriptions.
				continue
			}
			log.Error(err, "error trying to get subscription")
			return false
		}

		// If we are here means we don't have InstallPlans, but we do have a subscription
		// which means we may be in an intermediate state.
		return false
	}

	// No active subscriptions or InstallPlans found
	return true
}

// ensureIstio installs or updates Istio using the Sail Library.
// It returns an error if the installation fails.
func (r *reconciler) ensureIstio(ctx context.Context, istioVersion string, gatewayclasses []gatewayapiv1.GatewayClass, infraConfig *configv1.Infrastructure) error {
	sailInstaller := r.sailInstaller

	enableInferenceExtension, err := r.inferencepoolCrdExists(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for InferencePool CRD: %w", err)
	}

	// We can ignore the error if it is not found. It means the configuration of proxy will
	// be null, and no proxy will be configured in this case
	var proxyConfig configv1.Proxy
	if err := r.cache.Get(ctx, types.NamespacedName{Name: "cluster"}, &proxyConfig); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("error verifying cluster proxy configuration: %w", err)
	}

	// Build options from current state
	opts, err := r.buildInstallerOptions(enableInferenceExtension, istioVersion, gatewayclasses, &extraIstioConfig{
		proxyConfig: &proxyConfig,
		infraConfig: infraConfig,
	})
	if err != nil {
		return err
	}

	opts.OverwriteOLMManagedCRD = r.overwriteOLMManagedCRDFunc

	// Apply triggers asynchronous installation/update. Helm charts are only applied
	// if options or values have changed. Status is checked later via r.sailInstaller.Status()
	// and mapped to GatewayClass conditions.
	sailInstaller.Apply(opts)

	return nil
}

// buildInstallerOptions creates Sail Library installation options by merging
// Gateway API defaults with OpenShift-specific overrides
func (r *reconciler) buildInstallerOptions(enableInferenceExtension bool, istioVersion string, gatewayclasses []gatewayapiv1.GatewayClass, extraConfig *extraIstioConfig) (install.Options, error) {
	// Start with Gateway API defaults
	values := install.GatewayAPIDefaults()

	// Apply OpenShift-specific overrides
	openshiftOverrides, err := openshiftValues(enableInferenceExtension, r.config.OperandNamespace, gatewayclasses, extraConfig)
	if err != nil {
		return install.Options{}, err
	}
	values = install.MergeValues(values, openshiftOverrides)

	return install.Options{
		Namespace:      r.config.OperandNamespace,
		Revision:       controller.IstioName("").Name,
		Values:         values,
		Version:        istioVersion,
		ManageCRDs:     ptr.To(true),
		IncludeAllCRDs: ptr.To(true),
	}, nil
}

// openshiftValues returns the OpenShift-specific value overrides for Istio.
// These values are merged on top of the gateway-api preset defaults.
func openshiftValues(enableInferenceExtension bool, operandNamespace string, gatewayclasses []gatewayapiv1.GatewayClass, extraConfig *extraIstioConfig) (*sailv1.Values, error) {
	pilotEnv := gatewayAPIPilotEnv(enableInferenceExtension)

	val := &sailv1.Values{
		Global: &sailv1.GlobalConfig{
			DefaultPodDisruptionBudget: &sailv1.DefaultPodDisruptionBudgetConfig{
				Enabled: ptr.To(false),
			},
			IstioNamespace: ptr.To(operandNamespace),
			// Use system-cluster-critical priority for OpenShift system workloads
			PriorityClassName: ptr.To(systemClusterCriticalPriorityClassName),
			// OpenShift-specific CA trust bundle
			TrustBundleName: ptr.To(controller.OpenShiftGatewayCARootCertName),
		},
		Pilot: &sailv1.PilotConfig{
			Env: pilotEnv,
			// Workload partitioning for OpenShift management workloads
			PodAnnotations: map[string]string{
				WorkloadPartitioningManagementAnnotationKey: WorkloadPartitioningManagementPreferredScheduling,
			},
		},
		MeshConfig: &sailv1.MeshConfig{
			DefaultConfig: &sailv1.MeshConfigProxyConfig{},
		},
	}

	if extraConfig != nil {
		if extraConfig.proxyConfig != nil {
			if proxyMetadata := buildProxyMetadata(extraConfig.proxyConfig); proxyMetadata != nil {
				val.MeshConfig.DefaultConfig.ProxyMetadata = proxyMetadata
			}
		}
		if extraConfig.infraConfig != nil {
			if gwClassConfig, err := buildGatewayClassesConfig(extraConfig.infraConfig, gatewayclasses); err != nil {
				return nil, fmt.Errorf("failed to build gateway class config: %w", err)
			} else {
				val.GatewayClasses = gwClassConfig
			}
		}
	}
	return val, nil
}

func buildProxyMetadata(proxyConfig *configv1.Proxy) map[string]string {
	if proxyConfig == nil {
		return nil
	}
	proxyCfg := proxyConfig.Status
	proxyMetadata := map[string]string{}
	if proxyCfg.HTTPProxy != "" {
		proxyMetadata["HTTP_PROXY"] = proxyCfg.HTTPProxy
		proxyMetadata["http_proxy"] = proxyCfg.HTTPProxy
	}
	if proxyCfg.HTTPSProxy != "" {
		proxyMetadata["HTTPS_PROXY"] = proxyCfg.HTTPSProxy
		proxyMetadata["https_proxy"] = proxyCfg.HTTPSProxy
	}
	if proxyCfg.NoProxy != "" {
		proxyMetadata["NO_PROXY"] = proxyCfg.NoProxy
		proxyMetadata["no_proxy"] = proxyCfg.NoProxy
	}
	if len(proxyMetadata) == 0 {
		return nil
	}
	return proxyMetadata
}

// buildGatewayClassesConfig returns Istio per-gatewayclass configuration
// overlays given an infrastructure config and a slice of gatewayclasses.
func buildGatewayClassesConfig(infraConfig *configv1.Infrastructure, gatewayclasses []gatewayapiv1.GatewayClass) (json.RawMessage, error) {
	const maxReplicas = 10

	var minReplicas = 2
	if infraConfig.Status.InfrastructureTopology == configv1.SingleReplicaTopologyMode {
		minReplicas = 1
	}

	gatewayclassConfig := map[string]any{
		"horizontalPodAutoscaler": map[string]any{
			"spec": map[string]any{
				"minReplicas": minReplicas,
				"maxReplicas": maxReplicas,
			},
		},
		"deployment": map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []map[string]any{
							{
								"name":                     "istio-proxy",
								"terminationMessagePolicy": "FallbackToLogsOnError",
							},
						},
					},
				},
			},
		},
	}
	gatewayclassesConfig := map[string]any{}
	for _, gatewayclass := range gatewayclasses {
		gatewayclassesConfig[gatewayclass.Name] = gatewayclassConfig
	}

	gatewayclassesConfigJson, err := json.Marshal(gatewayclassesConfig)
	if err != nil {
		return nil, err
	}

	return gatewayclassesConfigJson, nil
}
