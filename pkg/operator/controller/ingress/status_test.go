package ingress

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	retryable "github.com/openshift/cluster-ingress-operator/pkg/util/retryableerror"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	utilclock "k8s.io/apimachinery/pkg/util/clock"
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

func pendingLBService(owner string, UID types.UID) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: owner,
			Labels: map[string]string{
				manifests.OwningIngressControllerLabel: owner,
			},
			UID: UID,
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

func failedCreateLBEvent(service string, UID types.UID) corev1.Event {
	return corev1.Event{
		Type:    "Warning",
		Reason:  "SyncLoadBalancerFailed",
		Message: "failed to ensure load balancer for service openshift-ingress/router-default: TooManyLoadBalancers: Exceeded quota of account",
		Source: corev1.EventSource{
			Component: "service-controller",
		},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Service",
			Name: service,
			UID:  UID,
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

func cond(t string, status operatorv1.ConditionStatus, reason string, lt time.Time) operatorv1.OperatorCondition {
	return operatorv1.OperatorCondition{
		Type:               t,
		Status:             status,
		Reason:             reason,
		LastTransitionTime: metav1.NewTime(lt),
	}
}

func TestComputeIngressDegradedCondition(t *testing.T) {
	tests := []struct {
		name                        string
		conditions                  []operatorv1.OperatorCondition
		expectIngressDegradedStatus operatorv1.ConditionStatus
		expectRequeue               bool
	}{
		{
			name:                        "no conditions set",
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               false,
		},
		{
			name: "not admitted",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerAdmittedConditionType, operatorv1.ConditionFalse, "", clock.Now()),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
		},
		{
			name: "deployment degraded for <30s",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerDeploymentDegradedConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Second*-20)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               true,
		},
		{
			name: "deployment degraded for >30s",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerDeploymentDegradedConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Second*-31)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
		},
		{
			name: "DNS and LB not managed",
			conditions: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               false,
		},
		{
			name: "LB provisioning failing <90s",
			conditions: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Minute*-3)),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Second*-60)),
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Minute*-3)),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Minute*-3)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               true,
		},
		{
			name: "LB provisioning failing >90s",
			conditions: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Minute*-3)),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Second*-120)),
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Minute*-3)),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Minute*-3)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
		},
		{
			name: "DNS failing <30s",
			conditions: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Minute*-3)),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Second*-120)),
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Minute*-3)),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Second*-15)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               true,
		},
		{
			name: "DNS failing >30s",
			conditions: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Minute*-3)),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Second*-120)),
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Minute*-3)),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Minute*-2)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
		},
		{
			name: "DNS not ready and deployment degraded",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerAdmittedConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(IngressControllerDeploymentDegradedConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
		},
		{
			name: "admitted, DNS, LB, and deployment OK",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerAdmittedConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(IngressControllerDeploymentDegradedConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               false,
		},
	}

	for _, test := range tests {
		actual, err := computeIngressDegradedCondition(test.conditions)
		switch err.(type) {
		case retryable.Error:
			if !test.expectRequeue {
				t.Errorf("%q: expected not to be told to requeue", test.name)
			}
		case nil:
			if test.expectRequeue {
				t.Errorf("%q: expected to be told to requeue", test.name)
			}
		default:
			t.Errorf("%q: unexpected error: %v", test.name, err)
			continue
		}
		if actual.Status != test.expectIngressDegradedStatus {
			t.Errorf("%q: expected status to be %s, got %s", test.name, test.expectIngressDegradedStatus, actual.Status)
		}
	}
}

