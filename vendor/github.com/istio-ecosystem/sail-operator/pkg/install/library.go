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

// Package install provides a library for managing istiod installations.
// It is designed for embedding in other operators (like OpenShift Ingress)
// that need to install and maintain Istio without running a separate operator.
//
// The Library runs as an independent actor: the consumer sends desired state
// via Apply(), and reads the result via Status(). A notification channel
// returned by Start() signals when the Library has reconciled.
//
// Usage:
//
//	lib, _ := install.New(kubeConfig, resourceFS)
//	notifyCh := lib.Start(ctx)
//
//	// In controller reconcile:
//	lib.Apply(install.Options{Values: values, Namespace: "openshift-ingress"})
//	status := lib.Status()
//	// update GatewayClass conditions from status
//
//	// In a goroutine or source.Channel watch:
//	for range notifyCh {
//	    status := lib.Status()
//	    // ...
//	}
package install

import (
	"fmt"
	"io/fs"
	"os"
	"sync"

	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/helm"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultNamespace      = "istio-system"
	defaultProfile        = "openshift"
	defaultHelmDriver     = "secret"
	defaultRevision       = v1.DefaultRevision
	defaultManagedByValue = "sail-library"
)

// Status represents the result of a reconciliation, covering both
// CRD management and Helm installation.
type Status struct {
	// CRDState is the aggregate ownership state of the target Istio CRDs.
	CRDState CRDManagementState

	// CRDMessage is a human-readable description of the CRD state.
	CRDMessage string

	// CRDs contains per-CRD detail (name, ownership, found on cluster).
	CRDs []CRDInfo

	// Installed is true if the Helm install/upgrade completed successfully.
	Installed bool

	// Version is the resolved Istio version (set even if Installed is false).
	Version string

	// Error is non-nil if something went wrong during CRD management or Helm installation.
	// CRD ownership problems (UnknownManagement, MixedOwnership) set this but do not
	// prevent Helm installation from being attempted.
	Error error
}

// String returns a human-readable summary of the status.
func (s Status) String() string {
	state := "not installed"
	if s.Installed {
		state = "installed"
	}

	ver := s.Version
	if ver == "" {
		ver = "unknown"
	}

	msg := fmt.Sprintf("%s version=%s crds=%s", state, ver, s.CRDState)
	if s.CRDMessage != "" {
		msg += fmt.Sprintf(" (%s)", s.CRDMessage)
	}
	if len(s.CRDs) > 0 {
		msg += " ["
		for i, crd := range s.CRDs {
			if i > 0 {
				msg += ", "
			}
			if crd.Found {
				msg += fmt.Sprintf("%s:%s", crd.Name, crd.State)
			} else {
				msg += fmt.Sprintf("%s:missing", crd.Name)
			}
		}
		msg += "]"
	}
	if s.Error != nil {
		msg += fmt.Sprintf(" error=%v", s.Error)
	}
	return msg
}

// Options for installing istiod.
type Options struct {
	// Namespace is the target namespace for installation.
	// Defaults to "istio-system" if not specified.
	Namespace string

	// Version is the Istio version to install.
	// Defaults to the latest supported version if not specified.
	Version string

	// Revision is the Istio revision name.
	// Defaults to "default" if not specified.
	Revision string

	// Values are Helm value overrides.
	// Use GatewayAPIDefaults() to get pre-configured values for Gateway API mode,
	// then modify as needed before passing here.
	Values *v1.Values

	// ManageCRDs controls whether the Library manages Istio CRDs.
	// When true (default), CRDs are classified by ownership and installed/updated
	// if we own them or none exist.
	// Set to false to skip CRD management entirely.
	ManageCRDs *bool

	// IncludeAllCRDs controls which CRDs are managed.
	// When true, all *.istio.io CRDs from the embedded FS are managed.
	// When false (default), only CRDs matching PILOT_INCLUDE_RESOURCES are managed.
	IncludeAllCRDs *bool

	// OverwriteOLMManagedCRD is called when a CRD is detected with OLM ownership labels.
	// If provided and returns true, the CRD is overwritten with CIO labels and adopted.
	// If nil or returns false, OLM-labeled CRDs are left alone.
	OverwriteOLMManagedCRD OverwriteOLMManagedCRDFunc
}

