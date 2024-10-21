package ingress

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/utils/pointer"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	util "github.com/openshift/cluster-ingress-operator/pkg/util"
	retryable "github.com/openshift/cluster-ingress-operator/pkg/util/retryableerror"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"

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

func Test_checkPodsScheduledForDeployment(t *testing.T) {
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
		name        string
		deployment  *appsv1.Deployment
		pods        []corev1.Pod
		expectError bool
	}{
		{
			name:        "nil deployment",
			deployment:  nil,
			pods:        []corev1.Pod{scheduledPod},
			expectError: true,
		},
		{
			name:        "no pods",
			deployment:  deployment,
			pods:        []corev1.Pod{unrelatedPod},
			expectError: false,
		},
		{
			name:        "all pods scheduled",
			deployment:  deployment,
			pods:        []corev1.Pod{scheduledPod},
			expectError: false,
		},
		{
			name:        "some pod unscheduled",
			deployment:  deployment,
			pods:        []corev1.Pod{scheduledPod, unscheduledPod},
			expectError: true,
		},
		{
			name:        "some pod unschedulable",
			deployment:  deployment,
			pods:        []corev1.Pod{scheduledPod, unschedulablePod},
			expectError: true,
		},
		{
			name:        "deployment with empty label selector",
			deployment:  invalidDeployment,
			expectError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch err := checkPodsScheduledForDeployment(test.deployment, test.pods); {
			case err == nil && test.expectError:
				t.Error("expected error, got nil")
			case err != nil && !test.expectError:
				t.Errorf("got unexpected error: %v", err)
			}
		})
	}
}

func Test_computeIngressDegradedCondition(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			actual, err := computeIngressDegradedCondition(test.conditions, test.icName)
			switch e := err.(type) {
			case retryable.Error:
				if !test.expectRequeue {
					t.Error("expected not to be told to requeue")
				}
				if test.expectAfter.Seconds() != e.After().Seconds() {
					t.Errorf("expected requeue after %s, got %s", test.expectAfter.String(), e.After().String())
				}
			case nil:
				if test.expectRequeue {
					t.Error("expected to be told to requeue")
				}
			default:
				t.Fatalf("unexpected error: %v", err)
			}
			if actual.Status != test.expectIngressDegradedStatus {
				t.Errorf("expected status to be %s, got %s", test.expectIngressDegradedStatus, actual.Status)
			}
		})
	}
}

// Test_computeDeploymentRollingOutCondition verifies that
// computeDeploymentRollingOutCondition returns the expected status condition.
func Test_computeDeploymentRollingOutCondition(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
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
				t.Errorf("expected status to be %s, got %s", test.expectStatus, actual.Status)
			}
			if len(test.expectMessageContains) != 0 && !strings.Contains(actual.Message, test.expectMessageContains) {
				t.Errorf("expected message to include %q, got %q", test.expectMessageContains, actual.Message)
			}
		})
	}
}

