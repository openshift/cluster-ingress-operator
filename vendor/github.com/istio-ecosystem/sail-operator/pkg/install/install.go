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
	"context"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/config"
	"github.com/istio-ecosystem/sail-operator/pkg/helm"
	sharedreconcile "github.com/istio-ecosystem/sail-operator/pkg/reconcile"
	"github.com/istio-ecosystem/sail-operator/pkg/revision"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultNamespace  = "istio-system"
	defaultProfile    = "openshift"
	defaultHelmDriver = "secret"
	defaultRevision   = v1.DefaultRevision

	// reconcileKey is the single key used in the workqueue.
	// Since we always reconcile the entire installation, we use a single key
	// to coalesce multiple events into one reconciliation.
	reconcileKey = "reconcile"

	// defaultResyncPeriod is the default resync period for informers.
	defaultResyncPeriod = 30 * time.Minute
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

	// OwnerRef is an optional owner reference for garbage collection.
	// If set, installed resources will be owned by this resource.
	OwnerRef *metav1.OwnerReference

	// ManageCRDs controls whether the Library manages Istio CRDs.
	// When true (default), CRDs are classified by ownership and installed/updated
	// if we own them or none exist.
	// Set to false to skip CRD management entirely.
	ManageCRDs *bool

	// IncludeAllCRDs controls which CRDs are managed.
	// When true, all *.istio.io CRDs from the embedded FS are managed.
	// When false (default), only CRDs matching PILOT_INCLUDE_RESOURCES are managed.
	IncludeAllCRDs *bool
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
	// Infrastructure
	chartManager *helm.ChartManager
	resourceFS   fs.FS
	kubeConfig   *rest.Config
	cl           client.Client
	dynamicCl    dynamic.Interface
	crdManager   *CRDManager

	// Desired state (set by Apply, read by worker)
	mu          sync.RWMutex
	desiredOpts *Options // nil until first Apply()

	// Latest status (written by worker, read by Status())
	statusMu sync.RWMutex
	status   Status

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
		chartManager: helm.NewChartManager(kubeConfig, defaultHelmDriver),
		resourceFS:   resourceFS,
		kubeConfig:   kubeConfig,
		cl:           cl,
		dynamicCl:    dynamicCl,
		crdManager:   NewCRDManager(cl),
	}, nil
}

// Start begins the Library's internal reconciliation loop. It returns a
// notification channel that receives a signal every time the Library
// finishes a reconciliation (whether triggered by Apply, drift detection,
// or CRD changes).
//
// The channel is owned by the Library and closed when ctx is cancelled.
// Buffer of 1 with non-blocking write: if the consumer hasn't drained
// the previous notification, the new one is dropped (latest-wins).
//
// Start is non-blocking — it launches goroutines internally and returns
// immediately. The Library sits idle until the first Apply() call.
func (l *Library) Start(ctx context.Context) <-chan struct{} {
	notifyCh := make(chan struct{}, 1)
	l.workqueue = workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())

	// Start worker goroutine
	go l.run(ctx, notifyCh)

	return notifyCh
}

// Apply sets the desired installation state. If the options differ from
// the previously applied options, a reconciliation is enqueued. If they
// are the same, this is a no-op.
//
// Apply is non-blocking. The result of the reconciliation will be
// available via Status() after the notification channel signals.
func (l *Library) Apply(opts Options) {
	opts.applyDefaults()

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.desiredOpts != nil && optionsEqual(*l.desiredOpts, opts) {
		return // no change
	}

	copied := opts
	if copied.Values != nil {
		copied.Values = copied.Values.DeepCopy()
	}
	l.desiredOpts = &copied
	l.enqueue()
}

// Status returns the latest reconciliation result. This is safe to call
// from any goroutine. Returns a zero-value Status if no reconciliation
// has completed yet.
func (l *Library) Status() Status {
	l.statusMu.RLock()
	defer l.statusMu.RUnlock()
	return l.status
}

// Uninstall removes the istiod installation from the specified namespace.
// This is a synchronous operation for the shutdown path.
// If revision is empty, it defaults to "default".
func (l *Library) Uninstall(ctx context.Context, namespace, revision string) error {
	if namespace == "" {
		namespace = defaultNamespace
	}
	if revision == "" {
		revision = defaultRevision
	}

	reconcilerCfg := sharedreconcile.Config{
		ResourceFS:        l.resourceFS,
		Platform:          config.PlatformOpenShift,
		DefaultProfile:    defaultProfile,
		OperatorNamespace: namespace,
		ChartManager:      l.chartManager,
	}

	istiodReconciler := sharedreconcile.NewIstiodReconciler(reconcilerCfg, l.cl)
	if err := istiodReconciler.Uninstall(ctx, namespace, revision); err != nil {
		return fmt.Errorf("uninstallation failed: %w", err)
	}

	return nil
}

