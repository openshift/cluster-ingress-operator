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
	"io/fs"
	"strings"

	"github.com/istio-ecosystem/sail-operator/pkg/config"
	"gopkg.in/yaml.v3"
)

// ImageNames defines the image name for each component (without registry or tag).
type ImageNames struct {
	Istiod  string
	Proxy   string
	CNI     string
	ZTunnel string
}

// SetImageDefaults populates config.Config.ImageDigests by scanning resourceFS
// for version directories and constructing image refs from the given registry
// and image names.
//
// This is a no-op if config.Config.ImageDigests is already populated (e.g. by
// config.Read() in the operator path).
//
// Example:
//
//	install.SetImageDefaults(resourceFS, "registry.redhat.io/openshift-service-mesh", install.ImageNames{
//	    Istiod:  "istio-pilot-rhel9",
//	    Proxy:   "istio-proxyv2-rhel9",
//	    CNI:     "istio-cni-rhel9",
//	    ZTunnel: "istio-ztunnel-rhel9",
//	})
func SetImageDefaults(resourceFS fs.FS, registry string, images ImageNames) error {
	if config.Config.ImageDigests != nil {
		return nil
	}
	entries, err := fs.ReadDir(resourceFS, ".")
	if err != nil {
		return fmt.Errorf("failed to read resource directory: %w", err)
	}
	config.Config.ImageDigests = make(map[string]config.IstioImageConfig)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		v := e.Name()
		tag := strings.TrimPrefix(v, "v")
		config.Config.ImageDigests[v] = config.IstioImageConfig{
			IstiodImage:  registry + "/" + images.Istiod + ":" + tag,
			ProxyImage:   registry + "/" + images.Proxy + ":" + tag,
			CNIImage:     registry + "/" + images.CNI + ":" + tag,
			ZTunnelImage: registry + "/" + images.ZTunnel + ":" + tag,
		}
	}
	return nil
}

// csvDeployment is the minimal structure needed to reach pod template annotations
// inside a ClusterServiceVersion YAML.
type csvDeployment struct {
	Spec struct {
		Install struct {
			Spec struct {
				Deployments []struct {
					Spec struct {
						Template struct {
							Metadata struct {
								Annotations map[string]string `yaml:"annotations"`
							} `yaml:"metadata"`
						} `yaml:"template"`
					} `yaml:"spec"`
				} `yaml:"deployments"`
			} `yaml:"spec"`
		} `yaml:"install"`
	} `yaml:"spec"`
}

// LoadImageDigestsFromCSV reads image references from ClusterServiceVersion
// YAML bytes and populates config.Config.ImageDigests.
//
// The CSV's pod template annotations are expected to contain keys of the form
// "images.<version>.<component>" (e.g. "images.v1_27_0.istiod"). Underscores
// in the version segment are converted to dots (v1_27_0 -> v1.27.0), matching
// the convention used by config.Read().
//
// This is a no-op if config.Config.ImageDigests is already populated.
func LoadImageDigestsFromCSV(csvData []byte) error {
	if config.Config.ImageDigests != nil {
		return nil
	}

	var csv csvDeployment
	if err := yaml.Unmarshal(csvData, &csv); err != nil {
		return fmt.Errorf("failed to parse CSV YAML: %w", err)
	}

	annotations := findImageAnnotations(csv)
	if len(annotations) == 0 {
		return fmt.Errorf("no image annotations found in CSV")
	}

	config.Config.ImageDigests = buildImageDigests(annotations)
	return nil
}

// findImageAnnotations extracts annotations starting with "images." from the
// first deployment in the CSV.
func findImageAnnotations(csv csvDeployment) map[string]string {
	deployments := csv.Spec.Install.Spec.Deployments
	if len(deployments) == 0 {
		return nil
	}
	all := deployments[0].Spec.Template.Metadata.Annotations
	filtered := make(map[string]string, len(all))
	for k, v := range all {
		if strings.HasPrefix(k, "images.") {
			filtered[k] = v
		}
	}
	return filtered
}

// buildImageDigests converts flat annotation keys ("images.v1_27_0.istiod")
// into a map[version]IstioImageConfig, replacing underscores with dots in
// the version segment.
func buildImageDigests(annotations map[string]string) map[string]config.IstioImageConfig {
	digests := make(map[string]config.IstioImageConfig)
	for key, image := range annotations {
		// key format: "images.<version>.<component>"
		parts := strings.SplitN(key, ".", 3)
		if len(parts) != 3 {
			continue
		}
		version := strings.ReplaceAll(parts[1], "_", ".")
		component := parts[2]

		cfg := digests[version]
		switch component {
		case "istiod":
			cfg.IstiodImage = image
		case "proxy":
			cfg.ProxyImage = image
		case "cni":
			cfg.CNIImage = image
		case "ztunnel":
			cfg.ZTunnelImage = image
		}
		digests[version] = cfg
	}
	return digests
}