// Test_computeLoadBalancerProgressingStatus verifies that
// computeLoadBalancerProgressingStatus returns the expected status condition.
func Test_computeLoadBalancerProgressingStatus(t *testing.T) {
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
	loadBalancerIngressControllerWithAllowedSourceRanges := operatorv1.IngressController{
		Spec: operatorv1.IngressControllerSpec{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope:               operatorv1.ExternalLoadBalancer,
					AllowedSourceRanges: []operatorv1.CIDR{"0.0.0.0/0"},
				},
			},
		},
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
		},
	}
	loadBalancerIngressControllerEmptyAllowedSourceRanges := operatorv1.IngressController{
		Spec: operatorv1.IngressControllerSpec{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope:               operatorv1.ExternalLoadBalancer,
					AllowedSourceRanges: nil,
				},
			},
		},
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: operatorv1.ExternalLoadBalancer,
				},
			},
		},
	}
	loadBalancerIngressControllerWithLBType := func(lbType operatorv1.AWSLoadBalancerType) *operatorv1.IngressController {
		eps := &operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.LoadBalancerServiceStrategyType,
			LoadBalancer: &operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: lbType,
					},
				},
			},
		}
		ic := &operatorv1.IngressController{
			Spec: operatorv1.IngressControllerSpec{
				EndpointPublishingStrategy: eps.DeepCopy(),
			},
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: eps.DeepCopy(),
			},
		}
		return ic
	}
	loadBalancerIngressControllerWithAWSSubnets := func(lbType operatorv1.AWSLoadBalancerType, subnetSpec *operatorv1.AWSSubnets, subnetStatus *operatorv1.AWSSubnets) *operatorv1.IngressController {
		ic := loadBalancerIngressControllerWithLBType(lbType)

		switch lbType {
		case operatorv1.AWSNetworkLoadBalancer:
			ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{
				Subnets: subnetSpec,
			}
			ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{
				Subnets: subnetStatus,
			}
		case operatorv1.AWSClassicLoadBalancer:
			ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.ClassicLoadBalancerParameters = &operatorv1.AWSClassicLoadBalancerParameters{
				Subnets: subnetSpec,
			}
			ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.ClassicLoadBalancerParameters = &operatorv1.AWSClassicLoadBalancerParameters{
				Subnets: subnetStatus,
			}
		}
		return ic
	}

	loadBalancerIngressControllerWithAWSEIPAllocations := func(eipAllocationSpec []operatorv1.EIPAllocation, eipAllocationStatus []operatorv1.EIPAllocation) *operatorv1.IngressController {
		eps := &operatorv1.EndpointPublishingStrategy{
			Type: operatorv1.LoadBalancerServiceStrategyType,
			LoadBalancer: &operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSNetworkLoadBalancer,
					},
				},
			},
		}
		ic := &operatorv1.IngressController{
			Spec: operatorv1.IngressControllerSpec{
				EndpointPublishingStrategy: eps.DeepCopy(),
			},
			Status: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: eps.DeepCopy(),
			},
		}

		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{
			EIPAllocations: eipAllocationSpec,
		}
		ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{
			EIPAllocations: eipAllocationStatus,
		}

		return ic
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
	lbServiceWithNLB := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AWSLBTypeAnnotation: AWSNLBAnnotation,
			},
		},
	}
	lbServiceWithInternalScopeOnAWS := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				awsInternalLBAnnotation: "true",
			},
		},
	}
	lbServiceWithSourceRangesAnnotation := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				corev1.AnnotationLoadBalancerSourceRangesKey: "127.0.0.0/8",
			},
		},
	}
	lbServiceWithSourceRangesField := &corev1.Service{
		Spec: corev1.ServiceSpec{
			LoadBalancerSourceRanges: []string{"127.0.0.0/8"},
		},
	}
	awsPlatformStatus := &configv1.PlatformStatus{
		Type: configv1.AWSPlatformType,
	}
	azurePlatformStatus := &configv1.PlatformStatus{
		Type: configv1.AzurePlatformType,
	}
	tests := []struct {
		name                     string
		conditions               []operatorv1.OperatorCondition
		ic                       *operatorv1.IngressController
		service                  *corev1.Service
		platformStatus           *configv1.PlatformStatus
		awsSubnetsEnabled        bool
		awsEIPAllocationsEnabled bool

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
		{
			name:           "LoadBalancerService, service.beta.kubernetes.io/load-balancer-source-ranges annotation is set",
			ic:             &loadBalancerIngressControllerEmptyAllowedSourceRanges,
			service:        lbServiceWithSourceRangesAnnotation,
			platformStatus: awsPlatformStatus,
			expectStatus:   operatorv1.ConditionTrue,
		},
		{
			name:           "LoadBalancerService, LoadBalancerSourceRanges is set when AllowedSourceRanges is empty",
			ic:             &loadBalancerIngressControllerEmptyAllowedSourceRanges,
			service:        lbServiceWithSourceRangesField,
			platformStatus: awsPlatformStatus,
			expectStatus:   operatorv1.ConditionTrue,
		},
		{
			name:           "LoadBalancerService, LoadBalancerSourceRanges and AllowedSourceRanges are set",
			ic:             &loadBalancerIngressControllerWithAllowedSourceRanges,
			service:        lbServiceWithSourceRangesField,
			platformStatus: awsPlatformStatus,
			expectStatus:   operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS Subnets nil spec and nil status",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				nil,
			),
			service:           lbServiceWithNLB,
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS Subnets nil spec and empty status",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				&operatorv1.AWSSubnets{},
			),
			service:           lbServiceWithNLB,
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS Subnets spec with names and nil status, but feature gate disabled",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-12345"},
				},
				nil,
			),
			service:           lbServiceWithNLB,
			awsSubnetsEnabled: false,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS Subnets spec with names and nil status",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-12345"},
				},
				nil,
			),
			service:           lbServiceWithNLB,
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionTrue,
		},
		{
			name: "NLB LoadBalancerService, AWS Subnets nil spec and status with ids",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"subnet-12345"},
				},
			),
			service:           lbServiceWithNLB,
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionTrue,
		},
		{
			name: "NLB LoadBalancerService, AWS Subnets spec and status are equal",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-12345"},
					Names: []operatorv1.AWSSubnetName{"name-12345"},
				},
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-12345"},
					Names: []operatorv1.AWSSubnetName{"name-12345"},
				},
			),
			service:           lbServiceWithNLB,
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS Subnets spec and status are NOT equal",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-12345"},
					Names: []operatorv1.AWSSubnetName{"name-12345"},
				},
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-67890"},
					Names: []operatorv1.AWSSubnetName{"name-67890"},
				},
			),
			service:           lbServiceWithNLB,
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionTrue,
		},
		{
			name: "NLB LoadBalancerService, AWS Subnets spec and status are equal with different order",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-12345", "subnet-67890"},
					Names: []operatorv1.AWSSubnetName{"name-12345", "name-67890"},
				},
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-67890", "subnet-12345"},
					Names: []operatorv1.AWSSubnetName{"name-67890", "name-12345"},
				},
			),
			service:           lbServiceWithNLB,
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS Subnets spec and status have extra items",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-67890", "subnet-12345", "subnet-54321"},
					Names: []operatorv1.AWSSubnetName{"name-12345", "name-67890"},
				},
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-12345", "subnet-67890"},
					Names: []operatorv1.AWSSubnetName{"name-67890", "name-12345", "name-54321"},
				},
			),
			service:           lbServiceWithNLB,
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionTrue,
		},
		{
			name: "CLB LoadBalancerService, AWS Subnets nil spec and nil status",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSClassicLoadBalancer,
				nil,
				nil,
			),
			service:           &corev1.Service{},
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionFalse,
		},
		{
			name: "CLB LoadBalancerService, AWS Subnets nil spec and empty status",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSClassicLoadBalancer,
				nil,
				&operatorv1.AWSSubnets{},
			),
			service:           &corev1.Service{},
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionFalse,
		},
		{
			name: "CLB LoadBalancerService, AWS Subnets spec with names and nil status",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-12345"},
				},
				nil,
			),
			service:           &corev1.Service{},
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionTrue,
		},
		{
			name: "CLB LoadBalancerService, AWS Subnets nil spec and status with ids",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSClassicLoadBalancer,
				nil,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"subnet-12345"},
				},
			),
			service:           &corev1.Service{},
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionTrue,
		},
		{
			name: "CLB LoadBalancerService, AWS Subnets spec and status are equal",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-12345"},
					Names: []operatorv1.AWSSubnetName{"name-12345"},
				},
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-12345"},
					Names: []operatorv1.AWSSubnetName{"name-12345"},
				},
			),
			service:           &corev1.Service{},
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionFalse,
		},
		{
			name: "CLB LoadBalancerService, AWS Subnets spec and status are NOT equal",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-12345"},
					Names: []operatorv1.AWSSubnetName{"name-12345"},
				},
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-67890"},
					Names: []operatorv1.AWSSubnetName{"name-67890"},
				},
			),
			service:           &corev1.Service{},
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionTrue,
		},
		{
			name: "CLB LoadBalancerService, AWS Subnets spec and status are equal with different order",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-12345", "subnet-67890"},
					Names: []operatorv1.AWSSubnetName{"name-12345", "name-67890"},
				},
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-67890", "subnet-12345"},
					Names: []operatorv1.AWSSubnetName{"name-67890", "name-12345"},
				},
			),
			service:           &corev1.Service{},
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionFalse,
		},
		{
			name: "CLB LoadBalancerService, AWS Subnets spec and status have extra items",
			ic: loadBalancerIngressControllerWithAWSSubnets(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-67890", "subnet-12345", "subnet-54321"},
					Names: []operatorv1.AWSSubnetName{"name-12345", "name-67890"},
				},
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-12345", "subnet-67890"},
					Names: []operatorv1.AWSSubnetName{"name-67890", "name-12345", "name-54321"},
				},
			),
			service:           &corev1.Service{},
			awsSubnetsEnabled: true,
			platformStatus:    awsPlatformStatus,
			expectStatus:      operatorv1.ConditionTrue,
		},
		{
			name: "NLB LoadBalancerService, AWS EIPAllocations nil spec and nil status",
			ic: loadBalancerIngressControllerWithAWSEIPAllocations(
				nil,
				nil,
			),
			service:                  lbServiceWithNLB,
			awsEIPAllocationsEnabled: true,
			platformStatus:           awsPlatformStatus,
			expectStatus:             operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS EIPAllocations nil spec and empty status",
			ic: loadBalancerIngressControllerWithAWSEIPAllocations(
				nil,
				[]operatorv1.EIPAllocation{},
			),
			service:                  lbServiceWithNLB,
			awsEIPAllocationsEnabled: true,
			platformStatus:           awsPlatformStatus,
			expectStatus:             operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS EIPAllocations spec with eipAllocations and nil status, but feature gate disabled",
			ic: loadBalancerIngressControllerWithAWSEIPAllocations(
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
				nil,
			),
			service:                  lbServiceWithNLB,
			awsEIPAllocationsEnabled: false,
			platformStatus:           awsPlatformStatus,
			expectStatus:             operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS EIPAllocations spec with eipAllocations and nil status",
			ic: loadBalancerIngressControllerWithAWSEIPAllocations(
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
				nil,
			),
			service:                  lbServiceWithNLB,
			awsEIPAllocationsEnabled: true,
			platformStatus:           awsPlatformStatus,
			expectStatus:             operatorv1.ConditionTrue,
		},
		{
			name: "NLB LoadBalancerService, AWS EIPAllocation nil spec and status with eipAllocations",
			ic: loadBalancerIngressControllerWithAWSEIPAllocations(
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
			),
			service:                  lbServiceWithNLB,
			awsEIPAllocationsEnabled: true,
			platformStatus:           awsPlatformStatus,
			expectStatus:             operatorv1.ConditionTrue,
		},
		{
			name: "NLB LoadBalancerService, AWS EIPAllocations spec and status are equal",
			ic: loadBalancerIngressControllerWithAWSEIPAllocations(
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
			),
			service:                  lbServiceWithNLB,
			awsEIPAllocationsEnabled: true,
			platformStatus:           awsPlatformStatus,
			expectStatus:             operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS EIPAllocations spec and status are NOT equal",
			ic: loadBalancerIngressControllerWithAWSEIPAllocations(
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
				[]operatorv1.EIPAllocation{"eipalloc-aaaaaaaaaaaaaaaaa", "eipalloc-bbbbbbbbbbbbbbbbb"},
			),
			service:                  lbServiceWithNLB,
			awsEIPAllocationsEnabled: true,
			platformStatus:           awsPlatformStatus,
			expectStatus:             operatorv1.ConditionTrue,
		},
		{
			name: "NLB LoadBalancerService, AWS EIPAllocations spec and status are equal with different order",
			ic: loadBalancerIngressControllerWithAWSEIPAllocations(
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
				[]operatorv1.EIPAllocation{"eipalloc-yyyyyyyyyyyyyyyyy", "eipalloc-xxxxxxxxxxxxxxxxx"},
			),
			service:                  lbServiceWithNLB,
			awsEIPAllocationsEnabled: true,
			platformStatus:           awsPlatformStatus,
			expectStatus:             operatorv1.ConditionFalse,
		},
		{
			name: "NLB LoadBalancerService, AWS EIPAllocations spec and status have extra items",
			ic: loadBalancerIngressControllerWithAWSEIPAllocations(
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy", "eipalloc-zzzzzzzzzzzzz"},
				[]operatorv1.EIPAllocation{"eipalloc-yyyyyyyyyyyyyyyyy", "eipalloc-xxxxxxxxxxxxxxxxx"},
			),
			service:                  lbServiceWithNLB,
			awsEIPAllocationsEnabled: true,
			platformStatus:           awsPlatformStatus,
			expectStatus:             operatorv1.ConditionTrue,
		},
		{
			name:           "LBType Empty LoadBalancerService (default Classic), IC Status LBType Classic",
			ic:             loadBalancerIngressControllerWithLBType(operatorv1.AWSClassicLoadBalancer),
			service:        lbService,
			platformStatus: awsPlatformStatus,
			expectStatus:   operatorv1.ConditionFalse,
		},
		{
			name:           "LBType Classic LoadBalancerService, IC Status LBType NLB",
			ic:             loadBalancerIngressControllerWithLBType(operatorv1.AWSNetworkLoadBalancer),
			service:        lbService,
			platformStatus: awsPlatformStatus,
			expectStatus:   operatorv1.ConditionTrue,
		},
		{
			name:           "LBType NLB LoadBalancerService, IC Status LBType Classic",
			ic:             loadBalancerIngressControllerWithLBType(operatorv1.AWSClassicLoadBalancer),
			service:        lbServiceWithNLB,
			platformStatus: awsPlatformStatus,
			expectStatus:   operatorv1.ConditionTrue,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := computeLoadBalancerProgressingStatus(test.ic, test.service, test.platformStatus, test.awsSubnetsEnabled, test.awsEIPAllocationsEnabled)
			if actual.Status != test.expectStatus {
				t.Errorf("expected status to be %s, got %s", test.expectStatus, actual.Status)
			}
			if len(test.expectMessageContains) != 0 && !strings.Contains(actual.Message, test.expectMessageContains) {
				t.Errorf("expected message to include %q, got %q", test.expectMessageContains, actual.Message)
			}
			if len(test.expectMessageDoesNotContain) != 0 && strings.Contains(actual.Message, test.expectMessageDoesNotContain) {
				t.Errorf("expected message not to include %q, got %q", test.expectMessageDoesNotContain, actual.Message)
			}
		})
	}
}

