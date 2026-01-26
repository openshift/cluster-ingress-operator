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

// Package install provides a simplified API for one-shot Istio installation.
// It is designed for embedding in other operators (like OpenShift Ingress)
// that need to install Istio without running a continuous controller.
//
// Example usage with embedded resources:
//
//	import "github.com/istio-ecosystem/sail-operator/resources"
//
//	installer, err := install.NewInstaller(install.Options{
//	    KubeConfig: kubeConfig,
//	    ResourceFS: resources.FS,
//	})
//	if err != nil {
//	    return err
//	}
//
//	// Simple installation with preset
//	err = installer.Install(ctx, install.PresetGatewayAPI)
//
// Example usage with filesystem path:
//
//	installer, err := install.NewInstaller(install.Options{
//	    KubeConfig: kubeConfig,
//	    ResourceFS: install.FromDirectory("/var/lib/sail-operator/resources"),
//	})
//
//	// Installation with overrides
//	err = installer.InstallWithOverrides(ctx, install.PresetGatewayAPI, &install.Overrides{
//	    Namespace: "custom-istio-system",
//	    Version:   "v1.28.2",
//	})
package install

import (
	"context"
	"fmt"
	"io/fs"
	"path"

	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/config"
	"github.com/istio-ecosystem/sail-operator/pkg/helm"
	"github.com/istio-ecosystem/sail-operator/pkg/istioversion"
	"github.com/istio-ecosystem/sail-operator/pkg/revision"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Chart name constants
const (
	istiodChartName  = "istiod"
	baseChartName    = "base"
	cniChartName     = "cni"
	ztunnelChartName = "ztunnel"
)

// Installer provides one-shot Istio installation capabilities.
// It does not run as a controller - it performs install/uninstall
// operations and then returns.
type Installer struct {
	chartManager      *helm.ChartManager
	resourceFS        fs.FS
	platform          config.Platform
	defaultProfile    string
	operatorNamespace string
	presets           map[PresetName]Preset
}

// NewInstaller creates a new Installer with the specified options.
// The Options must include a valid KubeConfig and ResourceFS.
func NewInstaller(opts Options) (*Installer, error) {
	if opts.KubeConfig == nil {
		return nil, fmt.Errorf("KubeConfig is required")
	}
	if opts.ResourceFS == nil {
		return nil, fmt.Errorf("ResourceFS is required")
	}

	opts.applyDefaults()

	// Auto-detect platform if not specified
	platform := opts.Platform
	if platform == "" {
		detectedPlatform, err := config.DetectPlatform(opts.KubeConfig)
		if err != nil {
			// Default to OpenShift if detection fails, since this module
			// is primarily designed for OpenShift Ingress
			platform = config.PlatformOpenShift
		} else {
			platform = detectedPlatform
		}
	}

	// Load presets from embedded YAML files
	presets, err := loadPresets()
	if err != nil {
		return nil, fmt.Errorf("failed to load presets: %w", err)
	}

	return &Installer{
		chartManager:      helm.NewChartManager(opts.KubeConfig, opts.HelmDriver),
		resourceFS:        opts.ResourceFS,
		platform:          platform,
		defaultProfile:    opts.DefaultProfile,
		operatorNamespace: opts.OperatorNamespace,
		presets:           presets,
	}, nil
}

// Install installs Istio using the specified preset with default settings.
// This is the simplest way to install Istio - just specify a preset name.
func (i *Installer) Install(ctx context.Context, presetName PresetName) error {
	return i.InstallWithOverrides(ctx, presetName, nil)
}

// InstallWithOverrides installs Istio using the specified preset with optional overrides.
// Overrides allow customizing the namespace, version, and Helm values.
func (i *Installer) InstallWithOverrides(ctx context.Context, presetName PresetName, overrides *Overrides) error {
	preset, ok := i.presets[presetName]
	if !ok {
		return fmt.Errorf("unknown preset: %s (available: %v)", presetName, GetAvailablePresetNames())
	}

	// Apply defaults to overrides
	if overrides == nil {
		overrides = &Overrides{}
	}
	overrides.applyDefaults()

	// Determine version
	version := overrides.Version
	if version == "" {
		version = istioversion.Default
	}

	// Resolve version alias to actual version
	resolvedVersion, err := istioversion.Resolve(version)
	if err != nil {
		return fmt.Errorf("invalid version %q: %w", version, err)
	}

	// Merge preset values with override values
	values := mergeValues(preset.Values, overrides.Values)

	// Compute final values (applies profiles, digests, platform settings, and overrides)
	revisionName := v1.DefaultRevision
	finalValues, err := revision.ComputeValues(
		values,
		overrides.Namespace,
		resolvedVersion,
		i.platform,
		i.defaultProfile,
		preset.BaseProfile,
		i.resourceFS,
		revisionName,
	)
	if err != nil {
		return fmt.Errorf("failed to compute values: %w", err)
	}

	// Install components based on preset configuration
	if preset.Components.Istiod {
		if err := i.installIstiod(ctx, resolvedVersion, overrides.Namespace, revisionName, finalValues); err != nil {
			return fmt.Errorf("failed to install istiod: %w", err)
		}
	}

	if preset.Components.CNI {
		if err := i.installCNI(ctx, resolvedVersion, overrides.Namespace, finalValues); err != nil {
			return fmt.Errorf("failed to install CNI: %w", err)
		}
	}

	if preset.Components.ZTunnel {
		if err := i.installZTunnel(ctx, resolvedVersion, overrides.Namespace, finalValues); err != nil {
			return fmt.Errorf("failed to install ZTunnel: %w", err)
		}
	}

	return nil
}

// installIstiod installs the istiod and base charts
func (i *Installer) installIstiod(ctx context.Context, version, namespace, revisionName string, values *v1.Values) error {
	helmValues := helm.FromValues(values)

	// Install base chart (CRDs) for default revision
	if revisionName == v1.DefaultRevision {
		baseChartPath := path.Join(version, "charts", baseChartName)
		baseReleaseName := revisionName + "-" + baseChartName
		_, err := i.chartManager.UpgradeOrInstallChart(
			ctx,
			i.resourceFS,
			baseChartPath,
			helmValues,
			i.operatorNamespace,
			baseReleaseName,
			nil, // No owner reference for one-shot install
		)
		if err != nil {
			return fmt.Errorf("failed to install base chart: %w", err)
		}
	}

	// Install istiod chart
	istiodChartPath := path.Join(version, "charts", istiodChartName)
	istiodReleaseName := revisionName + "-" + istiodChartName
	_, err := i.chartManager.UpgradeOrInstallChart(
		ctx,
		i.resourceFS,
		istiodChartPath,
		helmValues,
		namespace,
		istiodReleaseName,
		nil, // No owner reference for one-shot install
	)
	if err != nil {
		return fmt.Errorf("failed to install istiod chart: %w", err)
	}

	return nil
}

// installCNI installs the Istio CNI chart
func (i *Installer) installCNI(ctx context.Context, version, namespace string, values *v1.Values) error {
	cniChartPath := path.Join(version, "charts", cniChartName)
	cniReleaseName := "istio-cni"

	_, err := i.chartManager.UpgradeOrInstallChart(
		ctx,
		i.resourceFS,
		cniChartPath,
		helm.FromValues(values),
		namespace,
		cniReleaseName,
		nil,
	)
	return err
}

// installZTunnel installs the ZTunnel chart
func (i *Installer) installZTunnel(ctx context.Context, version, namespace string, values *v1.Values) error {
	ztunnelChartPath := path.Join(version, "charts", ztunnelChartName)
	ztunnelReleaseName := "ztunnel"

	_, err := i.chartManager.UpgradeOrInstallChart(
		ctx,
		i.resourceFS,
		ztunnelChartPath,
		helm.FromValues(values),
		namespace,
		ztunnelReleaseName,
		nil,
	)
	return err
}

// Uninstall removes the Istio installation for a preset from the default namespace.
func (i *Installer) Uninstall(ctx context.Context, presetName PresetName) error {
	return i.UninstallFromNamespace(ctx, presetName, "istio-system")
}

// UninstallFromNamespace removes the Istio installation for a preset from a specific namespace.
func (i *Installer) UninstallFromNamespace(ctx context.Context, presetName PresetName, namespace string) error {
	preset, ok := i.presets[presetName]
	if !ok {
		return fmt.Errorf("unknown preset: %s", presetName)
	}

	var errs []error

	// Uninstall in reverse order of installation
	if preset.Components.ZTunnel {
		if _, err := i.chartManager.UninstallChart(ctx, "ztunnel", namespace); err != nil {
			errs = append(errs, fmt.Errorf("failed to uninstall ztunnel: %w", err))
		}
	}

	if preset.Components.CNI {
		if _, err := i.chartManager.UninstallChart(ctx, "istio-cni", namespace); err != nil {
			errs = append(errs, fmt.Errorf("failed to uninstall CNI: %w", err))
		}
	}

	if preset.Components.Istiod {
		revisionName := v1.DefaultRevision
		istiodReleaseName := revisionName + "-" + istiodChartName
		if _, err := i.chartManager.UninstallChart(ctx, istiodReleaseName, namespace); err != nil {
			errs = append(errs, fmt.Errorf("failed to uninstall istiod: %w", err))
		}

		if revisionName == v1.DefaultRevision {
			baseReleaseName := revisionName + "-" + baseChartName
			if _, err := i.chartManager.UninstallChart(ctx, baseReleaseName, i.operatorNamespace); err != nil {
				errs = append(errs, fmt.Errorf("failed to uninstall base: %w", err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("uninstall errors: %v", errs)
	}
	return nil
}

// ListPresets returns all available presets with their details.
func (i *Installer) ListPresets() []Preset {
	presets := make([]Preset, 0, len(i.presets))
	for _, p := range i.presets {
		presets = append(presets, p)
	}
	return presets
}

// GetPreset returns details about a specific preset.
func (i *Installer) GetPreset(name PresetName) (Preset, bool) {
	p, ok := i.presets[name]
	return p, ok
}

// GetSupportedVersions returns all supported Istio versions.
func GetSupportedVersions() []istioversion.VersionInfo {
	return istioversion.List
}

// GetDefaultVersion returns the default Istio version.
func GetDefaultVersion() string {
	return istioversion.Default
}

// mergeValues merges override values on top of base values.
// Override values take precedence.
func mergeValues(base, override *v1.Values) *v1.Values {
	if base == nil && override == nil {
		return &v1.Values{}
	}
	if base == nil {
		return override.DeepCopy()
	}
	if override == nil {
		return base.DeepCopy()
	}

	// Start with a copy of base
	result := base.DeepCopy()

	// Merge override values using Helm's merge functionality
	baseMap := helm.FromValues(result)
	overrideMap := helm.FromValues(override)

	for key, value := range overrideMap {
		baseMap[key] = value
	}

	merged, err := helm.ToValues(baseMap, &v1.Values{})
	if err != nil {
		// If merge fails, return override (it takes precedence)
		return override.DeepCopy()
	}

	return merged
}

// InstallWithOwnerReference installs Istio with an owner reference for garbage collection.
// This is useful when the installation should be tied to a parent resource.
func (i *Installer) InstallWithOwnerReference(
	ctx context.Context,
	presetName PresetName,
	overrides *Overrides,
	ownerRef *metav1.OwnerReference,
) error {
	preset, ok := i.presets[presetName]
	if !ok {
		return fmt.Errorf("unknown preset: %s", presetName)
	}

	if overrides == nil {
		overrides = &Overrides{}
	}
	overrides.applyDefaults()

	version := overrides.Version
	if version == "" {
		version = istioversion.Default
	}

	resolvedVersion, err := istioversion.Resolve(version)
	if err != nil {
		return fmt.Errorf("invalid version %q: %w", version, err)
	}

	values := mergeValues(preset.Values, overrides.Values)
	revisionName := v1.DefaultRevision

	finalValues, err := revision.ComputeValues(
		values,
		overrides.Namespace,
		resolvedVersion,
		i.platform,
		i.defaultProfile,
		preset.BaseProfile,
		i.resourceFS,
		revisionName,
	)
	if err != nil {
		return fmt.Errorf("failed to compute values: %w", err)
	}

	helmValues := helm.FromValues(finalValues)

	// Install components with owner reference
	if preset.Components.Istiod {
		if revisionName == v1.DefaultRevision {
			baseChartPath := path.Join(resolvedVersion, "charts", baseChartName)
			baseReleaseName := revisionName + "-" + baseChartName
			if _, err := i.chartManager.UpgradeOrInstallChart(ctx, i.resourceFS, baseChartPath, helmValues,
				i.operatorNamespace, baseReleaseName, ownerRef); err != nil {
				return fmt.Errorf("failed to install base chart: %w", err)
			}
		}

		istiodChartPath := path.Join(resolvedVersion, "charts", istiodChartName)
		istiodReleaseName := revisionName + "-" + istiodChartName
		if _, err := i.chartManager.UpgradeOrInstallChart(ctx, i.resourceFS, istiodChartPath, helmValues,
			overrides.Namespace, istiodReleaseName, ownerRef); err != nil {
			return fmt.Errorf("failed to install istiod chart: %w", err)
		}
	}

	if preset.Components.CNI {
		cniChartPath := path.Join(resolvedVersion, "charts", cniChartName)
		if _, err := i.chartManager.UpgradeOrInstallChart(ctx, i.resourceFS, cniChartPath, helmValues,
			overrides.Namespace, "istio-cni", ownerRef); err != nil {
			return fmt.Errorf("failed to install CNI chart: %w", err)
		}
	}

	if preset.Components.ZTunnel {
		ztunnelChartPath := path.Join(resolvedVersion, "charts", ztunnelChartName)
		if _, err := i.chartManager.UpgradeOrInstallChart(ctx, i.resourceFS, ztunnelChartPath, helmValues,
			overrides.Namespace, "ztunnel", ownerRef); err != nil {
			return fmt.Errorf("failed to install ztunnel chart: %w", err)
		}
	}

	return nil
}
