package ingress

import (
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"

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

	// Verify that changing serviceDiscoveryRole is detected as a change.
	sm4 := desiredServiceMonitor(ic, svc, deploymentRef)
	if err := unstructured.SetNestedField(sm4.Object, "Endpoints", "spec", "serviceDiscoveryRole"); err != nil {
		t.Fatalf("failed to mutate servicemonitor: %v", err)
	}
	if changed, _ := serviceMonitorChanged(sm1, sm4); !changed {
		t.Fatal("expected changed to be true after mutating serviceDiscoveryRole")
	}
}

func Test_metricsRoleChanged(t *testing.T) {
	role1 := manifests.MetricsRole()
	role2 := manifests.MetricsRole()
	if changed, _ := metricsRoleChanged(role1, role2); changed {
		t.Fatal("expected changed to be false for two roles with identical rules")
	}
	role2.Rules = append(role2.Rules, rbacv1.PolicyRule{
		APIGroups: []string{"example.io"},
		Resources: []string{"foos"},
		Verbs:     []string{"get"},
	})
	if changed, updated := metricsRoleChanged(role1, role2); !changed {
		t.Fatal("expected changed to be true after adding a rule")
	} else if changedAgain, _ := metricsRoleChanged(role2, updated); changedAgain {
		t.Fatal("metricsRoleChanged does not behave as a fixed-point function")
	}
}
