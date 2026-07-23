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

func Test_SetNLBHairpinRiskMetric(t *testing.T) {
	testCases := []struct {
		name                 string
		inputIngress         *operatorv1.IngressController
		expectedMetricFormat string
	}{
		{
			name: "internal NLB, status protocol empty — at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1",
				nil,
				&operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.InternalLoadBalancer,
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						Type: operatorv1.AWSLoadBalancerProvider,
						AWS: &operatorv1.AWSLoadBalancerParameters{
							Type: operatorv1.AWSNetworkLoadBalancer,
						},
					},
				}}),
			expectedMetricFormat: `
			# HELP ingress_controller_aws_nlb_hairpin_risk Reports whether an IngressController using an internal AWS NLB has no explicit protocol setting and may be affected by hairpin connection failures. 0 is no risk, 1 is at risk.
			# TYPE ingress_controller_aws_nlb_hairpin_risk gauge
			ingress_controller_aws_nlb_hairpin_risk{name="test1"} 1
			`,
		},
		{
			name: "internal NLB, spec empty, status TCP — not at risk (managed)",
			inputIngress: testIngressControllerWithSpecAndStatus("test1",
				nil,
				&operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.InternalLoadBalancer,
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						Type: operatorv1.AWSLoadBalancerProvider,
						AWS: &operatorv1.AWSLoadBalancerParameters{
							Type: operatorv1.AWSNetworkLoadBalancer,
							NetworkLoadBalancerParameters: &operatorv1.AWSNetworkLoadBalancerParameters{
								Protocol: operatorv1.NLBProtocolTCP,
							},
						},
					},
				}}),
			expectedMetricFormat: `
			# HELP ingress_controller_aws_nlb_hairpin_risk Reports whether an IngressController using an internal AWS NLB has no explicit protocol setting and may be affected by hairpin connection failures. 0 is no risk, 1 is at risk.
			# TYPE ingress_controller_aws_nlb_hairpin_risk gauge
			ingress_controller_aws_nlb_hairpin_risk{name="test1"} 0
			`,
		},
		{
			name: "internal NLB, spec PROXY, status PROXY — not at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1",
				&operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						AWS: &operatorv1.AWSLoadBalancerParameters{
							NetworkLoadBalancerParameters: &operatorv1.AWSNetworkLoadBalancerParameters{
								Protocol: operatorv1.NLBProtocolProxy,
							},
						},
					},
				}},
				&operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.InternalLoadBalancer,
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						Type: operatorv1.AWSLoadBalancerProvider,
						AWS: &operatorv1.AWSLoadBalancerParameters{
							Type: operatorv1.AWSNetworkLoadBalancer,
							NetworkLoadBalancerParameters: &operatorv1.AWSNetworkLoadBalancerParameters{
								Protocol: operatorv1.NLBProtocolProxy,
							},
						},
					},
				}}),
			expectedMetricFormat: `
			# HELP ingress_controller_aws_nlb_hairpin_risk Reports whether an IngressController using an internal AWS NLB has no explicit protocol setting and may be affected by hairpin connection failures. 0 is no risk, 1 is at risk.
			# TYPE ingress_controller_aws_nlb_hairpin_risk gauge
			ingress_controller_aws_nlb_hairpin_risk{name="test1"} 0
			`,
		},
		{
			name: "internal NLB, spec TCP, status TCP — not at risk (explicit choice)",
			inputIngress: testIngressControllerWithSpecAndStatus("test1",
				&operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						AWS: &operatorv1.AWSLoadBalancerParameters{
							NetworkLoadBalancerParameters: &operatorv1.AWSNetworkLoadBalancerParameters{
								Protocol: operatorv1.NLBProtocolTCP,
							},
						},
					},
				}},
				&operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.InternalLoadBalancer,
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						Type: operatorv1.AWSLoadBalancerProvider,
						AWS: &operatorv1.AWSLoadBalancerParameters{
							Type: operatorv1.AWSNetworkLoadBalancer,
							NetworkLoadBalancerParameters: &operatorv1.AWSNetworkLoadBalancerParameters{
								Protocol: operatorv1.NLBProtocolTCP,
							},
						},
					},
				}}),
			expectedMetricFormat: `
			# HELP ingress_controller_aws_nlb_hairpin_risk Reports whether an IngressController using an internal AWS NLB has no explicit protocol setting and may be affected by hairpin connection failures. 0 is no risk, 1 is at risk.
			# TYPE ingress_controller_aws_nlb_hairpin_risk gauge
			ingress_controller_aws_nlb_hairpin_risk{name="test1"} 0
			`,
		},
		{
			name: "external NLB, spec empty, status TCP — not at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1",
				nil,
				&operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						Type: operatorv1.AWSLoadBalancerProvider,
						AWS: &operatorv1.AWSLoadBalancerParameters{
							Type: operatorv1.AWSNetworkLoadBalancer,
							NetworkLoadBalancerParameters: &operatorv1.AWSNetworkLoadBalancerParameters{
								Protocol: operatorv1.NLBProtocolTCP,
							},
						},
					},
				}}),
			expectedMetricFormat: `
			# HELP ingress_controller_aws_nlb_hairpin_risk Reports whether an IngressController using an internal AWS NLB has no explicit protocol setting and may be affected by hairpin connection failures. 0 is no risk, 1 is at risk.
			# TYPE ingress_controller_aws_nlb_hairpin_risk gauge
			ingress_controller_aws_nlb_hairpin_risk{name="test1"} 0
			`,
		},
		{
			name: "CLB — not at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1",
				nil,
				&operatorv1.EndpointPublishingStrategy{LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.InternalLoadBalancer,
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						Type: operatorv1.AWSLoadBalancerProvider,
						AWS: &operatorv1.AWSLoadBalancerParameters{
							Type: operatorv1.AWSClassicLoadBalancer,
						},
					},
				}}),
			expectedMetricFormat: `
			# HELP ingress_controller_aws_nlb_hairpin_risk Reports whether an IngressController using an internal AWS NLB has no explicit protocol setting and may be affected by hairpin connection failures. 0 is no risk, 1 is at risk.
			# TYPE ingress_controller_aws_nlb_hairpin_risk gauge
			ingress_controller_aws_nlb_hairpin_risk{name="test1"} 0
			`,
		},
		{
			name:         "no endpoint publishing strategy — not at risk",
			inputIngress: testIngressControllerWithSpecAndStatus("test1", nil, nil),
			expectedMetricFormat: `
			# HELP ingress_controller_aws_nlb_hairpin_risk Reports whether an IngressController using an internal AWS NLB has no explicit protocol setting and may be affected by hairpin connection failures. 0 is no risk, 1 is at risk.
			# TYPE ingress_controller_aws_nlb_hairpin_risk gauge
			ingress_controller_aws_nlb_hairpin_risk{name="test1"} 0
			`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nlbHairpinRisk.Reset()

			SetNLBHairpinRiskMetric(tc.inputIngress)

			err := testutil.CollectAndCompare(nlbHairpinRisk, strings.NewReader(tc.expectedMetricFormat))
			if err != nil {
				t.Error(err)
			}

			DeleteNLBHairpinRiskMetric(tc.inputIngress)
			err = testutil.CollectAndCompare(nlbHairpinRisk, strings.NewReader(""))
			if err != nil {
				t.Error(err)
			}
		})
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
