//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openshift/api/features"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayclass"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/storage/names"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// The expected OSSM subscription name.
	expectedSubscriptionName = "servicemeshoperator"
	// The expected OSSM catalog source name.
	expectedCatalogSourceName = "redhat-operators"
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
// The default route name to be constructed.
var defaultRoutename = ""

// If the Gateway API feature gate is enabled, run a series of tests in order
// to validate if Gateway API resources are available, objects can be created
// successfully and also work properly, and that the Istio installation was
// successful.
// NOTE: do not change the name of the test.  If new tests are added while the
// feature gate is still in effect, preface the test names with "TestGatewayAPI"
// so that they run via the openshift/release test configuration.
func TestGatewayAPI(t *testing.T) {
	t.Parallel()

	// Skip if feature is not enabled
	if gatewayAPIEnabled, err := isFeatureGateEnabled(features.FeatureGateGatewayAPI); err != nil {
		t.Fatalf("error checking feature gate enabled status: %v", err)
	} else if !gatewayAPIEnabled {
		t.Skip("Gateway API not enabled, skipping TestGatewayAPI")
	}

	gatewayAPIControllerEnabled, err := isFeatureGateEnabled(features.FeatureGateGatewayAPIController)
	if err != nil {
		t.Fatalf("error checking controller feature gate enabled status: %v", err)
	}

	// Defer the cleanup of the test gateway.
	t.Cleanup(func() {
		testGateway := gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: testGatewayName, Namespace: operatorcontroller.DefaultOperandNamespace}}
		if err := kclient.Delete(context.TODO(), &testGateway); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Errorf("failed to delete gateway %q: %v", testGateway.Name, err)
		}
		// TODO: Uninstall OSSM after test is completed.
	})

	t.Run("testGatewayAPIResources", testGatewayAPIResources)
	if gatewayAPIControllerEnabled {
		t.Run("testGatewayAPIObjects", testGatewayAPIObjects)
		t.Run("testGatewayAPIIstioInstallation", testGatewayAPIIstioInstallation)
	} else {
		t.Log("Gateway API Controller not enabled, skipping testGatewayAPIObjects and testGatewayAPIIstioInstallation")
	}
}

// testGatewayAPIResources tests that Gateway API Custom Resource Definitions are available.
// It specifically verifies that when the GatewayAPI feature gate is enabled, that the Gateway API
// CRDs are created.
// It also deletes and ensure the CRDs are recreated.
func testGatewayAPIResources(t *testing.T) {
	t.Helper()
	// Make sure all the *.gateway.networking.k8s.io CRDs are available since the FeatureGate is enabled.
	ensureCRDs(t)

	// Deleting CRDs to ensure they gets recreated again
	deleteCRDs(t)

	// Make sure all the *.gateway.networking.k8s.io CRDs are available since they should be recreated after manual deletion.
	ensureCRDs(t)
}

// testGatewayAPIIstioInstallation tests that once the Gateway API Custom Resource GatewayClass is created, that
// the following installation operations complete automatically and successfully:
// - the required Subscription and CatalogSource are created.
// - the OSSM Istio operator is installed successfully and has status Running and Ready. e.g. istio-operator-9f5c88857-2xfrr  -n openshift-operators
// - Istiod is installed successfully and has status Running and Ready.  e.g istiod-openshift-gateway-867bb8d5c7-4z6mp -n openshift-ingress
// - the SMCP is created successfully (OSSM 2.x).
// - deletes SMCP and subscription and tests if it gets recreated
func testGatewayAPIIstioInstallation(t *testing.T) {
	t.Helper()

	if err := assertSubscription(t, openshiftOperatorsNamespace, expectedSubscriptionName); err != nil {
		t.Fatalf("failed to find expected Subscription %s: %v", expectedSubscriptionName, err)
	}
	if err := assertCatalogSource(t, expectedCatalogSourceNamespace, expectedCatalogSourceName); err != nil {
		t.Fatalf("failed to find expected CatalogSource %s: %v", expectedCatalogSourceName, err)
	}
	if err := assertOSSMOperator(t); err != nil {
		t.Fatalf("failed to find expected Istio operator: %v", err)
	}
	if err := assertIstiodControlPlane(t); err != nil {
		t.Fatalf("failed to find expected Istiod control plane: %v", err)
	}
	// TODO - In OSSM 3.x the configuration object to check will be different.
	if err := assertSMCP(t); err != nil {
		t.Fatalf("failed to find expected SMCP: %v", err)
	}
	// delete existing SMCP to test it gets recreated
	if err := deleteExistingSMCP(t); err != nil {
		t.Fatalf("failed to delete existing SMCP: %v", err)
	}
	// check if SMCP gets recreated
	if err := assertSMCP(t); err != nil {
		t.Fatalf("failed to find expected SMCP: %v", err)
	}
	// delete existing Subscription to test it gets recreated
	if err := deleteExistingSubscription(t, openshiftOperatorsNamespace, expectedSubscriptionName); err != nil {
		t.Fatalf("failed to delete existing Subscription %s: %v", expectedSubscriptionName, err)
	}
	// checks if subscription gets recreated.
	if err := assertSubscription(t, openshiftOperatorsNamespace, expectedSubscriptionName); err != nil {
		t.Fatalf("failed to find expected Subscription %s: %v", expectedSubscriptionName, err)
	}
}

