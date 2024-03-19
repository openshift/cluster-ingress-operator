//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayclass"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"

	gwapi "sigs.k8s.io/gateway-api/apis/v1beta1"
)

const (
	// The expected OSSM subscription name.
	expectedSubscriptionName = "servicemeshoperator"
	// TODO - In OSSM 2.5 the catalog source name will change.
	// The expected OSSM catalog source name.
	expectedCatalogSourceName = "quay-iib"
	// The expected catalog source namespace.
	expectedCatalogSourceNamespace = "openshift-marketplace"
	// The test gateway name used in multiple places.
	testGatewayName = "test-gateway"
)

var crdNames = []string{
	"gatewayclasses.gateway.networking.k8s.io",
	"gateways.gateway.networking.k8s.io",
	"httproutes.gateway.networking.k8s.io",
	"referencegrants.gateway.networking.k8s.io",
}

// Global variables for testing.
// Record whether the feature gate for Gateway API is enabled.
var gatewayAPIEnabled = false

// The contents of the cluster feature gate.
var clusterFeatureGate = &configv1.FeatureGate{}

// The list of enabled features in the feature gate.
var enabledFeatures []configv1.FeatureGateName

// The default route name to be constructed.
var default_routename = ""

// The default test gateway to be constructed and later deleted.
var testGateway = &gwapi.Gateway{}

// TestGatewayAPIResources tests that Gateway API Custom Resource Definitions are available.
// It specifically verifies that when the GatewayAPI feature gate is enabled, that the Gateway API
// CRDs are created.
// NOTE: any test we need to run from CI has to have prefix "TestGatewayAPI" or it will not be run.
func TestGatewayAPIResources(t *testing.T) {
	// Get desired cluster version.
	version, err := getClusterVersion()
	if err != nil {
		t.Fatalf("cluster version not found: %v", err)
	}
	desiredVersion := version.Status.Desired.Version
	if len(desiredVersion) == 0 && len(version.Status.History) > 0 {
		desiredVersion = version.Status.History[0].Version
	}

	// Get the cluster feature gate.
	name := types.NamespacedName{"", "cluster"}
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, clusterFeatureGate); err != nil {
			t.Logf("failed to get %s feature gate: %v", name.Name, err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("failed to get cluster feature gate: %v", err)
	}

	// Check if the Gateway API feature gate is enabled, and get enabled feature gates.
	for _, fg := range clusterFeatureGate.Status.FeatureGates {
		if fg.Version != desiredVersion {
			continue
		}
		for _, fgAttribs := range fg.Enabled {
			if fgAttribs.Name == configv1.FeatureGateGatewayAPI {
				gatewayAPIEnabled = true
			}
			enabledFeatures = append(enabledFeatures, fgAttribs.Name)
		}
	}

	if gatewayAPIEnabled {
		// Make sure all the *.gateway.networking.k8s.io CRDs are available since FeatureGate is enabled.
		ensureCRDs(t)
	} else {
		t.Logf("Gateway API not enabled, skipping TestGatewayAPIResources")
	}
}

// TestGatewayAPIObjects tests that Gateway API objects can be created successfully.
// NOTE: any test we need to run from CI has to have prefix "TestGatewayAPI" or it will not be run.
func TestGatewayAPIObjects(t *testing.T) {
	// Skip if feature is not enabled
	if !gatewayAPIEnabled {
		t.Logf("Gateway API not enabled, skipping TestGatewayAPIObjects")
		return
	}

	// Create a test namespace that cleans itself up and sets up its own service account and role binding.
	ns := createNamespace(t, names.SimpleNameGenerator.GenerateName("test-e2e-gwapi-"))

	// Defer the cleanup of the test gateway.
	defer func() {
		if testGateway != nil {
			if err := kclient.Delete(context.TODO(), testGateway); err != nil {
				if errors.IsNotFound(err) {
					return
				}
				t.Fatalf("failed to delete gateway %q: %v", testGatewayName, err)
			}
		}
	}()

	// Update the ingress-operator cluster role with cluster-admin privileges.
	// TODO - Should not be needed in OSSM 2.5, but for now it still is. See https://issues.redhat.com/browse/OSSM-3508.
	if err := updateIngressOperatorRole(t); err != nil {
		t.Fatalf("failed to update ingress operator role: %v", err)
	}

	// Validate that Gateway API objects can be created.
	if err := ensureGatewayObjectCreation(t, ns); err != nil {
		t.Fatalf("failed to create one or more gateway object/s: %v", err)
	}

	// Wait for the Gateway API objects to reach a successful status.
	errors := ensureGatewayObjectSuccess(t, ns)
	if len(errors) > 0 {
		t.Errorf("failed to observe successful status of one or more gateway object/s: %v", strings.Join(errors, ","))
	} else {
		t.Logf("gateway class, gateway, and http route created successfully")
	}
}

