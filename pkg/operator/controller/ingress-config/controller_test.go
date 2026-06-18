package ingressconfig

import (
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayapi/managementmode"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResolveDesiredMode_defaultsManagedWhenEmpty(t *testing.T) {
	ingress := &operatorv1alpha1.Ingress{}
	assert.Equal(t, managementmode.ModeManaged, resolveDesiredMode(ingress, true))
	assert.Equal(t, managementmode.ModeManaged, resolveDesiredMode(nil, true))
}

func TestEffectiveMode_gateDisabledForcesManaged(t *testing.T) {
	ingress := &operatorv1alpha1.Ingress{
		Spec: operatorv1alpha1.IngressSpec{
			GatewayAPI: operatorv1alpha1.GatewayAPIIngressConfig{
				ManagementMode: operatorv1alpha1.GatewayAPIManagementModeUnmanaged,
			},
		},
	}
	assert.Equal(t, managementmode.ModeManaged, resolveDesiredMode(ingress, false))
	state := effectiveModeWhenGateDisabled()
	assert.True(t, state.StackReady)
}

func TestComputeGatewayAPIConditions_allReasons(t *testing.T) {
	profile := managementmode.SupportedProfile()
	compliant := managementmode.ComplianceResult{Compliant: true, Profile: profile, Message: "ok"}

	cases := []struct {
		name   string
		input  gatewayAPIConditionInputs
		status operatorv1.ConditionStatus
		reason string
		ctype  string
	}{
		{
			name: "managed idle",
			input: gatewayAPIConditionInputs{
				effectiveMode: managementmode.ModeManaged,
				compliance:    compliant,
				present:       true,
				managed:       true,
			},
			ctype:  managementmode.ConditionGatewayAPICRDsManaged,
			status: operatorv1.ConditionTrue,
			reason: managementmode.ReasonManagedByCIO,
		},
		{
			name: "unmanaged idle",
			input: gatewayAPIConditionInputs{
				effectiveMode: managementmode.ModeUnmanaged,
				compliance:    compliant,
				present:       true,
			},
			ctype:  managementmode.ConditionGatewayAPICRDsManaged,
			status: operatorv1.ConditionFalse,
			reason: managementmode.ReasonUnmanaged,
		},
		{
			name: "present",
			input: gatewayAPIConditionInputs{
				effectiveMode: managementmode.ModeManaged,
				compliance:    compliant,
				present:       true,
				managed:       true,
			},
			ctype:  managementmode.ConditionGatewayAPICRDsPresent,
			status: operatorv1.ConditionTrue,
			reason: managementmode.ReasonCRDsFound,
		},
		{
			name: "absent",
			input: gatewayAPIConditionInputs{
				effectiveMode: managementmode.ModeManaged,
				compliance:    managementmode.ComplianceResult{Profile: profile},
				present:       false,
			},
			ctype:  managementmode.ConditionGatewayAPICRDsPresent,
			status: operatorv1.ConditionFalse,
			reason: managementmode.ReasonCRDsNotFound,
		},
		{
			name: "compliant",
			input: gatewayAPIConditionInputs{
				effectiveMode: managementmode.ModeManaged,
				compliance:    compliant,
				present:       true,
				managed:       true,
			},
			ctype:  managementmode.ConditionGatewayAPICRDsCompliant,
			status: operatorv1.ConditionTrue,
			reason: managementmode.ReasonVersionMatch,
		},
		{
			name: "version mismatch",
			input: gatewayAPIConditionInputs{
				effectiveMode: managementmode.ModeManaged,
				compliance:    managementmode.ComplianceResult{Compliant: false, Profile: profile, Message: "mismatch"},
				present:       true,
			},
			ctype:  managementmode.ConditionGatewayAPICRDsCompliant,
			status: operatorv1.ConditionFalse,
			reason: managementmode.ReasonVersionMismatch,
		},
		{
			name: "unmanaged compliance N/A",
			input: gatewayAPIConditionInputs{
				effectiveMode: managementmode.ModeUnmanaged,
				compliance:    compliant,
			},
			ctype:  managementmode.ConditionGatewayAPICRDsCompliant,
			status: operatorv1.ConditionUnknown,
			reason: managementmode.ReasonNotApplicable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conditions := computeGatewayAPIConditions(tc.input)
			var found operatorv1.OperatorCondition
			for _, c := range conditions {
				if c.Type == tc.ctype {
					found = c
					break
				}
			}
			assert.Equal(t, tc.status, found.Status)
			assert.Equal(t, tc.reason, found.Reason)
		})
	}
}

