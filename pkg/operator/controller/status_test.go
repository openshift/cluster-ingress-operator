package controller

import (
	"fmt"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestComputeOperatorStatusConditions(t *testing.T) {
	type testInputs struct {
		haveNamespace           bool
		numWanted, numAvailable int
	}
	type testOutputs struct {
		failing, progressing, available bool
	}
	testCases := []struct {
		description string
		inputs      testInputs
		outputs     testOutputs
	}{
		{description: "no operand namespace",
			inputs: testInputs{
				haveNamespace: false,
				numWanted:     1,
				numAvailable:  0,
			},
			outputs: testOutputs{
				failing:     true,
				progressing: true,
				available:   false,
			},
		},
		{description: "no ingresscontrollers available",
			inputs: testInputs{
				haveNamespace: true,
				numWanted:     1,
				numAvailable:  0,
			},
			outputs: testOutputs{
				failing:     false,
				progressing: true,
				available:   false,
			},
		},
		{description: "0/2 ingresscontrollers available",
			inputs: testInputs{
				haveNamespace: true,
				numWanted:     2,
				numAvailable:  0,
			},
			outputs: testOutputs{
				failing:     false,
				progressing: true,
				available:   false,
			},
		},
		{description: "1/2 ingresscontrollers available",
			inputs: testInputs{
				haveNamespace: true,
				numWanted:     2,
				numAvailable:  1,
			},
			outputs: testOutputs{
				failing:     false,
				progressing: true,
				available:   false,
			},
		},
		{description: "2/2 ingresscontrollers available",
			inputs: testInputs{
				haveNamespace: true,
				numWanted:     2,
				numAvailable:  2,
			},
			outputs: testOutputs{
				failing:     false,
				progressing: false,
				available:   true,
			},
		},
	}

	for _, tc := range testCases {
		var (
			namespace *corev1.Namespace
			ingresses []operatorv1.IngressController

			failing, progressing, available configv1.ConditionStatus
		)
		if tc.inputs.haveNamespace {
			namespace = &corev1.Namespace{}
		}
		ingressAvailable := operatorv1.OperatorCondition{
			Type:   operatorv1.IngressControllerAvailableConditionType,
			Status: operatorv1.ConditionTrue,
		}
		for i := 0; i < tc.inputs.numWanted; i++ {
			ingresses = append(ingresses,
				operatorv1.IngressController{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("ingresscontroller-%d", i+1),
					},
				})
		}
		for i := 0; i < tc.inputs.numAvailable; i++ {
			ingresses[i].Status.Conditions = []operatorv1.OperatorCondition{ingressAvailable}
		}
		if tc.outputs.failing {
			failing = configv1.ConditionTrue
		} else {
			failing = configv1.ConditionFalse
		}
		if tc.outputs.progressing {
			progressing = configv1.ConditionTrue
		} else {
			progressing = configv1.ConditionFalse
		}
		if tc.outputs.available {
			available = configv1.ConditionTrue
		} else {
			available = configv1.ConditionFalse
		}
		expected := []configv1.ClusterOperatorStatusCondition{
			{
				Type:   configv1.OperatorFailing,
				Status: failing,
			},
			{
				Type:   configv1.OperatorProgressing,
				Status: progressing,
			},
			{
				Type:   configv1.OperatorAvailable,
				Status: available,
			},
		}
		new := computeOperatorStatusConditions(
			[]configv1.ClusterOperatorStatusCondition{},
			namespace,
			ingresses,
		)
		gotExpected := true
		if len(new) != len(expected) {
			gotExpected = false
		}
		for _, conditionA := range new {
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
				expected, new)
		}
	}
}

