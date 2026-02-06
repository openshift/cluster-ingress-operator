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
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/chart/crds"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
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

// ensureCRDs installs CRDs based on PILOT_IGNORE_RESOURCES and PILOT_INCLUDE_RESOURCES.
// Resources matching INCLUDE are installed. Resources matching IGNORE (but not INCLUDE) are skipped.
// If a CRD already exists, it is left alone.
func (i *Installer) ensureCRDs(ctx context.Context, values *v1.Values) error {
	if values == nil || values.Pilot == nil || values.Pilot.Env == nil {
		return nil
	}

	ignorePatterns := values.Pilot.Env[EnvPilotIgnoreResources]
	includePatterns := values.Pilot.Env[EnvPilotIncludeResources]

	// If no filters defined, nothing to do
	if ignorePatterns == "" && includePatterns == "" {
		return nil
	}

	// Install CRDs for explicitly included resources
	if includePatterns != "" {
		for _, resource := range strings.Split(includePatterns, ",") {
			resource = strings.TrimSpace(resource)
			if !shouldManageResource(resource, ignorePatterns, includePatterns) {
				continue
			}

			crdFile := resourceToCRDFilename(resource)
			if crdFile == "" {
				continue // invalid resource format (e.g., contains wildcard), skip
			}

			if err := i.ensureCRD(ctx, crdFile); err != nil {
				return fmt.Errorf("failed to ensure CRD %s: %w", resource, err)
			}
		}
	}
	return nil
}

// ensureCRD installs a single CRD if it doesn't exist
func (i *Installer) ensureCRD(ctx context.Context, filename string) error {
	data, err := fs.ReadFile(crds.FS, filename)
	if err != nil {
		return fmt.Errorf("failed to read CRD file %s: %w", filename, err)
	}

	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(data, crd); err != nil {
		return fmt.Errorf("failed to unmarshal CRD %s: %w", filename, err)
	}

	// Check if CRD exists
	existing := &apiextensionsv1.CustomResourceDefinition{}
	err = i.client.Get(ctx, client.ObjectKey{Name: crd.Name}, existing)
	if err == nil {
		// CRD exists, leave it alone
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to check CRD %s: %w", crd.Name, err)
	}

	// CRD doesn't exist, create it
	if err := i.client.Create(ctx, crd); err != nil {
		return fmt.Errorf("failed to create CRD %s: %w", crd.Name, err)
	}

	return nil
}