func TestTransition_unmanagedToManaged_nonCompliant_blocked(t *testing.T) {
	r := &reconciler{config: Config{GatewayAPIWithoutOLMEnabled: true}}
	compliance := managementmode.ComplianceResult{
		Compliant: false,
		Profile:   managementmode.SupportedProfile(),
		PartialSet: true,
	}
	current := managementmode.State{
		SpecMode:      managementmode.ModeManaged,
		EffectiveMode: managementmode.ModeUnmanaged,
	}
	result := r.stepTransition(t.Context(), managementmode.ModeManaged, current, compliance)
	assert.Equal(t, managementmode.TransitionPhaseAssessingCRDs, result.phase)
	assert.Equal(t, blockedRequeueInterval, result.requeueAfter)
}

func TestTransition_managedToUnmanaged_ordering(t *testing.T) {
	r := &reconciler{config: Config{GatewayAPIWithoutOLMEnabled: true}}
	current := managementmode.State{
		SpecMode:      managementmode.ModeUnmanaged,
		EffectiveMode: managementmode.ModeManaged,
		StackReady:    true,
		Managed:       true,
	}
	result := r.beginTransition(managementmode.ModeUnmanaged, transitionResult{
		effectiveMode: current.EffectiveMode,
		stackReady:  current.StackReady,
		managed:     current.Managed,
	})
	assert.Equal(t, managementmode.TransitionPhaseStoppingStack, result.phase)
	assert.False(t, result.stackReady)
}

func TestDesiredVAP_usesOperatorNamespace(t *testing.T) {
	const ns = "custom-ingress-operator"
	r := &reconciler{config: Config{OperatorNamespace: ns}}
	policy := r.desiredVAP()
	assert.Contains(t, policy.Spec.Validations[0].Expression, ns)
	assert.NotContains(t, policy.Spec.Validations[0].Expression, "openshift-ingress-operator")
}

func TestPatchGatewayAPIConditions_preservesTransitionTimeWhenStatusUnchanged(t *testing.T) {
	now := metav1.Now()
	existing := []operatorv1.OperatorCondition{{
		Type:               managementmode.ConditionGatewayAPICRDsManaged,
		Status:             operatorv1.ConditionTrue,
		Reason:             managementmode.ReasonManagedByCIO,
		Message:            "old message",
		LastTransitionTime: now,
	}}
	desired := []operatorv1.OperatorCondition{{
		Type:    managementmode.ConditionGatewayAPICRDsManaged,
		Status:  operatorv1.ConditionTrue,
		Reason:  managementmode.ReasonManagedByCIO,
		Message: "new message",
	}}
	patched := patchGatewayAPIConditions(existing, desired)
	assert.Equal(t, now, patched[0].LastTransitionTime)
	assert.Equal(t, "new message", patched[0].Message)
}

func TestPatchGatewayAPIConditions_updatesTransitionTimeWhenStatusChanges(t *testing.T) {
	now := metav1.Now()
	existing := []operatorv1.OperatorCondition{{
		Type:               managementmode.ConditionGatewayAPICRDsManaged,
		Status:             operatorv1.ConditionTrue,
		Reason:             managementmode.ReasonManagedByCIO,
		LastTransitionTime: now,
	}}
	desired := []operatorv1.OperatorCondition{{
		Type:   managementmode.ConditionGatewayAPICRDsManaged,
		Status: operatorv1.ConditionFalse,
		Reason: managementmode.ReasonUnmanaged,
	}}
	patched := patchGatewayAPIConditions(existing, desired)
	assert.NotEqual(t, now, patched[0].LastTransitionTime)
}

func TestPatchGatewayAPIConditions_stableOrdering(t *testing.T) {
	desired := computeGatewayAPIConditions(gatewayAPIConditionInputs{
		effectiveMode: managementmode.ModeManaged,
		compliance:    managementmode.ComplianceResult{Compliant: true, Profile: managementmode.SupportedProfile()},
		present:       true,
		managed:       true,
	})
	patched := patchGatewayAPIConditions(nil, desired)
	assert.Len(t, patched, 3)
	assert.Equal(t, managementmode.ConditionGatewayAPICRDsManaged, patched[0].Type)
	assert.Equal(t, managementmode.ConditionGatewayAPICRDsPresent, patched[1].Type)
	assert.Equal(t, managementmode.ConditionGatewayAPICRDsCompliant, patched[2].Type)
}
