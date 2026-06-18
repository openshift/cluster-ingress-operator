package ingressconfig

import (
	operatorv1 "github.com/openshift/api/operator/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayapi/managementmode"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type gatewayAPIConditionInputs struct {
	effectiveMode   managementmode.Mode
	transitionPhase managementmode.TransitionPhase
	compliance      managementmode.ComplianceResult
	present         bool
	stackReady      bool
	managed         bool
}

// computeGatewayAPIConditions builds the three Gateway API management status conditions.
func computeGatewayAPIConditions(in gatewayAPIConditionInputs) []operatorv1.OperatorCondition {
	now := metav1.Now()
	var conditions []operatorv1.OperatorCondition

	conditions = append(conditions, managedCondition(in, now))
	conditions = append(conditions, presentCondition(in, now))
	conditions = append(conditions, compliantCondition(in, now))
	return conditions
}

// managedCondition computes the GatewayAPICRDsManaged condition.
func managedCondition(in gatewayAPIConditionInputs, now metav1.Time) operatorv1.OperatorCondition {
	c := operatorv1.OperatorCondition{
		Type:               managementmode.ConditionGatewayAPICRDsManaged,
		LastTransitionTime: now,
	}
	switch {
	case in.effectiveMode == managementmode.ModeUnmanaged && in.transitionPhase == managementmode.TransitionPhaseNone:
		c.Status = operatorv1.ConditionFalse
		c.Reason = managementmode.ReasonUnmanaged
		c.Message = "Gateway API CRD management is Unmanaged"
	case in.managed:
		c.Status = operatorv1.ConditionTrue
		c.Reason = managementmode.ReasonManagedByCIO
		c.Message = "Gateway API CRDs are managed by the Cluster Ingress Operator"
	default:
		c.Status = operatorv1.ConditionFalse
		c.Reason = managementmode.ReasonUnmanaged
		if in.transitionPhase != managementmode.TransitionPhaseNone {
			c.Message = "Gateway API management mode transition in progress"
		} else if !in.compliance.Compliant {
			c.Message = in.compliance.Message
		} else {
			c.Message = "Gateway API CRDs are not yet managed by the Cluster Ingress Operator"
		}
	}
	return c
}

// presentCondition computes the GatewayAPICRDsPresent condition.
func presentCondition(in gatewayAPIConditionInputs, now metav1.Time) operatorv1.OperatorCondition {
	c := operatorv1.OperatorCondition{
		Type:               managementmode.ConditionGatewayAPICRDsPresent,
		LastTransitionTime: now,
	}
	if in.present {
		c.Status = operatorv1.ConditionTrue
		c.Reason = managementmode.ReasonCRDsFound
		c.Message = "Gateway API CRDs are present on the cluster"
	} else {
		c.Status = operatorv1.ConditionFalse
		c.Reason = managementmode.ReasonCRDsNotFound
		c.Message = "Gateway API CRDs are not present on the cluster"
	}
	return c
}

// compliantCondition computes the GatewayAPICRDsCompliant condition.
func compliantCondition(in gatewayAPIConditionInputs, now metav1.Time) operatorv1.OperatorCondition {
	c := operatorv1.OperatorCondition{
		Type:               managementmode.ConditionGatewayAPICRDsCompliant,
		LastTransitionTime: now,
	}
	if in.effectiveMode == managementmode.ModeUnmanaged && in.transitionPhase == managementmode.TransitionPhaseNone {
		c.Status = operatorv1.ConditionUnknown
		c.Reason = managementmode.ReasonNotApplicable
		c.Message = "Compliance assessment is not applicable in Unmanaged mode"
		return c
	}
	if in.compliance.Compliant {
		c.Status = operatorv1.ConditionTrue
		c.Reason = managementmode.ReasonVersionMatch
		c.Message = in.compliance.Message
	} else if !in.present {
		c.Status = operatorv1.ConditionUnknown
		c.Reason = managementmode.ReasonNotApplicable
		c.Message = "Compliance assessment is not applicable when Gateway API CRDs are absent"
	} else {
		c.Status = operatorv1.ConditionFalse
		c.Reason = managementmode.ReasonVersionMismatch
		c.Message = in.compliance.Message
	}
	return c
}

// patchGatewayAPIConditions merges desired Gateway API conditions into the existing set,
// preserving LastTransitionTime when Status is unchanged.
func patchGatewayAPIConditions(existing []operatorv1.OperatorCondition, desired []operatorv1.OperatorCondition) []operatorv1.OperatorCondition {
	byType := map[string]operatorv1.OperatorCondition{}
	for _, c := range existing {
		byType[c.Type] = c
	}
	for _, c := range desired {
		if prev, ok := byType[c.Type]; ok && prev.Status == c.Status {
			c.LastTransitionTime = prev.LastTransitionTime
		}
		byType[c.Type] = c
	}
	order := []string{
		managementmode.ConditionGatewayAPICRDsManaged,
		managementmode.ConditionGatewayAPICRDsPresent,
		managementmode.ConditionGatewayAPICRDsCompliant,
	}
	result := make([]operatorv1.OperatorCondition, 0, len(order))
	for _, t := range order {
		if c, ok := byType[t]; ok {
			result = append(result, c)
		}
	}
	return result
}

// gatewayAPIConditionInputsFromState maps Store state and compliance into condition inputs.
func gatewayAPIConditionInputsFromState(state managementmode.State, compliance managementmode.ComplianceResult) gatewayAPIConditionInputs {
	present := compliance.Compliant || compliance.PartialSet || len(compliance.MissingCRDs) < len(compliance.Profile.CRDNames)
	return gatewayAPIConditionInputs{
		effectiveMode:   state.EffectiveMode,
		transitionPhase: state.TransitionPhase,
		compliance:      compliance,
		present:         present,
		stackReady:      state.StackReady,
		managed:         state.Managed,
	}
}

// resolveDesiredMode returns the spec management mode, defaulting to Managed when unset.
func resolveDesiredMode(ingress *operatorv1alpha1.Ingress, fgEnabled bool) managementmode.Mode {
	if !fgEnabled {
		return managementmode.ModeManaged
	}
	if ingress == nil {
		return managementmode.ModeManaged
	}
	switch ingress.Spec.GatewayAPI.ManagementMode {
	case operatorv1alpha1.GatewayAPIManagementModeUnmanaged:
		return managementmode.ModeUnmanaged
	default:
		return managementmode.ModeManaged
	}
}

// effectiveModeWhenGateDisabled returns the Store state when GatewayAPIManagementMode is off.
func effectiveModeWhenGateDisabled() managementmode.State {
	return managementmode.DefaultManagedStackReadyState(false)
}