// run is the main loop. It starts informers (after first Apply), processes
// the workqueue, and shuts down when ctx is cancelled.
func (l *Library) run(ctx context.Context, notifyCh chan<- struct{}) {
	log := ctrllog.Log.WithName("install")
	defer close(notifyCh)
	defer l.workqueue.ShutDown()

	// Wait for the first Apply before setting up watches
	if !l.waitForDesiredState(ctx) {
		return // ctx cancelled
	}

	// Set up informers for drift detection
	stopCh := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(stopCh)
	}()

	l.setupInformers(stopCh)

	// Process work items until shutdown
	for {
		key, shutdown := l.workqueue.Get()
		if shutdown {
			return
		}

		log.Info("Reconciling")
		status := l.reconcile(ctx)
		l.setStatus(status)
		log.Info("Reconcile complete", "installed", status.Installed, "error", status.Error)

		// Non-blocking notify
		select {
		case notifyCh <- struct{}{}:
		default:
		}

		// Don't retry on error — the library is event-driven.
		// State changes trigger informer events, and Apply() enqueues explicitly.
		// Retrying permanent errors (CRD classification, bad config) just spins.
		l.workqueue.Forget(key)
		l.workqueue.Done(key)
	}
}

// waitForDesiredState blocks until Apply() has been called or ctx is cancelled.
func (l *Library) waitForDesiredState(ctx context.Context) bool {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			l.mu.RLock()
			hasOpts := l.desiredOpts != nil
			l.mu.RUnlock()
			if hasOpts {
				return true
			}
		}
	}
}

// setupInformers creates dynamic informers for Helm resources and CRDs
// based on the current desired options.
func (l *Library) setupInformers(stopCh <-chan struct{}) {
	log := ctrllog.Log.WithName("install")

	l.mu.RLock()
	opts := *l.desiredOpts
	l.mu.RUnlock()

	// Helm resource watches (drift detection)
	specs, err := l.getWatchSpecs(opts)
	if err != nil {
		log.Error(err, "Failed to compute watch specs; Helm drift detection disabled")
		specs = nil
	}

	// CRD watches (ownership changes, creation, deletion)
	if ptr.Deref(opts.ManageCRDs, true) {
		crdSpec := l.buildCRDWatchSpec(opts)
		if crdSpec != nil {
			specs = append(specs, *crdSpec)
		}
	}

	if len(specs) == 0 {
		log.Info("No watch specs; informers not started")
		return
	}

	log.Info("Setting up informers", "count", len(specs))

	namespacedFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		l.dynamicCl,
		defaultResyncPeriod,
		opts.Namespace,
		nil,
	)
	clusterScopedFactory := dynamicinformer.NewDynamicSharedInformerFactory(
		l.dynamicCl,
		defaultResyncPeriod,
	)

	for _, spec := range specs {
		var informer cache.SharedIndexInformer
		gvr := gvkToGVR(spec.GVK)

		if spec.ClusterScoped {
			informer = clusterScopedFactory.ForResource(gvr).Informer()
		} else {
			informer = namespacedFactory.ForResource(gvr).Informer()
		}

		handler := l.makeEventHandler(spec)
		if _, err := informer.AddEventHandler(handler); err != nil {
			log.Error(err, "Failed to add event handler", "gvk", spec.GVK)
			continue
		}
		log.V(1).Info("Watching", "gvk", spec.GVK, "type", spec.WatchType, "clusterScoped", spec.ClusterScoped)
	}

	namespacedFactory.Start(stopCh)
	clusterScopedFactory.Start(stopCh)
	namespacedFactory.WaitForCacheSync(stopCh)
	clusterScopedFactory.WaitForCacheSync(stopCh)
	log.Info("Informers synced and watching for drift")
}

// buildCRDWatchSpec computes a WatchSpec for Istio CRDs based on the current options.
// Returns nil if the target CRD set cannot be determined.
func (l *Library) buildCRDWatchSpec(opts Options) *WatchSpec {
	targetNames := l.crdManager.WatchTargets(opts.Values, ptr.Deref(opts.IncludeAllCRDs, false))
	if len(targetNames) == 0 {
		return nil
	}

	return &WatchSpec{
		GVK:           crdGVK,
		WatchType:     WatchTypeCRD,
		ClusterScoped: true,
		TargetNames:   targetNames,
	}
}

