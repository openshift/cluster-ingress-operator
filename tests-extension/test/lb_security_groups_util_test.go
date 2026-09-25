package test

import (
	"context"
	"errors"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type getClient struct {
	crclient.Client
	get func(context.Context, crclient.ObjectKey, crclient.Object, ...crclient.GetOption) error
}

func (c *getClient) Get(ctx context.Context, key crclient.ObjectKey, obj crclient.Object, opts ...crclient.GetOption) error {
	return c.get(ctx, key, obj, opts...)
}

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

func TestWaitForSecurityGroupsConvergedRetriesGetErrors(t *testing.T) {
	const sg operatorv1.SecurityGroupID = "sg-11111111"

	converged := newNLBIngressController("test", "test.example.com", []operatorv1.SecurityGroupID{sg})
	converged.Generation = 1
	converged.Status.ObservedGeneration = 1
	converged.Status.EndpointPublishingStrategy = converged.Spec.EndpointPublishingStrategy

	t.Run("retries read errors until convergence", func(t *testing.T) {
		readErr := errors.New("transient read failure")
		getCalls := 0
		client := &getClient{
			get: func(_ context.Context, _ crclient.ObjectKey, obj crclient.Object, _ ...crclient.GetOption) error {
				getCalls++
				if getCalls < 3 {
					return readErr
				}
				converged.DeepCopyInto(obj.(*operatorv1.IngressController))
				return nil
			},
		}

		c := &clients{client: client}
		err := c.waitForSecurityGroupsConvergedWithInterval(context.Background(), "test", []operatorv1.SecurityGroupID{sg}, time.Millisecond, 100*time.Millisecond)
		if err != nil {
			t.Fatalf("waitForSecurityGroupsConvergedWithInterval() error = %v", err)
		}
		if getCalls != 3 {
			t.Fatalf("waitForSecurityGroupsConvergedWithInterval() Get calls = %d, want 3", getCalls)
		}
	})

	t.Run("surfaces persistent read error after retries", func(t *testing.T) {
		firstReadErr := errors.New("first read failure")
		latestReadErr := errors.New("latest read failure")
		getCalls := 0
		client := &getClient{
			get: func(_ context.Context, _ crclient.ObjectKey, _ crclient.Object, _ ...crclient.GetOption) error {
				getCalls++
				if getCalls == 1 {
					return firstReadErr
				}
				return latestReadErr
			},
		}

		c := &clients{client: client}
		err := c.waitForSecurityGroupsConvergedWithInterval(context.Background(), "test", []operatorv1.SecurityGroupID{sg}, time.Millisecond, 20*time.Millisecond)
		if getCalls < 2 {
			t.Fatalf("waitForSecurityGroupsConvergedWithInterval() Get calls = %d, want at least 2", getCalls)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("waitForSecurityGroupsConvergedWithInterval() error = %v, want context deadline exceeded", err)
		}
		if !errors.Is(err, latestReadErr) {
			t.Errorf("waitForSecurityGroupsConvergedWithInterval() error = %v, want latest read error", err)
		}
		if errors.Is(err, firstReadErr) {
			t.Errorf("waitForSecurityGroupsConvergedWithInterval() error = %v, unexpectedly retained first read error", err)
		}
	})
}