// TestGatewayAPIIstioInstallation tests that once the Gateway API Custom Resource GatewayClass is created, that
// the following installation operations complete automatically and successfully:
// - the required Subscription and CatalogSource are created.
// - the OSSM Istio operator is installed successfully and has status Running and Ready. e.g. istio-operator-9f5c88857-2xfrr  -n openshift-operators
// - the Istiod proxy is installed successfully and has status Running and Ready.  e.g istiod-openshift-gateway-867bb8d5c7-4z6mp -n openshift-ingress
// - the SMCP is created successfully (OSSM 2.x).
// NOTE: any test we need to run from CI has to have prefix "TestGatewayAPI" or it will not be run.
func TestGatewayAPIIstioInstallation(t *testing.T) {
	// Skip if feature is not enabled
	if !gatewayAPIEnabled {
		t.Logf("Gateway API not enabled, skipping TestGatewayAPIIstioInstallation")
		return
	}

	if err := assertSubscription(t, openshiftOperatorsNamespace, expectedSubscriptionName); err != nil {
		t.Fatalf("failed to find expected Subscription %s", expectedSubscriptionName)
	}
	if err := assertCatalogSource(t, expectedCatalogSourceNamespace, expectedCatalogSourceName); err != nil {
		t.Fatalf("failed to find expected CatalogSource %s", expectedCatalogSourceName)
	}
	if err := assertOSSMOperator(t); err != nil {
		t.Fatalf("failed to find expected Istio operator")
	}
	if err := assertIstiodProxy(t); err != nil {
		t.Fatalf("failed to find expected Istiod proxy")
	}
	// TODO - In OSSM 3.x the configuration object to check will be different.
	if err := assertSMCP(t); err != nil {
		t.Fatalf("failed to find expected SMCP")
	}
}

func ensureCRDs(t *testing.T) {
	t.Helper()
	for _, crdName := range crdNames {
		crdVersion, err := assertCrdExists(t, crdName)
		if err != nil {
			t.Fatalf("failed to find crd %s: %v", crdName, err)
		}
		t.Logf("found crd %s at version %s", crdName, crdVersion)
	}
}

// ensureGatewayObjectCreation tests that gateway class, gateway, and http route objects can be created.
func ensureGatewayObjectCreation(t *testing.T, ns *corev1.Namespace) error {
	t.Helper()

	ok, gatewayClass := assertCanCreateGatewayClass(t, gatewayclass.OpenShiftDefaultGatewayClassName, gatewayclass.OpenShiftGatewayClassControllerName)
	if !ok {
		return fmt.Errorf("feature gate was enabled, but gateway class object could not be created")
	}
	// We don't need to delete the gateway class so there is no defer function for it.

	domain := getDefaultDomain(t)
	ok, testGateway = assertCanCreateGateway(t, gatewayClass, testGatewayName, openshiftIngressNamespace, domain)
	if !ok {
		return fmt.Errorf("feature gate was enabled, but gateway object could not be created")
	}
	// This is cleaned up by a defer function in TestGatewayAPIObjects.

	hostname := names.SimpleNameGenerator.GenerateName("test-hostname-")
	default_routename = hostname + "." + domain
	if domain == "" {
		default_routename = hostname + ".example.com"
	}
	ok, _ = assertCanCreateHttpRoute(t, ns.Name, "test-httproute", openshiftIngressNamespace, default_routename, testGatewayName+"-"+gatewayclass.OpenShiftDefaultGatewayClassName, testGateway)
	if !ok {
		return fmt.Errorf("feature gate was enabled, but http route object could not be created")
		// The http route is automatically deleted because its namespace is automatically deleted.
	}

	return nil
}

func enableFeatureGate(t *testing.T) {
	t.Helper()

	// Enable the feature gate for the rest of the test.
	clusterFeatureGate.Spec.FeatureSet = configv1.CustomNoUpgrade
	clusterFeatureGate.Spec.CustomNoUpgrade = &configv1.CustomFeatureGates{Enabled: append(enabledFeatures, configv1.FeatureGateGatewayAPI)}
	if err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Update(context.TODO(), clusterFeatureGate); err != nil {
			t.Log(err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Fatalf("error enabling Gateway API feature gate: %v", err)
	}
	t.Logf("enabled Gateway API feature gate")
}

func ensureGatewayObjectSuccess(t *testing.T, ns *corev1.Namespace) []string {
	t.Helper()
	errors := []string{}

	// Make sure gateway class was created successfully.
	err, _ := assertGatewayClassSuccessful(t, gatewayclass.OpenShiftDefaultGatewayClassName)
	if err != nil {
		errors = append(errors, error.Error(err))
	}

	// Make sure gateway was created successfully.
	err, gateway := assertGatewaySuccessful(t, openshiftIngressNamespace, testGatewayName)
	if err != nil {
		errors = append(errors, error.Error(err))
	}

	err, _ = assertHttpRouteSuccessful(t, ns.Name, "test-httproute", openshiftIngressNamespace, default_routename, gateway)
	if err != nil {
		errors = append(errors, error.Error(err))
	}

	// Validate the connectivity to the backend app via http route.
	err = assertHttpRouteConnection(t, default_routename)
	if err != nil {
		errors = append(errors, error.Error(err))
	}

	return errors
}