// reconcile does the actual work: CRDs + Helm.
func (l *Library) reconcile(ctx context.Context) Status {
	l.mu.RLock()
	opts := *l.desiredOpts
	l.mu.RUnlock()

	// Deep-copy Values so nothing in this function can mutate the
	// stored desiredOpts through shared pointers.
	if opts.Values != nil {
		opts.Values = opts.Values.DeepCopy()
	}

	status := Status{}

	// Default version from FS if not specified
	if opts.Version == "" {
		v, err := DefaultVersion(l.resourceFS)
		if err != nil {
			status.Error = fmt.Errorf("failed to determine default version: %w", err)
			return status
		}
		opts.Version = v
	}

	// Validate version
	if err := ValidateVersion(l.resourceFS, opts.Version); err != nil {
		status.Error = fmt.Errorf("invalid version %q: %w", opts.Version, err)
		return status
	}
	status.Version = opts.Version

	// Compute values
	values, err := revision.ComputeValues(
		opts.Values,
		opts.Namespace,
		opts.Version,
		config.PlatformOpenShift,
		defaultProfile,
		"",
		l.resourceFS,
		opts.Revision,
	)
	if err != nil {
		status.Error = fmt.Errorf("failed to compute values: %w", err)
		return status
	}

	// Manage CRDs
	if ptr.Deref(opts.ManageCRDs, true) {
		result := l.crdManager.Reconcile(ctx, values, ptr.Deref(opts.IncludeAllCRDs, false))
		status.CRDState = result.State
		status.CRDs = result.CRDs
		status.CRDMessage = result.Message
		if result.Error != nil {
			status.Error = result.Error
		}
	}

	// Helm install
	reconcilerCfg := sharedreconcile.Config{
		ResourceFS:        l.resourceFS,
		Platform:          config.PlatformOpenShift,
		DefaultProfile:    defaultProfile,
		OperatorNamespace: opts.Namespace,
		ChartManager:      l.chartManager,
	}

	istiodReconciler := sharedreconcile.NewIstiodReconciler(reconcilerCfg, l.cl)

	if err := istiodReconciler.Validate(ctx, opts.Version, opts.Namespace, values); err != nil {
		status.Error = combineErrors(status.Error, fmt.Errorf("validation failed: %w", err))
		return status
	}

	if err := istiodReconciler.Install(ctx, opts.Version, opts.Namespace, values, opts.Revision, opts.OwnerRef); err != nil {
		status.Error = combineErrors(status.Error, fmt.Errorf("installation failed: %w", err))
		return status
	}

	status.Installed = true
	return status
}

// setStatus atomically updates the stored status.
func (l *Library) setStatus(s Status) {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	l.status = s
}

// enqueue adds a reconciliation request to the workqueue.
func (l *Library) enqueue() {
	if l.workqueue != nil {
		l.workqueue.Add(reconcileKey)
	}
}

// caller returns file:line of the caller's caller (2 frames up).
func caller() string {
	_, file, line, ok := runtime.Caller(2)
	if !ok {
		return "unknown"
	}
	// Trim to just the filename for readability
	if idx := strings.LastIndex(file, "/"); idx >= 0 {
		file = file[idx+1:]
	}
	return fmt.Sprintf("%s:%d", file, line)
}

// makeEventHandler creates a cache.ResourceEventHandler that filters events
// and enqueues reconciliation requests.
func (l *Library) makeEventHandler(spec WatchSpec) cache.ResourceEventHandler {
	if spec.WatchType == WatchTypeCRD {
		return l.makeCRDEventHandler(spec)
	}
	return l.makeOwnedEventHandler(spec)
}

// makeOwnedEventHandler handles events for Helm-managed and namespace resources.
func (l *Library) makeOwnedEventHandler(spec WatchSpec) cache.ResourceEventHandler {
	log := ctrllog.Log.WithName("install").WithValues("gvk", spec.GVK, "watchType", spec.WatchType)
	return cache.ResourceEventHandlerFuncs{
		// Add events fire during initial cache sync — ignore them.
		AddFunc: func(obj interface{}) {},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldU, ok := oldObj.(*unstructured.Unstructured)
			if !ok {
				return
			}
			newU, ok := newObj.(*unstructured.Unstructured)
			if !ok {
				return
			}

			if !shouldReconcileOnUpdate(spec.GVK, oldU, newU) {
				log.V(2).Info("Skipping update (predicate)", "name", newU.GetName())
				return
			}

			if spec.WatchType == WatchTypeOwned && !l.isOwnedResource(newU) {
				log.V(1).Info("Skipping update (not owned)", "name", newU.GetName(), "labels", newU.GetLabels())
				return
			}

			log.Info("Drift detected (update)", "name", newU.GetName())
			l.enqueue()
		},
		DeleteFunc: func(obj interface{}) {
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}

			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return
			}

			if !shouldReconcileOnDelete(u) {
				return
			}

			if spec.WatchType == WatchTypeOwned && !l.isOwnedResource(u) {
				log.V(1).Info("Skipping delete (not owned)", "name", u.GetName())
				return
			}

			log.Info("Drift detected (delete)", "name", u.GetName())
			l.enqueue()
		},
	}
}

