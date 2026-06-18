package gatewayclass

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// subscriptionPrefix is the OLM label prefix indicating subscription ownership.
	subscriptionPrefix = "operators.coreos.com/"
)

// overwriteOLMCRDCheck is used as the OLM callback by the gRPC client.
// It receives the CRD name and labels from the sidecar and determines
// whether CIO should take ownership of the CRD.
func (r *reconciler) overwriteOLMCRDCheck(ctx context.Context, crdName string, crdLabels map[string]string) bool {
	if ctx == nil {
		return false
	}

	logOverwrite := log.WithValues("crd", crdName)

	labels := crdLabels
	if labels == nil {
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

	// Apply triggers asynchronous installation/update. Helm charts are only applied
	// if options or values have changed. Status is checked later via r.sailInstaller.Status()
	// and mapped to GatewayClass conditions.
	if err := sailInstaller.Apply(opts); err != nil {
		return fmt.Errorf("failed to apply Istio installation options: %w", err)
	}

	return nil
}

// buildInstallerOptions creates Sail Library installation options by merging
// Gateway API defaults with OpenShift-specific overrides.
func (r *reconciler) buildInstallerOptions(enableInferenceExtension bool, istioVersion string, gatewayclasses []gatewayapiv1.GatewayClass, extraConfig *extraIstioConfig) (SailOptions, error) {
	ns := r.config.OperandNamespace

	// Gateway API defaults (equivalent to install.GatewayAPIDefaults)
	values := map[string]any{
		"pilot": map[string]any{
			"env": map[string]string{
				"PILOT_ENABLE_GATEWAY_API":                       "true",
				"PILOT_ENABLE_GATEWAY_API_STATUS":                "true",
				"PILOT_ENABLE_GATEWAY_API_DEPLOYMENT_CONTROLLER": "true",
			},
		},
		"global": map[string]any{
			"istioNamespace":  ns,
			"trustBundleName": "openshift-gateway-ca-cert",
		},
	}

	// OpenShift-specific overrides
	overrides, err := openshiftValuesMap(enableInferenceExtension, ns, gatewayclasses, extraConfig)
	if err != nil {
		return SailOptions{}, err
	}
	values = mergeValuesMap(values, overrides)

	return SailOptions{
		Namespace:      ns,
		Revision:       controller.IstioName("").Name,
		Values:         values,
		Version:        istioVersion,
		ManageCRDs:     true,
		IncludeAllCRDs: true,
	}, nil
}

// openshiftValuesMap returns OpenShift-specific value overrides as a generic map.
func openshiftValuesMap(enableInferenceExtension bool, operandNamespace string, gatewayclasses []gatewayapiv1.GatewayClass, extraConfig *extraIstioConfig) (map[string]any, error) {
	pilotEnv := gatewayAPIPilotEnv(enableInferenceExtension)

	val := map[string]any{
		"global": map[string]any{
			"defaultPodDisruptionBudget": map[string]any{
				"enabled": false,
			},
			"istioNamespace":  operandNamespace,
			"priorityClassName": systemClusterCriticalPriorityClassName,
			"trustBundleName": controller.OpenShiftGatewayCARootCertName,
		},
		"pilot": map[string]any{
			"env": pilotEnv,
			"podAnnotations": map[string]string{
				WorkloadPartitioningManagementAnnotationKey: WorkloadPartitioningManagementPreferredScheduling,
			},
		},
		"meshConfig": map[string]any{
			"defaultConfig": map[string]any{},
		},
	}

	var infraConfig *configv1.Infrastructure
	if extraConfig != nil {
		if extraConfig.proxyConfig != nil {
			if proxyMetadata := buildProxyMetadata(extraConfig.proxyConfig); proxyMetadata != nil {
				meshDefault := val["meshConfig"].(map[string]any)["defaultConfig"].(map[string]any)
				meshDefault["proxyMetadata"] = proxyMetadata
			}
		}
		infraConfig = extraConfig.infraConfig
	}

	gwClassConfig, err := buildGatewayClassesConfigMap(infraConfig, gatewayclasses)
	if err != nil {
		return nil, fmt.Errorf("failed to build gateway class config: %w", err)
	}
	val["gatewayClasses"] = gwClassConfig

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

// buildGatewayClassesConfig returns per-gatewayclass config as JSON
// for the OLM path which uses sailv1.Istio with json.RawMessage fields.
func buildGatewayClassesConfig(infraConfig *configv1.Infrastructure, gatewayclasses []gatewayapiv1.GatewayClass) (json.RawMessage, error) {
	m, err := buildGatewayClassesConfigMap(infraConfig, gatewayclasses)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// buildGatewayClassesConfigMap returns per-gatewayclass config as a map.
func buildGatewayClassesConfigMap(infraConfig *configv1.Infrastructure, gatewayclasses []gatewayapiv1.GatewayClass) (map[string]any, error) {
	const maxReplicas = 10

	var minReplicas = 2
	if infraConfig != nil && infraConfig.Status.InfrastructureTopology == configv1.SingleReplicaTopologyMode {
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
								"name":                     gatewayProxyContainerName,
								"terminationMessagePolicy": string(v1.TerminationMessageFallbackToLogsOnError),
							},
						},
					},
				},
			},
		},
	}
	result := map[string]any{}
	for _, gc := range gatewayclasses {
		result[gc.Name] = gatewayclassConfig
	}

	return result, nil
}

// mergeValuesMap deep-merges overlay on top of base. Overlay wins.
func mergeValuesMap(base, overlay map[string]any) map[string]any {
	result := make(map[string]any, len(base))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		if baseMap, ok := result[k].(map[string]any); ok {
			if overlayMap, ok2 := v.(map[string]any); ok2 {
				result[k] = mergeValuesMap(baseMap, overlayMap)
				continue
			}
		}
		result[k] = v
	}
	return result
}
