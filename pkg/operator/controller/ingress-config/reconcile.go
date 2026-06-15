package ingressconfig

import (
	"context"
	"fmt"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayapi/managementmode"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Reconcile reads the Ingress operator config, assesses CRD compliance, advances
// the mode transition FSM, updates the shared Store, and patches status conditions.
func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Info("reconciling", "request", request)

	if !r.config.GatewayAPIManagementModeEnabled {
		state := effectiveModeWhenGateDisabled()
		r.config.StateStore.Update(state)
		return reconcile.Result{}, nil
	}

	ingress := &operatorv1alpha1.Ingress{}
	ingressExists := true
	if err := r.client.Get(ctx, request.NamespacedName, ingress); err != nil {
		if !errors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("failed to get ingress operator config: %w", err)
		}
		ingressExists = false
		ingress = nil
	}

	desiredMode := resolveDesiredMode(ingress, r.config.GatewayAPIManagementModeEnabled)
	compliance, err := managementmode.AssessGatewayAPICRDCompliance(ctx, r.client)
	if err != nil {
		return reconcile.Result{}, err
	}

	current := r.config.StateStore.Current()
	if current.GatewayAPIManagementModeEnabled && ingressExists {
		current.SpecMode = desiredMode
		current.ObservedGeneration = ingress.Generation
	} else {
		current = managementmode.State{
			SpecMode:                        desiredMode,
			EffectiveMode:                   desiredMode,
			GatewayAPIManagementModeEnabled: true,
		}
		if ingressExists {
			current.ObservedGeneration = ingress.Generation
		}
	}

	transition := r.stepTransition(ctx, desiredMode, current, compliance)
	if transition.err != nil {
		return reconcile.Result{}, transition.err
	}

	newState := managementmode.State{
		SpecMode:                        desiredMode,
		EffectiveMode:                   transition.effectiveMode,
		TransitionPhase:                 transition.phase,
		ObservedGeneration:              current.ObservedGeneration,
		Managed:                         transition.managed,
		Present:                         compliance.Compliant || compliance.PartialSet,
		Compliant:                       compliance.Compliant,
		StackReady:                    transition.stackReady,
		GatewayAPIManagementModeEnabled: true,
	}
	r.config.StateStore.Update(newState)

	if ingressExists {
		if err := r.patchIngressStatus(ctx, ingress, newState, compliance); err != nil {
			return reconcile.Result{}, err
		}
	}

	if transition.requeueAfter > 0 {
		return reconcile.Result{RequeueAfter: transition.requeueAfter}, nil
	}
	return reconcile.Result{}, nil
}

// patchIngressStatus updates Gateway API management conditions on the Ingress CR.
func (r *reconciler) patchIngressStatus(ctx context.Context, ingress *operatorv1alpha1.Ingress, state managementmode.State, compliance managementmode.ComplianceResult) error {
	inputs := gatewayAPIConditionInputsFromState(state, compliance)
	desiredConditions := computeGatewayAPIConditions(inputs)

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &operatorv1alpha1.Ingress{}
		if err := r.client.Get(ctx, client.ObjectKeyFromObject(ingress), current); err != nil {
			return err
		}
		updated := current.DeepCopy()
		updated.Status.Conditions = patchGatewayAPIConditions(updated.Status.Conditions, desiredConditions)
		if current.Status.ObservedGeneration != current.Generation {
			updated.Status.ObservedGeneration = current.Generation
		}
		patch := client.MergeFrom(current)
		return r.client.Status().Patch(ctx, updated, patch)
	})
}
