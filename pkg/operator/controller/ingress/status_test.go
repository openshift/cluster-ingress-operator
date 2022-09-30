package ingress

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"k8s.io/utils/pointer"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	retryable "github.com/openshift/cluster-ingress-operator/pkg/util/retryableerror"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilclock "k8s.io/utils/clock"
	utilclocktesting "k8s.io/utils/clock/testing"
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

func TestComputePodsScheduledCondition(t *testing.T) {
	deployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"ingresscontroller.operator.openshift.io/deployment-ingresscontroller": "default",
					"ingresscontroller.operator.openshift.io/hash":                         "75678b564c",
				},
			},
		},
	}
	unscheduledPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod",
			Labels: map[string]string{
				"ingresscontroller.operator.openshift.io/deployment-ingresscontroller": "default",
				"ingresscontroller.operator.openshift.io/hash":                         "75678b564c",
			},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodScheduled,
				Status: corev1.ConditionFalse,
			}},
		},
	}
	unschedulablePod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod",
			Labels: map[string]string{
				"ingresscontroller.operator.openshift.io/deployment-ingresscontroller": "default",
				"ingresscontroller.operator.openshift.io/hash":                         "75678b564c",
			},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  corev1.PodReasonUnschedulable,
				Message: "0/3 nodes are available: 3 node(s) didn't match node selector.",
			}},
		},
	}
	scheduledPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod",
			Labels: map[string]string{
				"ingresscontroller.operator.openshift.io/deployment-ingresscontroller": "default",
				"ingresscontroller.operator.openshift.io/hash":                         "75678b564c",
			},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodScheduled,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	unrelatedPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod",
			Labels: map[string]string{
				"ingresscontroller.operator.openshift.io/deployment-ingresscontroller": "default",
				"ingresscontroller.operator.openshift.io/hash":                         "8921af1f16",
			},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  corev1.PodReasonUnschedulable,
				Message: "0/3 nodes are available: 3 node(s) didn't match node selector.",
			}},
		},
	}
	invalidDeployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{},
		},
	}
	tests := []struct {
		name       string
		deployment *appsv1.Deployment
		pods       []corev1.Pod
		expect     operatorv1.ConditionStatus
	}{
		{
			name:       "no pods",
			deployment: deployment,
			pods:       []corev1.Pod{unrelatedPod},
			expect:     operatorv1.ConditionTrue,
		},
		{
			name:       "all pods scheduled",
			deployment: deployment,
			pods:       []corev1.Pod{scheduledPod},
			expect:     operatorv1.ConditionTrue,
		},
		{
			name:       "some pod unscheduled",
			deployment: deployment,
			pods:       []corev1.Pod{scheduledPod, unscheduledPod},
			expect:     operatorv1.ConditionFalse,
		},
		{
			name:       "some pod unschedulable",
			deployment: deployment,
			pods:       []corev1.Pod{scheduledPod, unschedulablePod},
			expect:     operatorv1.ConditionFalse,
		},
		{
			name:       "deployment with empty label selector",
			deployment: invalidDeployment,
			expect:     operatorv1.ConditionUnknown,
		},
	}
	for _, test := range tests {
		actual := computeDeploymentPodsScheduledCondition(test.deployment, test.pods)
		if actual.Status != test.expect {
			t.Errorf("%q: expected %v, got %v", test.name, test.expect, actual.Status)
		}
	}
}

