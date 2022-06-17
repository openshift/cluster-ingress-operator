package conditionexpectations

import (
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
)

// Expectation contains a condition that is expected to have a specific status.
// The expectation may be contingent on the presence of some other conditions.
// The expectation also may specify a grace period, indicating that violating
// the expectation is permitted as long as the elapsed time since the
// condition's last transition time is within the grace period.
type Expectation struct {
	// ConditionType is the type of the expected condition.
	ConditionType string
	// ConditionStatus is the expected status of the condition.
	ConditionStatus operatorv1.ConditionStatus
	// IfConditionsTrue is a list of prerequisite conditions that should be
	// true or else the condition is not checked.
	IfConditionsTrue []string
	// GracePeriod of the period of time following the condition's last
	// transition time, during which time it is permitted that this
	// expectation not be met.
	GracePeriod time.Duration
}

// Expectations specifies a set of expectations for operator conditions.
type Expectations []Expectation

// MakeExpectations makes an Expectations object from the provided Expectation
// values.  Each Expectation must have a unique ConditionType.
func MakeExpectations(expectations []Expectation) Expectations {
	return Expectations(expectations)
}

// Check examines the provided operator conditions to determine whether each has
// the expected status.  If an operator condition has no expectation with a
// matching type, the condition is ignored.  Check then returns two lists of
// pointers to those conditions and a time duration value:
//
// * The first list contains pointers to all conditions that did not have the
// expected status but are still within their respective grace periods.
//
// * The second list contains pointers to all conditions that did not have the
// expected status and have exceeded their respective grace periods.
//
// * The time duration value has the longest remaining grace period of any of
// the conditions in the first list, or 0 if no conditions are in grace.
//
// It is intended that the caller uses these results as follows:
//
// * If both lists are empty, then all is as expected, and no action is needed.
//
// * If the second list is empty, then the caller may warn the user about a
// potential issue.
//
// * If the second list is not empty, then the caller should alert the user that
// expectations were not met, and some user action may be required.
func (expectedConds Expectations) Check(conditions []operatorv1.OperatorCondition, now time.Time) ([]*operatorv1.OperatorCondition, []*operatorv1.OperatorCondition, time.Duration) {
	var graceConditions, degradedConditions []*operatorv1.OperatorCondition
	var requeueAfter time.Duration

	conditionsMap := make(map[string]*operatorv1.OperatorCondition)
	for i := range conditions {
		conditionsMap[conditions[i].Type] = &conditions[i]
	}

	for _, expected := range expectedConds {
		condition, haveCondition := conditionsMap[expected.ConditionType]
		if !haveCondition {
			continue
		}
		if condition.Status == expected.ConditionStatus {
			continue
		}

		failedPredicates := false
		for _, ifCond := range expected.IfConditionsTrue {
			predicate, havePredicate := conditionsMap[ifCond]
			if !havePredicate || predicate.Status != operatorv1.ConditionTrue {
				failedPredicates = true
				break
			}
		}
		if failedPredicates {
			continue
		}

		if expected.GracePeriod != 0 {
			t1 := now.Add(-expected.GracePeriod)
			t2 := condition.LastTransitionTime
			if t2.After(t1) {
				d := t2.Sub(t1)
				if len(graceConditions) == 0 || d < requeueAfter {
					// Recompute status conditions again
					// after the grace period has elapsed.
					requeueAfter = d
				}
				graceConditions = append(graceConditions, condition)
				continue
			}
		}

		degradedConditions = append(degradedConditions, condition)
	}

	return graceConditions, degradedConditions, requeueAfter
}
