package ingress

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

func TestDetermineReplicas(t *testing.T) {
	tests := []struct {
		name             string
		infraTopology    configv1.TopologyMode
		cpTopology       configv1.TopologyMode
		defaultPlacement configv1.DefaultPlacement
		expect           int32
	}{
		{
			name:          "HighlyAvailable infrastructure topology returns 2",
			infraTopology: configv1.HighlyAvailableTopologyMode,
			cpTopology:    configv1.HighlyAvailableTopologyMode,
			expect:        2,
		},
		{
			name:          "SingleReplica infrastructure topology returns 1",
			infraTopology: configv1.SingleReplicaTopologyMode,
			cpTopology:    configv1.HighlyAvailableTopologyMode,
			expect:        1,
		},
		{
			name:             "ControlPlane placement uses ControlPlaneTopology",
			infraTopology:    configv1.HighlyAvailableTopologyMode,
			cpTopology:       configv1.SingleReplicaTopologyMode,
			defaultPlacement: configv1.DefaultPlacementControlPlane,
			expect:           1,
		},
		{
			name:             "ControlPlane placement with HighlyAvailable CP returns 2",
			infraTopology:    configv1.SingleReplicaTopologyMode,
			cpTopology:       configv1.HighlyAvailableTopologyMode,
			defaultPlacement: configv1.DefaultPlacementControlPlane,
			expect:           2,
		},
		{
			name:          "empty topology defaults to 2",
			infraTopology: "",
			cpTopology:    "",
			expect:        2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			infraConfig := &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: tc.infraTopology,
					ControlPlaneTopology:   tc.cpTopology,
				},
			}
			ingressConfig := &configv1.Ingress{
				Status: configv1.IngressStatus{
					DefaultPlacement: tc.defaultPlacement,
				},
			}
			got := DetermineReplicas(ingressConfig, infraConfig)
			if got != tc.expect {
				t.Errorf("expected %d replicas, got %d", tc.expect, got)
			}
		})
	}
}
