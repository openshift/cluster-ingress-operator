package controller

import (
	"fmt"
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestComputeIngressStatusConditions(t *testing.T) {
	testCases := []struct {
		description     string
		availRepl, repl int32
		condType        string
		condStatus      operatorv1.ConditionStatus
	}{
		{"0/2 deployment replicas available", 0, 2, operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionFalse},
		{"1/2 deployment replicas available", 1, 2, operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionTrue},
		{"2/2 deployment replicas available", 2, 2, operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionTrue},
	}

	for i, tc := range testCases {
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("ingress-controller-%d", i+1),
			},
			Status: appsv1.DeploymentStatus{
				Replicas:          tc.repl,
				AvailableReplicas: tc.availRepl,
			},
		}

		expected := []operatorv1.OperatorCondition{
			{
				Type:   tc.condType,
				Status: tc.condStatus,
			},
		}
		actual := computeIngressStatusConditions([]operatorv1.OperatorCondition{}, deploy)
		gotExpected := true
		if len(actual) != len(expected) {
			gotExpected = false
		}
		for _, conditionA := range actual {
			foundMatchingCondition := false

			for _, conditionB := range expected {
				if conditionA.Type == conditionB.Type &&
					conditionA.Status == conditionB.Status {
					foundMatchingCondition = true
					break
				}
			}

			if !foundMatchingCondition {
				gotExpected = false
			}
		}
		if !gotExpected {
			t.Fatalf("%q: expected %#v, got %#v", tc.description,
				expected, actual)
		}
	}
}

func TestSetIngressStatusCondition(t *testing.T) {
	testCases := []struct {
		description   string
		oldConditions []operatorv1.OperatorCondition
		newCondition  *operatorv1.OperatorCondition
		expected      []operatorv1.OperatorCondition
	}{
		{
			description: "new condition",
			newCondition: &operatorv1.OperatorCondition{
				Type:   operatorv1.IngressControllerAvailableConditionType,
				Status: operatorv1.ConditionTrue,
			},
			expected: []operatorv1.OperatorCondition{
				{
					Type:   operatorv1.IngressControllerAvailableConditionType,
					Status: operatorv1.ConditionTrue,
				},
			},
		},
		{
			description: "existing condition, unchanged",
			oldConditions: []operatorv1.OperatorCondition{
				{
					Type:   operatorv1.IngressControllerAvailableConditionType,
					Status: operatorv1.ConditionTrue,
				},
			},
			newCondition: &operatorv1.OperatorCondition{
				Type:   operatorv1.IngressControllerAvailableConditionType,
				Status: operatorv1.ConditionTrue,
			},
			expected: []operatorv1.OperatorCondition{
				{
					Type:   operatorv1.IngressControllerAvailableConditionType,
					Status: operatorv1.ConditionTrue,
				},
			},
		},
		{
			description: "existing condition changed",
			oldConditions: []operatorv1.OperatorCondition{
				{
					Type:   operatorv1.IngressControllerAvailableConditionType,
					Status: operatorv1.ConditionFalse,
				},
			},
			newCondition: &operatorv1.OperatorCondition{
				Type:   operatorv1.IngressControllerAvailableConditionType,
				Status: operatorv1.ConditionTrue,
			},
			expected: []operatorv1.OperatorCondition{
				{
					Type:   operatorv1.IngressControllerAvailableConditionType,
					Status: operatorv1.ConditionTrue,
				},
			},
		},
	}

	for _, tc := range testCases {
		actual := setIngressStatusCondition(tc.oldConditions, tc.newCondition)
		a := operatorv1.IngressControllerStatus{Conditions: actual}
		b := operatorv1.IngressControllerStatus{Conditions: tc.expected}
		if !ingressStatusesEqual(a, b) {
			t.Fatalf("%q: expected %v, got %v", tc.description,
				tc.expected, actual)
		}
	}
}

func TestIngressStatusesEqual(t *testing.T) {
	testCases := []struct {
		description string
		expected    bool
		a, b        operatorv1.IngressControllerStatus
	}{
		{
			description: "nil and non-nil slices are equal",
			expected:    true,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{},
			},
		},
		{
			description: "empty slices should be equal",
			expected:    true,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{},
			},
			b: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{},
			},
		},
		{
			description: "condition LastTransitionTime should be ignored",
			expected:    true,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:               operatorv1.IngressControllerAvailableConditionType,
						Status:             operatorv1.ConditionTrue,
						LastTransitionTime: metav1.Unix(0, 0),
					},
				},
			},
			b: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:               operatorv1.IngressControllerAvailableConditionType,
						Status:             operatorv1.ConditionTrue,
						LastTransitionTime: metav1.Unix(1, 0),
					},
				},
			},
		},
		{
			description: "check condition reason differs",
			expected:    false,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:   operatorv1.IngressControllerAvailableConditionType,
						Status: operatorv1.ConditionFalse,
						Reason: "foo",
					},
				},
			},
			b: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:   operatorv1.IngressControllerAvailableConditionType,
						Status: operatorv1.ConditionFalse,
						Reason: "bar",
					},
				},
			},
		},
		{
			description: "condition status differs",
			expected:    false,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:   operatorv1.IngressControllerAvailableConditionType,
						Status: operatorv1.ConditionTrue,
					},
				},
			},
			b: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:   operatorv1.IngressControllerAvailableConditionType,
						Status: operatorv1.ConditionFalse,
					},
				},
			},
		},
		{
			description: "check duplicate with single condition",
			expected:    false,
			a: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:    operatorv1.IngressControllerAvailableConditionType,
						Message: "foo",
					},
				},
			},
			b: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:    operatorv1.IngressControllerAvailableConditionType,
						Message: "foo",
					},
					{
						Type:    operatorv1.IngressControllerAvailableConditionType,
						Message: "foo",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		if actual := ingressStatusesEqual(tc.a, tc.b); actual != tc.expected {
			t.Fatalf("%q: expected %v, got %v", tc.description, tc.expected, actual)
		}
	}
}
