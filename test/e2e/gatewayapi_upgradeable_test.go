//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	test_crds "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/crds"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var expectedCRDs = []*apiextensionsv1.CustomResourceDefinition{
	manifests.GatewayClassCRD_v1_2_1(),
	manifests.GatewayCRD_v1_2_1(),
	manifests.GRPCRouteCRD_v1_2_1(),
	manifests.HTTPRouteCRD_v1_2_1(),
	manifests.ReferenceGrantCRD_v1_2_1(),
}

var incompatibleCRDs = []*apiextensionsv1.CustomResourceDefinition{
	test_crds.ListenerSetCRD_experimental_v1(),
	test_crds.TCPRouteCRD_experimental_v1(),
}

// TestGatewayAPIUpgradeable verifies the operator's upgradeable condition and admin gate behavior
// when the Gateway API feature gate is disabled.
func TestGatewayAPIUpgradeable(t *testing.T) {
	t.Parallel()
	if gatewayAPIEnabled, err := isFeatureGateEnabled(features.FeatureGateGatewayAPI); err != nil {
		t.Fatalf("error checking feature gate enabled status: %v", err)
	} else if gatewayAPIEnabled {
		t.Skip("Gateway API is enabled, skipping TestGatewayAPIUpgradeable")
	}

	t.Cleanup(func() {
		for _, crd := range append(expectedCRDs, incompatibleCRDs...) {
			if err := kclient.Delete(context.TODO(), crd); err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				t.Errorf("failed to delete crd %q: %v", crd.Name, err)
			}
		}
	})

	createCRDs(t, expectedCRDs)
	testAdminGate(t, true)
	testOperatorUpgradeableCondition(t, true)
	createCRDs(t, incompatibleCRDs)
	testOperatorUpgradeableCondition(t, false)
	deleteExistingCRDs(t, append(expectedCRDs, incompatibleCRDs...))
	testAdminGate(t, false)
	testOperatorUpgradeableCondition(t, true)
}

func testOperatorUpgradeableCondition(t *testing.T, expectUpgradeable bool) {
	expectedStatusCondition := configv1.ConditionFalse
	if expectUpgradeable {
		expectedStatusCondition = configv1.ConditionTrue
	}

	expected := []configv1.ClusterOperatorStatusCondition{
		{Type: configv1.OperatorUpgradeable, Status: expectedStatusCondition},
	}

	if err := waitForClusterOperatorConditions(t, kclient, expected...); err != nil {
		t.Errorf("did not get expected upgradeable condition: %v", err)
	}
}

func createCRDs(t *testing.T, crds []*apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	for _, crd := range crds {
		if err := kclient.Create(context.TODO(), crd); err != nil {
			if !errors.IsAlreadyExists(err) {
				t.Fatalf("Failed to create CRD %s: %v", crd.Name, err)
			}
		}
	}
}

func deleteExistingCRDs(t *testing.T, crds []*apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	for _, crd := range crds {
		deleteExistingCRD(t, crd.Name)
	}
}

func testAdminGate(t *testing.T, shouldExist bool) {
	t.Helper()
	adminGatesConfigMap := &corev1.ConfigMap{}
	if err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(ctx context.Context) (bool, error) {
		if err := kclient.Get(ctx, client.ObjectKey{Namespace: "openshift-config-managed", Name: "admin-gates"}, adminGatesConfigMap); err != nil {
			t.Logf("Failed to get configmap admin-gates: %v, retrying...", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Fatalf("Timed out trying to get admin-gates configmap: %v", err)
	}

	_, adminGateKeyExists := adminGatesConfigMap.Data["ack-4.18-gateway-api-management-in-4.19"]
	if adminGateKeyExists != shouldExist {
		t.Fatalf("Expected admin gate key existence to be %v, but got %v", shouldExist, adminGateKeyExists)
	}
}
