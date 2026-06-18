package status

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/stretchr/testify/assert"
)

func TestComputeGatewayAPICRDsDegradedCondition_unmanagedSkipped(t *testing.T) {
	state := operatorState{
		gatewayAPIEffectiveModeUnmanaged: true,
		unmanagedGatewayAPICRDNames:      "foo",
	}
	cond := computeGatewayAPICRDsDegradedCondition(state)
	assert.Equal(t, configv1.ConditionStatus(""), cond.Status)
}

func TestComputeGatewayAPICRDsDegradedCondition_managedForeignCRDs(t *testing.T) {
	state := operatorState{
		unmanagedGatewayAPICRDNames: "tcproutes.gateway.networking.k8s.io",
	}
	cond := computeGatewayAPICRDsDegradedCondition(state)
	assert.Equal(t, configv1.ConditionTrue, cond.Status)
	assert.Equal(t, "GatewayAPICRDsDegraded", cond.Reason)
}
