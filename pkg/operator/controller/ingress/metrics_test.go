package ingress

import (
	"fmt"
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

func Test_DeleteIngressControllerConditionsMetric(t *testing.T) {

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

func Test_SetIngressControllerNLBMetric(t *testing.T) {
	testCases := []struct {
		name                 string
		inputIngress         *operatorv1.IngressController
		expectedMetricFormat string
	}{
		{
			name: "nlb metrics happy path",
			inputIngress: testIngressControllerWithEndpointPublishingStrategy("test1",
				&operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						Type: operatorv1.AWSLoadBalancerProvider,
						AWS: &operatorv1.AWSLoadBalancerParameters{
							Type: operatorv1.AWSNetworkLoadBalancer,
						}},
				}}),
			expectedMetricFormat: `
			# HELP ingress_controller_aws_nlb_active Report the number of active NLBs on AWS clusters.
			# TYPE ingress_controller_aws_nlb_active gauge
			ingress_controller_aws_nlb_active{name="test1"} 1
			`,
		},
		{
			name: "classic ELB metrics happy path",
			inputIngress: testIngressControllerWithEndpointPublishingStrategy("test1",
				&operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						Type: operatorv1.AWSLoadBalancerProvider,
						AWS: &operatorv1.AWSLoadBalancerParameters{
							Type: operatorv1.AWSClassicLoadBalancer,
						}},
				}}),
			expectedMetricFormat: `
			# HELP ingress_controller_aws_nlb_active Report the number of active NLBs on AWS clusters.
			# TYPE ingress_controller_aws_nlb_active gauge
			ingress_controller_aws_nlb_active{name="test1"} 0
			`,
		},
		{
			name:         "no endpoint publishing strategy",
			inputIngress: testIngressControllerWithEndpointPublishingStrategy("test1", nil),
			expectedMetricFormat: `
			# HELP ingress_controller_aws_nlb_active Report the number of active NLBs on AWS clusters.
			# TYPE ingress_controller_aws_nlb_active gauge
			ingress_controller_aws_nlb_active{name="test1"} 0
			`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// cleanup the ingress condition metrics
			activeNLBs.Reset()

			SetIngressControllerNLBMetric(tc.inputIngress)

			err := testutil.CollectAndCompare(activeNLBs, strings.NewReader(tc.expectedMetricFormat))
			if err != nil {
				t.Error(err)
			}

			DeleteActiveNLBMetrics(tc.inputIngress)
			err = testutil.CollectAndCompare(activeNLBs, strings.NewReader(""))
			if err != nil {
				t.Error(err)
			}
		})
	}
}

func awsLBStatus(scope operatorv1.LoadBalancerScope, lbType operatorv1.AWSLoadBalancerType, protocol operatorv1.NLBProtocol) *operatorv1.EndpointPublishingStrategy {
	eps := &operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
		Scope: scope,
		ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.AWSLoadBalancerProvider,
			AWS:  &operatorv1.AWSLoadBalancerParameters{Type: lbType},
		},
	}}
	if len(protocol) > 0 {
		eps.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{Protocol: protocol}
	}
	return eps
}

func Test_SetNLBHairpinRiskMetric(t *testing.T) {
	testCases := []struct {
		name          string
		inputIngress  *operatorv1.IngressController
		expectedValue float64
	}{
		{
			name: "internal NLB, protocol empty — at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1", nil,
				awsLBStatus(operatorv1.InternalLoadBalancer, operatorv1.AWSNetworkLoadBalancer, "")),
			expectedValue: 1,
		},
		{
			name: "internal NLB, protocol TCP — not at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1", nil,
				awsLBStatus(operatorv1.InternalLoadBalancer, operatorv1.AWSNetworkLoadBalancer, operatorv1.NLBProtocolTCP)),
			expectedValue: 0,
		},
		{
			name: "internal NLB, protocol PROXY — not at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1", nil,
				awsLBStatus(operatorv1.InternalLoadBalancer, operatorv1.AWSNetworkLoadBalancer, operatorv1.NLBProtocolProxy)),
			expectedValue: 0,
		},
		{
			name: "external NLB, protocol TCP — not at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1", nil,
				awsLBStatus(operatorv1.ExternalLoadBalancer, operatorv1.AWSNetworkLoadBalancer, operatorv1.NLBProtocolTCP)),
			expectedValue: 0,
		},
		{
			name: "external NLB, protocol empty — not at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1", nil,
				awsLBStatus(operatorv1.ExternalLoadBalancer, operatorv1.AWSNetworkLoadBalancer, "")),
			expectedValue: 0,
		},
		{
			name: "internal CLB — not at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1", nil,
				awsLBStatus(operatorv1.InternalLoadBalancer, operatorv1.AWSClassicLoadBalancer, "")),
			expectedValue: 0,
		},
		{
			name:          "no endpoint publishing strategy — not at risk",
			inputIngress:  testIngressControllerWithSpecAndStatus("test1", nil, nil),
			expectedValue: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nlbHairpinRisk.Reset()

			SetNLBHairpinRiskMetric(tc.inputIngress)

			expectedMetricFormat := fmt.Sprintf(`
			# HELP ingress_controller_aws_nlb_hairpin_risk Reports whether an IngressController using an internal AWS NLB has no explicit protocol setting and may be affected by hairpin connection failures. 0 is no risk, 1 is at risk.
			# TYPE ingress_controller_aws_nlb_hairpin_risk gauge
			ingress_controller_aws_nlb_hairpin_risk{name="test1"} %v
			`, tc.expectedValue)
			if err := testutil.CollectAndCompare(nlbHairpinRisk, strings.NewReader(expectedMetricFormat)); err != nil {
				t.Error(err)
			}

			DeleteNLBHairpinRiskMetric(tc.inputIngress)
			if err := testutil.CollectAndCompare(nlbHairpinRisk, strings.NewReader("")); err != nil {
				t.Error(err)
			}
		})
	}
}

