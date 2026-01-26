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
	"io/fs"
	"os"

	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/config"
	"k8s.io/client-go/rest"
)

// Options configures the Installer
type Options struct {
	// KubeConfig is the Kubernetes client configuration
	KubeConfig *rest.Config

	// ResourceFS provides access to Helm charts and profiles.
	// Can be an embed.FS (for embedded resources), os.DirFS (for filesystem path),
	// or any other fs.FS implementation.
	//
	// Example with embedded resources:
	//   import "github.com/istio-ecosystem/sail-operator/resources"
	//   ResourceFS: resources.FS
	//
	// Example with filesystem path:
	//   ResourceFS: os.DirFS("/var/lib/sail-operator/resources")
	ResourceFS fs.FS

	// HelmDriver specifies the Helm storage driver.
	// One of: "secret", "configmap", or "memory".
	// Defaults to "secret" if not specified.
	HelmDriver string

	// Platform specifies the Kubernetes platform (e.g., OpenShift, vanilla Kubernetes).
	// Defaults to auto-detection if not specified.
	Platform config.Platform

	// DefaultProfile is the base profile applied before user-selected profiles.
	// For OpenShift, this is typically "openshift".
	DefaultProfile string

	// OperatorNamespace is the namespace where the operator components are installed.
	// Used for installing the base chart (CRDs).
	// Defaults to "istio-system" if not specified.
	OperatorNamespace string
}

// Overrides allows customizing specific aspects of a preset installation
type Overrides struct {
	// Namespace overrides the default namespace for the Istio control plane.
	// Defaults to "istio-system" if not specified.
	Namespace string

	// Version overrides the Istio version to install.
	// Defaults to the latest supported version if not specified.
	Version string

	// Values are merged on top of the preset's values.
	// These take precedence over preset defaults.
	Values *v1.Values
}

// applyDefaults fills in default values for Options
func (o *Options) applyDefaults() {
	if o.HelmDriver == "" {
		o.HelmDriver = "secret"
	}
	if o.DefaultProfile == "" {
		if o.Platform == config.PlatformOpenShift {
			o.DefaultProfile = "openshift"
		} else {
			o.DefaultProfile = "default"
		}
	}
	if o.OperatorNamespace == "" {
		o.OperatorNamespace = "istio-system"
	}
}

// applyDefaults fills in default values for Overrides
func (o *Overrides) applyDefaults() {
	if o.Namespace == "" {
		o.Namespace = "istio-system"
	}
}

// FromDirectory creates an fs.FS from a filesystem directory path.
// This is a convenience function for consumers who want to load resources
// from the filesystem instead of using embedded resources.
//
// Example:
//
//	installer, _ := install.NewInstaller(install.Options{
//	    ResourceFS: install.FromDirectory("/var/lib/sail-operator/resources"),
//	})
func FromDirectory(path string) fs.FS {
	return os.DirFS(path)
}
