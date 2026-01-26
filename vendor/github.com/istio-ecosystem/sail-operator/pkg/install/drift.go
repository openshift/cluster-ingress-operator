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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	// reconcileKey is the single key used in the workqueue.
	// Since we always reconcile the entire installation, we use a single key
	// to coalesce multiple events into one reconciliation.
	reconcileKey = "reconcile"

	// defaultResyncPeriod is the default resync period for informers.
	defaultResyncPeriod = 30 * time.Minute
)

// DriftReconciler watches resources created by Install() and automatically
// calls Install() when drift is detected to restore the desired state.
type DriftReconciler struct {
	installer *Installer
	client    dynamic.Interface

	// mu protects running state
	mu      sync.Mutex
	running bool
	stopCh  chan struct{}

	// workqueue with rate limiting to coalesce rapid events
	workqueue workqueue.TypedRateLimitingInterface[string]

	// opts stores the installation options for re-installation
	opts Options
}

// newDriftReconciler creates a new DriftReconciler with the given options.
// This is called internally by Install() to ensure opts consistency.
func newDriftReconciler(installer *Installer, opts Options) (*DriftReconciler, error) {
	client, err := dynamic.NewForConfig(installer.kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &DriftReconciler{
		installer: installer,
		client:    client,
		opts:      opts,
	}, nil
}

// Start begins watching resources and automatically reconciling when drift is detected.
// This method blocks until the context is cancelled or Stop() is called.
//
// The reconciler uses the Options provided during Install() to know what to watch
// and how to restore resources when drift is detected.
//
// The reconciler:
//   - Gets watch specs from the installer to know which resources to watch
//   - Sets up dynamic informers for each resource type
//   - Applies predicates to filter out irrelevant events
//   - Queues reconciliation requests with rate limiting
//   - Calls Install() to restore drifted resources
func (d *DriftReconciler) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return fmt.Errorf("drift reconciler is already running")
	}
	d.running = true
	d.stopCh = make(chan struct{})
	d.workqueue = workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	d.mu.Unlock()

	// Get watch specs to know what resources to watch
	specs, err := d.installer.GetWatchSpecs(d.opts)
	if err != nil {
		d.cleanup()
		return fmt.Errorf("failed to get watch specs: %w", err)
	}

	// Create informer factories
	// We need two factories: one for namespaced resources (filtered by namespace)
	// and one for cluster-scoped resources (no namespace filter)
	namespacedFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		d.client,
		defaultResyncPeriod,
		d.opts.Namespace,
		nil,
	)
	clusterScopedFactory := dynamicinformer.NewDynamicSharedInformerFactory(
		d.client,
		defaultResyncPeriod,
	)

	// Set up informers for each watch spec
	for _, spec := range specs {
		var informer cache.SharedIndexInformer
		gvr := gvkToGVR(spec.GVK)

		if spec.ClusterScoped {
			informer = clusterScopedFactory.ForResource(gvr).Informer()
		} else {
			informer = namespacedFactory.ForResource(gvr).Informer()
		}

		// Add event handlers with predicate filtering
		_, err := informer.AddEventHandler(d.makeEventHandler(spec))
		if err != nil {
			d.cleanup()
			return fmt.Errorf("failed to add event handler for %v: %w", spec.GVK, err)
		}
	}

	// Start the factories
	namespacedFactory.Start(d.stopCh)
	clusterScopedFactory.Start(d.stopCh)

	// Wait for caches to sync
	namespacedFactory.WaitForCacheSync(d.stopCh)
	clusterScopedFactory.WaitForCacheSync(d.stopCh)

	// Start the worker
	go d.runWorker(ctx)

	// Wait for stop signal
	select {
	case <-ctx.Done():
	case <-d.stopCh:
	}

	d.cleanup()
	return nil
}

// Stop stops the drift reconciler and waits for it to finish.
func (d *DriftReconciler) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.running {
		return
	}

	close(d.stopCh)
}

