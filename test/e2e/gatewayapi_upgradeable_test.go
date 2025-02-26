//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	gatewayapi_upgradeable "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayapi-upgradeable"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"testing"
	"time"
)

const (
	waitForAdminGateInterval = 3 * time.Second
	waitForAdminGateTimeout  = 3 * time.Minute
)

// TestAdminGateForGatewayAPIManagement tests the gatewayapi_upgradeable control loop by creating and updating the "admin-gates" ConfigMap,
// expecting the Ingress Operator to set and remove the admin gate for Gateway API management.
func TestAdminGateForGatewayAPIManagement(t *testing.T) {
	// Ensure admin gate doesn't exist before starting the test.
	if err := waitForAdminGate(t, gatewayapi_upgradeable.GatewayAPIAdminKey, false, waitForAdminGateInterval, waitForAdminGateTimeout); err != nil {
		t.Fatalf("failed to observe initial admin gate value: %v", err)
	}

	// Create the "admin-gates" ConfigMap with no data initially.
	configMapName := types.NamespacedName{Name: operatorcontroller.AdminGatesConfigMapName().Name, Namespace: operatorcontroller.AdminGatesConfigMapName().Namespace}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName.Name,
			Namespace: configMapName.Namespace,
		},
		Data: map[string]string{},
	}
	if err := kclient.Create(context.Background(), configMap); err != nil {
		t.Fatalf("failed to create configmap %s: %v", configMapName, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(context.Background(), configMap); err != nil {
			t.Fatalf("failed to delete configmap %s: %v", configMapName, err)
		}
	})

	// Poll for the admin gate, and wait until it is added.
	if err := waitForAdminGate(t, gatewayapi_upgradeable.GatewayAPIAdminKey, true, waitForAdminGateInterval, waitForAdminGateTimeout); err != nil {
		t.Fatalf("admin gate observation error: %v", err)
	}

	// Resolve the admin gate by removing the key from the ConfigMap.
	t.Log("resolving admin gate issue by removing the admin key")
	if err := kclient.Get(context.Background(), configMapName, configMap); err != nil {
		t.Fatalf("failed to get configmap %s: %v", configMapName, err)
	}
	delete(configMap.Data, gatewayapi_upgradeable.GatewayAPIAdminKey)
	if err := kclient.Update(context.Background(), configMap); err != nil {
		t.Fatalf("failed to update configmap %s: %v", configMapName, err)
	}
	if err := waitForAdminGate(t, gatewayapi_upgradeable.GatewayAPIAdminKey, false, waitForAdminGateInterval, waitForAdminGateTimeout); err != nil {
		t.Fatalf("admin gate observation error: %v", err)
	}
}

// waitForAdminGate is a helper function to wait for the admin gate to be in the expected state.
func waitForAdminGate(t *testing.T, key string, expected bool, interval, timeout time.Duration) error {
	t.Helper()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timeoutCh := time.After(timeout)

	for {
		select {
		case <-ticker.C:
			configMap := &corev1.ConfigMap{}
			if err := kclient.Get(context.Background(), operatorcontroller.AdminGatesConfigMapName(), configMap); err != nil {
				return err
			}
			_, exists := configMap.Data[key]
			if exists == expected {
				return nil
			}
		case <-timeoutCh:
			return fmt.Errorf("timeout waiting for admin gate to be %v", expected)
		}
	}
}
