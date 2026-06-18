package ingressconfig

import (
	"context"
	"fmt"
	"time"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayapi/managementmode"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

const (
	blockedRequeueInterval = 30 * time.Second
	transitionRequeue      = 5 * time.Second
)

// transitionResult captures the outcome of one FSM step during a mode transition.
type transitionResult struct {
	phase         managementmode.TransitionPhase
	effectiveMode managementmode.Mode
	stackReady    bool
	managed       bool
	requeueAfter  time.Duration
	err           error
}

// stepTransition advances the Managed/Unmanaged transition FSM by one step.
func (r *reconciler) stepTransition(ctx context.Context, desired managementmode.Mode, current managementmode.State, compliance managementmode.ComplianceResult) transitionResult {
	result := transitionResult{
		phase:         current.TransitionPhase,
		effectiveMode: current.EffectiveMode,
		stackReady:    current.StackReady,
		managed:       current.Managed,
	}

	if desired != current.SpecMode {
		result.phase = managementmode.TransitionPhaseNone
	}

	if result.phase == managementmode.TransitionPhaseNone && desired != result.effectiveMode {
		result = r.beginTransition(desired, result)
	}

	switch result.phase {
	case managementmode.TransitionPhaseStoppingStack:
		return r.stepStoppingStack(ctx, result)
	case managementmode.TransitionPhaseRemovingVAP:
		return r.stepRemovingVAP(ctx, result)
	case managementmode.TransitionPhaseAssessingCRDs:
		return r.stepAssessingCRDs(desired, compliance, result)
	case managementmode.TransitionPhaseInstallingCRDs:
		return r.stepInstallingCRDs(compliance, result)
	case managementmode.TransitionPhaseDeployingVAP:
		return r.stepDeployingVAP(ctx, result)
	default:
		return r.idleState(ctx, desired, compliance, result)
	}
}

// beginTransition selects the first FSM phase when spec mode differs from effective mode.
func (r *reconciler) beginTransition(desired managementmode.Mode, result transitionResult) transitionResult {
	switch {
	case result.effectiveMode == managementmode.ModeManaged && desired == managementmode.ModeUnmanaged:
		result.phase = managementmode.TransitionPhaseStoppingStack
		result.stackReady = false
		result.managed = false
	case result.effectiveMode == managementmode.ModeUnmanaged && desired == managementmode.ModeManaged:
		result.phase = managementmode.TransitionPhaseAssessingCRDs
		result.stackReady = false
		result.managed = false
	}
	return result
}

// stepStoppingStack waits for the Istio control plane to stop before removing VAP.
func (r *reconciler) stepStoppingStack(ctx context.Context, result transitionResult) transitionResult {
	stopped, err := r.isIstioControlPlaneStopped(ctx)
	if err != nil {
		result.err = err
		return result
	}
	if !stopped {
		result.requeueAfter = transitionRequeue
		return result
	}
	result.phase = managementmode.TransitionPhaseRemovingVAP
	return result
}

// stepRemovingVAP deletes the Gateway API CRD VAP after the stack has stopped.
func (r *reconciler) stepRemovingVAP(ctx context.Context, result transitionResult) transitionResult {
	if err := r.deleteGatewayAPIVAP(ctx); err != nil {
		result.requeueAfter = transitionRequeue
		return result
	}
	present, err := r.gatewayAPIVAPPresent(ctx)
	if err != nil {
		result.err = err
		return result
	}
	if present {
		result.requeueAfter = transitionRequeue
		return result
	}
	result.phase = managementmode.TransitionPhaseNone
	result.effectiveMode = managementmode.ModeUnmanaged
	result.stackReady = false
	result.managed = false
	return result
}

// stepAssessingCRDs decides whether return to Managed can proceed or must wait for CRD fixes.
func (r *reconciler) stepAssessingCRDs(desired managementmode.Mode, compliance managementmode.ComplianceResult, result transitionResult) transitionResult {
	if desired != managementmode.ModeManaged {
		result.phase = managementmode.TransitionPhaseNone
		return result
	}
	if compliance.Compliant {
		result.phase = managementmode.TransitionPhaseDeployingVAP
		return result
	}
	if len(compliance.MissingCRDs) == len(compliance.Profile.CRDNames) {
		result.phase = managementmode.TransitionPhaseInstallingCRDs
		return result
	}
	result.requeueAfter = blockedRequeueInterval
	return result
}

// stepInstallingCRDs waits for gatewayapi to install absent CRDs before deploying VAP.
func (r *reconciler) stepInstallingCRDs(compliance managementmode.ComplianceResult, result transitionResult) transitionResult {
	if compliance.Compliant {
		result.phase = managementmode.TransitionPhaseDeployingVAP
		return result
	}
	result.requeueAfter = transitionRequeue
	return result
}

// stepDeployingVAP applies the VAP and completes transition to Managed mode.
func (r *reconciler) stepDeployingVAP(ctx context.Context, result transitionResult) transitionResult {
	if err := r.ensureGatewayAPIVAP(ctx); err != nil {
		result.err = err
		return result
	}
	result.phase = managementmode.TransitionPhaseNone
	result.effectiveMode = managementmode.ModeManaged
	result.stackReady = true
	result.managed = true
	return result
}

// idleState maintains steady-state behavior when no transition phase is active.
func (r *reconciler) idleState(ctx context.Context, desired managementmode.Mode, compliance managementmode.ComplianceResult, result transitionResult) transitionResult {
	result.effectiveMode = desired
	switch desired {
	case managementmode.ModeUnmanaged:
		result.stackReady = false
		result.managed = false
	case managementmode.ModeManaged:
		if compliance.Compliant {
			result.stackReady = true
			result.managed = true
			if err := r.ensureGatewayAPIVAPIfNeeded(ctx); err != nil {
				result.err = err
			}
		} else {
			result.stackReady = false
			result.managed = false
			if len(compliance.MissingCRDs) == len(compliance.Profile.CRDNames) {
				result.phase = managementmode.TransitionPhaseInstallingCRDs
			} else if !compliance.Compliant {
				result.requeueAfter = blockedRequeueInterval
			}
		}
	}
	return result
}

// ensureGatewayAPIVAPIfNeeded applies the VAP when idle in Managed mode but VAP is missing.
func (r *reconciler) ensureGatewayAPIVAPIfNeeded(ctx context.Context) error {
	present, err := r.gatewayAPIVAPPresent(ctx)
	if err != nil || present {
		return err
	}
	return r.ensureGatewayAPIVAP(ctx)
}

// isIstioControlPlaneStopped reports whether the Sail-managed istiod deployment is gone or scaled to zero.
func (r *reconciler) isIstioControlPlaneStopped(ctx context.Context) (bool, error) {
	if !r.config.GatewayAPIWithoutOLMEnabled {
		return true, nil
	}
	deployment := &appsv1.Deployment{}
	name := types.NamespacedName{
		Namespace: r.config.OperandNamespace,
		Name:      "istiod-" + operatorcontroller.IstioName("").Name,
	}
	if err := r.client.Get(ctx, name, deployment); err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("failed to get istiod deployment: %w", err)
	}
	if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 {
		return deployment.Status.ReadyReplicas == 0, nil
	}
	return deployment.Status.ReadyReplicas == 0 && deployment.Status.Replicas == 0, nil
}