func TestComputeDeploymentDegradedCondition(t *testing.T) {
	tests := []struct {
		name                           string
		deploymentConditions           []appsv1.DeploymentCondition
		expectDeploymentDegradedStatus operatorv1.ConditionStatus
	}{
		{
			name:                           "available absent",
			deploymentConditions:           []appsv1.DeploymentCondition{},
			expectDeploymentDegradedStatus: operatorv1.ConditionUnknown,
		},
		{
			name: "available true",
			deploymentConditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionTrue,
				},
			},
			expectDeploymentDegradedStatus: operatorv1.ConditionFalse,
		},
		{
			name: "available false",
			deploymentConditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionFalse,
				},
			},
			expectDeploymentDegradedStatus: operatorv1.ConditionTrue,
		},
	}

	for _, test := range tests {
		deploy := &appsv1.Deployment{
			Status: appsv1.DeploymentStatus{
				Conditions: test.deploymentConditions,
			},
		}

		actual := computeDeploymentDegradedCondition(deploy)
		if actual.Status != test.expectDeploymentDegradedStatus {
			t.Errorf("%q: expected %v, got %v", test.name, test.expectDeploymentDegradedStatus, actual.Status)
		}
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
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy", clock.Now()),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "LoadBalancerProvisioned", clock.Now()),
			},
		},
		{
			name:       "no events for current lb",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			service:    pendingLBService("default", "1"),
			events: []corev1.Event{
				schedulerEvent(),
				failedCreateLBEvent("secondary", "2"),
				failedCreateLBEvent("default", "3"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy", clock.Now()),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "LoadBalancerPending", clock.Now()),
			},
		},
		{
			name:       "lb pending, create failed events",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			service:    pendingLBService("default", "1"),
			events: []corev1.Event{
				schedulerEvent(),
				failedCreateLBEvent("secondary", "3"),
				failedCreateLBEvent("default", "1"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy", clock.Now()),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "SyncLoadBalancerFailed", clock.Now()),
			},
		},
		{
			name:       "unmanaged",
			controller: ingressController("default", operatorv1.HostNetworkStrategyType),
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionFalse, "EndpointPublishingStrategyExcludesManagedLoadBalancer", clock.Now()),
			},
		},
		{
			name:       "lb service missing",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy", clock.Now()),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "ServiceNotFound", clock.Now()),
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

func TestComputeIngressAvailableCondition(t *testing.T) {
	testCases := []struct {
		description          string
		deploymentConditions []appsv1.DeploymentCondition
		expect               operatorv1.OperatorCondition
	}{
		{
			description: "deployment available",
			deploymentConditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeAvailable, Status: operatorv1.ConditionTrue},
		},
		{
			description: "deployment not available",
			deploymentConditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeAvailable, Status: operatorv1.ConditionFalse},
		},
		{
			description: "deployment availability unknown",
			deploymentConditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionUnknown},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeAvailable, Status: operatorv1.ConditionFalse},
		},
		{
			description: "deployment availability not present",
			deploymentConditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionUnknown},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeAvailable, Status: operatorv1.ConditionFalse},
		},
	}

	for i, tc := range testCases {
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("ingress-controller-%d", i+1),
			},
			Status: appsv1.DeploymentStatus{
				Conditions: tc.deploymentConditions,
			},
		}

		actual := computeIngressAvailableCondition(deploy)
		conditionsCmpOpts := []cmp.Option{
			cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime", "Reason", "Message"),
			cmpopts.EquateEmpty(),
		}
		if !cmp.Equal(actual, tc.expect, conditionsCmpOpts...) {
			t.Fatalf("%q: expected %#v, got %#v", tc.description, tc.expect, actual)
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

func TestMergeConditions(t *testing.T) {
	// Inject a fake clock and don't forget to reset it
	fakeClock := utilclock.NewFakeClock(time.Time{})
	clock = fakeClock
	defer func() {
		clock = utilclock.RealClock{}
	}()

	start := fakeClock.Now()
	middle := start.Add(1 * time.Minute)
	later := start.Add(2 * time.Minute)

	tests := map[string]struct {
		conditions []operatorv1.OperatorCondition
		updates    []operatorv1.OperatorCondition
		expected   []operatorv1.OperatorCondition
	}{
		"updates": {
			conditions: []operatorv1.OperatorCondition{
				cond("A", "False", "Reason", start),
				cond("B", "True", "Reason", start),
				cond("Ignored", "True", "Reason", start),
			},
			updates: []operatorv1.OperatorCondition{
				cond("A", "True", "Reason", middle),
				cond("B", "True", "Reason", middle),
				cond("C", "False", "Reason", middle),
			},
			expected: []operatorv1.OperatorCondition{
				cond("A", "True", "Reason", later),
				cond("B", "True", "Reason", start),
				cond("C", "False", "Reason", later),
				cond("Ignored", "True", "Reason", start),
			},
		},
	}

	// Simulate the passage of time between original condition creation
	// and update processing
	fakeClock.SetTime(later)

	for name, test := range tests {
		t.Logf("test: %s", name)
		actual := mergeConditions(test.conditions, test.updates...)
		if !conditionsEqual(test.expected, actual) {
			t.Errorf("expected:\n%v\nactual:\n%v", toYaml(test.expected), toYaml(actual))
		}
	}
}