func TestSetOperatorStatusCondition(t *testing.T) {
	testCases := []struct {
		description   string
		oldConditions []configv1.ClusterOperatorStatusCondition
		newCondition  *configv1.ClusterOperatorStatusCondition
		expected      []configv1.ClusterOperatorStatusCondition
	}{
		{
			description: "new condition",
			newCondition: &configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorAvailable,
				Status: configv1.ConditionTrue,
			},
			expected: []configv1.ClusterOperatorStatusCondition{
				{
					Type:   configv1.OperatorAvailable,
					Status: configv1.ConditionTrue,
				},
			},
		},
		{
			description: "existing condition, unchanged",
			oldConditions: []configv1.ClusterOperatorStatusCondition{
				{
					Type:   configv1.OperatorAvailable,
					Status: configv1.ConditionTrue,
				},
			},
			newCondition: &configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorAvailable,
				Status: configv1.ConditionTrue,
			},
			expected: []configv1.ClusterOperatorStatusCondition{
				{
					Type:   configv1.OperatorAvailable,
					Status: configv1.ConditionTrue,
				},
			},
		},
		{
			description: "existing conditions, one changed",
			oldConditions: []configv1.ClusterOperatorStatusCondition{
				{
					Type:   configv1.OperatorFailing,
					Status: configv1.ConditionFalse,
				},
				{
					Type:   configv1.OperatorProgressing,
					Status: configv1.ConditionFalse,
				},
				{
					Type:   configv1.OperatorAvailable,
					Status: configv1.ConditionFalse,
				},
			},
			newCondition: &configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorAvailable,
				Status: configv1.ConditionTrue,
			},
			expected: []configv1.ClusterOperatorStatusCondition{
				{
					Type:   configv1.OperatorFailing,
					Status: configv1.ConditionFalse,
				},
				{
					Type:   configv1.OperatorProgressing,
					Status: configv1.ConditionFalse,
				},
				{
					Type:   configv1.OperatorAvailable,
					Status: configv1.ConditionTrue,
				},
			},
		},
	}

	for _, tc := range testCases {
		actual := setOperatorStatusCondition(tc.oldConditions, tc.newCondition)
		a := configv1.ClusterOperatorStatus{Conditions: actual}
		b := configv1.ClusterOperatorStatus{Conditions: tc.expected}
		if !operatorStatusesEqual(a, b) {
			t.Fatalf("%q: expected %v, got %v", tc.description,
				tc.expected, actual)
		}
	}
}

func TestOperatorStatusesEqual(t *testing.T) {
	testCases := []struct {
		description string
		expected    bool
		a, b        configv1.ClusterOperatorStatus
	}{
		{
			description: "zero-valued ClusterOperatorStatus should be equal",
			expected:    true,
		},
		{
			description: "nil and non-nil slices are equal",
			expected:    true,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{},
			},
		},
		{
			description: "empty slices should be equal",
			expected:    true,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{},
			},
			b: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{},
			},
		},
		{
			description: "check no change in versions",
			expected:    true,
			a: configv1.ClusterOperatorStatus{
				Versions: []configv1.OperandVersion{
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
			b: configv1.ClusterOperatorStatus{
				Versions: []configv1.OperandVersion{
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
		},
		{
			description: "condition LastTransitionTime should be ignored",
			expected:    true,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:               configv1.OperatorAvailable,
						Status:             configv1.ConditionTrue,
						LastTransitionTime: metav1.Unix(0, 0),
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:               configv1.OperatorAvailable,
						Status:             configv1.ConditionTrue,
						LastTransitionTime: metav1.Unix(1, 0),
					},
				},
			},
		},
		{
			description: "order of versions should not matter",
			expected:    true,
			a: configv1.ClusterOperatorStatus{
				Versions: []configv1.OperandVersion{
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
			b: configv1.ClusterOperatorStatus{
				Versions: []configv1.OperandVersion{
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
		},
		{
			description: "check missing related objects",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				RelatedObjects: []configv1.ObjectReference{
					{
						Name: "openshift-ingress",
					},
					{
						Name: "default",
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				RelatedObjects: []configv1.ObjectReference{
					{
						Name: "default",
					},
				},
			},
		},
		{
			description: "check extra related objects",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				RelatedObjects: []configv1.ObjectReference{
					{
						Name: "default",
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				RelatedObjects: []configv1.ObjectReference{
					{
						Name: "openshift-ingress",
					},
					{
						Name: "default",
					},
				},
			},
		},
		{
			description: "check condition reason differs",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:   configv1.OperatorAvailable,
						Status: configv1.ConditionFalse,
						Reason: "foo",
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:   configv1.OperatorAvailable,
						Status: configv1.ConditionFalse,
						Reason: "bar",
					},
				},
			},
		},
		{
			description: "check duplicate with single condition",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:    configv1.OperatorAvailable,
						Message: "foo",
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
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
		},
		{
			description: "check duplicate with multiple conditions",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
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
			},
			b: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
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
		},
	}

	for _, tc := range testCases {
		if actual := operatorStatusesEqual(tc.a, tc.b); actual != tc.expected {
			t.Fatalf("%q: expected %v, got %v", tc.description, tc.expected, actual)
		}
	}
}