func TestComputeIngressDegradedCondition(t *testing.T) {
	// Inject a fake clock and don't forget to reset it
	fakeClock := utilclocktesting.NewFakeClock(time.Time{})
	clock = fakeClock
	defer func() {
		clock = utilclock.RealClock{}
	}()

	tests := []struct {
		name                        string
		icName                      string
		conditions                  []operatorv1.OperatorCondition
		expectIngressDegradedStatus operatorv1.ConditionStatus
		expectRequeue               bool
		// A degraded condition will give a 1 minute retry duration
		// unless there is a grace period expected
		expectAfter time.Duration
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
			// Just use the one minute retry duration for this degraded condition
			expectAfter: time.Minute,
		},
		{
			name: "pods not scheduled for <10m",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerPodsScheduledConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Minute*-9)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               true,
			// Grace period is 10 minutes, subtract the 9 minute spoofed last transition time
			expectAfter: time.Minute,
		},
		{
			name: "pods not scheduled for >10m",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerPodsScheduledConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Minute*-31)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
			// Just use the one minute retry duration for this degraded condition
			expectAfter: time.Minute,
		},
		{
			name: "deployment unavailable for <30s",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerDeploymentAvailableConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Second*-20)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               true,
			// Grace period is 30 seconds, subtract the 20 second spoofed last transition time
			expectAfter: time.Second * 10,
		},
		{
			name: "deployment unavailable for >30s",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerDeploymentAvailableConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Second*-31)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
			// Exceeded grace period, just use the one minute for this degraded condition
			expectAfter: time.Minute,
		},
		{
			name: "deployment minimum replicas unavailable for <60s",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerDeploymentReplicasMinAvailableConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Second*-20)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               true,
			// Grace period is 60 seconds, subtract the 20 second spoofed last transition time
			expectAfter: time.Second * 40,
		},
		{
			name: "deployment minimum replicas unavailable for >60s",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerDeploymentReplicasMinAvailableConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Second*-61)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
			// Exceeded grace period, just use the one minute for this degraded condition
			expectAfter: time.Minute,
		},
		{
			name: "deployment not all replicas available for <60m",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerDeploymentReplicasAllAvailableConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Minute*-20)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               true,
			// Grace period is 60 minutes, subtract the 20 minute spoofed last transition time
			expectAfter: time.Minute * 40,
		},
		{
			name: "deployment not all replicas available for >60m",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerDeploymentReplicasAllAvailableConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Minute*-61)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
			// Exceeded grace period, just use the one minute for this degraded condition
			expectAfter: time.Minute,
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
			// Minimum grace period of combined conditions (DNSReadyIngressConditionType) is 30 seconds
			expectAfter: time.Second * 30,
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
			// Exceeded grace period, just use the one minute for this degraded condition
			expectAfter: time.Minute,
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
			// Minimum grace period of combined conditions (DNSReadyIngressConditionType) is 30 seconds, subtract the 15 second spoofed last transition time
			expectAfter: time.Second * 15,
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
			// Exceeded grace period, just use the one minute for this degraded condition
			expectAfter: time.Minute,
		},
		{
			name: "DNS not ready and deployment unavailable",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerAdmittedConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(IngressControllerPodsScheduledConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
				cond(IngressControllerDeploymentAvailableConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
				cond(IngressControllerDeploymentReplicasMinAvailableConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
				cond(IngressControllerDeploymentReplicasAllAvailableConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Hour*-1)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
			// Exceeded grace period, just use the one minute for these degraded conditions
			expectAfter: time.Minute,
		},
		{
			name: "admitted, DNS, LB, and deployment OK",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerAdmittedConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(IngressControllerPodsScheduledConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(IngressControllerDeploymentAvailableConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(IngressControllerDeploymentReplicasMinAvailableConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(IngressControllerDeploymentReplicasAllAvailableConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.DNSManagedIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
				cond(operatorv1.DNSReadyIngressConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Hour*-1)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               false,
		},
		{
			name: "default ingress controller, canary check failing",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerCanaryCheckSuccessConditionType, operatorv1.ConditionFalse, "", clock.Now().Add(time.Second*-61)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionTrue,
			expectRequeue:               true,
			icName:                      "default",
			// Exceeded grace period, just use the one minute for these degraded conditions
			expectAfter: time.Minute,
		},
		{
			name: "default ingress controller, canary check passing",
			conditions: []operatorv1.OperatorCondition{
				cond(IngressControllerCanaryCheckSuccessConditionType, operatorv1.ConditionTrue, "", clock.Now().Add(time.Minute*-1)),
			},
			expectIngressDegradedStatus: operatorv1.ConditionFalse,
			expectRequeue:               false,
			icName:                      "default",
		},
	}
	for _, test := range tests {
		actual, err := computeIngressDegradedCondition(test.conditions, test.icName)
		switch e := err.(type) {
		case retryable.Error:
			if !test.expectRequeue {
				t.Errorf("%q: expected not to be told to requeue", test.name)
			}
			if test.expectAfter.Seconds() != e.After().Seconds() {
				t.Errorf("%q: expected requeue after %s, got %s", test.name, test.expectAfter.String(), e.After().String())
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

// TestComputeDeploymentRollingOutCondition verifies that
// computeDeploymentRollingOutCondition returns the expected status condition.
func TestComputeDeploymentRollingOutCondition(t *testing.T) {
	tests := []struct {
		name                  string
		replicasWanted        *int32
		replicasHave          *int32
		replicasUpdated       *int32
		replicasAvailable     *int32
		expectStatus          operatorv1.ConditionStatus
		expectMessageContains string
	}{
		{
			name:                  "Router pod replicas not rolling out",
			expectStatus:          operatorv1.ConditionFalse,
			replicasHave:          pointer.Int32(2),
			replicasWanted:        pointer.Int32(2),
			replicasUpdated:       pointer.Int32(2),
			replicasAvailable:     pointer.Int32(2),
			expectMessageContains: "Deployment is not actively rolling out",
		},
		{
			name:                  "Router pod replicas have/updated < want",
			expectStatus:          operatorv1.ConditionTrue,
			replicasHave:          pointer.Int32(1),
			replicasWanted:        pointer.Int32(4),
			replicasUpdated:       pointer.Int32(1),
			replicasAvailable:     pointer.Int32(2),
			expectMessageContains: "1 out of 4 new replica(s) have been updated",
		},
		{
			name:                  "Router pod replicas have > updated",
			expectStatus:          operatorv1.ConditionTrue,
			replicasHave:          pointer.Int32(3),
			replicasWanted:        pointer.Int32(1),
			replicasUpdated:       pointer.Int32(1),
			replicasAvailable:     pointer.Int32(1),
			expectMessageContains: "2 old replica(s) are pending termination",
		},
		{
			name:                  "Router pod replicas have > updated, but want is nil",
			expectStatus:          operatorv1.ConditionTrue,
			replicasHave:          pointer.Int32(3),
			replicasWanted:        nil,
			replicasUpdated:       pointer.Int32(1),
			replicasAvailable:     pointer.Int32(1),
			expectMessageContains: "2 old replica(s) are pending termination",
		},
		{
			name:                  "Router pods replicas available < updated",
			expectStatus:          operatorv1.ConditionTrue,
			replicasHave:          pointer.Int32(4),
			replicasWanted:        pointer.Int32(4),
			replicasUpdated:       pointer.Int32(4),
			replicasAvailable:     pointer.Int32(1),
			expectMessageContains: "1 of 4 updated replica(s) are available",
		},
		{
			name:                  "Router pods replicas available < updated, but want is nil",
			expectStatus:          operatorv1.ConditionTrue,
			replicasHave:          pointer.Int32(4),
			replicasWanted:        nil,
			replicasUpdated:       pointer.Int32(4),
			replicasAvailable:     pointer.Int32(1),
			expectMessageContains: "1 of 4 updated replica(s) are available",
		},
		{
			name:                  "Router pods replicas equal but want is nil",
			expectStatus:          operatorv1.ConditionFalse,
			replicasHave:          pointer.Int32(1),
			replicasWanted:        nil,
			replicasUpdated:       pointer.Int32(1),
			replicasAvailable:     pointer.Int32(1),
			expectMessageContains: "Deployment is not actively rolling out",
		},
		{
			name:                  "Router pods replicas have < updated/available (not a possible scenario)",
			expectStatus:          operatorv1.ConditionFalse,
			replicasHave:          pointer.Int32(1),
			replicasWanted:        pointer.Int32(1),
			replicasUpdated:       pointer.Int32(2),
			replicasAvailable:     pointer.Int32(2),
			expectMessageContains: "Deployment is not actively rolling out",
		},
	}
	for _, test := range tests {
		routerDeploy := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Replicas: test.replicasWanted,
			},
			Status: appsv1.DeploymentStatus{
				Replicas:          *test.replicasHave,
				AvailableReplicas: *test.replicasAvailable,
				UpdatedReplicas:   *test.replicasUpdated,
			},
		}
		actual := computeDeploymentRollingOutCondition(routerDeploy)
		if actual.Status != test.expectStatus {
			t.Errorf("%q: expected status to be %s, got %s", test.name, test.expectStatus, actual.Status)
		}
		if len(test.expectMessageContains) != 0 && !strings.Contains(actual.Message, test.expectMessageContains) {
			t.Errorf("%q: expected message to include %q, got %q", test.name, test.expectMessageContains, actual.Message)
		}
	}
}

// TestComputeLoadBalancerProgressingStatus verifies that
// computeLoadBalancerProgressingStatus returns the expected status condition.
func TestComputeLoadBalancerProgressingStatus(t *testing.T) {
	hostNetworkIngressController := operatorv1.IngressController{
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.HostNetworkStrategyType,
			},
		},
	}
	loadBalancerIngressController := operatorv1.IngressController{
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
			},
		},
	}
	loadBalancerIngressControllerWithInternalScope := operatorv1.IngressController{
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.InternalLoadBalancer,
				},
			},
		},
	}
	loadBalancerIngressControllerWithExternalScope := operatorv1.IngressController{
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
		},
	}
	nodePortIngressController := operatorv1.IngressController{
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.NodePortServiceStrategyType,
			},
		},
	}
	privateIngressController := operatorv1.IngressController{
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.PrivateStrategyType,
			},
		},
	}
	lbService := &corev1.Service{}
	lbServiceWithInternalScopeOnAWS := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				awsInternalLBAnnotation: "true",
			},
		},
	}
	awsPlatformStatus := &configv1.PlatformStatus{
		Type: configv1.AWSPlatformType,
	}
	azurePlatformStatus := &configv1.PlatformStatus{
		Type: configv1.AzurePlatformType,
	}
	tests := []struct {
		name                        string
		conditions                  []operatorv1.OperatorCondition
		ic                          *operatorv1.IngressController
		service                     *corev1.Service
		platformStatus              *configv1.PlatformStatus
		expectStatus                operatorv1.ConditionStatus
		expectMessageContains       string
		expectMessageDoesNotContain string
	}{
		{
			name:         "Private",
			ic:           &privateIngressController,
			expectStatus: operatorv1.ConditionFalse,
		},
		{
			name:         "NodePortService",
			ic:           &nodePortIngressController,
			expectStatus: operatorv1.ConditionFalse,
		},
		{
			name:         "HostNetwork",
			ic:           &hostNetworkIngressController,
			expectStatus: operatorv1.ConditionFalse,
		},
		{
			name:         "LoadBalancerService, no service",
			ic:           &loadBalancerIngressControllerWithExternalScope,
			expectStatus: operatorv1.ConditionTrue,
		},
		{
			name:         "LoadBalancerService, no status",
			ic:           &loadBalancerIngressController,
			expectStatus: operatorv1.ConditionUnknown,
		},
		{
			name:                  "LoadBalancerService, inconsistent scope on AWS",
			ic:                    &loadBalancerIngressControllerWithInternalScope,
			service:               lbService,
			platformStatus:        awsPlatformStatus,
			expectStatus:          operatorv1.ConditionTrue,
			expectMessageContains: "delete",
		},
		{
			name:                        "LoadBalancerService, inconsistent scope on Azure",
			ic:                          &loadBalancerIngressControllerWithInternalScope,
			service:                     lbService,
			platformStatus:              azurePlatformStatus,
			expectStatus:                operatorv1.ConditionTrue,
			expectMessageDoesNotContain: "delete",
		},
		{
			name:           "LoadBalancerService, internal scope",
			ic:             &loadBalancerIngressControllerWithInternalScope,
			service:        lbServiceWithInternalScopeOnAWS,
			platformStatus: awsPlatformStatus,
			expectStatus:   operatorv1.ConditionFalse,
		},
		{
			name:           "LoadBalancerService, external scope",
			ic:             &loadBalancerIngressControllerWithExternalScope,
			service:        lbService,
			platformStatus: awsPlatformStatus,
			expectStatus:   operatorv1.ConditionFalse,
		},
	}
	for _, test := range tests {
		actual := computeLoadBalancerProgressingStatus(test.ic, test.service, test.platformStatus)
		if actual.Status != test.expectStatus {
			t.Errorf("%q: expected status to be %s, got %s", test.name, test.expectStatus, actual.Status)
		}
		if len(test.expectMessageContains) != 0 && !strings.Contains(actual.Message, test.expectMessageContains) {
			t.Errorf("%q: expected message to include %q, got %q", test.name, test.expectMessageContains, actual.Message)
		}
		if len(test.expectMessageDoesNotContain) != 0 && strings.Contains(actual.Message, test.expectMessageDoesNotContain) {
			t.Errorf("%q: expected message not to include %q, got %q", test.name, test.expectMessageDoesNotContain, actual.Message)
		}
	}
}