func Test_computeDeploymentAvailableCondition(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			deploy := &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: test.deploymentConditions,
				},
			}

			actual := computeDeploymentAvailableCondition(deploy)
			if actual.Status != test.expectDeploymentAvailableStatus {
				t.Errorf("expected %v, got %v", test.expectDeploymentAvailableStatus, actual.Status)
			}
		})
	}
}

func Test_computeDeploymentReplicasMinAvailableCondition(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {

			deploy := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: test.replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"ingresscontroller.operator.openshift.io/deployment-ingresscontroller": "default",
							"ingresscontroller.operator.openshift.io/hash":                         "75678b564c",
						},
					},
					Strategy: appsv1.DeploymentStrategy{
						Type:          appsv1.RollingUpdateDeploymentStrategyType,
						RollingUpdate: test.rollingUpdate,
					},
				},
				Status: appsv1.DeploymentStatus{
					AvailableReplicas: test.availableReplicas,
				},
			}

			actual := computeDeploymentReplicasMinAvailableCondition(deploy, []corev1.Pod{})
			if actual.Status != test.expectDeploymentReplicasMinAvailableStatus {
				t.Errorf("%q: expected %v, got %v", test.name, test.expectDeploymentReplicasMinAvailableStatus, actual.Status)
			}
		})
	}
}

