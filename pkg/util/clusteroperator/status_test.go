package clusteroperator

import (
	"testing"

	osv1 "github.com/openshift/cluster-version-operator/pkg/apis/operatorstatus.openshift.io/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetStatusCondition(t *testing.T) {
	testCases := []struct {
		description   string
		oldConditions []osv1.ClusterOperatorStatusCondition
		newCondition  *osv1.ClusterOperatorStatusCondition
		expected      []osv1.ClusterOperatorStatusCondition
	}{
		{
			description: "new condition",
			newCondition: &osv1.ClusterOperatorStatusCondition{
				Type:   osv1.OperatorAvailable,
				Status: osv1.ConditionTrue,
			},
			expected: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorAvailable,
					Status: osv1.ConditionTrue,
				},
			},
		},
		{
			description: "existing condition, unchanged",
			oldConditions: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorAvailable,
					Status: osv1.ConditionTrue,
				},
			},
			newCondition: &osv1.ClusterOperatorStatusCondition{
				Type:   osv1.OperatorAvailable,
				Status: osv1.ConditionTrue,
			},
			expected: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorAvailable,
					Status: osv1.ConditionTrue,
				},
			},
		},
		{
			description: "existing conditions, one changed",
			oldConditions: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorFailing,
					Status: osv1.ConditionFalse,
				},
				{
					Type:   osv1.OperatorProgressing,
					Status: osv1.ConditionFalse,
				},
				{
					Type:   osv1.OperatorAvailable,
					Status: osv1.ConditionFalse,
				},
			},
			newCondition: &osv1.ClusterOperatorStatusCondition{
				Type:   osv1.OperatorAvailable,
				Status: osv1.ConditionTrue,
			},
			expected: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorFailing,
					Status: osv1.ConditionFalse,
				},
				{
					Type:   osv1.OperatorProgressing,
					Status: osv1.ConditionFalse,
				},
				{
					Type:   osv1.OperatorAvailable,
					Status: osv1.ConditionTrue,
				},
			},
		},
	}

	for _, tc := range testCases {
		actual := SetStatusCondition(tc.oldConditions, tc.newCondition)
		if !ConditionsEqual(actual, tc.expected) {
			t.Fatalf("%q: expected %v, got %v", tc.description,
				tc.expected, actual)
		}
	}
}

func TestConditionsEqual(t *testing.T) {
	testCases := []struct {
		description string
		expected    bool
		a, b        []osv1.ClusterOperatorStatusCondition
	}{
		{
			description: "empty statuses should be equal",
			expected:    true,
		},
		{
			description: "condition LastTransitionTime should be ignored",
			expected:    true,
			a: []osv1.ClusterOperatorStatusCondition{
				{
					Type:               osv1.OperatorAvailable,
					Status:             osv1.ConditionTrue,
					LastTransitionTime: metav1.Unix(0, 0),
				},
			},
			b: []osv1.ClusterOperatorStatusCondition{
				{
					Type:               osv1.OperatorAvailable,
					Status:             osv1.ConditionTrue,
					LastTransitionTime: metav1.Unix(1, 0),
				},
			},
		},
		{
			description: "order of conditions should not matter",
			expected:    true,
			a: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorAvailable,
					Status: osv1.ConditionTrue,
				},
				{
					Type:   osv1.OperatorProgressing,
					Status: osv1.ConditionTrue,
				},
			},
			b: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorProgressing,
					Status: osv1.ConditionTrue,
				},
				{
					Type:   osv1.OperatorAvailable,
					Status: osv1.ConditionTrue,
				},
			},
		},
		{
			description: "check missing condition",
			expected:    false,
			a: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorProgressing,
					Status: osv1.ConditionTrue,
				},
			},
			b: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorAvailable,
					Status: osv1.ConditionTrue,
				},
				{
					Type:   osv1.OperatorProgressing,
					Status: osv1.ConditionTrue,
				},
			},
		},
		{
			description: "check condition reason differs",
			expected:    false,
			a: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorAvailable,
					Status: osv1.ConditionFalse,
					Reason: "foo",
				},
			},
			b: []osv1.ClusterOperatorStatusCondition{
				{
					Type:   osv1.OperatorAvailable,
					Status: osv1.ConditionFalse,
					Reason: "bar",
				},
			},
		},
		{
			description: "check condition message differs",
			expected:    false,
			a: []osv1.ClusterOperatorStatusCondition{
				{
					Type:    osv1.OperatorAvailable,
					Status:  osv1.ConditionFalse,
					Message: "foo",
				},
			},
			b: []osv1.ClusterOperatorStatusCondition{
				{
					Type:    osv1.OperatorAvailable,
					Status:  osv1.ConditionFalse,
					Message: "bar",
				},
			},
		},
	}

	for _, tc := range testCases {
		actual := ConditionsEqual(tc.a, tc.b)
		if actual != tc.expected {
			t.Fatalf("%q: expected %v, got %v", tc.description,
				tc.expected, actual)
		}
	}
}
