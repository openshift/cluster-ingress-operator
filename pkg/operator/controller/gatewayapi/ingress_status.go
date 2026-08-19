package gatewayapi

import (
	"context"
	"fmt"
	"strings"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	bundleVersionAnnotation = "gateway.networking.k8s.io/bundle-version"

	conditionTypeGatewayAPICRDsManaged   = "GatewayAPICRDsManaged"
	conditionTypeGatewayAPICRDsPresent   = "GatewayAPICRDsPresent"
	conditionTypeGatewayAPICRDsCompliant = "GatewayAPICRDsCompliant"

	reasonManagedByIngressOperator = "ManagedByIngressOperator"
	reasonUnmanaged                = "Unmanaged"
	reasonTakeoverBlocked          = "TakeoverBlocked"
	reasonCRDsFound                = "CRDsFound"
	reasonCRDsNotFound             = "CRDsNotFound"
	reasonVersionMatch             = "VersionMatch"
	reasonVersionMismatch          = "VersionMismatch"
)

// reconcileIngressStatus computes CRD management conditions, updates
// the mode accessor, and writes conditions back to the Ingress status.
//
// The snapshot parameter carries the single authoritative Ingress CR
// read for this reconcile pass, preventing TOCTOU divergence between
// this function and reconcileAdmissionPolicyTransition.
func (r *reconciler) reconcileIngressStatus(ctx context.Context, snapshot ingressModeSnapshot) error {
	presentCond, compliantCond, anyExistingNonCompliant, err := r.computeCRDConditions(ctx)
	if err != nil {
		return err
	}
	present := presentCond.Status == metav1.ConditionTrue
	compliant := compliantCond.Status == metav1.ConditionTrue

	// Unrecognized CRDs in the gateway.networking.k8s.io group (e.g. from
	// a Gateway API version bump this operator doesn't know about yet)
	// must also block takeover: we cannot safely determine compatibility
	// with an API surface we don't manage.
	unknownCRDs, err := r.listUnmanagedGatewayAPICRDs(ctx)
	if err != nil {
		return err
	}
	blocksTakeover := anyExistingNonCompliant || len(unknownCRDs) > 0

	// Check if we were already managing CRDs before this reconcile.
	// This distinguishes between:
	// 1. Upgrade (already Managed, version mismatch) -> allow update
	// 2. Takeover (Unmanaged->Managed, customer modified) -> block takeover
	//
	// We read the CURRENT Managed condition from the Ingress status (not
	// in-memory state) so this works correctly across pod restarts.
	wasAlreadyManaged := false
	if snapshot.found && snapshot.ingress != nil {
		if currentManagedCond := apimeta.FindStatusCondition(snapshot.ingress.Status.Conditions, conditionTypeGatewayAPICRDsManaged); currentManagedCond != nil {
			wasAlreadyManaged = currentManagedCond.Status == metav1.ConditionTrue
		}
	}

	managedCond := computeManagedCondition(snapshot.desiredMode, present, compliant, blocksTakeover, wasAlreadyManaged)
	managed := managedCond.Status == metav1.ConditionTrue

	r.config.ModeAccessor.Update(snapshot.desiredMode, managed, present, compliant)

	updateManagementModeMetrics(managedCond, compliantCond, embeddedGatewayAPIVersion(), parseOSSMVersion(r.config.OSSMVersion))

	if !snapshot.found {
		return nil
	}
	return r.updateIngressStatus(ctx, snapshot.ingress, []metav1.Condition{managedCond, presentCond, compliantCond})
}

// computeCRDConditions inspects the live CRDs on the cluster and
// returns the Present condition, Compliant condition, a boolean
// indicating whether any existing CRD is non-compliant (even if the
// full set is incomplete), and an error. It returns an error if a CRD
// Get fails for a reason other than NotFound, so that transient API
// errors are not mistaken for missing CRDs.
func (r *reconciler) computeCRDConditions(ctx context.Context) (metav1.Condition, metav1.Condition, bool, error) {
	var missingCRDs, annotationMismatches, schemaMismatches []string
	anyExistingNonCompliant := false

	for _, desired := range managedCRDs {
		var current apiextensionsv1.CustomResourceDefinition
		if err := r.client.Get(ctx, types.NamespacedName{Name: desired.Name}, &current); err != nil {
			if errors.IsNotFound(err) {
				missingCRDs = append(missingCRDs, desired.Name)
				continue
			}
			return metav1.Condition{}, metav1.Condition{}, false, fmt.Errorf("failed to get CRD %s: %w", desired.Name, err)
		}

		expectedVersion := desired.Annotations[bundleVersionAnnotation]
		actualVersion := current.Annotations[bundleVersionAnnotation]
		if expectedVersion != actualVersion {
			anyExistingNonCompliant = true
			annotationMismatches = append(annotationMismatches,
				fmt.Sprintf("%s (expected %s, got %s)", desired.Name, expectedVersion, actualVersion))
			continue
		}

		if !crdSpecCompliant(&current, desired) {
			anyExistingNonCompliant = true
			schemaMismatches = append(schemaMismatches, desired.Name)
		}
	}

	presentCond := buildPresentCondition(missingCRDs)

	if len(missingCRDs) > 0 {
		var msg string
		if anyExistingNonCompliant {
			var details []string
			if len(annotationMismatches) > 0 {
				details = append(details, fmt.Sprintf("bundle-version annotation mismatch: %s", strings.Join(annotationMismatches, "; ")))
			}
			if len(schemaMismatches) > 0 {
				details = append(details, fmt.Sprintf("schema differs despite matching annotation: %s", strings.Join(schemaMismatches, ", ")))
			}
			msg = fmt.Sprintf("Not all Gateway API CRDs are present and some existing CRDs are non-compliant: %s", strings.Join(details, "; "))
		} else {
			msg = "Cannot determine compliance: not all Gateway API CRDs are present"
		}
		return presentCond, metav1.Condition{
			Type:    conditionTypeGatewayAPICRDsCompliant,
			Status:  metav1.ConditionFalse,
			Reason:  reasonVersionMismatch,
			Message: msg,
		}, anyExistingNonCompliant, nil
	}

	compliantCond := buildCompliantCondition(annotationMismatches, schemaMismatches)
	return presentCond, compliantCond, anyExistingNonCompliant, nil
}

