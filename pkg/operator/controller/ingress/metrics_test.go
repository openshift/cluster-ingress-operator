package ingress

import (
	"strings"
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type metricValue struct {
	labels []string
	value  float64
}

func TestDeleteIngressControllerConditionsMetric(t *testing.T) {

	testCases := []struct {
		name                 string
		inputMetricValues    []metricValue // metrics which existed before the call
		inputIngress         *operatorv1.IngressController
		expectedMetricFormat string
	}{
		{
			name: "Nominal",
			inputMetricValues: []metricValue{
				{
					[]string{"default", "Available"},
					1.0,
				},
				{
					[]string{"default", "Degraded"},
					0.0,
				},
				{
					[]string{"test1", "Available"},
					0.0,
				},
				{
					[]string{"test1", "Degraded"},
					1.0,
				},
			},
			inputIngress: testIngressControllerWithConditions("test1", []operatorv1.OperatorCondition{
				{Type: "Available", Status: operatorv1.ConditionFalse},
				{Type: "Degraded", Status: operatorv1.ConditionTrue},
			}),
			expectedMetricFormat: `
            # HELP ingress_controller_conditions Report the conditions for ingress controllers. 0 is False and 1 is True.
            # TYPE ingress_controller_conditions gauge
            ingress_controller_conditions{condition="Available",name="default"} 1
            ingress_controller_conditions{condition="Degraded",name="default"} 0
            `,
		},
		{
			name: "Not reported conditions",
			inputMetricValues: []metricValue{
				{
					[]string{"default", "Available"},
					1.0,
				},
				{
					[]string{"default", "Degraded"},
					0.0,
				},
				{
					[]string{"test1", "Available"},
					0.0,
				},
				{
					[]string{"test1", "Degraded"},
					1.0,
				},
			},
			inputIngress: testIngressControllerWithConditions("test1", []operatorv1.OperatorCondition{
				{Type: "Available", Status: operatorv1.ConditionFalse},
				{Type: "Degraded", Status: operatorv1.ConditionTrue},
				{Type: "Admitted", Status: operatorv1.ConditionTrue},
				{Type: "PodsScheduled", Status: operatorv1.ConditionTrue},
			}),
			expectedMetricFormat: `
            # HELP ingress_controller_conditions Report the conditions for ingress controllers. 0 is False and 1 is True.
            # TYPE ingress_controller_conditions gauge
            ingress_controller_conditions{condition="Available",name="default"} 1
            ingress_controller_conditions{condition="Degraded",name="default"} 0
            `,
		},
		{
			name: "Conditions updated but not metrics",
			inputMetricValues: []metricValue{
				{
					[]string{"default", "Available"},
					1.0,
				},
				{
					[]string{"default", "Degraded"},
					0.0,
				},
			},
			// update managed to set the conditions but didn't reach the place where the metrics are set (deletion came before)
			inputIngress: testIngressControllerWithConditions("test1", []operatorv1.OperatorCondition{
				{Type: "Available", Status: operatorv1.ConditionFalse},
				{Type: "Degraded", Status: operatorv1.ConditionTrue},
			}),
			expectedMetricFormat: `
            # HELP ingress_controller_conditions Report the conditions for ingress controllers. 0 is False and 1 is True.
            # TYPE ingress_controller_conditions gauge
            ingress_controller_conditions{condition="Available",name="default"} 1
            ingress_controller_conditions{condition="Degraded",name="default"} 0
            `,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// cleanup the ingress condition metrics
			ingressControllerConditions.Reset()

			// fill the metric up with the input values
			for _, val := range tc.inputMetricValues {
				ingressControllerConditions.WithLabelValues(val.labels...).Set(val.value)
			}

			// check the testutil collected all the metrics
			gotNumMetrics := testutil.CollectAndCount(ingressControllerConditions)
			if gotNumMetrics != len(tc.inputMetricValues) {
				t.Errorf("collected a different number of metrics before deletion: expected %d, got %d", len(tc.inputMetricValues), gotNumMetrics)
				t.SkipNow()
			}

			DeleteIngressControllerConditionsMetric(tc.inputIngress)

			// Check the remaining metrics.
			err := testutil.CollectAndCompare(ingressControllerConditions, strings.NewReader(tc.expectedMetricFormat))
			if err != nil {
				t.Error(err)
			}
		})
	}
}

func testIngressControllerWithConditions(name string, conditions []operatorv1.OperatorCondition) *operatorv1.IngressController {
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: operatorv1.IngressControllerStatus{
			Conditions: conditions,
		},
	}
}