func TestComputeDeploymentAvailableCondition(t *testing.T) {
	tests := []struct {
		name                            string
		deploymentConditions            []appsv1.DeploymentCondition
		expectDeploymentAvailableStatus operatorv1.ConditionStatus
	}{
		{
			name:                            "available absent",
			deploymentConditions:            []appsv1.DeploymentCondition{},
			expectDeploymentAvailableStatus: operatorv1.ConditionUnknown,
		},
		{
			name: "available true",
			deploymentConditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionTrue,
				},
			},
			expectDeploymentAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name: "available false",
			deploymentConditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionFalse,
				},
			},
			expectDeploymentAvailableStatus: operatorv1.ConditionFalse,
		},
	}

	for _, test := range tests {
		deploy := &appsv1.Deployment{
			Status: appsv1.DeploymentStatus{
				Conditions: test.deploymentConditions,
			},
		}

		actual := computeDeploymentAvailableCondition(deploy)
		if actual.Status != test.expectDeploymentAvailableStatus {
			t.Errorf("%q: expected %v, got %v", test.name, test.expectDeploymentAvailableStatus, actual.Status)
		}
	}
}

func TestComputeDeploymentReplicasMinAvailableCondition(t *testing.T) {
	pointerToInt32 := func(i int32) *int32 { return &i }
	pointerToIntVal := func(val intstr.IntOrString) *intstr.IntOrString { return &val }
	tests := []struct {
		name                                       string
		availableReplicas                          int32
		replicas                                   *int32
		rollingUpdate                              *appsv1.RollingUpdateDeployment
		expectDeploymentReplicasMinAvailableStatus operatorv1.ConditionStatus
	}{
		{
			name:              "replicas not specified, 0 available, rolling update parameters not specified",
			availableReplicas: int32(0),
			replicas:          nil,
			rollingUpdate:     nil,
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionFalse,
		},
		{
			name:              "replicas not specified, 0 available, maxSurge nil, maxUnavailable nil",
			availableReplicas: int32(0),
			replicas:          nil,
			rollingUpdate:     &appsv1.RollingUpdateDeployment{},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionFalse,
		},
		{
			name:              "replicas not specified, 0 available, maxSurge 25%, maxUnavailable 50%",
			availableReplicas: int32(0),
			replicas:          nil,
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionFalse,
		},
		{
			name:              "replicas not specified, 1 available, maxSurge 25%, maxUnavailable 50%",
			availableReplicas: int32(1),
			replicas:          nil,
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "0/1 replicas available, maxSurge 0, maxUnavailable 50%",
			availableReplicas: int32(0),
			replicas:          pointerToInt32(int32(1)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromInt(0)),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "1/1 replicas available, maxSurge 0, maxUnavailable 50%",
			availableReplicas: int32(1),
			replicas:          pointerToInt32(int32(1)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromInt(0)),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "0/2 replicas available, maxSurge 0, maxUnavailable 50%",
			availableReplicas: int32(0),
			replicas:          pointerToInt32(int32(2)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromInt(0)),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionFalse,
		},
		{
			name:              "1/2 replicas available, maxSurge 0, maxUnavailable 50%",
			availableReplicas: int32(1),
			replicas:          pointerToInt32(int32(2)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromInt(0)),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "2/2 replicas available, maxSurge 0, maxUnavailable 50%",
			availableReplicas: int32(2),
			replicas:          pointerToInt32(int32(2)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromInt(0)),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "0/3 replicas available, maxSurge 25%, maxUnavailable 50%",
			availableReplicas: int32(0),
			replicas:          pointerToInt32(int32(3)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionFalse,
		},
		{
			name:              "1/3 replicas available, maxSurge 25%, maxUnavailable 50%",
			availableReplicas: int32(1),
			replicas:          pointerToInt32(int32(3)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionFalse,
		},
		{
			name:              "2/3 replicas available, maxSurge 25%, maxUnavailable 50%",
			availableReplicas: int32(2),
			replicas:          pointerToInt32(int32(3)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "3/3 replicas available, maxSurge 25%, maxUnavailable 50%",
			availableReplicas: int32(3),
			replicas:          pointerToInt32(int32(3)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("50%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "3/5 replicas available, maxSurge 25%, maxUnavailable 25%",
			availableReplicas: int32(3),
			replicas:          pointerToInt32(int32(5)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("25%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionFalse,
		},
		{
			name:              "4/5 replicas available, maxSurge 25%, maxUnavailable 25%",
			availableReplicas: int32(4),
			replicas:          pointerToInt32(int32(5)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("25%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "5/5 replicas available, maxSurge 25%, maxUnavailable 25%",
			availableReplicas: int32(5),
			replicas:          pointerToInt32(int32(5)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("25%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "6/5 replicas available, maxSurge 25%, maxUnavailable 25%",
			availableReplicas: int32(6),
			replicas:          pointerToInt32(int32(5)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("25%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "9/12 replicas available, maxSurge 25%, maxUnavailable 25%",
			availableReplicas: int32(9),
			replicas:          pointerToInt32(int32(12)),
			rollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxSurge:       pointerToIntVal(intstr.FromString("25%")),
				MaxUnavailable: pointerToIntVal(intstr.FromString("25%")),
			},
			expectDeploymentReplicasMinAvailableStatus: operatorv1.ConditionTrue,
		},
	}

	for _, test := range tests {
		deploy := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Replicas: test.replicas,
				Strategy: appsv1.DeploymentStrategy{
					Type:          appsv1.RollingUpdateDeploymentStrategyType,
					RollingUpdate: test.rollingUpdate,
				},
			},
			Status: appsv1.DeploymentStatus{
				AvailableReplicas: test.availableReplicas,
			},
		}

		actual := computeDeploymentReplicasMinAvailableCondition(deploy)
		if actual.Status != test.expectDeploymentReplicasMinAvailableStatus {
			t.Errorf("%q: expected %v, got %v", test.name, test.expectDeploymentReplicasMinAvailableStatus, actual.Status)
		}
	}
}

func TestComputeDeploymentReplicasAllAvailableCondition(t *testing.T) {
	pointerTo := func(i int32) *int32 { return &i }
	tests := []struct {
		name                                       string
		availableReplicas                          int32
		replicas                                   *int32
		expectDeploymentReplicasAllAvailableStatus operatorv1.ConditionStatus
	}{
		{
			name:              "replicas not specified, 0 available",
			availableReplicas: int32(0),
			replicas:          nil,
			expectDeploymentReplicasAllAvailableStatus: operatorv1.ConditionFalse,
		},
		{
			name:              "replicas not specified, 1 available",
			availableReplicas: int32(1),
			replicas:          nil,
			expectDeploymentReplicasAllAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "too few replicas available",
			availableReplicas: int32(4),
			replicas:          pointerTo(int32(5)),
			expectDeploymentReplicasAllAvailableStatus: operatorv1.ConditionFalse,
		},
		{
			name:              "all replicas available",
			availableReplicas: int32(5),
			replicas:          pointerTo(int32(5)),
			expectDeploymentReplicasAllAvailableStatus: operatorv1.ConditionTrue,
		},
		{
			name:              "excess replicas available",
			availableReplicas: int32(6),
			replicas:          pointerTo(int32(5)),
			expectDeploymentReplicasAllAvailableStatus: operatorv1.ConditionTrue,
		},
	}

	for _, test := range tests {
		deploy := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Replicas: test.replicas,
			},
			Status: appsv1.DeploymentStatus{
				AvailableReplicas: test.availableReplicas,
			},
		}

		actual := computeDeploymentReplicasAllAvailableCondition(deploy)
		if actual.Status != test.expectDeploymentReplicasAllAvailableStatus {
			t.Errorf("%q: expected %v, got %v", test.name, test.expectDeploymentReplicasAllAvailableStatus, actual.Status)
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

// TestComputeIngressProgressingCondition verifies that
// computeIngressProgressingCondition returns the expected status condition.
func TestComputeIngressProgressingCondition(t *testing.T) {
	testCases := []struct {
		description string
		conditions  []operatorv1.OperatorCondition
		expect      operatorv1.OperatorCondition
	}{
		{
			description: "load balancer is not progressing and router deployment is not rolling out",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionFalse},
				{Type: IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionFalse},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionFalse},
		},
		{
			description: "load balancer is progressing and router deployment is not rolling out",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionTrue},
				{Type: IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionFalse},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionTrue},
		},
		{
			description: "load balancer is progressing, but unmanaged load balancer type and router deployment is not rolling out",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionTrue},
				{Type: IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionFalse},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionFalse},
		},
		{
			description: "load balancer is not progressing, but unmanaged load balancer type and router deployment is rolling out",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionFalse},
				{Type: IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionTrue},
		},
		{
			description: "load balancer is not progressing and router deployment is rolling out",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionFalse},
				{Type: IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionTrue},
		},
		{
			description: "load balancer is progressing and router deployment is rolling out",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionTrue},
				{Type: IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionTrue},
		},
		{
			description: "load balancer is unknown progressing and router deployment is not rolling out",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionUnknown},
				{Type: IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionFalse},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionTrue},
		},
		{
			description: "load balancer is not progressing and router deployment is unknown",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionFalse},
				{Type: IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionUnknown},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionTrue},
		},
		{
			description: "load balancer progressing condition missing and router deployment is not rolling out",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionFalse},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionFalse},
		},
		{
			description: "load balancer is not progressing and router rolling out is missing",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionFalse},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionFalse},
		},
		{
			description: "all progressing unknown",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerLoadBalancerProgressingConditionType, Status: operatorv1.ConditionUnknown},
				{Type: IngressControllerDeploymentRollingOutConditionType, Status: operatorv1.ConditionUnknown},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionTrue},
		},
		{
			description: "all progressing not present",
			conditions:  []operatorv1.OperatorCondition{},
			expect:      operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionFalse},
		},
	}

	for _, tc := range testCases {
		actual := computeIngressProgressingCondition(tc.conditions)
		conditionsCmpOpts := []cmp.Option{
			cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime", "Reason", "Message"),
			cmpopts.EquateEmpty(),
		}
		if !cmp.Equal(actual, tc.expect, conditionsCmpOpts...) {
			t.Fatalf("%q: expected %#v, got %#v", tc.description, tc.expect, actual)
		}
	}
}

