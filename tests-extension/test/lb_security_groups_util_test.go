package test

import (
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
)

func TestSecurityGroupsConverged(t *testing.T) {
	t.Parallel()

	const (
		sg1 operatorv1.SecurityGroupID = "sg-11111111"
		sg2 operatorv1.SecurityGroupID = "sg-22222222"
	)

	testCases := []struct {
		name                   string
		generation             int64
		observedGeneration     int64
		specSecurityGroups     []operatorv1.SecurityGroupID
		statusSecurityGroups   []operatorv1.SecurityGroupID
		expectedSecurityGroups []operatorv1.SecurityGroupID
		want                   bool
	}{
		{
			name:                   "converged",
			generation:             1,
			observedGeneration:     1,
			specSecurityGroups:     []operatorv1.SecurityGroupID{sg1},
			statusSecurityGroups:   []operatorv1.SecurityGroupID{sg1},
			expectedSecurityGroups: []operatorv1.SecurityGroupID{sg1},
			want:                   true,
		},
		{
			name:                   "stale observed generation",
			generation:             2,
			observedGeneration:     1,
			specSecurityGroups:     []operatorv1.SecurityGroupID{sg1},
			statusSecurityGroups:   []operatorv1.SecurityGroupID{sg1},
			expectedSecurityGroups: []operatorv1.SecurityGroupID{sg1},
			want:                   false,
		},
		{
			name:                   "unexpected spec security groups",
			generation:             1,
			observedGeneration:     1,
			specSecurityGroups:     []operatorv1.SecurityGroupID{sg1, sg2},
			statusSecurityGroups:   []operatorv1.SecurityGroupID{sg1},
			expectedSecurityGroups: []operatorv1.SecurityGroupID{sg1},
			want:                   false,
		},
		{
			name:                   "unexpected status security groups",
			generation:             1,
			observedGeneration:     1,
			specSecurityGroups:     []operatorv1.SecurityGroupID{sg1},
			statusSecurityGroups:   []operatorv1.SecurityGroupID{sg1, sg2},
			expectedSecurityGroups: []operatorv1.SecurityGroupID{sg1},
			want:                   false,
		},
		{
			name:                   "security group order differs",
			generation:             1,
			observedGeneration:     1,
			specSecurityGroups:     []operatorv1.SecurityGroupID{sg1, sg2},
			statusSecurityGroups:   []operatorv1.SecurityGroupID{sg2, sg1},
			expectedSecurityGroups: []operatorv1.SecurityGroupID{sg1, sg2},
			want:                   true,
		},
		{
			name:                   "security group multiplicity differs",
			generation:             1,
			observedGeneration:     1,
			specSecurityGroups:     []operatorv1.SecurityGroupID{sg1, sg1, sg2},
			statusSecurityGroups:   []operatorv1.SecurityGroupID{sg1, sg1, sg2},
			expectedSecurityGroups: []operatorv1.SecurityGroupID{sg1, sg2, sg2},
			want:                   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ic := newNLBIngressController("test", "test.example.com", tc.specSecurityGroups)
			status := newNLBIngressController("test", "test.example.com", tc.statusSecurityGroups)
			ic.Generation = tc.generation
			ic.Status.ObservedGeneration = tc.observedGeneration
			ic.Status.EndpointPublishingStrategy = status.Spec.EndpointPublishingStrategy

			if got := securityGroupsConverged(ic, tc.expectedSecurityGroups); got != tc.want {
				t.Errorf("securityGroupsConverged() = %t, want %t", got, tc.want)
			}
		})
	}
}
