package conditionexpectations_test

import (
	"fmt"
	"time"

	"github.com/openshift/cluster-ingress-operator/pkg/util/conditionexpectations"

	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilclock "k8s.io/apimachinery/pkg/util/clock"
)

func print(gcs, dcs []*operatorv1.OperatorCondition, d time.Duration) {
	var gss, dss []string
	for i := range gcs {
		gss = append(gss, fmt.Sprintf("%v=%v", gcs[i].Type, gcs[i].Status))
	}
	for i := range dcs {
		dss = append(dss, fmt.Sprintf("%v=%v", dcs[i].Type, dcs[i].Status))
	}
	fmt.Println(gss, dss, d)
}

// ExamplePredicate shows how Expectations can be used to check two example
// status conditions where one condition depends on the other.  In this example,
// the first condition indicates whether the operator is managing some resource,
// and the second condition reports whether that resource is available.
func ExamplePredicate() {
	expectations := conditionexpectations.MakeExpectations([]conditionexpectations.Expectation{{
		ConditionType:    "FooAvailable",
		ConditionStatus:  operatorv1.ConditionTrue,
		IfConditionsTrue: []string{"FooManaged"},
	}})

	fmt.Println("If FooManaged=False:")

	conditions := []operatorv1.OperatorCondition{
		{
			Type:   "FooManaged",
			Status: operatorv1.ConditionFalse,
		},
		{
			Type:   "FooAvailable",
			Status: operatorv1.ConditionFalse,
		},
	}
	print(expectations.Check(conditions, time.Now()))

	fmt.Println("If FooManaged=True and FooAvailable=False:")

	conditions = []operatorv1.OperatorCondition{{
		Type:   "FooManaged",
		Status: operatorv1.ConditionTrue,
	}, {
		Type:   "FooAvailable",
		Status: operatorv1.ConditionFalse,
	}}
	print(expectations.Check(conditions, time.Now()))

	fmt.Println("If FooAvailable=True:")

	conditions = []operatorv1.OperatorCondition{{
		Type:   "FooManaged",
		Status: operatorv1.ConditionFalse,
	}, {
		Type:   "FooAvailable",
		Status: operatorv1.ConditionFalse,
	}}
	print(expectations.Check(conditions, time.Now()))

	// Output: If FooManaged=False:
	// [] [] 0s
	// If FooManaged=True and FooAvailable=False:
	// [] [FooAvailable=False] 0s
	// If FooAvailable=True:
	// [] [] 0s
}

// ExampleGrace shows how Expectations can be used to check a set of status
// conditions where one of the status conditions has a grace period.
func ExampleGrace() {
	expectations := conditionexpectations.MakeExpectations([]conditionexpectations.Expectation{{
		ConditionType:   "FooAvailable",
		ConditionStatus: operatorv1.ConditionTrue,
	}, {
		ConditionType:   "BarAvailable",
		ConditionStatus: operatorv1.ConditionTrue,
	}, {
		ConditionType:   "BazAvailable",
		ConditionStatus: operatorv1.ConditionTrue,
		GracePeriod:     1 * time.Minute,
	}})

	fakeClock := utilclock.NewFakeClock(time.Time{})

	fmt.Println("If all conditions are satisfied:")

	conditions := []operatorv1.OperatorCondition{
		{
			Type:   "FooAvailable",
			Status: operatorv1.ConditionTrue,
		},
		{
			Type:   "BarAvailable",
			Status: operatorv1.ConditionTrue,
		},
		{
			Type:   "BazAvailable",
			Status: operatorv1.ConditionTrue,
		},
	}
	print(expectations.Check(conditions, fakeClock.Now()))

	fmt.Println(`If the "Baz" condition is unsatisfied but in grace:`)

	conditions = []operatorv1.OperatorCondition{
		{
			Type:   "FooAvailable",
			Status: operatorv1.ConditionTrue,
		},
		{
			Type:   "BarAvailable",
			Status: operatorv1.ConditionTrue,
		},
		{
			Type:               "BazAvailable",
			Status:             operatorv1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-10 * time.Second)),
		},
	}
	print(expectations.Check(conditions, fakeClock.Now()))

	fmt.Println(`If the "Baz" condition is unsatisfied and past grace:`)

	conditions = []operatorv1.OperatorCondition{
		{
			Type:   "FooAvailable",
			Status: operatorv1.ConditionTrue,
		},
		{
			Type:   "BarAvailable",
			Status: operatorv1.ConditionTrue,
		},
		{
			Type:               "BazAvailable",
			Status:             operatorv1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-2 * time.Minute)),
		},
	}
	print(expectations.Check(conditions, fakeClock.Now()))

	// Output: If all conditions are satisfied:
	// [] [] 0s
	// If the "Baz" condition is unsatisfied but in grace:
	// [BazAvailable=False] [] 50s
	// If the "Baz" condition is unsatisfied and past grace:
	// [] [BazAvailable=False] 0s
}
