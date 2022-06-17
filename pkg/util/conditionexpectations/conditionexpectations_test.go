package conditionexpectations

import (
	"reflect"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilclock "k8s.io/apimachinery/pkg/util/clock"
)

// TestCheck verifies that Check behaves as expected.
func TestCheck(t *testing.T) {
	fakeClock := utilclock.NewFakeClock(time.Time{})

	var (
		expectFooTrue = Expectation{
			ConditionType:   "Foo",
			ConditionStatus: operatorv1.ConditionTrue,
		}
		expectFooTrueWithFiveMinuteGrace = Expectation{
			ConditionType:   "Foo",
			ConditionStatus: operatorv1.ConditionTrue,
			GracePeriod:     5 * time.Minute,
		}
		expectBarTrueWithThreeMinuteGrace = Expectation{
			ConditionType:   "Bar",
			ConditionStatus: operatorv1.ConditionTrue,
			GracePeriod:     3 * time.Minute,
		}
		expectBazFalse = Expectation{
			ConditionType:   "Baz",
			ConditionStatus: operatorv1.ConditionFalse,
		}
		expectBazTrueIfFooTrue = Expectation{
			ConditionType:    "Baz",
			ConditionStatus:  operatorv1.ConditionTrue,
			IfConditionsTrue: []string{"Foo"},
		}
		fooFalse = operatorv1.OperatorCondition{
			Type:               "Foo",
			Status:             operatorv1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(fakeClock.Now()),
		}
		fooTrue = operatorv1.OperatorCondition{
			Type:               "Foo",
			Status:             operatorv1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(fakeClock.Now()),
		}
		barFalse = operatorv1.OperatorCondition{
			Type:               "Bar",
			Status:             operatorv1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(fakeClock.Now()),
		}
		bazTrue = operatorv1.OperatorCondition{
			Type:               "Baz",
			Status:             operatorv1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(fakeClock.Now()),
		}
		bazFalse = operatorv1.OperatorCondition{
			Type:               "Baz",
			Status:             operatorv1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(fakeClock.Now()),
		}
	)

	testCases := []struct {
		description                string
		expectations               Expectations
		conditions                 []operatorv1.OperatorCondition
		expectedGraceConditions    []*operatorv1.OperatorCondition
		expectedDegradedConditions []*operatorv1.OperatorCondition
		expectedTimeDuration       time.Duration
		now                        time.Time
	}{
		{
			description:                "empty expectations",
			expectations:               MakeExpectations([]Expectation{}),
			conditions:                 []operatorv1.OperatorCondition{},
			expectedGraceConditions:    nil,
			expectedDegradedConditions: nil,
			expectedTimeDuration:       time.Duration(0),
		},
		{
			description:                "nonempty, non-matching expectations",
			expectations:               MakeExpectations([]Expectation{expectFooTrue}),
			conditions:                 []operatorv1.OperatorCondition{barFalse},
			expectedGraceConditions:    nil,
			expectedDegradedConditions: nil,
			expectedTimeDuration:       time.Duration(0),
		},
		{
			description:                "satisfied expectation",
			expectations:               MakeExpectations([]Expectation{expectFooTrue}),
			conditions:                 []operatorv1.OperatorCondition{fooTrue},
			expectedGraceConditions:    nil,
			expectedDegradedConditions: nil,
			expectedTimeDuration:       time.Duration(0),
		},
		{
			description:                "unsatisfied expectation",
			expectations:               MakeExpectations([]Expectation{expectFooTrue}),
			conditions:                 []operatorv1.OperatorCondition{fooFalse},
			expectedGraceConditions:    nil,
			expectedDegradedConditions: []*operatorv1.OperatorCondition{&fooFalse},
			expectedTimeDuration:       time.Duration(0),
		},
		{
			description: "unsatisfied expectation with grace",
			expectations: MakeExpectations([]Expectation{
				expectFooTrueWithFiveMinuteGrace,
				expectBarTrueWithThreeMinuteGrace,
				expectBazFalse,
			}),
			conditions:                 []operatorv1.OperatorCondition{fooFalse, barFalse, bazTrue},
			expectedGraceConditions:    []*operatorv1.OperatorCondition{&fooFalse, &barFalse},
			expectedDegradedConditions: []*operatorv1.OperatorCondition{&bazTrue},
			expectedTimeDuration:       3 * time.Minute,
		},
		{
			description: "unsatisfied expectations, one with remaining grace and one expired",
			expectations: MakeExpectations([]Expectation{
				expectFooTrueWithFiveMinuteGrace,
				expectBarTrueWithThreeMinuteGrace,
				expectBazFalse,
			}),
			conditions:                 []operatorv1.OperatorCondition{fooFalse, barFalse, bazTrue},
			expectedGraceConditions:    []*operatorv1.OperatorCondition{&fooFalse},
			expectedDegradedConditions: []*operatorv1.OperatorCondition{&barFalse, &bazTrue},
			expectedTimeDuration:       2 * time.Minute,
			now:                        fakeClock.Now().Add(3 * time.Minute),
		},
		{
			description: "unsatisfied expectations with expired grace",
			expectations: MakeExpectations([]Expectation{
				expectFooTrueWithFiveMinuteGrace,
				expectBarTrueWithThreeMinuteGrace,
				expectBazFalse,
			}),
			conditions:                 []operatorv1.OperatorCondition{fooFalse, barFalse, bazTrue},
			expectedGraceConditions:    nil,
			expectedDegradedConditions: []*operatorv1.OperatorCondition{&fooFalse, &barFalse, &bazTrue},
			expectedTimeDuration:       time.Duration(0),
			now:                        fakeClock.Now().Add(10 * time.Minute),
		},
		{
			description:                "expectation with unsatisfied predicate",
			expectations:               MakeExpectations([]Expectation{expectBazTrueIfFooTrue}),
			conditions:                 []operatorv1.OperatorCondition{bazFalse},
			expectedGraceConditions:    nil,
			expectedDegradedConditions: nil,
			expectedTimeDuration:       time.Duration(0),
		},
		{
			description:                "unsatisfied expectation with satisfied predicate",
			expectations:               MakeExpectations([]Expectation{expectBazTrueIfFooTrue}),
			conditions:                 []operatorv1.OperatorCondition{fooTrue, bazFalse},
			expectedGraceConditions:    nil,
			expectedDegradedConditions: []*operatorv1.OperatorCondition{&bazFalse},
			expectedTimeDuration:       time.Duration(0),
		},
		{
			description:                "satisfied expectation with satisfied predicate",
			expectations:               MakeExpectations([]Expectation{expectBazTrueIfFooTrue}),
			conditions:                 []operatorv1.OperatorCondition{fooTrue, bazTrue},
			expectedGraceConditions:    nil,
			expectedDegradedConditions: nil,
			expectedTimeDuration:       time.Duration(0),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			actualGraceConditions, actualDegradedConditions, actualTimeDuration := MakeExpectations(tc.expectations).Check(tc.conditions, tc.now)
			if !reflect.DeepEqual(actualGraceConditions, tc.expectedGraceConditions) {
				expected := make([]operatorv1.OperatorCondition, 0, len(tc.expectedGraceConditions))
				for i := range tc.expectedGraceConditions {
					expected = append(expected, *tc.expectedGraceConditions[i])
				}
				actual := make([]operatorv1.OperatorCondition, 0, len(actualGraceConditions))
				for i := range actualGraceConditions {
					actual = append(actual, *actualGraceConditions[i])
				}
				t.Errorf("expected grace conditions: %+v, got: %+v", expected, actual)
			}
			if !reflect.DeepEqual(actualDegradedConditions, tc.expectedDegradedConditions) {
				expected := make([]operatorv1.OperatorCondition, 0, len(tc.expectedDegradedConditions))
				for i := range tc.expectedDegradedConditions {
					expected = append(expected, *tc.expectedDegradedConditions[i])
				}
				actual := make([]operatorv1.OperatorCondition, 0, len(actualDegradedConditions))
				for i := range actualDegradedConditions {
					actual = append(actual, *actualDegradedConditions[i])
				}
				t.Errorf("expected degraded conditions: %v, got: %v", expected, actual)
			}
			if !reflect.DeepEqual(actualTimeDuration, tc.expectedTimeDuration) {
				t.Errorf("expected time duration: %v, got: %v", tc.expectedTimeDuration, actualTimeDuration)
			}
		})
	}

}
