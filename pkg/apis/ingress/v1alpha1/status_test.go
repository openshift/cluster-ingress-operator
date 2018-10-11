package v1alpha1

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
)

func TestClusterIngressStatusEqual(t *testing.T) {
	testCases := []struct {
		description string
		expected    bool
		a, b        ClusterIngressStatus
	}{
		{
			description: "empty statuses should be equal",
			expected:    true,
		},
		{
			description: "condition LastTransitionTime should be ignored",
			expected:    true,
			a: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:               operatorv1alpha1.OperatorStatusTypeAvailable,
							Status:             operatorv1alpha1.ConditionTrue,
							LastTransitionTime: metav1.Unix(0, 0),
						},
					},
				},
			},
			b: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:               operatorv1alpha1.OperatorStatusTypeAvailable,
							Status:             operatorv1alpha1.ConditionTrue,
							LastTransitionTime: metav1.Unix(1, 0),
						},
					},
				},
			},
		},
		{
			description: "order of conditions should not matter",
			expected:    true,
			a: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
			b: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			description: "order of ingresses should not matter",
			expected:    true,
			a: ClusterIngressStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							IP: "1.2.3.4",
						},
						{
							Hostname: "example.com",
						},
					},
				},
			},
			b: ClusterIngressStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							Hostname: "example.com",
						},
						{
							IP: "1.2.3.4",
						},
					},
				},
			},
		},
		{
			description: "check missing condition",
			expected:    false,
			a: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
			b: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			description: "check missing ingress",
			expected:    false,
			a: ClusterIngressStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{IP: "1.2.3.4"},
						{Hostname: "example.com"},
					},
				},
			},
			b: ClusterIngressStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{Hostname: "example.com"},
					},
				},
			},
		},
		{
			description: "check condition reason differs",
			expected:    false,
			a: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionFalse,
							Reason: "foo",
						},
					},
				},
			},
			b: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionFalse,
							Reason: "bar",
						},
					},
				},
			},
		},
		{
			description: "check condition message differs",
			expected:    false,
			a: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status:  operatorv1alpha1.ConditionFalse,
							Message: "foo",
						},
					},
				},
			},
			b: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status:  operatorv1alpha1.ConditionFalse,
							Message: "bar",
						},
					},
				},
			},
		},
		{
			description: "check ingress IP differs",
			expected:    false,
			a: ClusterIngressStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{IP: "1.2.3.4"},
					},
				},
			},
			b: ClusterIngressStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{IP: "1.2.3.5"},
					},
				},
			},
		},
		{
			description: "check ingress host name differs",
			expected:    false,
			a: ClusterIngressStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{Hostname: "example.com"},
					},
				},
			},
			b: ClusterIngressStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{Hostname: "example.net"},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		actual := clusterIngressStatusEqual(&tc.a, &tc.b)
		if actual != tc.expected {
			t.Fatalf("%q: expected %v, got %v", tc.description,
				tc.expected, actual)
		}
	}
}

func TestSetStatusSyncCondition(t *testing.T) {
	testCases := []struct {
		description string
		err         error
		old, new    ClusterIngressStatus
	}{
		{
			description: "had no condition, got success",
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionFalse,
						},
					},
				},
			},
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionFalse,
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			description: "had no condition, got error",
			err:         errors.New("some error"),
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionFalse,
						},
					},
				},
			},
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionFalse,
						},
						{
							Type:    operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "SyncFailed",
							Message: "some error",
						},
					},
				},
			},
		},
		{
			description: "had success condition, got success",
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			description: "had success condition, got error",
			err:         errors.New("some error"),
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "SyncFailed",
							Message: "some error",
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			description: "had error condition, got success",
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "SyncFailed",
							Message: "some error",
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionFalse,
						},
					},
				},
			},
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionFalse,
						},
					},
				},
			},
		},
		{
			description: "had error, got same error",
			err:         errors.New("some error"),
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "SyncFailed",
							Message: "some error",
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "SyncFailed",
							Message: "some error",
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			description: "had error, got different error",
			err:         errors.New("some other error"),
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "SyncFailed",
							Message: "some error",
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "SyncFailed",
							Message: "some other error",
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		ci := ClusterIngress{}
		ci.Status = tc.old
		ci.SetStatusSyncCondition(tc.err)
		if !clusterIngressStatusEqual(&ci.Status, &tc.new) {
			t.Fatalf("%q: expected %#v, got %#v", tc.description,
				tc.new, ci.Status)
		}
	}
}

func TestSetStatusAvailableCondition(t *testing.T) {
	testCases := []struct {
		description string
		available   bool
		message     string
		old, new    ClusterIngressStatus
	}{
		{
			description: "had no condition, got available",
			available:   true,
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			description: "had no condition, got unavailable",
			message:     "some error",
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeAvailable,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "ServiceNotReady",
							Message: "some error",
						},
					},
				},
			},
		},
		{
			description: "had available condition, got available",
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
			available: true,
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			description: "had available condition, got unavailable",
			message:     "some error",
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeAvailable,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "ServiceNotReady",
							Message: "some error",
						},
					},
				},
			},
		},
		{
			description: "had unavailable condition, got available",
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeAvailable,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "ServiceNotReady",
							Message: "some error",
						},
					},
				},
			},
			available: true,
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeAvailable,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			description: "had unavailable condition, got same condition",
			message:     "some error",
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeAvailable,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "ServiceNotReady",
							Message: "some error",
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:    operatorv1alpha1.OperatorStatusTypeAvailable,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "ServiceNotReady",
							Message: "some error",
						},
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			description: "had unavailable condition, got different unavailable condition",
			message:     "some other error",
			old: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
						{
							Type:    operatorv1alpha1.OperatorStatusTypeAvailable,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "ServiceNotReady",
							Message: "some error",
						},
					},
				},
			},
			new: ClusterIngressStatus{
				OperatorStatus: operatorv1alpha1.OperatorStatus{
					Conditions: []operatorv1alpha1.OperatorCondition{
						{
							Type:   operatorv1alpha1.OperatorStatusTypeSyncSuccessful,
							Status: operatorv1alpha1.ConditionTrue,
						},
						{
							Type:    operatorv1alpha1.OperatorStatusTypeAvailable,
							Status:  operatorv1alpha1.ConditionFalse,
							Reason:  "ServiceNotReady",
							Message: "some other error",
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		ci := ClusterIngress{}
		ci.Status = tc.old
		ci.SetStatusAvailableCondition(tc.available, tc.message)
		if !clusterIngressStatusEqual(&ci.Status, &tc.new) {
			t.Fatalf("%q: expected %#v, got %#v", tc.description,
				tc.new, ci.Status)
		}
	}
}