// applyDefaults fills in default values for Options.
// Version is not defaulted here — it requires access to the resource FS,
// so it is resolved during reconciliation via DefaultVersion().
func (o *Options) applyDefaults() {
	o.Version = NormalizeVersion(o.Version)
	if o.Namespace == "" {
		o.Namespace = defaultNamespace
	}
	if o.Revision == "" {
		o.Revision = defaultRevision
	}
	if o.ManageCRDs == nil {
		o.ManageCRDs = ptr.To(true)
	}
	if o.IncludeAllCRDs == nil {
		o.IncludeAllCRDs = ptr.To(false)
	}
}

// Library manages the lifecycle of an istiod installation. It runs as an
// independent actor: the consumer sends desired state via Apply() and reads
// the result via Status(). Start() returns a notification channel that
// signals when the Library has reconciled (drift repair, CRD change, or
// a new Apply).
type Library struct {
	// Core install/uninstall logic (no concurrency concerns)
	inst *installer

	// managedByValue is the value of the "managed-by" label set on all
	// Helm-managed resources. Used both by the post-renderer (write) and
	// by informer predicates (read) for ownership filtering.
	managedByValue string

	// Infrastructure needed only by Library (informers, dynamic client)
	kubeConfig *rest.Config
	dynamicCl  dynamic.Interface

	// Lifecycle serialization (Apply and Uninstall hold this)
	lifecycleMu sync.Mutex

	// Desired state (set by Apply, read by worker)
	mu          sync.RWMutex
	desiredOpts *Options // nil until first Apply(); nil again after Uninstall()

	// Informer lifecycle (per install cycle)
	informerStop   chan struct{} // closed to stop current informer cycle
	processingDone chan struct{} // closed when processWorkQueue exits

	// Latest status (written by worker, read by Status())
	statusMu sync.RWMutex
	status   Status

	// Signal channel: Apply() sends on it to wake waitForDesiredState().
	// Buffered 1, non-blocking write — if the signal is already pending,
	// the new one is dropped.
	applySignal chan struct{}

	// Internal workqueue
	workqueue workqueue.TypedRateLimitingInterface[string]
}

// New creates a new Library.
//
// Parameters:
//   - kubeConfig: Kubernetes client configuration (required)
//   - resourceFS: Filesystem containing Helm charts and profiles (required).
//     Use resources.FS for embedded resources or FromDirectory() for a filesystem path.
func New(kubeConfig *rest.Config, resourceFS fs.FS) (*Library, error) {
	if kubeConfig == nil {
		return nil, fmt.Errorf("kubeConfig is required")
	}
	if resourceFS == nil {
		return nil, fmt.Errorf("resourceFS is required")
	}

	cl, err := client.New(kubeConfig, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	dynamicCl, err := dynamic.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	// Populate default image refs from the provided FS (no-op if already set by config.Read)
	if err := SetImageDefaults(resourceFS, defaultRegistry, ImageNames{
		Istiod:  defaultIstiodImage,
		Proxy:   defaultProxyImage,
		CNI:     defaultCNIImage,
		ZTunnel: defaultZTunnelImage,
	}); err != nil {
		return nil, fmt.Errorf("failed to set image defaults: %w", err)
	}

	return &Library{
		inst: &installer{
			resourceFS:   resourceFS,
			chartManager: helm.NewChartManager(kubeConfig, defaultHelmDriver, helm.WithManagedByValue(defaultManagedByValue)),
			cl:           cl,
			crdManager:   newCRDManager(cl),
		},
		managedByValue: defaultManagedByValue,
		kubeConfig:     kubeConfig,
		dynamicCl:      dynamicCl,
		applySignal:    make(chan struct{}, 1),
	}, nil
}

// FromDirectory creates an fs.FS from a filesystem directory path.
// This is a convenience function for consumers who want to load resources
// from the filesystem instead of using embedded resources.
//
// Example:
//
//	lib, _ := install.New(kubeConfig, install.FromDirectory("/var/lib/sail-operator/resources"))
func FromDirectory(path string) fs.FS {
	return os.DirFS(path)
}
