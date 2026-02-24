// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package install

import (
	"fmt"

	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/helm"
	"k8s.io/utils/ptr"
)

// GatewayAPIDefaults returns pre-configured values for Gateway API mode on OpenShift.
// These values configure istiod to work as a Gateway API controller.
//
// Usage:
//
//	values := install.GatewayAPIDefaults()
//	values.Pilot.Env["PILOT_GATEWAY_API_CONTROLLER_NAME"] = "my-controller"
//	values.Pilot.Env["PILOT_GATEWAY_API_DEFAULT_GATEWAYCLASS_NAME"] = "my-class"
//	installer.Install(ctx, Options{Values: values})
//
// Consumer must set:
//   - pilot.env.PILOT_GATEWAY_API_CONTROLLER_NAME
//   - pilot.env.PILOT_GATEWAY_API_DEFAULT_GATEWAYCLASS_NAME
//
// Consumer may optionally set:
//   - global.trustBundleName (if using custom CA)
func GatewayAPIDefaults() *v1.Values {
	return &v1.Values{
		Global: &v1.GlobalConfig{
			// Disable PodDisruptionBudget - managed externally
			DefaultPodDisruptionBudget: &v1.DefaultPodDisruptionBudgetConfig{
				Enabled: ptr.To(false),
			},
			// Use cluster-critical priority for control plane
			PriorityClassName: ptr.To("system-cluster-critical"),
		},
		Pilot: &v1.PilotConfig{
			// Disable CNI - not needed for Gateway API only mode
			Cni: &v1.CNIUsageConfig{
				Enabled: ptr.To(false),
			},
			Enabled: ptr.To(true),
			Env: map[string]string{
				// Enable Gateway API support
				"PILOT_ENABLE_GATEWAY_API": "true",
				// Disable experimental/alpha Gateway API features
				"PILOT_ENABLE_ALPHA_GATEWAY_API": "false",
				// Enable status updates on Gateway API resources
				"PILOT_ENABLE_GATEWAY_API_STATUS": "true",
				// Enable automated deployment (creates Envoy proxy + service for gateways)
				"PILOT_ENABLE_GATEWAY_API_DEPLOYMENT_CONTROLLER": "true",
				// Disable gatewayclass controller (admin manages gatewayclass)
				"PILOT_ENABLE_GATEWAY_API_GATEWAYCLASS_CONTROLLER": "false",
				// Disable multi-network gateway discovery
				"PILOT_MULTI_NETWORK_DISCOVER_GATEWAY_API": "false",
				// Disable manual deployment (only automated deployment allowed)
				"ENABLE_GATEWAY_API_MANUAL_DEPLOYMENT": "false",
				// Only create CA bundle configmap in namespaces with gateways
				"PILOT_ENABLE_GATEWAY_API_CA_CERT_ONLY": "true",
				// Don't copy labels/annotations from gateways to generated resources
				"PILOT_ENABLE_GATEWAY_API_COPY_LABELS_ANNOTATIONS": "false",
				// Resource filtering for Gateway API mode (X_ prefix until Istio feature is ready)
				// When active, istiod will only reconcile Gateway API + the 3 included Istio resources
				envPilotIgnoreResources:  gatewayAPIIgnoreResources,
				envPilotIncludeResources: gatewayAPIIncludeResources,
			},
		},
		SidecarInjectorWebhook: &v1.SidecarInjectorConfig{
			// Disable sidecar injection by default (Gateway API mode only)
			EnableNamespacesByDefault: ptr.To(false),
		},
		MeshConfig: &v1.MeshConfig{
			// Enable access logging
			AccessLogFile: ptr.To("/dev/stdout"),
			// Disable legacy ingress controller
			IngressControllerMode: v1.MeshConfigIngressControllerModeOff,
			// Configure proxy defaults
			DefaultConfig: &v1.MeshConfigProxyConfig{
				ProxyHeaders: &v1.ProxyConfigProxyHeaders{
					// Don't set Server header
					Server: &v1.ProxyConfigProxyHeadersServer{
						Disabled: ptr.To(true),
					},
					// Don't set X-Envoy-* debug headers
					EnvoyDebugHeaders: &v1.ProxyConfigProxyHeadersEnvoyDebugHeaders{
						Disabled: ptr.To(true),
					},
					// Only exchange metadata headers for in-mesh traffic
					MetadataExchangeHeaders: &v1.ProxyConfigProxyHeadersMetadataExchangeHeaders{
						Mode: v1.ProxyConfigProxyHeadersMetadataExchangeModeInMesh,
					},
				},
			},
		},
	}
}

// mergeOverwrite recursively merges overlay into base, with overlay taking precedence.
// NOTE: This is a copy of istiovalues.mergeOverwrite. Consider exporting the original
// to avoid duplication once the library API stabilizes.
func mergeOverwrite(base map[string]any, overrides map[string]any) map[string]any {
	if base == nil {
		base = make(map[string]any, 1)
	}
	for key, value := range overrides {
		if _, exists := base[key]; !exists {
			base[key] = value
			continue
		}
		childOverrides, overrideValueIsMap := value.(map[string]any)
		childBase, baseValueIsMap := base[key].(map[string]any)
		if baseValueIsMap && overrideValueIsMap {
			base[key] = mergeOverwrite(childBase, childOverrides)
		} else {
			base[key] = value
		}
	}
	return base
}

// MergeValues merges two Values structs, with overlay taking precedence.
// This is useful for combining GatewayAPIDefaults() with custom overrides.
//
// Maps are merged recursively (overlay keys override base keys).
// Lists are replaced entirely (overlay list replaces base list).
//
// Example:
//
//	base := install.GatewayAPIDefaults()
//	overrides := &v1.Values{Global: &v1.GlobalConfig{Hub: ptr.To("my-registry")}}
//	merged := install.MergeValues(base, overrides)
func MergeValues(base, overlay *v1.Values) *v1.Values {
	if base == nil {
		return overlay
	}
	if overlay == nil {
		return base
	}
	baseMap := helm.FromValues(base)
	overlayMap := helm.FromValues(overlay)
	merged := mergeOverwrite(baseMap, overlayMap)
	result, err := helm.ToValues(merged, &v1.Values{})
	if err != nil {
		panic(fmt.Sprintf("failed to convert merged values: %v", err))
	}
	return result
}
