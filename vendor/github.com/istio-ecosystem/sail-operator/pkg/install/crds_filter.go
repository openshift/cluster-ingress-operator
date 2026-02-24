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
	"path/filepath"
	"strings"

	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/chart/crds"
)

// Environment variable names for Istio resource filtering.
// X_ prefix prevents Istio from processing until the feature is ready.
// When activating, remove the X_ prefix.
// See: https://github.com/istio/istio/commit/7e58d08397ee7b7119bf49abc9bd7b4f550f7839
const (
	envPilotIgnoreResources  = "X_PILOT_IGNORE_RESOURCES"
	envPilotIncludeResources = "X_PILOT_INCLUDE_RESOURCES"
)

// Resource filtering values for Gateway API mode.
// Ignore all istio.io resources except the 3 needed for gateway customization.
const (
	gatewayAPIIgnoreResources  = "*.istio.io"
	gatewayAPIIncludeResources = "wasmplugins.extensions.istio.io,envoyfilters.networking.istio.io,destinationrules.networking.istio.io"
)

// resourceToCRDFilename converts a resource name to its CRD filename.
// The naming convention is: "{plural}.{group}" -> "{group}_{plural}.yaml"
// Example: "wasmplugins.extensions.istio.io" -> "extensions.istio.io_wasmplugins.yaml"
func resourceToCRDFilename(resource string) string {
	parts := strings.SplitN(resource, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	plural := parts[0] // e.g., "wasmplugins"
	group := parts[1]  // e.g., "extensions.istio.io"
	return fmt.Sprintf("%s_%s.yaml", group, plural)
}

// crdFilenameToResource converts a CRD filename back to a resource name.
// The naming convention is: "{group}_{plural}.yaml" -> "{plural}.{group}"
// Example: "extensions.istio.io_wasmplugins.yaml" -> "wasmplugins.extensions.istio.io"
func crdFilenameToResource(filename string) string {
	// Strip .yaml suffix
	name := strings.TrimSuffix(filename, ".yaml")
	// Split on underscore: group_plural
	parts := strings.SplitN(name, "_", 2)
	if len(parts) != 2 {
		return ""
	}
	group := parts[0]  // e.g., "extensions.istio.io"
	plural := parts[1] // e.g., "wasmplugins"
	return fmt.Sprintf("%s.%s", plural, group)
}

// matchesPattern checks if a resource matches a filter pattern.
// Patterns support glob-style wildcards:
//   - "*.istio.io" matches "virtualservices.networking.istio.io"
//   - "virtualservices.networking.istio.io" matches exactly
//
// The resource format is "{plural}.{group}" (e.g., "wasmplugins.extensions.istio.io")
func matchesPattern(resource, pattern string) bool {
	// Handle glob patterns using filepath.Match
	// Pattern "*.istio.io" should match "virtualservices.networking.istio.io"
	matched, err := filepath.Match(pattern, resource)
	if err != nil {
		return false
	}
	return matched
}

// matchesAnyPattern checks if a resource matches any pattern in a comma-separated list.
func matchesAnyPattern(resource, patterns string) bool {
	if patterns == "" {
		return false
	}
	for _, pattern := range strings.Split(patterns, ",") {
		pattern = strings.TrimSpace(pattern)
		if matchesPattern(resource, pattern) {
			return true
		}
	}
	return false
}

// shouldManageResource determines if a resource should be managed based on
// IGNORE and INCLUDE filters. The logic follows Istio's resource filtering:
//   - If resource matches INCLUDE, manage it (INCLUDE overrides IGNORE)
//   - If resource matches IGNORE (and not INCLUDE), skip it
//   - If resource matches neither, manage it (default allow)
func shouldManageResource(resource, ignorePatterns, includePatterns string) bool {
	// INCLUDE takes precedence - if explicitly included, always manage
	if matchesAnyPattern(resource, includePatterns) {
		return true
	}
	// If ignored (and not included), skip
	if matchesAnyPattern(resource, ignorePatterns) {
		return false
	}
	// Default: manage if not matched by any filter
	return true
}

// allIstioCRDs returns all *.istio.io CRD resource names from the embedded CRD filesystem.
// Sail operator CRDs (sailoperator.io) are excluded since those belong to the Sail operator.
func allIstioCRDs() ([]string, error) {
	entries, err := fs.ReadDir(crds.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to read CRD directory: %w", err)
	}

	var resources []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		// Skip Sail operator CRDs
		if strings.HasPrefix(entry.Name(), "sailoperator.io_") {
			continue
		}
		resource := crdFilenameToResource(entry.Name())
		if resource != "" {
			resources = append(resources, resource)
		}
	}
	return resources, nil
}

// targetCRDsFromValues determines the target CRD set based on values and options.
// If includeAllCRDs is true, returns all *.istio.io CRDs.
// Otherwise, returns CRDs derived from PILOT_INCLUDE_RESOURCES in values.
func targetCRDsFromValues(values *v1.Values, includeAllCRDs bool) ([]string, error) {
	if includeAllCRDs {
		return allIstioCRDs()
	}

	if values == nil || values.Pilot == nil || values.Pilot.Env == nil {
		return nil, nil
	}

	ignorePatterns := values.Pilot.Env[envPilotIgnoreResources]
	includePatterns := values.Pilot.Env[envPilotIncludeResources]

	// If no filters defined, nothing to do
	if ignorePatterns == "" && includePatterns == "" {
		return nil, nil
	}

	var targets []string
	if includePatterns != "" {
		for _, resource := range strings.Split(includePatterns, ",") {
			resource = strings.TrimSpace(resource)
			if !shouldManageResource(resource, ignorePatterns, includePatterns) {
				continue
			}
			// Skip resources that don't map to a valid CRD filename (e.g. wildcards)
			if resourceToCRDFilename(resource) == "" {
				continue
			}
			targets = append(targets, resource)
		}
	}
	return targets, nil
}
