package controller

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

func TestComputeDeploymentStatusConditions(t *testing.T) {
	type testInputs struct {
		desireRepl             int32
		unavailRepl, availRepl int32
	}
	type testOutputs struct {
		deployAvailable bool
	}
	testCases := []struct {
		description string
		inputs      testInputs
		outputs     testOutputs
	}{
		{
			description: "0/2 deployment replicas available",
			inputs:      testInputs{2, 0, 0},
			outputs:     testOutputs{false},
		},
		{
			description: "1/2 deployment replicas available",
			inputs:      testInputs{1, 1, 1},
			outputs:     testOutputs{true},
		},
		{
			description: "2/2 deployment replicas available",
			inputs:      testInputs{0, 2, 1},
			outputs:     testOutputs{true},
		},
	}

	for i, tc := range testCases {
		var deployStatus operatorv1.ConditionStatus
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("ingress-controller-%d", i+1),
			},
			Status: appsv1.DeploymentStatus{
				UnavailableReplicas: tc.inputs.unavailRepl,
				AvailableReplicas:   tc.inputs.availRepl,
			},
		}
		if tc.outputs.deployAvailable {
			deployStatus = operatorv1.ConditionTrue
		} else {
			deployStatus = operatorv1.ConditionFalse
		}
		expected := operatorv1.OperatorCondition{Type: DeploymentAvailableConditionType, Status: deployStatus}
		actual := computeDeploymentStatus(deployment)
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

func TestComputeIngressStatusConditions(t *testing.T) {
	tests := []struct {
		description string
		domainSet   bool
		input       []operatorv1.OperatorCondition
		expect      []operatorv1.OperatorCondition
	}{
		{
			description: "no status domain set",
			domainSet:   false,
			input: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "LoadBalancerProvisioned"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionFalse, "StatusDomainUnset"),
			},
		},
		{
			description: "status domain set",
			domainSet:   true,
			input:       []operatorv1.OperatorCondition{},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionTrue, "AsExpected"),
			},
		},
		{
			description: "load balancer provisioned",
			domainSet:   true,
			input: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "LoadBalancerProvisioned"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionTrue, "AsExpected"),
			},
		},
		{
			description: "no load balancer provisioned",
			domainSet:   true,
			input: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "LoadBalancerProvisioned"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionFalse, "DependantResourceFailure"),
			},
		},
		{
			description: "dns no failed zones",
			domainSet:   true,
			input: []operatorv1.OperatorCondition{
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "Normal"),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionTrue, "NoFailedZones"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionTrue, "AsExpected"),
			},
		},
		{
			description: "dns record not found",
			domainSet:   true,
			input: []operatorv1.OperatorCondition{
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "Normal"),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionFalse, "RecordNotFound"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionFalse, "DependantResourceFailure"),
			},
		},
		{
			description: "load balancer provisioned, no failed dns zones",
			domainSet:   true,
			input: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy"),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "LoadBalancerProvisioned"),
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "Normal"),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionTrue, "NoFailedZones"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.OperatorStatusTypeAvailable, operatorv1.ConditionTrue, "AsExpected"),
			},
		},
	}

	for i, test := range tests {
		t.Logf("evaluating test %s", test.description)
		ic := ingressController(fmt.Sprintf("status-test-%d", i+1), operatorv1.LoadBalancerServiceStrategyType)
		if test.domainSet {
			ic.Status.Domain = "test.local"
		}
		actual := computeIngressStatusConditions(ic, test.input)
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

func TestSetIngressLastTransitionTime(t *testing.T) {
	type testInputs struct {
		statusChange, reasonChange, msgChange bool
	}
	type testOutputs struct {
		transition bool
	}
	testCases := []struct {
		description string
		inputs      testInputs
		outputs     testOutputs
	}{
		{
			description: "no condition changes",
			inputs:      testInputs{false, false, false},
			outputs:     testOutputs{false},
		},
		{
			description: "condition status changed",
			inputs:      testInputs{true, false, false},
			outputs:     testOutputs{true},
		},
		{
			description: "condition reason changed",
			inputs:      testInputs{false, true, false},
			outputs:     testOutputs{true},
		},
		{
			description: "condition message changed",
			inputs:      testInputs{false, false, true},
			outputs:     testOutputs{true},
		},
	}

	for _, tc := range testCases {
		var (
			reason                            = "old reason"
			msg                               = "old msg"
			status operatorv1.ConditionStatus = "old status"
		)
		oldCondition := &operatorv1.OperatorCondition{
			Type:               operatorv1.OperatorStatusTypeAvailable,
			Status:             status,
			Reason:             reason,
			Message:            msg,
			LastTransitionTime: metav1.Unix(0, 0),
		}
		if tc.inputs.msgChange {
			msg = "new message"
		}
		if tc.inputs.reasonChange {
			reason = "new reason"
		}
		if tc.inputs.statusChange {
			status = "new status"
		}
		expectCondition := &operatorv1.OperatorCondition{
			Type:    operatorv1.OperatorStatusTypeAvailable,
			Status:  status,
			Reason:  reason,
			Message: msg,
		}
		setIngressLastTransitionTime(expectCondition, oldCondition)
		if tc.outputs.transition != (expectCondition.LastTransitionTime != oldCondition.LastTransitionTime) {
			t.Fatalf(fmt.Sprintf("%q: expected LastTransitionTime %v, got %v", tc.description,
				oldCondition.LastTransitionTime, expectCondition.LastTransitionTime))
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