func Test_RecordDeploymentAvailableTransition(t *testing.T) {
	condition := func(status operatorv1.ConditionStatus) operatorv1.OperatorCondition {
		return operatorv1.OperatorCondition{Type: IngressControllerDeploymentAvailableConditionType, Status: status}
	}

	testCases := []struct {
		name          string
		icName        string
		previous      operatorv1.OperatorCondition
		current       operatorv1.OperatorCondition
		expectedCount float64
	}{
		{
			name:          "status unchanged",
			icName:        "test1",
			previous:      condition(operatorv1.ConditionTrue),
			current:       condition(operatorv1.ConditionTrue),
			expectedCount: 0,
		},
		{
			name:          "status flipped true to false",
			icName:        "test1",
			previous:      condition(operatorv1.ConditionTrue),
			current:       condition(operatorv1.ConditionFalse),
			expectedCount: 1,
		},
		{
			name:          "status flipped false to true",
			icName:        "test1",
			previous:      condition(operatorv1.ConditionFalse),
			current:       condition(operatorv1.ConditionTrue),
			expectedCount: 1,
		},
		{
			name:          "no previous observation is not counted as a transition",
			icName:        "test1",
			previous:      operatorv1.OperatorCondition{},
			current:       condition(operatorv1.ConditionFalse),
			expectedCount: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			deploymentAvailableTransitions.Reset()

			RecordDeploymentAvailableTransition(tc.icName, tc.previous, tc.current)

			expectedMetricFormat := ""
			if tc.expectedCount != 0 {
				expectedMetricFormat = fmt.Sprintf(`
				# HELP ingress_controller_deployment_available_transitions_total Reports the cumulative number of times the DeploymentAvailable status condition has changed status for an IngressController. A high rate indicates the underlying deployment is flapping between available and unavailable.
				# TYPE ingress_controller_deployment_available_transitions_total counter
				ingress_controller_deployment_available_transitions_total{name="%s"} %v
				`, tc.icName, tc.expectedCount)
			}
			if err := testutil.CollectAndCompare(deploymentAvailableTransitions, strings.NewReader(expectedMetricFormat)); err != nil {
				t.Error(err)
			}

			DeleteDeploymentAvailableTransitionsMetric(testIngressControllerWithConditions(tc.icName, nil))
		})
	}
}

func Test_RecordDeploymentAvailableTransition_Cumulative(t *testing.T) {
	deploymentAvailableTransitions.Reset()
	defer deploymentAvailableTransitions.Reset()

	condition := func(status operatorv1.ConditionStatus) operatorv1.OperatorCondition {
		return operatorv1.OperatorCondition{Type: IngressControllerDeploymentAvailableConditionType, Status: status}
	}

	// Simulate a deployment flapping repeatedly; each flip should increment
	// the counter, demonstrating that the flapping remains visible via this
	// metric even though it may be masked by the Available/Degraded grace
	// period.
	RecordDeploymentAvailableTransition("test1", condition(operatorv1.ConditionTrue), condition(operatorv1.ConditionFalse))
	RecordDeploymentAvailableTransition("test1", condition(operatorv1.ConditionFalse), condition(operatorv1.ConditionTrue))
	RecordDeploymentAvailableTransition("test1", condition(operatorv1.ConditionTrue), condition(operatorv1.ConditionFalse))

	expectedMetricFormat := `
	# HELP ingress_controller_deployment_available_transitions_total Reports the cumulative number of times the DeploymentAvailable status condition has changed status for an IngressController. A high rate indicates the underlying deployment is flapping between available and unavailable.
	# TYPE ingress_controller_deployment_available_transitions_total counter
	ingress_controller_deployment_available_transitions_total{name="test1"} 3
	`
	if err := testutil.CollectAndCompare(deploymentAvailableTransitions, strings.NewReader(expectedMetricFormat)); err != nil {
		t.Error(err)
	}
}

func testIngressControllerWithSpecAndStatus(name string, spec, status *operatorv1.EndpointPublishingStrategy) *operatorv1.IngressController {
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: operatorv1.IngressControllerSpec{
			EndpointPublishingStrategy: spec,
		},
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: status,
		},
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

func testIngressControllerWithEndpointPublishingStrategy(name string, eps *operatorv1.EndpointPublishingStrategy) *operatorv1.IngressController {
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: eps,
		},
	}
}
