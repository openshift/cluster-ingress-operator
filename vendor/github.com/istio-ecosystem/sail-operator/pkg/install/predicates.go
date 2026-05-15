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
	"reflect"
	"regexp"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Predicate filtering logic adapted from controllers/istiorevision for use with dynamic informers.
// These functions determine whether a resource change should trigger reconciliation.

const (
	// ignoreAnnotation is the annotation that, when set to "true", causes updates to be ignored.
	ignoreAnnotation = "sailoperator.io/ignore"
)

// GVKs that need special predicate handling
var (
	serviceGVK                        = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}
	serviceAccountGVK                 = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ServiceAccount"}
	namespaceGVK                      = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}
	networkPolicyGVK                  = schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}
	pdbGVK                            = schema.GroupVersionKind{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"}
	hpaGVK                            = schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"}
	validatingWebhookConfigurationGVK = schema.GroupVersionKind{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration"}
)

// shouldReconcileOnCreate determines if a create event should trigger reconciliation.
func shouldReconcileOnCreate(obj *unstructured.Unstructured) bool {
	// Always reconcile on create
	return true
}

// shouldReconcileOnDelete determines if a delete event should trigger reconciliation.
func shouldReconcileOnDelete(obj *unstructured.Unstructured) bool {
	// Always reconcile on delete to restore the resource
	return true
}

// shouldReconcileOnUpdate determines if an update event should trigger reconciliation.
// Implements the same logic as the operator's predicates.
func shouldReconcileOnUpdate(gvk schema.GroupVersionKind, oldObj, newObj *unstructured.Unstructured) bool {
	// Check ignore annotation first - applies to all resources
	if hasIgnoreAnnotation(newObj) {
		return false
	}

	// ServiceAccounts: ignore all updates to prevent removing pull secrets added by other controllers
	if gvk == serviceAccountGVK {
		return false
	}

	// ValidatingWebhookConfiguration: special handling for istiod-managed webhooks
	if gvk == validatingWebhookConfigurationGVK {
		return shouldReconcileValidatingWebhook(oldObj, newObj)
	}

	// Resources that need status-change filtering
	if needsStatusChangeFilter(gvk) {
		return !isStatusOnlyChange(gvk, oldObj, newObj)
	}

	// Default: reconcile on any change
	return true
}

// needsStatusChangeFilter returns true for GVKs that should ignore status-only changes.
func needsStatusChangeFilter(gvk schema.GroupVersionKind) bool {
	switch gvk {
	case serviceGVK, networkPolicyGVK, pdbGVK, hpaGVK, namespaceGVK:
		return true
	default:
		return false
	}
}

// isStatusOnlyChange returns true if only the status changed (no spec/labels/annotations/etc).
// This prevents unnecessary reconciliation when only status fields are updated.
func isStatusOnlyChange(gvk schema.GroupVersionKind, oldObj, newObj *unstructured.Unstructured) bool {
	// Check if spec was updated
	if specWasUpdated(gvk, oldObj, newObj) {
		return false
	}

	// Check if labels changed
	if !reflect.DeepEqual(oldObj.GetLabels(), newObj.GetLabels()) {
		return false
	}

	// Check if annotations changed
	if !reflect.DeepEqual(oldObj.GetAnnotations(), newObj.GetAnnotations()) {
		return false
	}

	// Check if owner references changed
	if !reflect.DeepEqual(oldObj.GetOwnerReferences(), newObj.GetOwnerReferences()) {
		return false
	}

	// Check if finalizers changed
	if !reflect.DeepEqual(oldObj.GetFinalizers(), newObj.GetFinalizers()) {
		return false
	}

	// Only status changed
	return true
}

// specWasUpdated checks if the spec of a resource was updated.
func specWasUpdated(gvk schema.GroupVersionKind, oldObj, newObj *unstructured.Unstructured) bool {
	// For HPAs, k8s doesn't set metadata.generation, so we check the spec directly
	if gvk == hpaGVK {
		oldSpec, _, _ := unstructured.NestedMap(oldObj.Object, "spec")
		newSpec, _, _ := unstructured.NestedMap(newObj.Object, "spec")
		return !reflect.DeepEqual(oldSpec, newSpec)
	}

	// For other resources, comparing metadata.generation suffices
	return oldObj.GetGeneration() != newObj.GetGeneration()
}

// hasIgnoreAnnotation checks if the resource has the sailoperator.io/ignore annotation set to "true".
func hasIgnoreAnnotation(obj *unstructured.Unstructured) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return false
	}
	return annotations[ignoreAnnotation] == "true"
}

// shouldReconcileValidatingWebhook handles special filtering for ValidatingWebhookConfiguration.
// Istiod updates the caBundle and failurePolicy fields in its validator webhook configs,
// and we must ignore these changes to prevent an endless update loop.
func shouldReconcileValidatingWebhook(oldObj, newObj *unstructured.Unstructured) bool {
	name := newObj.GetName()

	// Check if this is an istiod-managed validator webhook
	matched, _ := regexp.MatchString("istiod-.*-validator|istio-validator.*", name)
	if !matched {
		// Not an istiod validator, reconcile normally
		return true
	}

	// For istiod validators, compare objects after clearing fields that istiod updates
	oldCopy := clearWebhookIgnoredFields(oldObj.DeepCopy())
	newCopy := clearWebhookIgnoredFields(newObj.DeepCopy())

	return !reflect.DeepEqual(oldCopy.Object, newCopy.Object)
}

// clearWebhookIgnoredFields clears fields that should be ignored when comparing webhook configs.
func clearWebhookIgnoredFields(obj *unstructured.Unstructured) *unstructured.Unstructured {
	// Clear metadata fields that change frequently
	obj.SetResourceVersion("")
	obj.SetGeneration(0)
	obj.SetManagedFields(nil)

	// Clear failurePolicy in each webhook
	webhooks, found, _ := unstructured.NestedSlice(obj.Object, "webhooks")
	if found {
		for i := range webhooks {
			if webhook, ok := webhooks[i].(map[string]interface{}); ok {
				delete(webhook, "failurePolicy")
				webhooks[i] = webhook
			}
		}
		_ = unstructured.SetNestedSlice(obj.Object, webhooks, "webhooks")
	}

	return obj
}