func TestComputeIngressAvailableCondition(t *testing.T) {
	testCases := []struct {
		description string
		conditions  []operatorv1.OperatorCondition
		expect      operatorv1.OperatorCondition
	}{
		{
			description: "deployment, dns, and lb available",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerDeploymentAvailableConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.DNSReadyIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeAvailable, Status: operatorv1.ConditionTrue},
		},
		{
			description: "deployment not available, but dns and lb available",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerDeploymentAvailableConditionType, Status: operatorv1.ConditionFalse},
				{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.DNSReadyIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionTrue},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeAvailable, Status: operatorv1.ConditionFalse},
		},
		{
			description: "dns not available, but deployment and lb available",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerDeploymentAvailableConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.DNSReadyIngressConditionType, Status: operatorv1.ConditionFalse},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeAvailable, Status: operatorv1.ConditionFalse},
		},
		{
			description: "lb not available, but dns and deployment available",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerDeploymentAvailableConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.DNSReadyIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionFalse},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeAvailable, Status: operatorv1.ConditionFalse},
		},
		{
			description: "all availability unknown",
			conditions: []operatorv1.OperatorCondition{
				{Type: IngressControllerDeploymentAvailableConditionType, Status: operatorv1.ConditionUnknown},
				{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.DNSReadyIngressConditionType, Status: operatorv1.ConditionUnknown},
				{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionTrue},
				{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionUnknown},
			},
			expect: operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeAvailable, Status: operatorv1.ConditionFalse},
		},
		{
			description: "all availability not present",
			conditions:  []operatorv1.OperatorCondition{},
			expect:      operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeAvailable, Status: operatorv1.ConditionFalse},
		},
	}

	for _, tc := range testCases {
		actual := computeIngressAvailableCondition(tc.conditions)
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
		if actual := IngressStatusesEqual(tc.a, tc.b); actual != tc.expected {
			t.Fatalf("%q: expected %v, got %v", tc.description, tc.expected, actual)
		}
	}
}

