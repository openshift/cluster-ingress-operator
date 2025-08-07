//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
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

	var (
		initialOSSMVersion  = "servicemeshoperator3.v3.0.0"
		initialIstioVersion = "v1.24.3"
		upgradeOSSMVersion  = "servicemeshoperator3.v3.1.0"
		upgradeIstioVersion = "v1.26.2"
	)

	// Installation.
	t.Logf("Creating GatewayClass with OSSMversion %q ans Istio version %q...", initialOSSMVersion, initialIstioVersion)
	gatewayClass := buildGatewayClass("openshift-default", "openshift.io/gateway-controller/v1")
	gatewayClass.Annotations = map[string]string{
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
	t.Log("Checking for the CatalogSource...")
	if err := assertCatalogSource(t, expectedCatalogSourceNamespace, expectedCatalogSourceName); err != nil {
		t.Fatalf("Failed to find expected CatalogSource %s: %v", expectedCatalogSourceName, err)
	}
	t.Log("Checking for the OSSM operator deployment...")
	if err := assertOSSMOperatorWithConfig(t, initialOSSMVersion, 2*time.Second, 60*time.Second); err != nil {
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
			Reason: "OSSMOperatorUpgrading",
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
			Reason: "AsExpectedAndOSSMOperatorUpToDate",
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

// testConnectivity tests the connectivity to an HTTPRoute.
func testConnectivity(t *testing.T, gatewayClass *gatewayapiv1.GatewayClass) {
	t.Helper()

	// Use the dnsConfig base domain set up in TestMain.
	domain := "gws." + dnsConfig.Spec.BaseDomain

	t.Log("Creating gateway...")
	testGateway, err := createGateway(gatewayClass, "test-upgrade-gateway", "openshift-ingress", domain)
	if err != nil {
		t.Fatalf("Gateway could not be created: %v", err)
	}
	// The gateway is cleaned up in TestGatewayAPI.

	// Create a test namespace that cleans itself up and sets up its own service account and role binding.
	ns := createNamespace(t, names.SimpleNameGenerator.GenerateName("test-e2e-gwapi-upgrade-"))
	routeName := names.SimpleNameGenerator.GenerateName("test-hostname-") + "." + domain

	t.Log("Creating httproute...")
	if _, err = createHttpRoute(ns.Name, "test-httproute", "openshift-ingress", routeName, "test-upgrade-gateway-openshift-default", testGateway); err != nil {
		t.Fatalf("HTTPRoute could not be created: %v", err)
	}
	// The http route is cleaned up when the namespace is deleted.

	t.Log("Making sure the gateway is accepted...")
	if _, err := assertGatewaySuccessful(t, testGateway.Namespace, testGateway.Name); err != nil {
		t.Fatalf("Failed to find successful gateway: %v", err)
	}

	t.Log("Making sure the httproute is accepted...")
	if _, err := assertHttpRouteSuccessful(t, ns.Name, "test-httproute", testGateway); err != nil {
		t.Fatalf("Failed to find successful httproute: %v", err)
	}

	t.Log("Validating the connectivity to the backend application via the httproute...")
	if err := assertHttpRouteConnection(t, routeName, testGateway); err != nil {
		t.Fatalf("Failed to find successful httproute: %v", err)
	}
}
