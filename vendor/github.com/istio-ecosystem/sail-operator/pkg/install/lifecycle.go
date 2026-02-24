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
	"reflect"

	"github.com/istio-ecosystem/sail-operator/pkg/helm"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// reconcileKey is the single key used in the workqueue.
	// Since we always reconcile the entire installation, we use a single key
	// to coalesce multiple events into one reconciliation.
	reconcileKey = "reconcile"
)

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
// Apply blocks if Uninstall is in progress (they share a lifecycle lock).
// The result of the reconciliation will be available via Status() after
// the notification channel signals.
func (l *Library) Apply(opts Options) {
	l.lifecycleMu.Lock()
	defer l.lifecycleMu.Unlock()

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

	// Wake waitForDesiredState if it's blocking
	select {
	case l.applySignal <- struct{}{}:
	default:
	}
}

// Enqueue forces a reconciliation without changing the desired options.
// Use this when cluster state that affects reconciliation has changed
// externally but the Options passed to Apply remain the same. For example,
// if an OLM Subscription managing Istio CRDs is deleted, the CRD ownership
// labels haven't changed yet, but the consumer knows a takeover is now
// possible. Calling Enqueue triggers the Library to re-classify CRDs and
// re-run the Helm install using the previously applied options.
//
// Enqueue is a no-op if Apply has not been called yet (no desired state).
// It is safe to call from any goroutine.
func (l *Library) Enqueue() {
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

// Uninstall stops drift detection and removes the istiod installation.
// It first stops informers and waits for the processing loop to exit,
// then performs the Helm uninstall. This prevents the drift-repair loop
// from fighting the uninstall.
//
// Uninstall is a synchronous, blocking operation. It holds the lifecycle
// lock, so Apply() will block until Uninstall completes.
//
// The caller provides the namespace and revision to uninstall. Empty strings
// default to "istio-system" and "default" respectively. This allows Uninstall
// to work even after a crash, when no prior Apply() state exists in memory.
//
// If an active reconcile loop is running, it is stopped before the Helm
// uninstall proceeds. After Uninstall, calling Apply() will start a new
// install cycle.
func (l *Library) Uninstall(ctx context.Context, namespace, revision string) error {
	l.lifecycleMu.Lock()
	defer l.lifecycleMu.Unlock()

	log := ctrllog.Log.WithName("install")

	if namespace == "" {
		namespace = defaultNamespace
	}
	if revision == "" {
		revision = defaultRevision
	}

	// Clear desired state and capture loop handles so we can stop
	// the processing loop if one is active.
	l.mu.Lock()
	l.desiredOpts = nil
	informerStop := l.informerStop
	processingDone := l.processingDone
	l.mu.Unlock()

	log.Info("Uninstalling", "namespace", namespace, "revision", revision)

	// Stop informers so they don't fire events during teardown
	if informerStop != nil {
		close(informerStop)
	}

	// Enqueue a sentinel so processWorkQueue unblocks from Get()
	// and checks the nil desiredOpts condition
	l.enqueue()

	// Wait for the processing loop to exit
	if processingDone != nil {
		<-processingDone
	}

	// Now safe to Helm uninstall — nothing is watching or reconciling
	if err := l.inst.uninstall(ctx, namespace, revision); err != nil {
		return err
	}

	log.Info("Uninstall complete", "namespace", namespace, "revision", revision)
	return nil
}

// run is the main loop. It alternates between idle (waiting for Apply) and
// active (informers running, processing workqueue). Uninstall() nils
// desiredOpts, which causes processWorkQueue to exit and the loop to
// return to idle. The loop exits only when ctx is cancelled.
func (l *Library) run(ctx context.Context, notifyCh chan<- struct{}) {
	log := ctrllog.Log.WithName("install")
	defer close(notifyCh)
	defer l.workqueue.ShutDown()

	for {
		// Idle: block until Apply() sets desiredOpts (or ctx cancelled)
		if !l.waitForDesiredState(ctx) {
			return // ctx cancelled
		}

		// Active: set up informers for this install cycle
		l.mu.Lock()
		l.informerStop = make(chan struct{})
		l.processingDone = make(chan struct{})
		l.mu.Unlock()

		l.setupInformers(l.informerStop)

		log.Info("Processing workqueue")
		l.processWorkQueue(ctx, notifyCh)
		log.Info("Workqueue processing stopped, returning to idle")

		// Loop back to idle, waiting for next Apply()
	}
}

// processWorkQueue processes work items until desiredOpts goes nil (Uninstall)
// or the workqueue shuts down (ctx cancelled). It closes l.processingDone on exit
// so Uninstall() can wait for processing to stop before doing Helm cleanup.
func (l *Library) processWorkQueue(ctx context.Context, notifyCh chan<- struct{}) {
	log := ctrllog.Log.WithName("install")
	defer func() {
		l.mu.RLock()
		done := l.processingDone
		l.mu.RUnlock()
		if done != nil {
			close(done)
		}
	}()

	for {
		key, shutdown := l.workqueue.Get()
		if shutdown {
			return
		}

		// Read desiredOpts once under a single lock to avoid a race
		// where Uninstall() nils it between a nil-check and dereference.
		l.mu.RLock()
		optsPtr := l.desiredOpts
		l.mu.RUnlock()

		if optsPtr == nil {
			l.workqueue.Done(key)
			return // back to idle
		}
		opts := *optsPtr

		log.Info("Reconciling")
		status := l.inst.reconcile(ctx, opts)
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
	// Fast path: check if opts are already set (e.g. Uninstall->Apply cycle)
	l.mu.RLock()
	hasOpts := l.desiredOpts != nil
	l.mu.RUnlock()
	if hasOpts {
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return false
		case <-l.applySignal:
			l.mu.RLock()
			hasOpts := l.desiredOpts != nil
			l.mu.RUnlock()
			if hasOpts {
				return true
			}
		}
	}
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

	// Compare Values via map conversion for deep equality
	aMap := helm.FromValues(a.Values)
	bMap := helm.FromValues(b.Values)
	return reflect.DeepEqual(aMap, bMap)
}