func TestMergeConditions(t *testing.T) {
	// Inject a fake clock and don't forget to reset it
	fakeClock := utilclocktesting.NewFakeClock(time.Time{})
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
		actual := MergeConditions(test.conditions, test.updates...)
		if !conditionsEqual(test.expected, actual) {
			t.Errorf("expected:\n%v\nactual:\n%v", toYaml(test.expected), toYaml(actual))
		}
	}
}

func TestZoneInConfig(t *testing.T) {
	var z *configv1.DNSZone
	var dnsZone configv1.DNSZone
	tag := make(map[string]string)
	tagZone := make(map[string]string)

	testCases := []struct {
		description        string
		expected           bool
		in, zone, zoneType string
	}{
		{
			description: "[PrivateZone] empty strings (should fail)",
			expected:    false,
			in:          "",
			zone:        "",
			zoneType:    "ID",
		},
		{
			description: "[PrivateZone] zone.ID empty string (should fail)",
			expected:    false,
			in:          "test",
			zone:        "",
			zoneType:    "ID",
		}, {
			description: "[PrivateZone] zone.ID with value (not equal should fail)",
			expected:    false,
			in:          "test",
			zone:        "notest",
			zoneType:    "ID",
		}, {
			description: "[PrivateZone] zone.ID with value (equal should pass)",
			expected:    true,
			in:          "test",
			zone:        "test",
			zoneType:    "ID",
		}, {
			description: "[PrivateZone] empty strings (should fail)",
			expected:    false,
			in:          "",
			zone:        "",
			zoneType:    "TAG",
		}, {
			description: "[PrivateZone] zone.Tags['Name'] empty string (should fail)",
			expected:    false,
			in:          "test",
			zone:        "",
			zoneType:    "TAG",
		}, {
			description: "[PrivateZone] zone.Tags['Name'] with value (not equal should fail)",
			expected:    false,
			in:          "test",
			zone:        "notest",
			zoneType:    "TAG",
		}, {
			description: "[PrivateZone] zone.tags['Name'] with value (equal should pass)",
			expected:    true,
			in:          "test",
			zone:        "test",
			zoneType:    "TAG",
		},
	}

	for _, test := range testCases {
		t.Run(test.description, func(t *testing.T) {
			if test.zoneType == "ID" {
				z = &configv1.DNSZone{ID: test.in}
				dnsZone = configv1.DNSZone{ID: test.zone}
			} else {
				tag["Name"] = test.in
				z = &configv1.DNSZone{Tags: tag}
				tagZone["Name"] = test.zone
				dnsZone = configv1.DNSZone{Tags: tagZone}
			}
			dnsSpec := configv1.DNSSpec{PrivateZone: z}
			dnsConfig := &configv1.DNS{Spec: dnsSpec}
			actual := checkZoneInConfig(dnsConfig, dnsZone)
			if actual != test.expected {
				t.Errorf("expected:%v actual:%v\n", test.expected, actual)
			}
		})
	}
}

