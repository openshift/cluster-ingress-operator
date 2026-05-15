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
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/istio-ecosystem/sail-operator/pkg/config"
	"github.com/istio-ecosystem/sail-operator/pkg/constants"
	sharedreconcile "github.com/istio-ecosystem/sail-operator/pkg/reconcile"
	"github.com/istio-ecosystem/sail-operator/pkg/helm"
	"github.com/istio-ecosystem/sail-operator/pkg/revision"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// WatchType indicates how events for a resource type should be handled.
type WatchType int

const (
	// WatchTypeOwned indicates resources owned by the installation.
	// Use owner reference filtering to only watch resources created by the installer.
	WatchTypeOwned WatchType = iota

	// WatchTypeNamespace indicates namespace resources should be watched.
	// Used to detect when the target namespace is created/deleted.
	WatchTypeNamespace
)

// String returns the string representation of the WatchType.
func (w WatchType) String() string {
	switch w {
	case WatchTypeOwned:
		return "Owned"
	case WatchTypeNamespace:
		return "Namespace"
	default:
		return fmt.Sprintf("Unknown(%d)", w)
	}
}

// WatchSpec describes a resource type that should be watched for drift detection.
type WatchSpec struct {
	// GVK is the GroupVersionKind of the resource to watch.
	GVK schema.GroupVersionKind

	// WatchType indicates how events should be handled.
	WatchType WatchType

	// ClusterScoped indicates if this is a cluster-scoped resource (vs namespaced).
	// Cluster-scoped resources require a different informer factory setup.
	ClusterScoped bool
}

// GetWatchSpecs renders the istiod Helm charts and extracts the GVKs
// of all resources that would be created. This allows library consumers
// to set up watches for drift detection.
//
// The returned specs include:
//   - All resource types from the base and istiod charts (WatchTypeOwned)
//   - Namespace (WatchTypeNamespace) for namespace existence checks
func (i *Installer) GetWatchSpecs(opts Options) ([]WatchSpec, error) {
	opts.applyDefaults()

	// Validate version directory exists in the resource FS
	if err := ValidateVersion(i.resourceFS, opts.Version); err != nil {
		return nil, fmt.Errorf("invalid version %q: %w", opts.Version, err)
	}
	resolvedVersion := opts.Version

	// Compute values using the same pipeline as Install()
	values, err := revision.ComputeValues(
		opts.Values,
		opts.Namespace,
		resolvedVersion,
		config.PlatformOpenShift,
		defaultProfile,
		"",
		i.resourceFS,
		opts.Revision,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to compute values: %w", err)
	}
	helmValues := helm.FromValues(values)

	// Collect GVKs from both charts
	gvkSet := make(map[schema.GroupVersionKind]struct{})

	// Render base chart (for default revision)
	if opts.Revision == defaultRevision {
		baseChartPath := sharedreconcile.GetChartPath(resolvedVersion, constants.BaseChartName)
		rendered, err := helm.RenderChart(i.resourceFS, baseChartPath, helmValues, opts.Namespace, "base")
		if err != nil {
			return nil, fmt.Errorf("failed to render base chart: %w", err)
		}
		if err := extractGVKsFromRendered(rendered, gvkSet); err != nil {
			return nil, fmt.Errorf("failed to extract GVKs from base chart: %w", err)
		}
	}

	// Render istiod chart
	istiodChartPath := sharedreconcile.GetChartPath(resolvedVersion, constants.IstiodChartName)
	rendered, err := helm.RenderChart(i.resourceFS, istiodChartPath, helmValues, opts.Namespace, "istiod")
	if err != nil {
		return nil, fmt.Errorf("failed to render istiod chart: %w", err)
	}
	if err := extractGVKsFromRendered(rendered, gvkSet); err != nil {
		return nil, fmt.Errorf("failed to extract GVKs from istiod chart: %w", err)
	}

	// Convert to specs
	specs := make([]WatchSpec, 0, len(gvkSet)+1)
	for gvk := range gvkSet {
		specs = append(specs, WatchSpec{
			GVK:           gvk,
			WatchType:     WatchTypeOwned,
			ClusterScoped: isClusterScoped(gvk),
		})
	}

	// Add Namespace watch
	specs = append(specs, WatchSpec{
		GVK:           corev1.SchemeGroupVersion.WithKind("Namespace"),
		WatchType:     WatchTypeNamespace,
		ClusterScoped: true, // Namespaces are cluster-scoped
	})

	// Sort for deterministic output
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].GVK.Group != specs[j].GVK.Group {
			return specs[i].GVK.Group < specs[j].GVK.Group
		}
		if specs[i].GVK.Version != specs[j].GVK.Version {
			return specs[i].GVK.Version < specs[j].GVK.Version
		}
		return specs[i].GVK.Kind < specs[j].GVK.Kind
	})

	return specs, nil
}

// extractGVKsFromRendered parses rendered Helm output and extracts GVKs.
func extractGVKsFromRendered(rendered map[string]string, gvkSet map[schema.GroupVersionKind]struct{}) error {
	for name, content := range rendered {
		// Skip empty files and notes
		if content == "" || strings.HasSuffix(name, "NOTES.txt") {
			continue
		}

		// Parse multi-document YAML
		decoder := yaml.NewYAMLOrJSONDecoder(
			bufio.NewReader(strings.NewReader(content)),
			4096,
		)

		for {
			var obj map[string]interface{}
			if err := decoder.Decode(&obj); err != nil {
				if err == io.EOF {
					break
				}
				return fmt.Errorf("failed to decode YAML from %s: %w", name, err)
			}

			// Skip empty documents
			if obj == nil {
				continue
			}

			// Extract apiVersion and kind
			apiVersion, ok := obj["apiVersion"].(string)
			if !ok {
				continue
			}
			kind, ok := obj["kind"].(string)
			if !ok {
				continue
			}

			gvk := parseAPIVersionKind(apiVersion, kind)
			gvkSet[gvk] = struct{}{}
		}
	}
	return nil
}

// parseAPIVersionKind converts apiVersion and kind strings to a GroupVersionKind.
func parseAPIVersionKind(apiVersion, kind string) schema.GroupVersionKind {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		// Fallback for malformed apiVersion
		return schema.GroupVersionKind{
			Group:   "",
			Version: apiVersion,
			Kind:    kind,
		}
	}
	return gv.WithKind(kind)
}

// isClusterScoped returns true if the given GVK is a cluster-scoped resource.
// This is based on the known cluster-scoped kinds used in Istio charts.
func isClusterScoped(gvk schema.GroupVersionKind) bool {
	// Known cluster-scoped kinds from Istio charts
	clusterScopedKinds := map[string]bool{
		"Namespace":                      true,
		"ClusterRole":                    true,
		"ClusterRoleBinding":             true,
		"CustomResourceDefinition":       true,
		"MutatingWebhookConfiguration":   true,
		"ValidatingWebhookConfiguration": true,
	}

	return clusterScopedKinds[gvk.Kind]
}
