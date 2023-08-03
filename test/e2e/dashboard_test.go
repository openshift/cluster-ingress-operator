//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	monitoringdashboard "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/monitoring-dashboard"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	monitoringdashboard "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/monitoring-dashboard"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"testing"
	"time"
)

func TestDashboardCreation(t *testing.T) {
	t.Parallel()

	dashboardCM := &corev1.ConfigMap{}
	if err := kclient.Get(context.TODO(), monitoringdashboard.ConfigMapName(), dashboardCM); err != nil {
		t.Fatalf("failed to get dashboard configmap: %v", err)
	}

	// Change dashboard in configmap and check for update from the operator
	dashboardCM.Data = map[string]string{
		"dashboard.json": "",
	}
	if err := kclient.Update(context.TODO(), dashboardCM); err != nil {
		t.Fatalf("failed to update dashboard configmap: %v", err)
	}
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), monitoringdashboard.ConfigMapName(), dashboardCM); err != nil {
			t.Logf("failed to get configmap: %v, retrying...", err)
			return false, nil
		}
		dashboard, ok := dashboardCM.Data["dashboard.json"]
		if !(ok && len(dashboard) > 0) {
			t.Logf("Controller did not modify the ConfigMap back to its original state, retrying...")
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe configmap: %v", err)
	}

	// Delete configmap and check for update from the operator
	dashboardCM.Data = map[string]string{
		"dashboard.json": "",
	}
	if err := kclient.Delete(context.TODO(), dashboardCM); err != nil {
		t.Fatalf("failed to delete dashboard configmap: %v", err)
	}
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), monitoringdashboard.ConfigMapName(), dashboardCM); err != nil {
			t.Logf("failed to get configmap: %v", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe configmap: %v", err)
	}

}
