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
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/config"
	"github.com/istio-ecosystem/sail-operator/pkg/constants"
	"github.com/istio-ecosystem/sail-operator/pkg/helm"
	sharedreconcile "github.com/istio-ecosystem/sail-operator/pkg/reconcile"
	"github.com/istio-ecosystem/sail-operator/pkg/revision"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// watchType indicates how events for a resource type should be handled.
type watchType int

const (
	// watchTypeOwned indicates resources owned by the installation.
	// Use owner reference filtering to only watch resources created by the installer.
	watchTypeOwned watchType = iota

	// watchTypeNamespace indicates namespace resources should be watched.
	// Used to detect when the target namespace is created/deleted.
	watchTypeNamespace

	// watchTypeCRD indicates CRD resources that should be watched for ownership changes.
	// Events are filtered by TargetNames and trigger on label/annotation changes,
	// creation, or deletion — allowing the Library to re-classify CRD ownership.
	watchTypeCRD
)

// String returns the string representation of the watchType.
func (w watchType) String() string {
	switch w {
	case watchTypeOwned:
		return "Owned"
	case watchTypeNamespace:
		return "Namespace"
	case watchTypeCRD:
		return "CRD"
	default:
		return fmt.Sprintf("Unknown(%d)", w)
	}
}

// watchSpec describes a resource type that should be watched for drift detection.
type watchSpec struct {
	// GVK is the GroupVersionKind of the resource to watch.
	GVK schema.GroupVersionKind

	// watchType indicates how events should be handled.
	watchType watchType

	// ClusterScoped indicates if this is a cluster-scoped resource (vs namespaced).
	// Cluster-scoped resources require a different informer factory setup.
	ClusterScoped bool

	// TargetNames is an optional set of resource names to filter on.
	// Only used with watchTypeCRD to limit events to specific CRDs.
	// When nil or empty, all resources of this GVK are watched.
	TargetNames map[string]struct{}
}

// installer owns the worker dependencies and all install/watch logic.
// It is separated from Library so that reconcile, uninstall, and watch-spec
// computation can be tested and reasoned about without concurrency machinery.
type installer struct {
	resourceFS   fs.FS
	chartManager *helm.ChartManager
	cl           client.Client
	crdManager   *crdManager
}

// resolveValues resolves the version and computes Helm values from Options.
// This eliminates the duplication between reconcile and getWatchSpecs.
func (inst *installer) resolveValues(opts Options) (string, *v1.Values, error) {
	opts.applyDefaults()

	// Default version from FS if not specified
	if opts.Version == "" {
		v, err := DefaultVersion(inst.resourceFS)
		if err != nil {
			return "", nil, fmt.Errorf("failed to determine default version: %w", err)
		}
		opts.Version = v
	}

	// Validate version
	if err := ValidateVersion(inst.resourceFS, opts.Version); err != nil {
		return "", nil, fmt.Errorf("invalid version %q: %w", opts.Version, err)
	}

	// Compute values
	values, err := revision.ComputeValues(
		opts.Values,
		opts.Namespace,
		opts.Version,
		config.PlatformOpenShift,
		defaultProfile,
		"",
		inst.resourceFS,
		opts.Revision,
	)
	if err != nil {
		return "", nil, fmt.Errorf("failed to compute values: %w", err)
	}

	return opts.Version, values, nil
}

// reconcile does the actual work: CRDs + Helm. Takes opts as an argument
// so the caller reads opts under lock and passes them in — no lock access here.
func (inst *installer) reconcile(ctx context.Context, opts Options) Status {
	// Deep-copy Values so nothing in this function can mutate the
	// stored desiredOpts through shared pointers.
	if opts.Values != nil {
		opts.Values = opts.Values.DeepCopy()
	}

	status := Status{}

	resolvedVersion, values, err := inst.resolveValues(opts)
	if err != nil {
		status.Error = err
		return status
	}
	status.Version = resolvedVersion

	// Manage CRDs
	if ptr.Deref(opts.ManageCRDs, true) {
		result := inst.crdManager.Reconcile(ctx, values, ptr.Deref(opts.IncludeAllCRDs, false), opts.OverwriteOLMManagedCRD)
		status.CRDState = result.State
		status.CRDs = result.CRDs
		status.CRDMessage = result.Message
		if result.Error != nil {
			status.Error = result.Error
		}
	}

	// Helm install
	reconcilerCfg := sharedreconcile.Config{
		ResourceFS:        inst.resourceFS,
		Platform:          config.PlatformOpenShift,
		DefaultProfile:    defaultProfile,
		OperatorNamespace: opts.Namespace,
		ChartManager:      inst.chartManager,
	}

	istiodReconciler := sharedreconcile.NewIstiodReconciler(reconcilerCfg, inst.cl)

	if err := istiodReconciler.Validate(ctx, resolvedVersion, opts.Namespace, values); err != nil {
		status.Error = errors.Join(status.Error, fmt.Errorf("validation failed: %w", err))
		return status
	}

	if err := istiodReconciler.Install(ctx, resolvedVersion, opts.Namespace, values, opts.Revision, nil); err != nil {
		status.Error = errors.Join(status.Error, fmt.Errorf("installation failed: %w", err))
		return status
	}

	status.Installed = true
	return status
}

