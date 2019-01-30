package util

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestElementsEqual(t *testing.T) {
	testCases := []struct {
		description string
		expected    bool
		a, b        interface{}
		cmpOpts     []cmp.Option
	}{
		{
			description: "nil slices should be equal",
			expected:    true,
		},
		{
			description: "nil and non-nil slices are unequal",
			expected:    false,
			a: []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: "v1",
				},
			},
		},
		{
			description: "empty slices should be equal",
			expected:    true,
			a:           []int{},
			b:           []int{},
		},
		{
			description: "check no change in versions",
			expected:    true,
			a: []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: "v1",
				},
				{
					Name:    "router",
					Version: "v2",
				},
			},
			b: []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: "v1",
				},
				{
					Name:    "router",
					Version: "v2",
				},
			},
		},
		{
			description: "condition LastTransitionTime should be ignored",
			expected:    true,
			a: []configv1.ClusterOperatorStatusCondition{
				{
					Type:               configv1.OperatorAvailable,
					Status:             configv1.ConditionTrue,
					LastTransitionTime: metav1.Unix(0, 0),
				},
			},
			b: []configv1.ClusterOperatorStatusCondition{
				{
					Type:               configv1.OperatorAvailable,
					Status:             configv1.ConditionTrue,
					LastTransitionTime: metav1.Unix(1, 0),
				},
			},
			cmpOpts: []cmp.Option{
				cmpopts.IgnoreFields(configv1.ClusterOperatorStatusCondition{}, "LastTransitionTime"),
			},
		},
		{
			description: "order of versions should not matter",
			expected:    true,
			a: []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: "v1",
				},
				{
					Name:    "router",
					Version: "v2",
				},
			},
			b: []configv1.OperandVersion{
				{
					Name:    "router",
					Version: "v2",
				},
				{
					Name:    "operator",
					Version: "v1",
				},
			},
		},
		{
			description: "check missing related objects",
			expected:    false,
			a: []configv1.ObjectReference{
				{
					Name: "openshift-ingress",
				},
				{
					Name: "default",
				},
			},
			b: []configv1.ObjectReference{
				{
					Name: "default",
				},
			},
		},
		{
			description: "check extra related objects",
			expected:    false,
			a: []configv1.ObjectReference{
				{
					Name: "default",
				},
			},
			b: []configv1.ObjectReference{
				{
					Name: "openshift-ingress",
				},
				{
					Name: "default",
				},
			},
		},
		{
			description: "check condition reason differs",
			expected:    false,
			a: []configv1.ClusterOperatorStatusCondition{
				{
					Type:   configv1.OperatorAvailable,
					Status: configv1.ConditionFalse,
					Reason: "foo",
				},
			},
			b: []configv1.ClusterOperatorStatusCondition{
				{
					Type:   configv1.OperatorAvailable,
					Status: configv1.ConditionFalse,
					Reason: "bar",
				},
			},
		},
		{
			description: "check duplicate with single condition",
			expected:    false,
			a: []configv1.ClusterOperatorStatusCondition{
				{
					Type:    configv1.OperatorAvailable,
					Message: "foo",
				},
			},
			b: []configv1.ClusterOperatorStatusCondition{
				{
					Type:    configv1.OperatorAvailable,
					Message: "foo",
				},
				{
					Type:    configv1.OperatorAvailable,
					Message: "foo",
				},
			},
		},
		{
			description: "check duplicate with multiple conditions",
			expected:    false,
			a: []configv1.ClusterOperatorStatusCondition{
				{
					Type: configv1.OperatorAvailable,
				},
				{
					Type: configv1.OperatorProgressing,
				},
				{
					Type: configv1.OperatorAvailable,
				},
			},
			b: []configv1.ClusterOperatorStatusCondition{
				{
					Type: configv1.OperatorProgressing,
				},
				{
					Type: configv1.OperatorAvailable,
				},
				{
					Type: configv1.OperatorProgressing,
				},
			},
		},
	}

	for _, tc := range testCases {
		actual, err := ElementsEqual(tc.a, tc.b, tc.cmpOpts)
		if err != nil {
			t.Fatalf("%q failed: %v", tc.description, err)
		}
		if actual != tc.expected {
			t.Fatalf("%q: expected %v, got %v", tc.description,
				tc.expected, actual)
		}
	}
}