func Test_computeDeploymentReplicasAllAvailableCondition(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
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
				t.Errorf("expected %v, got %v", test.expectDeploymentReplicasAllAvailableStatus, actual.Status)
			}
		})
	}
}

func Test_computeLoadBalancerStatus(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			actual := computeLoadBalancerStatus(test.controller, test.service, test.events)

			conditionsCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime", "Message"),
				cmpopts.EquateEmpty(),
				cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
			}
			if !cmp.Equal(actual, test.expect, conditionsCmpOpts...) {
				t.Fatalf("expected:\n%#v\ngot:\n%#v", test.expect, actual)
			}
		})
	}
}

// Test_computeIngressProgressingCondition verifies that
// computeIngressProgressingCondition returns the expected status condition.
func Test_computeIngressProgressingCondition(t *testing.T) {
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
		t.Run(tc.description, func(t *testing.T) {
			actual := computeIngressProgressingCondition(tc.conditions)
			conditionsCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime", "Reason", "Message"),
				cmpopts.EquateEmpty(),
			}
			if !cmp.Equal(actual, tc.expect, conditionsCmpOpts...) {
				t.Fatalf("expected %#v, got %#v", tc.expect, actual)
			}
		})
	}
}

func Test_computeIngressAvailableCondition(t *testing.T) {
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
		t.Run(tc.description, func(t *testing.T) {
			actual := computeIngressAvailableCondition(tc.conditions)
			conditionsCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime", "Reason", "Message"),
				cmpopts.EquateEmpty(),
			}
			if !cmp.Equal(actual, tc.expect, conditionsCmpOpts...) {
				t.Fatalf("expected %#v, got %#v", tc.expect, actual)
			}
		})
	}
}