func TestComputeIngressUpgradeableCondition(t *testing.T) {
	makeDefaultCertificateSecret := func(cn string, sans []string) *corev1.Secret {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("failed to generate key: %v", err)
		}

		certTemplate := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: cn},
			DNSNames:     sans,
		}
		cert, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &key.PublicKey, key)
		if err != nil {
			t.Fatalf("failed to generate certificate: %v", err)
		}

		certData := pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: cert,
		})

		return &corev1.Secret{
			Data: map[string][]byte{"tls.crt": certData},
		}
	}
	const (
		ingressDomain  = "apps.foo.com"
		wildcardDomain = "*." + ingressDomain
	)
	testCases := []struct {
		description string
		mutate      func(*corev1.Service)
		secret      *corev1.Secret
		expect      bool
	}{
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags stays the same",
			mutate: func(svc *corev1.Service) {
				svc.Annotations[awsLBAdditionalResourceTags] = "Key1=Value1"
			},
			expect: true,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations[awsLBAdditionalResourceTags] = "Key2=Value2"
			},
			expect: false,
		},
		{
			description: "if the default certificate has a SAN",
			secret:      makeDefaultCertificateSecret(wildcardDomain, []string{wildcardDomain}),
			expect:      true,
		},
		{
			description: "if the default certificate has no SAN",
			secret:      makeDefaultCertificateSecret(wildcardDomain, []string{}),
			expect:      false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ic := &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
				},
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
					},
					Domain: ingressDomain,
				},
			}
			trueVar := true
			deploymentRef := metav1.OwnerReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "router-default",
				UID:        "1",
				Controller: &trueVar,
			}
			platformStatus := &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
				AWS: &configv1.AWSPlatformStatus{
					ResourceTags: []configv1.AWSResourceTag{
						{
							Key:   "Key1",
							Value: "Value1",
						},
					},
				},
			}
			wantSvc, service, err := desiredLoadBalancerService(ic, deploymentRef, platformStatus)
			if err != nil {
				t.Errorf("%q: unexpected error from desiredLoadBalancerService: %v", tc.description, err)
				return
			}
			if !wantSvc {
				t.Errorf("%q: unexpected false value from desiredLoadBalancerService", tc.description)
				return
			}
			if tc.mutate != nil {
				tc.mutate(service)
			}
			secret := tc.secret
			if secret == nil {
				secret = makeDefaultCertificateSecret("", []string{wildcardDomain})
			}

			expectedStatus := operatorv1.ConditionFalse
			if tc.expect {
				expectedStatus = operatorv1.ConditionTrue
			}

			actual := computeIngressUpgradeableCondition(ic, deploymentRef, service, platformStatus, secret)
			if actual.Status != expectedStatus {
				t.Errorf("%q: expected Upgradeable to be %q, got %q", tc.description, expectedStatus, actual.Status)
			}

		})
	}
}
