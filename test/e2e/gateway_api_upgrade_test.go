//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
)

// TestOSSMOperatorUpgradeViaIntermediateVersions verifies that the OSSM operator
// correctly upgrades through intermediate versions. It ensures the upgrade logic automatically approves
// each InstallPlan along the upgrade graph until the desired version is reached.
func TestOSSMOperatorUpgradeViaIntermediateVersions(t *testing.T) {
	gatewayAPIEnabled, err := isFeatureGateEnabled(features.FeatureGateGatewayAPI)
	if err != nil {
		t.Fatalf("Error checking feature gate enabled status: %v", err)
	}

	gatewayAPIControllerEnabled, err := isFeatureGateEnabled(features.FeatureGateGatewayAPIController)
	if err != nil {
		t.Fatalf("Error checking controller feature gate enabled status: %v", err)
	}

	if !gatewayAPIEnabled || !gatewayAPIControllerEnabled {
		t.Skip("Gateway API featuregates are not enabled, skipping TestOSSMOperatorUpgradeViaIntermediateVersions")
	}

	t.Cleanup(func() {
		t.Logf("Cleaning GatewayClass and OSSM operator...")
		if err := cleanupGatewayClass(t, "openshift-default"); err != nil {
			t.Errorf("Failed to cleanup gatewayclass: %v", err)
		}
		if err := cleanupOLMOperator(t, "openshift-operators", "servicemeshoperator3.openshift-operators"); err != nil {
			t.Errorf("Failed to cleanup OSSM operator: %v", err)
		}
	})

	var (
		// CatalogSource image saved before the release of servicemeshoperator 3.1.0.
		// This image is useful because it includes intermediate versions in the
		// servicemeshoperator3 upgrade graph. After 3.1.0 was released, all
		// upgrades from 3.0.z were changed to jump directly to 3.1.0, skipping
		// intermediate versions.
		customCatalogSourceImage = "registry.redhat.io/redhat/redhat-operator-index@sha256:069050dbf2970f0762b816a054342e551802c45fb77417c216aed70cec5e843c"
		customCatalogSourceName  = "custom-redhat-operators-4-19"
		initialOSSMVersion       = "servicemeshoperator3.v3.0.0"
		initialIstioVersion      = "v1.24.3"
		upgradeOSSMVersion       = "servicemeshoperator3.v3.0.3"
		upgradeIstioVersion      = "v1.24.6"
	)

	// Create custom catalog source with expected upgrade graph for servicemeshoperator3.
	t.Log("Creating custom CatalogSource...")
	if err := createCatalogSource(t, "openshift-marketplace", customCatalogSourceName, customCatalogSourceImage); err != nil {
		t.Fatalf("Failed to create CatalogSource %q: %v", customCatalogSourceName, err)
	}
	t.Log("Checking for the CatalogSource...")
	if err := assertCatalogSourceWithConfig(t, "openshift-marketplace", customCatalogSourceName, 2*time.Second, 2*time.Minute); err != nil {
		t.Fatalf("Failed to find expected CatalogSource %q: %v", customCatalogSourceName, err)
	}

	// Installation.
	t.Logf("Creating GatewayClass with OSSMversion %q ans Istio version %q...", initialOSSMVersion, initialIstioVersion)
	gatewayClass := buildGatewayClass("openshift-default", "openshift.io/gateway-controller/v1")
	gatewayClass.Annotations = map[string]string{
		"unsupported.do-not-use.openshift.io/ossm-catalog":  customCatalogSourceName,
		"unsupported.do-not-use.openshift.io/ossm-version":  initialOSSMVersion,
		"unsupported.do-not-use.openshift.io/istio-version": initialIstioVersion,
	}
	if err := kclient.Create(context.TODO(), gatewayClass); err != nil {
		t.Fatalf("Failed to create gatewayclass %s: %v", gatewayClass.Name, err)
	}
	t.Log("Checking for the Subscription...")
	if err := assertSubscription(t, openshiftOperatorsNamespace, expectedSubscriptionName); err != nil {
		t.Fatalf("Failed to find expected Subscription %s: %v", expectedSubscriptionName, err)
	}
	t.Log("Checking for the OSSM operator deployment...")
	if err := assertOSSMOperatorWithConfig(t, initialOSSMVersion, 2*time.Second, 2*time.Minute); err != nil {
		t.Fatalf("Failed to find expected Istio operator: %v", err)
	}
	t.Log("Checking for the Istio CR...")
	if err := assertIstioWithConfig(t, initialIstioVersion); err != nil {
		t.Fatalf("Failed to find expected Istio: %v", err)
	}
	t.Log("Checking for the GatewayClass readiness...")
	if _, err := assertGatewayClassSuccessful(t, "openshift-default"); err != nil {
		t.Fatalf("Failed to find successful GatewayClass: %v", err)
	}

	// Upgrade.
	t.Logf("Upgrading GatewayClass to version %q and Istio version %q...", upgradeOSSMVersion, upgradeIstioVersion)
	if err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(context context.Context) (bool, error) {
		gc := &gatewayapiv1.GatewayClass{}
		if err := kclient.Get(context, types.NamespacedName{Name: gatewayClass.Name}, gc); err != nil {
			t.Logf("Failed to get GatewayClass %q: %v, retrying...", gatewayClass.Name, err)
			return false, nil
		}
		gc.Annotations = map[string]string{
			"unsupported.do-not-use.openshift.io/ossm-catalog":  customCatalogSourceName,
			"unsupported.do-not-use.openshift.io/ossm-version":  upgradeOSSMVersion,
			"unsupported.do-not-use.openshift.io/istio-version": upgradeIstioVersion,
		}
		if err := kclient.Update(context, gc); err != nil {
			t.Logf("Failed to update GatewayClass %q: %v, retrying...", gc.Name, err)
			return false, nil
		}
		t.Logf("GatewayClass %q has been updated to version %q", gc.Name, upgradeOSSMVersion)
		return true, nil
	}); err != nil {
		t.Fatalf("Failed to update GatewayClass to next version: %v", err)
	}
	t.Log("Checking for the status...")
	progressingNotDegraded := []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
			Reason: "GatewayAPIOperatorUpgrading",
		},
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
			Reason: "IngressNotDegraded",
		},
	}
	if err := waitForClusterOperatorConditions(t, kclient, progressingNotDegraded...); err != nil {
		t.Fatalf("Operator should be Progressing=True and Degraded=False while upgrading: %v", err)
	}
	t.Log("Checking for the OSSM operator deployment...")
	if err := assertOSSMOperatorWithConfig(t, upgradeOSSMVersion, 2*time.Second, 4*time.Minute); err != nil {
		t.Fatalf("failed to find expected Istio operator: %v", err)
	}
	t.Log("Re-checking for the status...")
	notProgressingNotDegraded := []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionFalse,
			Reason: "AsExpectedAndGatewayAPIOperatorUpToDate",
		},
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
			Reason: "IngressNotDegraded",
		},
	}
	if err := waitForClusterOperatorConditions(t, kclient, notProgressingNotDegraded...); err != nil {
		t.Fatalf("Operator should be Progressing=False and Degraded=False once upgrade reached desired version: %v", err)
	}
	t.Log("Checking for the GatewayClass readiness...")
	if _, err := assertGatewayClassSuccessful(t, "openshift-default"); err != nil {
		t.Fatalf("Failed to find successful GatewayClass: %v", err)
	}
	t.Log("Checking for the Istio CR...")
	if err := assertIstioWithConfig(t, upgradeIstioVersion); err != nil {
		t.Fatalf("Failed to find expected Istio: %v", err)
	}
}
