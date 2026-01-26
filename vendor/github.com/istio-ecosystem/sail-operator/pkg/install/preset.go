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
	"embed"
	"fmt"

	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"gopkg.in/yaml.v3"
)

//go:embed presets/*.yaml
var presetsFS embed.FS

// PresetName identifies a curated installation configuration
type PresetName string

const (
	// PresetGatewayAPI is a minimal Istio installation for Gateway API only.
	// Includes: istiod (minimal), Gateway API CRDs.
	// Excludes: sidecar injection, telemetry, etc.
	PresetGatewayAPI PresetName = "gateway-api"

	// PresetGatewayAPIAmbient adds ambient mesh support to Gateway API.
	// Includes: istiod, CNI, ZTunnel.
	PresetGatewayAPIAmbient PresetName = "gateway-api-ambient"

	// PresetFull is a full Istio installation with all features.
	PresetFull PresetName = "full"
)

// Preset defines a curated installation configuration
type Preset struct {
	// Name is the preset identifier
	Name PresetName `yaml:"name"`

	// Description explains what this preset provides
	Description string `yaml:"description"`

	// BaseProfile is the underlying Sail/Istio profile (e.g., "openshift", "openshift-ambient")
	BaseProfile string `yaml:"baseProfile"`

	// Components specifies which Istio components to install
	Components Components `yaml:"components"`

	// Values are the preset's default Helm values
	Values *v1.Values `yaml:"values,omitempty"`
}

// Components specifies which Istio components are included in a preset
type Components struct {
	// Istiod enables the Istio control plane (istiod)
	Istiod bool `yaml:"istiod"`

	// CNI enables the Istio CNI plugin (required on OpenShift)
	CNI bool `yaml:"cni"`

	// ZTunnel enables the ztunnel component (for ambient mesh)
	ZTunnel bool `yaml:"ztunnel"`
}

// loadPresets loads all preset definitions from embedded YAML files
func loadPresets() (map[PresetName]Preset, error) {
	presets := make(map[PresetName]Preset)

	entries, err := presetsFS.ReadDir("presets")
	if err != nil {
		return nil, fmt.Errorf("failed to read presets directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		data, err := presetsFS.ReadFile("presets/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("failed to read preset file %s: %w", entry.Name(), err)
		}

		var preset Preset
		if err := yaml.Unmarshal(data, &preset); err != nil {
			return nil, fmt.Errorf("failed to parse preset %s: %w", entry.Name(), err)
		}

		if preset.Name == "" {
			return nil, fmt.Errorf("preset %s has no name defined", entry.Name())
		}

		presets[preset.Name] = preset
	}

	return presets, nil
}

// GetAvailablePresetNames returns all available preset names
func GetAvailablePresetNames() []PresetName {
	return []PresetName{
		PresetGatewayAPI,
		PresetGatewayAPIAmbient,
		PresetFull,
	}
}
