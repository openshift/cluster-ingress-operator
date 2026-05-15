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
	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"k8s.io/utils/ptr"
)

// Environment variable names for Istio resource filtering.
// X_ prefix prevents Istio from processing until the feature is ready.
// When activating, remove the X_ prefix.
// See: https://github.com/istio/istio/commit/7e58d08397ee7b7119bf49abc9bd7b4f550f7839
const (
	EnvPilotIgnoreResources  = "X_PILOT_IGNORE_RESOURCES"
	EnvPilotIncludeResources = "X_PILOT_INCLUDE_RESOURCES"
)

// Resource filtering values for Gateway API mode.
// Ignore all istio.io resources except the 3 needed for gateway customization.
const (
	GatewayAPIIgnoreResources  = "*.istio.io"
	GatewayAPIIncludeResources = "wasmplugins.extensions.istio.io,envoyfilters.networking.istio.io,destinationrules.networking.istio.io"
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
				EnvPilotIgnoreResources:  GatewayAPIIgnoreResources,
				EnvPilotIncludeResources: GatewayAPIIncludeResources,
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

// MergeValues merges two Values structs, with overlay taking precedence.
// This is useful for combining GatewayAPIDefaults() with custom overrides.
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

	// Deep copy base to avoid modifying it
	result := base.DeepCopy()

	// Merge Global
	if overlay.Global != nil {
		if result.Global == nil {
			result.Global = overlay.Global.DeepCopy()
		} else {
			mergeGlobal(result.Global, overlay.Global)
		}
	}

	// Merge Pilot
	if overlay.Pilot != nil {
		if result.Pilot == nil {
			result.Pilot = overlay.Pilot.DeepCopy()
		} else {
			mergePilot(result.Pilot, overlay.Pilot)
		}
	}

	// Merge SidecarInjectorWebhook
	if overlay.SidecarInjectorWebhook != nil {
		if result.SidecarInjectorWebhook == nil {
			result.SidecarInjectorWebhook = overlay.SidecarInjectorWebhook.DeepCopy()
		} else {
			mergeSidecarInjector(result.SidecarInjectorWebhook, overlay.SidecarInjectorWebhook)
		}
	}

	// Merge MeshConfig
	if overlay.MeshConfig != nil {
		if result.MeshConfig == nil {
			result.MeshConfig = overlay.MeshConfig.DeepCopy()
		} else {
			mergeMeshConfig(result.MeshConfig, overlay.MeshConfig)
		}
	}

	// Copy other fields from overlay if set
	if overlay.Revision != nil {
		result.Revision = overlay.Revision
	}

	return result
}

func mergeGlobal(base, overlay *v1.GlobalConfig) {
	if overlay.DefaultPodDisruptionBudget != nil {
		base.DefaultPodDisruptionBudget = overlay.DefaultPodDisruptionBudget.DeepCopy()
	}
	if overlay.PriorityClassName != nil {
		base.PriorityClassName = overlay.PriorityClassName
	}
	if overlay.IstioNamespace != nil {
		base.IstioNamespace = overlay.IstioNamespace
	}
	if overlay.TrustBundleName != nil {
		base.TrustBundleName = overlay.TrustBundleName
	}
	if overlay.Hub != nil {
		base.Hub = overlay.Hub
	}
	if overlay.Tag != nil {
		base.Tag = overlay.Tag
	}
}

func mergePilot(base, overlay *v1.PilotConfig) {
	if overlay.Enabled != nil {
		base.Enabled = overlay.Enabled
	}
	if overlay.Cni != nil {
		base.Cni = overlay.Cni.DeepCopy()
	}
	if overlay.Env != nil {
		if base.Env == nil {
			base.Env = make(map[string]string)
		}
		for k, v := range overlay.Env {
			base.Env[k] = v
		}
	}
	if overlay.PodAnnotations != nil {
		if base.PodAnnotations == nil {
			base.PodAnnotations = make(map[string]string)
		}
		for k, v := range overlay.PodAnnotations {
			base.PodAnnotations[k] = v
		}
	}
	if overlay.ExtraContainerArgs != nil {
		base.ExtraContainerArgs = overlay.ExtraContainerArgs
	}
}

func mergeSidecarInjector(base, overlay *v1.SidecarInjectorConfig) {
	if overlay.EnableNamespacesByDefault != nil {
		base.EnableNamespacesByDefault = overlay.EnableNamespacesByDefault
	}
}

func mergeMeshConfig(base, overlay *v1.MeshConfig) {
	if overlay.AccessLogFile != nil {
		base.AccessLogFile = overlay.AccessLogFile
	}
	if overlay.IngressControllerMode != "" {
		base.IngressControllerMode = overlay.IngressControllerMode
	}
	if overlay.DefaultConfig != nil {
		if base.DefaultConfig == nil {
			base.DefaultConfig = overlay.DefaultConfig.DeepCopy()
		} else {
			// Just overwrite for now - could be more granular
			if overlay.DefaultConfig.ProxyHeaders != nil {
				base.DefaultConfig.ProxyHeaders = overlay.DefaultConfig.ProxyHeaders.DeepCopy()
			}
		}
	}
}
