package ingress

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ingressController(name string, t operatorv1.EndpointPublishingStrategyType) *operatorv1.IngressController {
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: t,
			},
		},
	}
}

func pendingLBService(owner string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: owner,
			Labels: map[string]string{
				manifests.OwningIngressControllerLabel: owner,
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
		},
	}
}

func provisionedLBservice(owner string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: owner,
			Labels: map[string]string{
				manifests.OwningIngressControllerLabel: owner,
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{Hostname: "lb.cloudprovider.example.com"},
				},
			},
		},
	}
}

func clusterIPservice(name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: name + "-internal",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
		},
	}
}

func failedCreateLBEvent(service string) corev1.Event {
	return corev1.Event{
		Type:    "Warning",
		Reason:  "CreatingLoadBalancerFailed",
		Message: "failed to ensure load balancer for service openshift-ingress/router-default: TooManyLoadBalancers: Exceeded quota of account",
		Source: corev1.EventSource{
			Component: "service-controller",
		},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Service",
			Name: service,
		},
	}
}

func schedulerEvent() corev1.Event {
	return corev1.Event{
		Type:   "Normal",
		Reason: "Scheduled",
		Source: corev1.EventSource{
			Component: "default-scheduler",
		},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod",
			Name: "router-default-1",
		},
	}
}

func cond(t string, status operatorv1.ConditionStatus, reason string) operatorv1.OperatorCondition {
	return operatorv1.OperatorCondition{
		Type:   t,
		Status: status,
		Reason: reason,
	}
}

func TestComputeLoadBalancerStatus(t *testing.T) {
	tests := []struct {
		name       string
		controller *operatorv1.IngressController
		service    *corev1.Service
		events     []corev1.Event
		expect     []operatorv1.OperatorCondition
	}{
		{
			name:       "lb provisioned",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			service:    provisionedLBservice("default"),
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "LoadBalancerProvisioned"),
			},
		},
		{
			name:       "no events for current lb",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			service:    pendingLBService("default"),
			events: []corev1.Event{
				schedulerEvent(),
				failedCreateLBEvent("secondary"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "LoadBalancerPending"),
			},
		},
		{
			name:       "lb pending, create failed events",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			service:    pendingLBService("default"),
			events: []corev1.Event{
				schedulerEvent(),
				failedCreateLBEvent("secondary"),
				failedCreateLBEvent("default"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "CreatingLoadBalancerFailed"),
			},
		},
		{
			name:       "unmanaged",
			controller: ingressController("default", operatorv1.HostNetworkStrategyType),
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionFalse, "UnsupportedEndpointPublishingStrategy"),
			},
		},
		{
			name:       "lb service missing",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "ServiceNotFound"),
			},
		},
	}

	for _, test := range tests {
		t.Logf("evaluating test %s", test.name)

		actual := computeLoadBalancerStatus(test.controller, test.service, test.events)

		conditionsCmpOpts := []cmp.Option{
			cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime", "Message"),
			cmpopts.EquateEmpty(),
			cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
		}
		if !cmp.Equal(actual, test.expect, conditionsCmpOpts...) {
			t.Fatalf("expected:\n%#v\ngot:\n%#v", test.expect, actual)
		}
	}
}

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
		conditionsCmpOpts := []cmp.Option{
			cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime", "Reason", "Message"),
			cmpopts.EquateEmpty(),
			cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
		}
		if !cmp.Equal(actual, expected, conditionsCmpOpts...) {
			t.Fatalf("%q: expected %#v, got %#v", tc.description, expected, actual)
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
			description: "condition LastTransitionTime should not be ignored",
			expected:    false,
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