func Test_IngressStatusesEqual(t *testing.T) {
	icStatusWithSubnetsOrEIPAllocations := func(lbType operatorv1.AWSLoadBalancerType, subnets *operatorv1.AWSSubnets, eipAllocations []operatorv1.EIPAllocation) operatorv1.IngressControllerStatus {
		icStatus := operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
						AWS: &operatorv1.AWSLoadBalancerParameters{
							Type: lbType,
						},
					},
				},
			},
		}
		switch lbType {
		case operatorv1.AWSNetworkLoadBalancer:
			icStatus.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{
				Subnets:        subnets,
				EIPAllocations: eipAllocations,
			}
		case operatorv1.AWSClassicLoadBalancer:
			icStatus.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.ClassicLoadBalancerParameters = &operatorv1.AWSClassicLoadBalancerParameters{
				Subnets: subnets,
			}
		}
		return icStatus
	}
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
		{
			description: "NLB Subnets names changed",
			expected:    false,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-567890"},
				},
				nil,
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-123456"},
				},
				nil,
			),
		},
		{
			description: "NLB Subnets names changed with multiple",
			expected:    false,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-123456", "name-890123"},
				},
				nil,
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-123456"},
				},
				nil,
			),
		},
		{
			description: "NLB Subnets names equal",
			expected:    true,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-123456"},
				},
				nil,
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-123456"},
				},
				nil,
			),
		},
		{
			description: "NLB Subnets different order names equal",
			expected:    true,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-123456", "name-890123"},
				},
				nil,
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"name-890123", "name-123456"},
				},
				nil,
			),
		},
		{
			description: "CLB Subnets IDs changed",
			expected:    false,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"subnet-890123"},
				},
				nil,
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"subnet-123456"},
				},
				nil,
			),
		},
		{
			description: "CLB Subnets IDs changed with multiple",
			expected:    false,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"subnet-123456", "subnet-890123"},
				},
				nil,
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"subnet-123456"},
				},
				nil,
			),
		},
		{
			description: "CLB Subnets IDs equal",
			expected:    true,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"name-123456"},
				},
				nil,
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"name-123456"},
				},
				nil,
			),
		},
		{
			description: "CLB Subnets different order IDs equal",
			expected:    true,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"subnet-123456", "subnet-890123"},
				},
				nil,
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"subnet-890123", "subnet-123456"},
				},
				nil,
			),
		},
		{
			description: "NLB EIPAllocations changed",
			expected:    false,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx"},
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-yyyyyyyyyyyyyyyyy"},
			),
		},
		{
			description: "NLB EIPAllocations changed with multiple",
			expected:    false,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx"},
			),
		},
		{
			description: "NLB EIPAllocations equal",
			expected:    true,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx"},
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx"},
			),
		},
		{
			description: "NLB EIPAllocations different order but equal",
			expected:    true,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
			),
			b: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-yyyyyyyyyyyyyyyyy", "eipalloc-xxxxxxxxxxxxxxxxx"},
			),
		},
		{
			description: "classicLoadBalancer parameters cleared",
			expected:    false,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSClassicLoadBalancer,
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx"},
			),
			b: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: operatorv1.LoadBalancerServiceStrategyType,
					LoadBalancer: &operatorv1.LoadBalancerStrategy{
						ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
							AWS: &operatorv1.AWSLoadBalancerParameters{},
						},
					},
				},
			},
		},
		{
			description: "networkLoadBalancer parameters cleared",
			expected:    false,
			a: icStatusWithSubnetsOrEIPAllocations(
				operatorv1.AWSNetworkLoadBalancer,
				nil,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx"},
			),
			b: operatorv1.IngressControllerStatus{
				EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
					Type: operatorv1.LoadBalancerServiceStrategyType,
					LoadBalancer: &operatorv1.LoadBalancerStrategy{
						ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
							AWS: &operatorv1.AWSLoadBalancerParameters{},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			if actual := IngressStatusesEqual(tc.a, tc.b); actual != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, actual)
			}
		})
	}
}