// makeCRDEventHandler handles events for CRD resources. Unlike owned resources,
// CRD events trigger on create (new CRD appeared), delete (CRD removed), and
// label/annotation changes (ownership transfer). Events are filtered by name
// to only watch target Istio CRDs.
func (l *Library) makeCRDEventHandler(spec WatchSpec) cache.ResourceEventHandler {
	log := ctrllog.Log.WithName("install").WithValues("watchType", spec.WatchType)
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
			if !isTargetCRD(u, spec.TargetNames) {
				return
			}
			log.Info("Drift detected (CRD added)", "name", u.GetName())
			l.enqueue()
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldU, ok := oldObj.(*unstructured.Unstructured)
			if !ok {
				return
			}
			newU, ok := newObj.(*unstructured.Unstructured)
			if !ok {
				return
			}
			if !isTargetCRD(newU, spec.TargetNames) {
				return
			}
			if !shouldReconcileCRDOnUpdate(oldU, newU) {
				return
			}
			log.Info("Drift detected (CRD updated)", "name", newU.GetName())
			l.enqueue()
		},
		DeleteFunc: func(obj interface{}) {
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
			if !isTargetCRD(u, spec.TargetNames) {
				return
			}
			log.Info("Drift detected (CRD deleted)", "name", u.GetName())
			l.enqueue()
		},
	}
}

// isTargetCRD checks if an unstructured CRD object's name is in the target set.
func isTargetCRD(obj *unstructured.Unstructured, targets map[string]struct{}) bool {
	if len(targets) == 0 {
		return false
	}
	_, ok := targets[obj.GetName()]
	return ok
}

// isOwnedResource checks if the resource is owned by our installation.
func (l *Library) isOwnedResource(obj *unstructured.Unstructured) bool {
	l.mu.RLock()
	opts := l.desiredOpts
	l.mu.RUnlock()

	if opts == nil {
		return false
	}

	// Check owner reference
	if opts.OwnerRef != nil {
		if hasMatchingOwnerRef(obj, opts.OwnerRef) {
			return true
		}
	}

	// Check Istio labels
	labels := obj.GetLabels()
	if labels == nil {
		return false
	}

	if rev, ok := labels["istio.io/rev"]; ok {
		expectedRev := opts.Revision
		if expectedRev == defaultRevision {
			expectedRev = "default"
		}
		return rev == expectedRev
	}

	if _, ok := labels["operator.istio.io/component"]; ok {
		return true
	}

	if managedBy, ok := labels["app.kubernetes.io/managed-by"]; ok {
		return managedBy == "Helm" || managedBy == "sail-operator"
	}

	return false
}

// hasMatchingOwnerRef checks if the object has an owner reference matching the expected one.
func hasMatchingOwnerRef(obj *unstructured.Unstructured, expected *metav1.OwnerReference) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.APIVersion == expected.APIVersion &&
			ref.Kind == expected.Kind &&
			ref.Name == expected.Name {
			if expected.UID == "" || ref.UID == expected.UID {
				return true
			}
		}
	}
	return false
}

// optionsEqual compares two Options for equality.
// Used by Apply() to skip no-op updates.
func optionsEqual(a, b Options) bool {
	if a.Namespace != b.Namespace ||
		a.Version != b.Version ||
		a.Revision != b.Revision ||
		ptr.Deref(a.ManageCRDs, true) != ptr.Deref(b.ManageCRDs, true) ||
		ptr.Deref(a.IncludeAllCRDs, false) != ptr.Deref(b.IncludeAllCRDs, false) {
		return false
	}

	// Compare OwnerRef
	if !reflect.DeepEqual(a.OwnerRef, b.OwnerRef) {
		return false
	}

	// Compare Values via map conversion for deep equality
	aMap := helm.FromValues(a.Values)
	bMap := helm.FromValues(b.Values)
	return reflect.DeepEqual(aMap, bMap)
}

// combineErrors returns the first non-nil error, or wraps both if both are non-nil.
func combineErrors(existing, newErr error) error {
	if existing == nil {
		return newErr
	}
	if newErr == nil {
		return existing
	}
	return fmt.Errorf("%w; %w", existing, newErr)
}

// gvkToGVR converts a GroupVersionKind to a GroupVersionResource.
func gvkToGVR(gvk schema.GroupVersionKind) schema.GroupVersionResource {
	plural, _ := meta.UnsafeGuessKindToResource(gvk)
	return plural
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