// uninstall removes the istiod Helm release.
func (inst *installer) uninstall(ctx context.Context, namespace, revision string) error {
	reconcilerCfg := sharedreconcile.Config{
		ResourceFS:        inst.resourceFS,
		Platform:          config.PlatformOpenShift,
		DefaultProfile:    defaultProfile,
		OperatorNamespace: namespace,
		ChartManager:      inst.chartManager,
	}

	istiodReconciler := sharedreconcile.NewIstiodReconciler(reconcilerCfg, inst.cl)
	if err := istiodReconciler.Uninstall(ctx, namespace, revision); err != nil {
		return fmt.Errorf("uninstallation failed: %w", err)
	}
	return nil
}

// getWatchSpecs renders the istiod Helm charts and extracts the GVKs
// of all resources that would be created. Used internally by the Library
// to set up informers for drift detection.
//
// The returned specs include:
//   - All resource types from the base and istiod charts (watchTypeOwned)
//   - Namespace (watchTypeNamespace) for namespace existence checks
func (inst *installer) getWatchSpecs(opts Options) ([]watchSpec, error) {
	resolvedVersion, values, err := inst.resolveValues(opts)
	if err != nil {
		return nil, err
	}
	helmValues := helm.FromValues(values)

	// Collect GVKs from both charts
	gvkSet := make(map[schema.GroupVersionKind]struct{})

	// Render base chart (for default revision)
	if opts.Revision == defaultRevision {
		baseChartPath := sharedreconcile.GetChartPath(resolvedVersion, constants.BaseChartName)
		rendered, err := helm.RenderChart(inst.resourceFS, baseChartPath, helmValues, opts.Namespace, "base")
		if err != nil {
			return nil, fmt.Errorf("failed to render base chart: %w", err)
		}
		if err := extractGVKsFromRendered(rendered, gvkSet); err != nil {
			return nil, fmt.Errorf("failed to extract GVKs from base chart: %w", err)
		}
	}

	// Render istiod chart
	istiodChartPath := sharedreconcile.GetChartPath(resolvedVersion, constants.IstiodChartName)
	rendered, err := helm.RenderChart(inst.resourceFS, istiodChartPath, helmValues, opts.Namespace, "istiod")
	if err != nil {
		return nil, fmt.Errorf("failed to render istiod chart: %w", err)
	}
	if err := extractGVKsFromRendered(rendered, gvkSet); err != nil {
		return nil, fmt.Errorf("failed to extract GVKs from istiod chart: %w", err)
	}

	// Convert to specs
	specs := make([]watchSpec, 0, len(gvkSet)+1)
	for gvk := range gvkSet {
		specs = append(specs, watchSpec{
			GVK:           gvk,
			watchType:     watchTypeOwned,
			ClusterScoped: isClusterScoped(gvk),
		})
	}

	// Add Namespace watch
	specs = append(specs, watchSpec{
		GVK:           corev1.SchemeGroupVersion.WithKind("Namespace"),
		watchType:     watchTypeNamespace,
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

			gvk := apiVersionKindToGVK(apiVersion, kind)
			gvkSet[gvk] = struct{}{}
		}
	}
	return nil
}

// apiVersionKindToGVK converts apiVersion and kind strings to a GroupVersionKind.
func apiVersionKindToGVK(apiVersion, kind string) schema.GroupVersionKind {
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

// clusterScopedKinds is the set of known cluster-scoped kinds used in Istio charts.
var clusterScopedKinds = map[string]bool{
	"Namespace":                      true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"CustomResourceDefinition":       true,
	"MutatingWebhookConfiguration":   true,
	"ValidatingWebhookConfiguration": true,
}

// isClusterScoped returns true if the given GVK is a cluster-scoped resource.
func isClusterScoped(gvk schema.GroupVersionKind) bool {
	return clusterScopedKinds[gvk.Kind]
}
