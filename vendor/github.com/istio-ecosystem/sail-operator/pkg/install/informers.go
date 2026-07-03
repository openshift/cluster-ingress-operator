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
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// defaultResyncPeriod is the default resync period for informers.
	defaultResyncPeriod = 30 * time.Minute
)

// setupInformers creates dynamic informers for Helm resources and CRDs
// based on the current desired options.
func (l *Library) setupInformers(stopCh <-chan struct{}) {
	log := ctrllog.Log.WithName("install")

	l.mu.RLock()
	opts := *l.desiredOpts
	l.mu.RUnlock()

	// Helm resource watches (drift detection)
	specs, err := l.inst.getWatchSpecs(opts)
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

	// Capture revision and managedByValue once for all event handlers — no lock needed per event.
	revision := opts.Revision
	managedByValue := l.managedByValue
	enqueue := l.enqueue

	for _, spec := range specs {
		var informer cache.SharedIndexInformer
		gvr := gvkToGVR(spec.GVK)

		if spec.ClusterScoped {
			informer = clusterScopedFactory.ForResource(gvr).Informer()
		} else {
			informer = namespacedFactory.ForResource(gvr).Informer()
		}

		var handler cache.ResourceEventHandler
		if spec.watchType == watchTypeCRD {
			handler = makeCRDEventHandler(spec.TargetNames, enqueue)
		} else {
			handler = makeOwnedEventHandler(spec.GVK, spec.watchType, revision, managedByValue, enqueue)
		}

		if _, err := informer.AddEventHandler(handler); err != nil {
			log.Error(err, "Failed to add event handler", "gvk", spec.GVK)
			continue
		}
		log.V(1).Info("Watching", "gvk", spec.GVK, "type", spec.watchType, "clusterScoped", spec.ClusterScoped)
	}

	namespacedFactory.Start(stopCh)
	clusterScopedFactory.Start(stopCh)
	namespacedFactory.WaitForCacheSync(stopCh)
	clusterScopedFactory.WaitForCacheSync(stopCh)
	log.Info("Informers synced and watching for drift")
}

// buildCRDWatchSpec computes a watchSpec for Istio CRDs based on the current options.
// Returns nil if the target CRD set cannot be determined.
func (l *Library) buildCRDWatchSpec(opts Options) *watchSpec {
	targetNames := l.inst.crdManager.WatchTargets(opts.Values, ptr.Deref(opts.IncludeAllCRDs, false))
	if len(targetNames) == 0 {
		return nil
	}

	return &watchSpec{
		GVK:           crdGVK,
		watchType:     watchTypeCRD,
		ClusterScoped: true,
		TargetNames:   targetNames,
	}
}

// makeOwnedEventHandler handles events for Helm-managed and namespace resources.
// It takes explicit dependencies instead of closing over Library, enabling unit testing
// without concurrency machinery.
func makeOwnedEventHandler(gvk schema.GroupVersionKind, wt watchType, revision, managedByValue string, enqueue func()) cache.ResourceEventHandler {
	log := ctrllog.Log.WithName("install").WithValues("gvk", gvk, "watchType", wt)
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

			if !shouldReconcileOnUpdate(gvk, oldU, newU) {
				log.V(2).Info("Skipping update (predicate)", "name", newU.GetName())
				return
			}

			if wt == watchTypeOwned && !isOwnedResource(newU, revision, managedByValue) {
				log.V(1).Info("Skipping update (not owned)", "name", newU.GetName(), "labels", newU.GetLabels())
				return
			}

			log.Info("Drift detected (update)", "name", newU.GetName())
			enqueue()
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

			if wt == watchTypeOwned && !isOwnedResource(u, revision, managedByValue) {
				log.V(1).Info("Skipping delete (not owned)", "name", u.GetName())
				return
			}

			log.Info("Drift detected (delete)", "name", u.GetName())
			enqueue()
		},
	}
}

// makeCRDEventHandler handles events for CRD resources. Unlike owned resources,
// CRD events trigger on create (new CRD appeared), delete (CRD removed), and
// label/annotation changes (ownership transfer). Events are filtered by name
// to only watch target Istio CRDs.
func makeCRDEventHandler(targets map[string]struct{}, enqueue func()) cache.ResourceEventHandler {
	log := ctrllog.Log.WithName("install").WithValues("watchType", watchTypeCRD)
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
			if !isTargetCRD(u, targets) {
				return
			}
			log.Info("Drift detected (CRD added)", "name", u.GetName())
			enqueue()
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
			if !isTargetCRD(newU, targets) {
				return
			}
			if !shouldReconcileCRDOnUpdate(oldU, newU) {
				return
			}
			log.Info("Drift detected (CRD updated)", "name", newU.GetName())
			enqueue()
		},
		DeleteFunc: func(obj interface{}) {
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
			if !isTargetCRD(u, targets) {
				return
			}
			log.Info("Drift detected (CRD deleted)", "name", u.GetName())
			enqueue()
		},
	}
}

// gvkToGVR converts a GroupVersionKind to a GroupVersionResource.
func gvkToGVR(gvk schema.GroupVersionKind) schema.GroupVersionResource {
	plural, _ := meta.UnsafeGuessKindToResource(gvk)
	return plural
}
