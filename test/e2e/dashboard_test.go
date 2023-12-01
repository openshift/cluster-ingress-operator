//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	monitoringdashboard "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/monitoring-dashboard"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"time"
)

func TestDashboardCreation(t *testing.T) {
	t.Parallel()

	infraConfig := &configv1.Infrastructure{}
	if err := kclient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		t.Fatalf("failed to get infraConfig: %v", err)
	}

	dashboardCM := &corev1.ConfigMap{}
	if err := kclient.Get(context.TODO(), monitoringdashboard.ConfigMapName(), dashboardCM); err != nil {
		if errors.IsNotFound(err) && infraConfig.Status.ControlPlaneTopology == configv1.ExternalTopologyMode {
			// Dashboard is not created when external topology is externel
			return
		}
		t.Fatalf("failed to get dashboard configmap: %v", err)
	}

	initialData := dashboardCM.Data

	// Change dashboard in configmap and check for update from the operator
	dashboardCM.Data = map[string]string{
		monitoringdashboard.DashboardFileName: "",
	}
	if err := kclient.Update(context.TODO(), dashboardCM); err != nil {
		t.Fatalf("failed to update dashboard configmap: %v", err)
	}
	err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		err := kclient.Get(context.TODO(), monitoringdashboard.ConfigMapName(), dashboardCM)
		if err != nil {
			t.Logf("Failed to get ConfigMap, retrying... Error: %v", err)
			return false, nil
		}
		if dashboard, ok := dashboardCM.Data[monitoringdashboard.DashboardFileName]; ok && dashboard != "" {
			return true, nil
		}
		t.Logf("ConfigMap not yet updated, retrying...")
		return false, nil
	})
	if err != nil {
		t.Fatalf("failed to observe configmap: %v", err)
	}

	// Delete configmap and check for update from the operator
	dashboardCM.Data = map[string]string{
		monitoringdashboard.DashboardFileName: "",
	}
	if err := kclient.Delete(context.TODO(), dashboardCM); err != nil {
		t.Fatalf("failed to delete dashboard configmap: %v", err)
	}
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), monitoringdashboard.ConfigMapName(), dashboardCM); err != nil {
			t.Logf("failed to get configmap: %v, retying...", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to observe configmap: %v", err)
	}
	if !reflect.DeepEqual(dashboardCM.Data, initialData) {
		t.Fatalf("data mismatch")
	}
}