func Test_computeDNSStatus(t *testing.T) {
	tests := []struct {
		name           string
		controller     *operatorv1.IngressController
		record         *iov1.DNSRecord
		platformStatus *configv1.PlatformStatus
		dnsConfig      *configv1.DNS
		expect         []operatorv1.OperatorCondition
	}{
		{
			name: "DNSManaged false due to NoDNSZones",
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					PublicZone:  nil,
					PrivateZone: nil,
				},
			},
			expect: []operatorv1.OperatorCondition{{
				Type:   "DNSManaged",
				Status: operatorv1.ConditionFalse,
				Reason: "NoDNSZones",
			}},
		},
		{
			name: "DNSManaged false due to UnsupportedEndpointPublishingStrategy",
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.HostNetworkStrategyType,
					},
				},
			},
			expect: []operatorv1.OperatorCondition{{
				Type:   "DNSManaged",
				Status: operatorv1.ConditionFalse,
				Reason: "UnsupportedEndpointPublishingStrategy",
			}},
		},
		{
			name: "DNSManaged false due to UnmanagedLoadBalancerDNS",
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.UnmanagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.UnmanagedDNS,
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionFalse,
					Reason: "UnmanagedLoadBalancerDNS",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionUnknown,
					Reason: "UnmanagedDNS",
				},
			},
		},
		{
			name: "DNSManaged true and DNSReady is true due to NoFailedZones",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionTrue),
									LastTransitionTime: metav1.Now(),
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain:  "basedomain.com",
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionTrue,
					Reason: "NoFailedZones",
				},
			},
		},
		{
			name: "DNSManaged true but DNSReady is false due to RecordNotFound",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: nil,
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain:  "basedomain.com",
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionFalse,
					Reason: "RecordNotFound",
				},
			},
		},
		{
			name: "DNSManaged true but DNSReady is false due to NoZones",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain:  "basedomain.com",
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionFalse,
					Reason: "NoZones",
				},
			},
		},
		{
			name: "DNSManaged true but DNSReady is Unknown due to UnmanagedDNS",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.UnmanagedDNS,
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain:  "basedomain.com",
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionUnknown,
					Reason: "UnmanagedDNS",
				},
			},
		},
		{
			name: "DNSManaged true and DNSReady is false due to FailedZones",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionFalse),
									LastTransitionTime: metav1.Now(),
									Reason:             "FailedZones",
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: "basedomain.com",
					PublicZone: &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{
						ID: "zone1",
					},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionFalse,
					Reason: "FailedZones",
				},
			},
		},
		{
			name: "DNSManaged true and DNSReady is true with even with previously FailedZones",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordFailedConditionType,
									Status:             string(operatorv1.ConditionTrue),
									LastTransitionTime: metav1.NewTime(time.Now().Add(5 * time.Minute)),
									Reason:             "FailedZones",
								},
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionTrue),
									LastTransitionTime: metav1.NewTime(time.Now().Add(15 * time.Minute)),
									Reason:             "NoFailedZones",
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: "basedomain.com",
					PublicZone: &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{
						ID: "zone1",
					},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionTrue,
					Reason: "NoFailedZones",
				},
			},
		},
		{
			name: "DNSManaged true and DNSReady is false due to unknown condition",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionUnknown),
									LastTransitionTime: metav1.NewTime(time.Now().Add(15 * time.Minute)),
									Reason:             "UnknownZones",
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: "basedomain.com",
					PublicZone: &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{
						ID: "zone1",
					},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionFalse,
					Reason: "UnknownZones",
				},
			},
		},
		{
			// This text checks if precedence is given to failed zones over unknown zones.
			name: "DNSManaged true and DNSReady is false due to failed and unknown conditions",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionUnknown),
									LastTransitionTime: metav1.NewTime(time.Now().Add(15 * time.Minute)),
									Reason:             "UnknownZones",
								},
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionFalse),
									LastTransitionTime: metav1.NewTime(time.Now().Add(15 * time.Minute)),
									Reason:             "FailedZones",
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: "basedomain.com",
					PublicZone: &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{
						ID: "zone1",
					},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionFalse,
					Reason: "FailedZones",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actualConditions := computeDNSStatus(tc.controller, tc.record, tc.platformStatus, tc.dnsConfig)
			opts := cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "Message", "LastTransitionTime")
			if !cmp.Equal(actualConditions, tc.expect, opts) {
				t.Fatalf("found diff between actual and expected operator condition:\n%s", cmp.Diff(actualConditions, tc.expect, opts))
			}
		})
	}
}

