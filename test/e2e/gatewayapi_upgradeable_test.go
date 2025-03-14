//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	test_crds "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/crds"

	"k8s.io/apimachinery/pkg/api/errors"
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

func TestGatewayAPIUpgradeable(t *testing.T) {
	if gatewayAPIEnabled, err := isFeatureGateEnabled(features.FeatureGateGatewayAPI); err != nil {
		t.Fatalf("error checking feature gate enabled status: %v", err)
	} else if gatewayAPIEnabled {
		t.Skip("Gateway API is enabled, skipping TestGatewayAPIUpgradeable")
	}

	t.Parallel()

	defer deleteExistingCRDs(t, append(expectedCRDs, incompatibleCRDs...))

	createCRDs(t, expectedCRDs)
	testOperatorUpgradeableCondition(t, true)
	createCRDs(t, incompatibleCRDs)
	testOperatorUpgradeableCondition(t, false)
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
			continue
		}
	}
}

func deleteExistingCRDs(t *testing.T, crds []*apiextensionsv1.CustomResourceDefinition) {
	t.Helper()
	for _, crd := range crds {
		deleteExistingCRD(t, crd.Name)
	}
}