// cleanup cleans up resources when stopping
func (d *DriftReconciler) cleanup() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.workqueue != nil {
		d.workqueue.ShutDown()
	}
	d.running = false
}

// makeEventHandler creates a cache.ResourceEventHandler that filters events
// using predicates and enqueues reconciliation requests.
func (d *DriftReconciler) makeEventHandler(spec WatchSpec) cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		// AddFunc is intentionally empty - we don't reconcile on Add events.
		// Add events fire during initial cache sync for every existing resource,
		// which would cause unnecessary reconciliation right after Install().
		// Drift detection only needs Update (resource changed) and Delete (resource removed).
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

			// Apply predicates
			if !shouldReconcileOnUpdate(spec.GVK, oldU, newU) {
				return
			}

			// For owned resources, check owner reference
			if spec.WatchType == WatchTypeOwned && !d.isOwnedResource(newU) {
				return
			}

			d.enqueue()
		},
		DeleteFunc: func(obj interface{}) {
			// Handle DeletedFinalStateUnknown
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}

			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return
			}

			// Skip if we shouldn't reconcile on delete
			if !shouldReconcileOnDelete(u) {
				return
			}

			// For owned resources, check owner reference
			if spec.WatchType == WatchTypeOwned && !d.isOwnedResource(u) {
				return
			}

			d.enqueue()
		},
	}
}

// isOwnedResource checks if the resource is owned by our installation.
// This is determined by checking owner references (if configured) or labels.
func (d *DriftReconciler) isOwnedResource(obj *unstructured.Unstructured) bool {
	// If OwnerRef is configured, check for matching owner reference
	if d.opts.OwnerRef != nil {
		if hasMatchingOwnerRef(obj, d.opts.OwnerRef) {
			return true
		}
	}

	// Check for Istio labels that indicate the resource belongs to our installation
	labels := obj.GetLabels()
	if labels == nil {
		return false
	}

	// Check for common Istio labels
	// istio.io/rev label indicates which revision owns the resource
	if rev, ok := labels["istio.io/rev"]; ok {
		expectedRev := d.opts.Revision
		if expectedRev == defaultRevision {
			expectedRev = "default"
		}
		return rev == expectedRev
	}

	// Check for operator.istio.io/component label
	if _, ok := labels["operator.istio.io/component"]; ok {
		return true
	}

	// Check for app.kubernetes.io/managed-by label
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
			// UID match is optional - if expected has a UID, check it
			if expected.UID == "" || ref.UID == expected.UID {
				return true
			}
		}
	}
	return false
}

// enqueue adds a reconciliation request to the workqueue.
func (d *DriftReconciler) enqueue() {
	d.workqueue.Add(reconcileKey)
}

// runWorker runs the worker loop that processes the workqueue.
func (d *DriftReconciler) runWorker(ctx context.Context) {
	for d.processNextWorkItem(ctx) {
	}
}

// processNextWorkItem processes items from the workqueue.
func (d *DriftReconciler) processNextWorkItem(ctx context.Context) bool {
	key, shutdown := d.workqueue.Get()
	if shutdown {
		return false
	}
	defer d.workqueue.Done(key)

	// Reconcile by calling Install (ignore the returned DriftReconciler)
	_, err := d.installer.Install(ctx, d.opts)
	if err != nil {
		// Requeue with rate limiting on error
		d.workqueue.AddRateLimited(key)
		return true
	}

	// Success - forget the key to reset rate limiting
	d.workqueue.Forget(key)
	return true
}

// gvkToGVR converts a GroupVersionKind to a GroupVersionResource.
// Uses k8s.io/apimachinery/pkg/api/meta.UnsafeGuessKindToResource for proper pluralization.
func gvkToGVR(gvk schema.GroupVersionKind) schema.GroupVersionResource {
	// UnsafeGuessKindToResource returns (plural, singular) - we want the plural
	plural, _ := meta.UnsafeGuessKindToResource(gvk)
	return plural
}