func Test_MergeConditions(t *testing.T) {
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
				cond("D", "True", "Reason", start),
			},
			updates: []operatorv1.OperatorCondition{
				cond("A", "True", "Reason", middle),
				cond("B", "True", "Reason", middle),
				cond("C", "False", "Reason", middle),
				cond("D", "True", "NewReason", start),
			},
			expected: []operatorv1.OperatorCondition{
				cond("A", "True", "Reason", later),
				cond("B", "True", "Reason", start),
				// Test case C has lastTransitionTime update because it is a new condition.
				cond("C", "False", "Reason", later),
				cond("Ignored", "True", "Reason", start),
				// Test case D does not change lastTransitionTime because status did not change.
				cond("D", "True", "NewReason", start),
			},
		},
		"an update to only the message in CanaryChecksSucceeding should not change lastTransitionTime": {
			conditions: []operatorv1.OperatorCondition{{
				Type:               "CanaryChecksSucceeding",
				Status:             "False",
				LastTransitionTime: metav1.NewTime(start),
				Reason:             "CanaryChecksRepetitiveFailures",
				Message:            "Canary route checks for the default ingress controller are failing. Last 1 error messages:\nerror sending canary HTTP Request: Timeout: Get \"https://canary-openshift-ingress-canary.apps.example.local\": context deadline exceeded (Client.Timeout exceeded while awaiting headers) (x5 over 4m40s)",
			}},
			updates: []operatorv1.OperatorCondition{{
				Type:               "CanaryChecksSucceeding",
				Status:             "False",
				LastTransitionTime: metav1.NewTime(start),
				Reason:             "CanaryChecksRepetitiveFailures",
				Message:            "Canary route checks for the default ingress controller are failing. Last 1 error messages:\nerror sending canary HTTP Request: Timeout: Get \"https://canary-openshift-ingress-canary.apps.example.local\": context deadline exceeded (Client.Timeout exceeded while awaiting headers) (x14 over 15m10s)",
			}},
			expected: []operatorv1.OperatorCondition{{
				Type:   "CanaryChecksSucceeding",
				Status: "False",
				// A change to the condition's message should not change the lastTransitionTime.
				LastTransitionTime: metav1.NewTime(start),
				Reason:             "CanaryChecksRepetitiveFailures",
				// Use the updated message.
				Message: "Canary route checks for the default ingress controller are failing. Last 1 error messages:\nerror sending canary HTTP Request: Timeout: Get \"https://canary-openshift-ingress-canary.apps.example.local\": context deadline exceeded (Client.Timeout exceeded while awaiting headers) (x14 over 15m10s)",
			}},
		},
		"an update to message, reason, and status in CanaryChecksSucceeding should change lastTransitionTime, message, reason, and status": {
			conditions: []operatorv1.OperatorCondition{{
				Type:               "CanaryChecksSucceeding",
				Status:             "True",
				LastTransitionTime: metav1.NewTime(start),
				Reason:             "CanaryChecksSucceeding",
				Message:            "Canary route checks for the default ingress controller are successful",
			}},
			updates: []operatorv1.OperatorCondition{{
				Type:               "CanaryChecksSucceeding",
				Status:             "False",
				LastTransitionTime: metav1.NewTime(middle),
				Reason:             "CanaryChecksRepetitiveFailures",
				Message:            "Canary route checks for the default ingress controller are failing. Last 1 error messages:\nerror sending canary HTTP Request: Timeout: Get \"https://canary-openshift-ingress-canary.apps.example.local\": context deadline exceeded (Client.Timeout exceeded while awaiting headers) (x5 over 4m40s)",
			}},
			expected: []operatorv1.OperatorCondition{{
				Type:   "CanaryChecksSucceeding",
				Status: "False",
				// Update lastTransitionTime if status changed.
				LastTransitionTime: metav1.NewTime(later),
				Reason:             "CanaryChecksRepetitiveFailures",
				Message:            "Canary route checks for the default ingress controller are failing. Last 1 error messages:\nerror sending canary HTTP Request: Timeout: Get \"https://canary-openshift-ingress-canary.apps.example.local\": context deadline exceeded (Client.Timeout exceeded while awaiting headers) (x5 over 4m40s)",
			}},
		},
		"an update to only the status in CanaryChecksSucceeding should change lastTransitionTime and status": {
			conditions: []operatorv1.OperatorCondition{{
				Type:               "CanaryChecksSucceeding",
				Status:             "Unknown",
				LastTransitionTime: metav1.NewTime(start),
				Reason:             "CanaryChecksSucceeding",
				Message:            "Canary route checks for the default ingress controller are successful",
			}},
			updates: []operatorv1.OperatorCondition{{
				Type:               "CanaryChecksSucceeding",
				Status:             "True",
				LastTransitionTime: metav1.NewTime(middle),
				Reason:             "CanaryChecksSucceeding",
				Message:            "Canary route checks for the default ingress controller are successful",
			}},
			expected: []operatorv1.OperatorCondition{{
				Type:   "CanaryChecksSucceeding",
				Status: "True",
				// Update lastTransitionTime if status changed.
				LastTransitionTime: metav1.NewTime(later),
				Reason:             "CanaryChecksSucceeding",
				Message:            "Canary route checks for the default ingress controller are successful",
			}},
		},
		"an update to only the lastTransitionTime in CanaryChecksSucceeding should not change anything": {
			conditions: []operatorv1.OperatorCondition{{
				Type:               "CanaryChecksSucceeding",
				Status:             "True",
				LastTransitionTime: metav1.NewTime(start),
				Reason:             "CanaryChecksSucceeding",
				Message:            "Canary route checks for the default ingress controller are successful",
			}},
			updates: []operatorv1.OperatorCondition{{
				Type:               "CanaryChecksSucceeding",
				Status:             "True",
				LastTransitionTime: metav1.NewTime(middle),
				Reason:             "CanaryChecksSucceeding",
				Message:            "Canary route checks for the default ingress controller are successful",
			}},
			expected: []operatorv1.OperatorCondition{{
				Type:   "CanaryChecksSucceeding",
				Status: "True",
				// Don't update lastTransitionTime since status did not change.
				LastTransitionTime: metav1.NewTime(start),
				Reason:             "CanaryChecksSucceeding",
				Message:            "Canary route checks for the default ingress controller are successful",
			}},
		},
	}

	// Simulate the passage of time between original condition creation
	// and update processing
	fakeClock.SetTime(later)

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Logf("test: %s", name)
			actual := MergeConditions(test.conditions, test.updates...)
			if !conditionsEqual(test.expected, actual) {
				t.Errorf("expected:\n%v\nactual:\n%v", util.ToYaml(test.expected), util.ToYaml(actual))
			}
		})
	}
}

func Test_checkZoneInConfig(t *testing.T) {
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
			description: "empty strings (should fail)",
			expected:    false,
			in:          "",
			zone:        "",
			zoneType:    "ID",
		},
		{
			description: "zone.ID empty string (should fail)",
			expected:    false,
			in:          "test",
			zone:        "",
			zoneType:    "ID",
		},
		{
			description: "zone.ID with value (not equal should fail)",
			expected:    false,
			in:          "test",
			zone:        "notest",
			zoneType:    "ID",
		},
		{
			description: "zone.ID with value (equal should pass)",
			expected:    true,
			in:          "test",
			zone:        "test",
			zoneType:    "ID",
		},
		{
			description: "empty strings (should fail)",
			expected:    false,
			in:          "",
			zone:        "",
			zoneType:    "TAG",
		},
		{
			description: "zone.Tags['Name'] empty string (should fail)",
			expected:    false,
			in:          "test",
			zone:        "",
			zoneType:    "TAG",
		},
		{
			description: "zone.Tags['Name'] with value (not equal should fail)",
			expected:    false,
			in:          "test",
			zone:        "notest",
			zoneType:    "TAG",
		},
		{
			description: "zone.tags['Name'] with value (equal should pass)",
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
			dnsSpec = configv1.DNSSpec{PublicZone: z}
			dnsConfig = &configv1.DNS{Spec: dnsSpec}
			actual = checkZoneInConfig(dnsConfig, dnsZone)
			if actual != test.expected {
				t.Errorf("expected:%v actual:%v\n", test.expected, actual)
			}
		})
	}
}