// computeManagedCondition returns the GatewayAPICRDsManaged condition
// based on the desired mode and the observed CRD state.
// anyExistingNonCompliant is true when at least one existing managed
// CRD does not match the expected version/schema, even if the full
// set of CRDs is not present.
// wasAlreadyManaged indicates whether the operator was managing CRDs
// in the previous reconcile, used to distinguish upgrades from takeover.
func computeManagedCondition(desiredMode operatorv1alpha1.GatewayAPIManagementMode, present, compliant, anyExistingNonCompliant, wasAlreadyManaged bool) metav1.Condition {
	if desiredMode == operatorv1alpha1.GatewayAPIManagementModeUnmanaged {
		return metav1.Condition{
			Type:    conditionTypeGatewayAPICRDsManaged,
			Status:  metav1.ConditionFalse,
			Reason:  reasonUnmanaged,
			Message: "Gateway API CRD management is set to Unmanaged mode",
		}
	}

	// Only block takeover when:
	// 1. CRDs exist with mismatched version/schema, AND
	// 2. We were NOT already managing them (i.e., Unmanaged->Managed transition)
	//
	// If we were already managing the CRDs (wasAlreadyManaged=true), then
	// a version mismatch is a normal upgrade scenario (v1.0->v1.1) and we
	// should proceed with updating the CRDs, not block takeover.
	if anyExistingNonCompliant && !wasAlreadyManaged {
		return metav1.Condition{
			Type:    conditionTypeGatewayAPICRDsManaged,
			Status:  metav1.ConditionFalse,
			Reason:  reasonTakeoverBlocked,
			Message: "Cannot take ownership of existing Gateway API CRDs: CRDs do not match the expected version",
		}
	}

	return metav1.Condition{
		Type:    conditionTypeGatewayAPICRDsManaged,
		Status:  metav1.ConditionTrue,
		Reason:  reasonManagedByIngressOperator,
		Message: "The ingress operator is managing Gateway API CRDs",
	}
}

func buildPresentCondition(missingCRDs []string) metav1.Condition {
	if len(missingCRDs) == 0 {
		return metav1.Condition{
			Type:    conditionTypeGatewayAPICRDsPresent,
			Status:  metav1.ConditionTrue,
			Reason:  reasonCRDsFound,
			Message: "All Gateway API CRDs are present on the cluster",
		}
	}
	return metav1.Condition{
		Type:    conditionTypeGatewayAPICRDsPresent,
		Status:  metav1.ConditionFalse,
		Reason:  reasonCRDsNotFound,
		Message: fmt.Sprintf("Missing Gateway API CRDs: %s", strings.Join(missingCRDs, ", ")),
	}
}

func buildCompliantCondition(annotationMismatches, schemaMismatches []string) metav1.Condition {
	if len(annotationMismatches) == 0 && len(schemaMismatches) == 0 {
		return metav1.Condition{
			Type:    conditionTypeGatewayAPICRDsCompliant,
			Status:  metav1.ConditionTrue,
			Reason:  reasonVersionMatch,
			Message: "All Gateway API CRDs match the expected version",
		}
	}
	var messages []string
	if len(annotationMismatches) > 0 {
		messages = append(messages, fmt.Sprintf("bundle-version annotation mismatch: %s", strings.Join(annotationMismatches, "; ")))
	}
	if len(schemaMismatches) > 0 {
		messages = append(messages, fmt.Sprintf("schema differs despite matching annotation: %s", strings.Join(schemaMismatches, ", ")))
	}
	return metav1.Condition{
		Type:    conditionTypeGatewayAPICRDsCompliant,
		Status:  metav1.ConditionFalse,
		Reason:  reasonVersionMismatch,
		Message: strings.Join(messages, "; "),
	}
}

// updateIngressStatus writes the given conditions to the Ingress CR's
// status subresource, updating observedGeneration as well. Uses Patch
// instead of Update to leverage the listType=map merge semantics for
// conditions, which prevents conflicts when multiple controllers update
// different condition types concurrently.
func (r *reconciler) updateIngressStatus(ctx context.Context, ingress *operatorv1alpha1.Ingress, conditions []metav1.Condition) error {
	updated := ingress.DeepCopy()
	changed := false

	for i := range conditions {
		conditions[i].ObservedGeneration = ingress.Generation
		if apimeta.SetStatusCondition(&updated.Status.Conditions, conditions[i]) {
			changed = true
		}
	}

	if updated.Status.ObservedGeneration < ingress.Generation {
		updated.Status.ObservedGeneration = ingress.Generation
		changed = true
	}

	if !changed {
		return nil
	}

	if err := r.client.Status().Patch(ctx, updated, client.MergeFrom(ingress)); err != nil {
		return fmt.Errorf("failed to patch Ingress status: %w", err)
	}

	log.Info("updated Ingress status conditions")
	return nil
}
