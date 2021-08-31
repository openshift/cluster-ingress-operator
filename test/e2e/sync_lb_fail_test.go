//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

var icName = types.NamespacedName{Namespace: "openshift-ingress", Name: "synclb"}

var badLBService = corev1.Service{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "router-" + icName.Name,
		Namespace: "openshift-ingress",
		Annotations: map[string]string{
			// this annotation is intentionally set to an invalid value, which
			// should cause a SyncLoadBalancerFailed event
			"service.beta.kubernetes.io/aws-load-balancer-proxy-protocol": "nonsense",
		},
		Labels: map[string]string{
			"app": "router",
			"ingresscontroller.operator.openshift.io/owning-ingresscontroller": icName.Name,
			"router": "router-" + icName.Name,
		},
	},
	Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeLoadBalancer,
		Selector: map[string]string{
			"ingresscontroller.operator.openshift.io/deployment-ingresscontroller": icName.Name,
		},
		Ports: []corev1.ServicePort{
			{
				Name:       "http",
				Port:       80,
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.IntOrString{IntVal: 80},
			},
			{
				Name:       "https",
				Port:       443,
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.IntOrString{IntVal: 443},
			},
		},
	},
}

func TestSyncLBFailEvent(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.Platform != configv1.AWSPlatformType {
		t.Skip("test skipped on non-aws platforms")
	}
	if err := kclient.Create(context.TODO(), &badLBService); err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	defer kclient.Delete(context.TODO(), &badLBService)
	ic := newLoadBalancerController(icName, icName.Name+dnsConfig.Spec.BaseDomain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	defer kclient.Delete(context.TODO(), ic)
	eventList := &corev1.EventList{}
	err := wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		if err := kclient.List(context.TODO(), eventList, client.InNamespace(badLBService.ObjectMeta.Namespace)); err != nil {
			return false, nil
		}
		for _, event := range eventList.Items {
			if event.Reason == "SyncLoadBalancerFailed" {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Timed out waiting for SyncLoadBalancerFailed event")
	}
	expectedConditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.LoadBalancerReadyIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.OperatorStatusTypeDegraded, Status: operatorv1.ConditionTrue},
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, expectedConditions...); err != nil {
		t.Fatalf("Timed out waiting for status conditions LoadBalancerReady=false and Degraded=true for ingresscontroller %s", icName.Name)
	}
	// Make sure the LoadBalancerReady condition stays in a
	// SyncLoadBalancerFailed state even when the original events are removed.
	// If the loop exits due to timing out, then the status stayed in the
	// expected state, and the test should pass. If the condition does not stay
	// in the expected state, the test will immediately fail
	err = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		if err := kclient.List(context.TODO(), eventList, client.InNamespace(badLBService.ObjectMeta.Namespace)); err != nil {
			return false, nil
		}
		// Remove any SyncLoadBalancerFailed events that have been raised
		for _, event := range eventList.Items {
			if event.Reason == "SyncLoadBalancerFailed" {
				kclient.Delete(context.TODO(), &event)
			}
		}
		ic := &operatorv1.IngressController{}
		if err := kclient.Get(context.TODO(), icName, ic); err != nil {
			t.Logf("Failed to get ingresscontroller %s: %v", icName.Name, err)
			return false, nil
		}
		// Verify the ingresscontroller stays degraded with the LoadBalancerReady=False/SyncLoadBalancerFailed state
		expected := operatorConditionMap(expectedConditions...)
		current := operatorConditionMap(ic.Status.Conditions...)
		if conditionsMatchExpected(expected, current) {
			for _, condition := range ic.Status.Conditions {
				if condition.Type == operatorv1.LoadBalancerReadyIngressConditionType {
					if condition.Reason != "SyncLoadBalancerFailed" {
						t.Fatalf("ingresscontroller %s: expected %s to be SyncLoadBalancerFailed, got %s", icName.Name, operatorv1.LoadBalancerReadyIngressConditionType, condition.Reason)
					}
				}
			}
		} else {
			t.Fatalf("ingresscontroller %s should remain degraded", icName.Name)
		}
		return false, nil
	})
	if err != nil && err != wait.ErrWaitTimeout {
		t.Fatalf(err.Error())
	}
}