// testGatewayAPIObjects tests that Gateway API objects can be created successfully.
func testGatewayAPIObjects(t *testing.T) {
	t.Helper()

	// Create a test namespace that cleans itself up and sets up its own service account and role binding.
	ns := createNamespace(t, names.SimpleNameGenerator.GenerateName("test-e2e-gwapi-"))

	// Validate that Gateway API objects can be created.
	if err := ensureGatewayObjectCreation(ns); err != nil {
		t.Fatalf("failed to create one or more gateway object/s: %v", err)
	}

	// Wait for the Gateway API objects to reach a successful status.
	errs := ensureGatewayObjectSuccess(t, ns)
	if len(errs) > 0 {
		t.Errorf("failed to observe successful status of one or more gateway object/s: %v", strings.Join(errs, ","))
	} else {
		t.Log("gateway class, gateway, and http route created successfully")
	}
}

// ensureCRDs tests that the Gateway API custom resource definitions exist.
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

// deleteCRDs deletes Gateway API custom resource definitions.
func deleteCRDs(t *testing.T) {
	t.Helper()
	for _, crdName := range crdNames {
		err := deleteExistingCRD(t, crdName)
		if err != nil {
			t.Errorf("failed to delete crd %s: %v", crdName, err)
		}
	}
}

// ensureGatewayObjectCreation tests that gateway class, gateway, and http route objects can be created.
func ensureGatewayObjectCreation(ns *corev1.Namespace) error {
	var domain string

	gatewayClass, err := createGatewayClass(gatewayclass.OpenShiftDefaultGatewayClassName, gatewayclass.OpenShiftGatewayClassControllerName)
	if err != nil {
		return fmt.Errorf("feature gate was enabled, but gateway class object could not be created: %v", err)
	}
	// We don't need to delete the gateway class so there is no cleanup function for it.

	// Use the dnsConfig base domain set up in TestMain.
	domain = "gws." + dnsConfig.Spec.BaseDomain

	testGateway, err := createGateway(gatewayClass, testGatewayName, operatorcontroller.DefaultOperandNamespace, domain)
	if err != nil {
		return fmt.Errorf("feature gate was enabled, but gateway object could not be created: %v", err)
	}
	// The gateway is cleaned up in TestGatewayAPI.

	hostname := names.SimpleNameGenerator.GenerateName("test-hostname-")
	defaultRoutename = hostname + "." + domain

	_, err = createHttpRoute(ns.Name, "test-httproute", operatorcontroller.DefaultOperandNamespace, defaultRoutename, testGatewayName+"-"+gatewayclass.OpenShiftDefaultGatewayClassName, testGateway)
	if err != nil {
		return fmt.Errorf("feature gate was enabled, but http route object could not be created: %v", err)
	}
	// The http route is cleaned up when the namespace is deleted.

	return nil
}

// ensureGatewayObjectSuccess tests that gateway class, gateway, and http route objects were accepted as valid,
// and that a curl to the application via the http route returns with a valid response.
func ensureGatewayObjectSuccess(t *testing.T, ns *corev1.Namespace) []string {
	t.Helper()
	errs := []string{}
	gateway := &gatewayapiv1.Gateway{}

	// Make sure gateway class was created successfully.
	_, err := assertGatewayClassSuccessful(t, gatewayclass.OpenShiftDefaultGatewayClassName)
	if err != nil {
		errs = append(errs, error.Error(err))
	}

	// Make sure gateway was created successfully.
	gateway, err = assertGatewaySuccessful(t, operatorcontroller.DefaultOperandNamespace, testGatewayName)
	if err != nil {
		errs = append(errs, error.Error(err))
	}

	_, err = assertHttpRouteSuccessful(t, ns.Name, "test-httproute", gateway)
	if err != nil {
		errs = append(errs, error.Error(err))
	} else {
		// Validate the connectivity to the backend app via http route.
		err = assertHttpRouteConnection(t, defaultRoutename, gateway)
		if err != nil {
			errs = append(errs, error.Error(err))
		}
	}

	return errs
}