func Test_computeIngressUpgradeableCondition(t *testing.T) {
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
			description: "if the service.beta.kubernetes.io/load-balancer-source-ranges is set",
			mutate: func(svc *corev1.Service) {
				svc.Annotations[corev1.AnnotationLoadBalancerSourceRangesKey] = "127.0.0.0/8"
			},
			expect: true,
		},
		{
			description: "if the LoadBalancerSourceRanges is set when AllowedSourceRanges is empty",
			mutate: func(svc *corev1.Service) {
				svc.Spec.LoadBalancerSourceRanges = []string{"127.0.0.0/8"}
			},
			expect: true,
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
			wantSvc, service, err := desiredLoadBalancerService(ic, deploymentRef, platformStatus, true, true, true)
			if err != nil {
				t.Errorf("unexpected error from desiredLoadBalancerService: %v", err)
				return
			}
			if !wantSvc {
				t.Error("unexpected false value from desiredLoadBalancerService")
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

			actual := computeIngressUpgradeableCondition(ic, deploymentRef, service, platformStatus, secret, true, true)
			if actual.Status != expectedStatus {
				t.Errorf("expected Upgradeable to be %q, got %q", expectedStatus, actual.Status)
			}
		})
	}
}

func Test_computeIngressEvaluationConditionsDetectedCondition(t *testing.T) {
	const (
		ingressDomain = "apps.foo.com"
	)
	testCases := []struct {
		description string
		mutate      func(*corev1.Service)
		secret      *corev1.Secret
		expect      bool
	}{
		{
			description: "if the service.beta.kubernetes.io/load-balancer-source-ranges is set to the empty string",
			mutate: func(svc *corev1.Service) {
				svc.Annotations[corev1.AnnotationLoadBalancerSourceRangesKey] = ""
			},
			expect: false,
		},
		{
			description: "if the service.beta.kubernetes.io/load-balancer-source-ranges is set",
			mutate: func(svc *corev1.Service) {
				svc.Annotations[corev1.AnnotationLoadBalancerSourceRangesKey] = "127.0.0.0/8"
			},
			expect: true,
		},
		{
			description: "if the LoadBalancerSourceRanges and AllowedSourceRanges are empty",
			mutate: func(svc *corev1.Service) {
				svc.Spec.LoadBalancerSourceRanges = []string{}
			},
			expect: false,
		},
		{
			description: "if the LoadBalancerSourceRanges is set when AllowedSourceRanges is empty",
			mutate: func(svc *corev1.Service) {
				svc.Spec.LoadBalancerSourceRanges = []string{"127.0.0.0/8"}
			},
			expect: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ic := &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
				},
				Spec: operatorv1.IngressControllerSpec{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							AllowedSourceRanges: []operatorv1.CIDR{},
						},
					},
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

			wantSvc, service, err := desiredLoadBalancerService(ic, deploymentRef, platformStatus, true, true, true)
			if err != nil {
				t.Fatalf("unexpected error from desiredLoadBalancerService: %v", err)
			}
			if !wantSvc {
				t.Fatal("unexpected false value from desiredLoadBalancerService")
			}
			if tc.mutate != nil {
				tc.mutate(service)
			}
			expectedStatus := operatorv1.ConditionFalse
			if tc.expect {
				expectedStatus = operatorv1.ConditionTrue
			}

			actual := computeIngressEvaluationConditionsDetectedCondition(ic, service)
			if actual.Status != expectedStatus {
				t.Errorf("expected EvaluationConditionDetected to be %q, got %q", expectedStatus, actual.Status)
			}

		})
	}
}

func Test_computeAllowedSourceRanges(t *testing.T) {
	tests := []struct {
		name    string
		service *corev1.Service
		expect  []operatorv1.CIDR
	}{
		{
			name:    "service is nil",
			service: nil,
			expect:  nil,
		},
		{
			name: "service doesn't have spec.LoadBalancerSourceRanges",
			service: &corev1.Service{
				Spec: corev1.ServiceSpec{},
			},
			expect: nil,
		},
		{
			name: "service has spec.LoadBalancerSourceRanges",
			service: &corev1.Service{
				Spec: corev1.ServiceSpec{
					LoadBalancerSourceRanges: []string{"10.0.0.0/8", "192.128.0.0/16"},
				},
			},
			expect: []operatorv1.CIDR{"10.0.0.0/8", "192.128.0.0/16"},
		},
		{
			name: "service has service.beta.kubernetes.io/load-balancer-source-ranges",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"service.beta.kubernetes.io/load-balancer-source-ranges": "10.0.0.0/8,192.128.0.0/16",
					},
				},
				Spec: corev1.ServiceSpec{},
			},
			expect: []operatorv1.CIDR{"10.0.0.0/8", "192.128.0.0/16"},
		},
		{
			name: "service has service.beta.kubernetes.io/load-balancer-source-ranges, but it's empty",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"service.beta.kubernetes.io/load-balancer-source-ranges": "",
					},
				},
			},
			expect: nil,
		},
		{
			name: "service has service.beta.kubernetes.io/load-balancer-source-ranges and spec.LoadBalancerSourceRanges",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"service.beta.kubernetes.io/load-balancer-source-ranges": "10.0.0.0/8,192.128.0.0/16",
					},
				},
				Spec: corev1.ServiceSpec{
					LoadBalancerSourceRanges: []string{"172.0.0.0/8", "210.128.0.0/16"},
				},
			},
			expect: []operatorv1.CIDR{"172.0.0.0/8", "210.128.0.0/16"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := computeAllowedSourceRanges(test.service)
			if !reflect.DeepEqual(actual, test.expect) {
				t.Errorf("expected %v, got %v", test.expect, actual)
			}
		})
	}
}
