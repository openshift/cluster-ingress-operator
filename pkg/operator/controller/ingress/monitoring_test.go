package ingress

import (
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func Test_serviceMonitorChanged(t *testing.T) {
	trueVar := true
	ic := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "router-default",
			Namespace: "openshift-ingress",
		},
	}
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       "router-default",
		UID:        "1",
		Controller: &trueVar,
	}
	sm1 := desiredServiceMonitor(ic, svc, deploymentRef)
	sm2 := desiredServiceMonitor(ic, svc, deploymentRef)
	if changed, _ := serviceMonitorChanged(sm1, sm2); changed {
		t.Fatal("expected changed to be false for two servicemonitors defined for the same ingresscontroller and service")
	}
	if err := unstructured.SetNestedField(sm2.Object, nil, "spec", "selector"); err != nil {
		t.Fatalf("failed to mutate servicemonitor: %v", err)
	}
	if changed, sm3 := serviceMonitorChanged(sm1, sm2); !changed {
		t.Fatal("expected changed to be true after clearing servicemonitor's selector")
	} else {
		if updatedChanged, _ := serviceMonitorChanged(sm1, sm3); !updatedChanged {
			t.Error("serviceMonitorChanged reported changes but did not make any update")
		}
		if changedAgain, _ := serviceMonitorChanged(sm2, sm3); changedAgain {
			t.Fatal("serviceMonitorChanged does not behave as a fixed-point function")
		}
	}
}
