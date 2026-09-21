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
		workerCount      int32
		expect           int32
	}{
		{
			name:          "HighlyAvailable infrastructure topology returns 2",
			infraTopology: configv1.HighlyAvailableTopologyMode,
			cpTopology:    configv1.HighlyAvailableTopologyMode,
			workerCount:   3,
			expect:        2,
		},
		{
			name:          "SingleReplica infrastructure topology returns 1",
			infraTopology: configv1.SingleReplicaTopologyMode,
			cpTopology:    configv1.HighlyAvailableTopologyMode,
			workerCount:   1,
			expect:        1,
		},
		{
			name:             "ControlPlane placement uses ControlPlaneTopology",
			infraTopology:    configv1.HighlyAvailableTopologyMode,
			cpTopology:       configv1.SingleReplicaTopologyMode,
			defaultPlacement: configv1.DefaultPlacementControlPlane,
			workerCount:      3,
			expect:           1,
		},
		{
			name:             "ControlPlane placement with HighlyAvailable CP returns 2",
			infraTopology:    configv1.SingleReplicaTopologyMode,
			cpTopology:       configv1.HighlyAvailableTopologyMode,
			defaultPlacement: configv1.DefaultPlacementControlPlane,
			workerCount:      3,
			expect:           2,
		},
		{
			name:          "empty topology defaults to 2",
			infraTopology: "",
			cpTopology:    "",
			workerCount:   3,
			expect:        2,
		},
		{
			name:          "External CP topology with zero workers returns 0",
			infraTopology: configv1.HighlyAvailableTopologyMode,
			cpTopology:    configv1.ExternalTopologyMode,
			workerCount:   0,
			expect:        0,
		},
		{
			name:          "External CP topology with nonzero workers returns 2",
			infraTopology: configv1.HighlyAvailableTopologyMode,
			cpTopology:    configv1.ExternalTopologyMode,
			workerCount:   3,
			expect:        2,
		},
		{
			name:          "Non-external CP topology with zero workers returns 2",
			infraTopology: configv1.HighlyAvailableTopologyMode,
			cpTopology:    configv1.HighlyAvailableTopologyMode,
			workerCount:   0,
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
			got := DetermineReplicas(ingressConfig, infraConfig, tc.workerCount)
			if got != tc.expect {
				t.Errorf("expected %d replicas, got %d", tc.expect, got)
			}
		})
	}
}
