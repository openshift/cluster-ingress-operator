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
	"github.com/openshift/api/features"
	iov1 "github.com/openshift/api/operatoringress/v1"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	util "github.com/openshift/cluster-ingress-operator/pkg/util"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// The expected OSSM subscription name.
	expectedSubscriptionName = "servicemeshoperator3"
	// The expected OSSM catalog source name.
	expectedCatalogSourceName = "redhat-operators"
	// The expected catalog source namespace.
	expectedCatalogSourceNamespace = "openshift-marketplace"
	// The test gateway name used in multiple places.
	testGatewayName = "test-gateway"
	// gwapiCRDVAPName is the name of the ingress operator's Validating Admission Policy (VAP).
	gwapiCRDVAPName = "openshift-ingress-operator-gatewayapi-crd-admission"
)

var crdNames = []string{
	"gatewayclasses.gateway.networking.k8s.io",
	"gateways.gateway.networking.k8s.io",
	"httproutes.gateway.networking.k8s.io",
	"referencegrants.gateway.networking.k8s.io",
}

var xcrdNames = []string{
	"listenersets.gateway.networking.x-k8s.io",
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
		t.Run("testGatewayAPIManualDeployment", testGatewayAPIManualDeployment)
		t.Run("testGatewayAPIIstioInstallation", testGatewayAPIIstioInstallation)
		t.Run("testGatewayAPIDNS", testGatewayAPIDNS)
		t.Run("testGatewayAPIDNSListenerUpdate", testGatewayAPIDNSListenerUpdate)
		t.Run("testGatewayAPIDNSListenerWithNoHostname", testGatewayAPIDNSListenerWithNoHostname)

	} else {
		t.Log("Gateway API Controller not enabled, skipping controller tests")
	}
	t.Run("testGatewayAPIResourcesProtection", testGatewayAPIResourcesProtection)
	t.Run("testGatewayAPIRBAC", testGatewayAPIRBAC)
	t.Run("testOperatorDegradedCondition", testOperatorDegradedCondition)
}

// testGatewayAPIResources tests that Gateway API Custom Resource Definitions are available.
// It specifically verifies that when the GatewayAPI feature gate is enabled, that the Gateway API
// CRDs are created.
// It also deletes and ensure the CRDs are recreated.
func testGatewayAPIResources(t *testing.T) {
	// Make sure all the *.gateway.networking.k8s.io CRDs are available since the FeatureGate is enabled.
	ensureCRDs(t)

	// Deleting CRDs to ensure they gets recreated again
	bypassVAP(t, deleteCRDs)

	// Make sure all the *.gateway.networking.k8s.io CRDs are available since they should be recreated after manual deletion.
	ensureCRDs(t)
}

// testGatewayAPIIstioInstallation verifies that once the gatewayclass is
// created, the following operations are completed automatically and
// successfully:
//
//   - The required Subscription and CatalogSource are created.
//
//   - The OSSM operator is installed successfully, and it reports status
//     Running and Ready.
//
//   - Istiod is installed successfully and has status Running and Ready.
//
//   - The Istio CR is created successfully.
//
//   - If the Istio and Subscription CRs are deleted, they are recreated
//     automatically.
func testGatewayAPIIstioInstallation(t *testing.T) {
	t.Log("Checking for the Subscription...")
	if err := assertSubscription(t, openshiftOperatorsNamespace, expectedSubscriptionName); err != nil {
		t.Fatalf("failed to find expected Subscription %s: %v", expectedSubscriptionName, err)
	}
	t.Log("Checking for the CatalogSource...")
	if err := assertCatalogSource(t, expectedCatalogSourceNamespace, expectedCatalogSourceName); err != nil {
		t.Fatalf("failed to find expected CatalogSource %s: %v", expectedCatalogSourceName, err)
	}
	t.Log("Checking for the OSSM operator deployment and pods...")
	if err := assertOSSMOperator(t); err != nil {
		t.Fatalf("failed to find expected Istio operator: %v", err)
	}
	t.Log("Checking for the Istiod pods...")
	if err := assertIstiodControlPlane(t); err != nil {
		t.Fatalf("failed to find expected Istiod control plane: %v", err)
	}
	t.Log("Checking for the Istio CR...")
	if err := assertIstio(t); err != nil {
		t.Fatalf("failed to find expected Istio: %v", err)
	}
	t.Log("Deleting the Istio CR...")
	if err := deleteExistingIstio(t); err != nil {
		t.Fatalf("failed to delete existing Istio: %v", err)
	}
	t.Log("Checking that the Istio CR gets recreated...")
	if err := assertIstio(t); err != nil {
		t.Fatalf("failed to find expected Istio: %v", err)
	}
	t.Log("Deleting the Subscription...")
	if err := deleteExistingSubscription(t, openshiftOperatorsNamespace, expectedSubscriptionName); err != nil {
		t.Fatalf("failed to delete existing Subscription %s: %v", expectedSubscriptionName, err)
	}
	t.Log("Checking that the Subscription gets recreated...")
	if err := assertSubscription(t, openshiftOperatorsNamespace, expectedSubscriptionName); err != nil {
		t.Fatalf("failed to find expected Subscription %s: %v", expectedSubscriptionName, err)
	}
}

// testGatewayAPIObjects tests that Gateway API objects can be created successfully.
func testGatewayAPIObjects(t *testing.T) {
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

// testGatewayAPIManualDeployment verifies that Istio's "manual deployment"
// feature is not enabled (see
// <https://istio.io/latest/docs/tasks/traffic-management/ingress/gateway-api/#manual-deployment>).
// We only want Istio to allow "automated deployment" (see
// <https://istio.io/latest/docs/tasks/traffic-management/ingress/gateway-api/#automated-deployment>).
//
// When manual deployment is enabled, then Istio allows a gateway to use an
// existing service (for example, another gateway's service) by specifying that
// service in spec.addresses.  When a gateway using manual deployment specifies
// another gateway's service, the resulting behavior is effectively the same
// behavior as Gateway API's concept of gateway listener "merging" (see
// <https://github.com/kubernetes-sigs/gateway-api/blob/v1.2.1/apis/v1/gateway_types.go#L181-L182>).
//
// Gateway listener merging is underspecified in Gateway API and is not
// consistently implemented among Gateway API implementations, and so we do not
// want to allow it or any similar behavior (such as Istio's "manual
// deployment") until such a time as it is well defined, standard behavior.
// Instead, for the time being, we expect Istio to provision a service for a
// gateway ("automated deployment"), even if the gateway specifies some existing
// service in spec.addresses.
func testGatewayAPIManualDeployment(t *testing.T) {
	gatewayClass, err := createGatewayClass("openshift-default", "openshift.io/gateway-controller/v1")
	if err != nil {
		t.Fatalf("Failed to create gatewayclass: %v", err)
	}

	gatewayName := types.NamespacedName{
		Name:      "manual-deployment",
		Namespace: "openshift-ingress",
	}
	// Use the router's internal service in order to ensure that the
	// referent exists.  Using an existing service isn't strictly necessary
	// in order to verify that Istio does not use manual deployment; if
	// manual deployment *is* enabled, Istio rejects the gateway if it
	// points to a non-existent referent.  However, using an existing
	// service more closely reflects the way that manual deployment *would*
	// be used if it were allowed.
	const existingServiceHostname = "router-internal-default.openshift-ingress.svc.cluster.local"
	gateway := gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gatewayName.Name,
			Namespace: gatewayName.Namespace,
		},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: gatewayapiv1.ObjectName(gatewayClass.Name),
			Addresses: []gatewayapiv1.GatewayAddress{{
				Type:  ptr.To(gatewayapiv1.HostnameAddressType),
				Value: existingServiceHostname,
			}},
			Listeners: []gatewayapiv1.Listener{{
				Name:     "http",
				Hostname: ptr.To(gatewayapiv1.Hostname(fmt.Sprintf("*.manual-deployment.%s", dnsConfig.Spec.BaseDomain))),
				Port:     80,
				Protocol: "HTTP",
			}},
		},
	}
	t.Logf("Creating gateway %q...", gatewayName)
	if err := kclient.Create(context.Background(), &gateway); err != nil {
		t.Fatalf("Failed to create gateway %v: %v", gatewayName, err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Dumping gateway %q...", gatewayName)
			var gateway gatewayapiv1.Gateway
			if err := kclient.Get(context.Background(), gatewayName, &gateway); err != nil {
				t.Errorf("Failed to get gateway %v: %v", gatewayName, err)
			}
			t.Log(util.ToYaml(gateway))
		}
		if err := kclient.Delete(context.Background(), &gateway); err != nil {
			if !errors.IsNotFound(err) {
				t.Errorf("Failed to delete gateway %v: %v", gatewayName, err)
			}
		}
	})

	interval, timeout := 5*time.Second, 5*time.Minute
	t.Logf("Polling for up to %v to verify that the gateway is accepted...", timeout)
	if err := wait.PollUntilContextTimeout(context.Background(), interval, timeout, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, gatewayName, &gateway); err != nil {
			t.Logf("Failed to get gateway %v: %v; retrying...", gatewayName, err)

			return false, nil
		}

		for _, condition := range gateway.Status.Conditions {
			if condition.Type == string(gatewayapiv1.GatewayConditionAccepted) {
				t.Logf("Found %q status condition: %+v", gatewayapiv1.GatewayConditionAccepted, condition)

				if condition.Status == metav1.ConditionTrue {
					return true, nil
				}
			}
		}

		t.Logf("Observed that gateway %v is not yet accepted; retrying...", gatewayName)

		return false, nil
	}); err != nil {
		t.Errorf("Failed to observe the expected condition for gateway %v: %v", gatewayName, err)
	}

	serviceName := types.NamespacedName{
		Name:      fmt.Sprintf("%s-%s", gateway.Name, gatewayClass.Name),
		Namespace: gateway.Namespace,
	}
	var service corev1.Service
	t.Logf("Polling for up to %v to verify that service %q is created...", timeout, serviceName)
	if err := wait.PollUntilContextTimeout(context.Background(), interval, timeout, false, func(context context.Context) (bool, error) {
		if err := kclient.Get(context, serviceName, &service); err != nil {
			t.Logf("Failed to get service %s: %v; retrying...", serviceName, err)

			return false, nil
		}

		// Just verify that the service is created.  No need to verify
		// that a load balancer is provisioned.  Indeed, provisioning
		// will likely fail because Istio copies the address hostname to
		// the service spec.loadBalancerIP field, which at least some
		// cloud provider implementations reject.

		t.Logf("Found service %q", serviceName)

		return true, nil
	}); err != nil {
		t.Errorf("Failed to observe the expected condition for service %v: %v", serviceName, err)
	}
}

// testGatewayAPIResourcesProtection verifies that the ingress operator's Validating Admission Policy
// denies admission requests attempting to modify Gateway API CRDs on behalf of a user
// who is not the ingress operator's service account.
func testGatewayAPIResourcesProtection(t *testing.T) {
	// Create test experimental CRDs to be able to check the VAP protection
	// of the update verb for the experimental Gateway API group.
	// Since an API `Get` is called before the update, the CRD must exist in the cluster,
	// just like standard Gateway API CRDs.
	bypassVAP(t, ensureExperimentalCRDs)
	t.Cleanup(func() {
		bypassVAP(t, deleteExperimentalCRDs)
	})

	// Get kube client which impersonates ingress operator's service account.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	kubeConfig.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:openshift-ingress-operator:ingress-operator",
	}
	kubeClient, err := operatorclient.NewClient(kubeConfig)
	if err != nil {
		t.Fatalf("failed to to create kube client: %v", err)
	}

	// Create test CRDs.
	var testCRDs []*apiextensionsv1.CustomResourceDefinition
	for _, name := range append(crdNames, xcrdNames...) {
		testCRDs = append(testCRDs, buildGWAPICRDFromName(name))
	}

	testCases := []struct {
		name           string
		kclient        client.Client
		expectedErrMsg string
	}{
		{
			name:           "Ingress operator service account required",
			kclient:        kclient,
			expectedErrMsg: "Gateway API Custom Resource Definitions are managed by the Ingress Operator and may not be modified",
		},
		{
			name:           "Pod binding required",
			kclient:        kubeClient,
			expectedErrMsg: "this user must have both \"authentication.kubernetes.io/node-name\" and \"authentication.kubernetes.io/pod-name\" claims",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Verify that GatewayAPI CRD creation is forbidden.
			for i := range testCRDs {
				if err := wait.PollUntilContextTimeout(context.Background(), 2*time.Second, 30*time.Second, false, func(ctx context.Context) (bool, error) {
					if err := tc.kclient.Create(ctx, testCRDs[i]); err != nil {
						if kerrors.IsAlreadyExists(err) {
							// VAP was disabled and re-enabled at the beginning of the test.
							// It may take some time for the API server to process this change and register the VAP.
							// As a result, we might encounter a "CRD X already exists" error.
							// To handle this, we allow the API server some time to catch up.
							t.Logf("Failed to create CRD %q: %v; retrying...", testCRDs[i].Name, err)
							return false, nil
						}
						if !strings.Contains(err.Error(), tc.expectedErrMsg) {
							return false, fmt.Errorf("unexpected error received while creating CRD %q: %v", testCRDs[i].Name, err)
						}
						return true, nil
					}
					return false, fmt.Errorf("admission error is expected while creating CRD %q but not received", testCRDs[i].Name)
				}); err != nil {
					t.Errorf("failed to verify VAP protection for creating gateway API CRD %q: %v", testCRDs[i].Name, err)
				}
			}

			// Verify that GatewayAPI CRD update is forbidden.
			for i := range testCRDs {
				crdName := types.NamespacedName{Name: testCRDs[i].Name}
				crd := &apiextensionsv1.CustomResourceDefinition{}
				if err := tc.kclient.Get(context.Background(), crdName, crd); err != nil {
					t.Errorf("failed to get %q CRD: %v", crdName.Name, err)
					continue
				}
				crd.Spec = testCRDs[i].Spec
				if err := tc.kclient.Update(context.Background(), crd); err != nil {
					if !strings.Contains(err.Error(), tc.expectedErrMsg) {
						t.Errorf("unexpected error received while updating CRD %q: %v", testCRDs[i].Name, err)
					}
				} else {
					t.Errorf("admission error is expected while updating CRD %q but not received", testCRDs[i].Name)
				}
			}

			// Verify that GatewayAPI CRD deletion is forbidden.
			for i := range testCRDs {
				if err := tc.kclient.Delete(context.Background(), testCRDs[i]); err != nil {
					if !strings.Contains(err.Error(), tc.expectedErrMsg) {
						t.Errorf("unexpected error received while deleting CRD %q: %v", testCRDs[i].Name, err)
					}
				} else {
					t.Errorf("admission error is expected while deleting CRD %q but not received", testCRDs[i].Name)
				}
			}
		})
	}
}

// testGatewayAPIRBAC checks whether RBAC resources for Gateway API (such as the
// aggregated ClusterRoles) are properly deployed and aggregated.
func testGatewayAPIRBAC(t *testing.T) {
	aggregationMapping := map[string][]string{
		"system:openshift:gateway-api:aggregate-to-admin": {"admin", "edit"},
		"system:openshift:gateway-api:aggregate-to-view":  {"view"},
	}

	for srcClusterRoleName, destClusterRoleNames := range aggregationMapping {
		for _, destClusterRoleName := range destClusterRoleNames {
			t.Logf("verifying that ClusterRole %s aggregates all PolicyRules from %s", destClusterRoleName, srcClusterRoleName)

			if err := eventuallyClusterRoleContainsAggregatedPolicies(t, destClusterRoleName, srcClusterRoleName); err != nil {
				t.Errorf("ClusterRole %s did not aggregate PolicyRules from %s", destClusterRoleName, srcClusterRoleName)
			}
		}
	}
}

func testGatewayAPIDNS(t *testing.T) {
	domain := "gws." + dnsConfig.Spec.BaseDomain

	gatewayClass, err := createGatewayClass(operatorcontroller.OpenShiftDefaultGatewayClassName, operatorcontroller.OpenShiftGatewayClassControllerName)
	if err != nil {
		t.Fatalf("failed to create gatewayclass: %v", err)
	}

	testCases := []struct {
		name                       string
		createGateways             []testGateway
		expectedListenerConditions []metav1.Condition
		expectedDNSRecords         map[expectedDnsRecord]bool
	}{
		// TODO: In this case Gateway Listeners should be reported as conflicted. To be fixed in the future release.
		{
			name: "multipleGatewaysSameListenerHostname",
			createGateways: []testGateway{
				{gatewayName: "gw1",
					namespace: operatorcontroller.DefaultOperandNamespace,
					listeners: []testListener{
						{
							name:     "http",
							hostname: ptr.To("abc." + domain),
						},
					},
				},
				{gatewayName: "gw2",
					namespace: operatorcontroller.DefaultOperandNamespace,
					listeners: []testListener{
						{
							name:     "http",
							hostname: ptr.To("abc." + domain)},
					},
				},
			},
			expectedListenerConditions: []metav1.Condition{
				{Type: "Accepted", Status: metav1.ConditionTrue},
				{Type: "Conflicted", Status: metav1.ConditionFalse},
				{Type: "Programmed", Status: metav1.ConditionTrue},
				{Type: "ResolvedRefs", Status: metav1.ConditionTrue},
			},
			expectedDNSRecords: map[expectedDnsRecord]bool{
				{dnsName: "abc." + domain + ".", gatewayName: "gw1"}: true,
				{dnsName: "abc." + domain + ".", gatewayName: "gw2"}: true,
			},
		},
		{
			name: "gatewayListenersWithOverlappingHostname",
			createGateways: []testGateway{
				{gatewayName: "gw3",
					namespace: operatorcontroller.DefaultOperandNamespace,
					listeners: []testListener{
						{
							name:     "http",
							hostname: ptr.To("qwe." + domain)},
					},
				},
				{gatewayName: "gw4",
					namespace: operatorcontroller.DefaultOperandNamespace,
					listeners: []testListener{
						{
							name:     "http",
							hostname: ptr.To("*." + domain),
						},
					},
				},
			},
			expectedListenerConditions: []metav1.Condition{
				{Type: "Accepted", Status: metav1.ConditionTrue},
				{Type: "Conflicted", Status: metav1.ConditionFalse},
				{Type: "Programmed", Status: metav1.ConditionTrue},
				{Type: "ResolvedRefs", Status: metav1.ConditionTrue},
			},
			expectedDNSRecords: map[expectedDnsRecord]bool{
				{dnsName: "qwe." + domain + ".", gatewayName: "gw3"}: true,
				{dnsName: "*." + domain + ".", gatewayName: "gw4"}:   true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gateways []*gatewayapiv1.Gateway

			// Create gateways
			for _, gateway := range tc.createGateways {
				createdGateway, err := createGatewayWithListeners(gatewayClass, gateway.gatewayName, gateway.namespace, gateway.listeners)
				gateways = append(gateways, createdGateway)
				if err != nil {
					t.Fatalf("failed to create gateway %s: %v", gateway.gatewayName, err)
				}
			}

			t.Cleanup(func() {
				for _, gateway := range gateways {
					if err := kclient.Delete(context.TODO(), gateway); err != nil {
						if errors.IsNotFound(err) {
							continue
						}
						t.Errorf("Failed to delete gateway %q: %v", gateway.Name, err)
					}
				}
			})

			for _, gateway := range gateways {
				gateway, err := assertGatewaySuccessful(t, operatorcontroller.DefaultOperandNamespace, gateway.Name)
				if err != nil {
					t.Errorf("Failed to create %s gateway: %v", gateway.Name, err)
				}
			}

			if err := assertExpectedDNSRecords(t, tc.expectedDNSRecords); err != nil {
				t.Fatalf("dnsRecord expectations not met: %v", err)
			}

			t.Logf("Check if gateways listener conditions match expected state.")
			for _, gateway := range gateways {
				for _, listener := range gateway.Spec.Listeners {
					if err := waitForGatewayListenerCondition(t, types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}, string(listener.Name), tc.expectedListenerConditions...); err != nil {
						t.Fatalf("did not get expected listener %s condition: %v", listener.Name, err)
					} else {
						t.Logf("gateway: %s listener %s conditions match expected state.", gateway.Name, listener.Name)
					}
				}
			}
		})
	}

}

func testGatewayAPIDNSListenerUpdate(t *testing.T) {
	gatewayClass, err := createGatewayClass(operatorcontroller.OpenShiftDefaultGatewayClassName, operatorcontroller.OpenShiftGatewayClassControllerName)
	if err != nil {
		t.Fatalf("failed to create gatewayclass: %v", err)
	}

	domain := "gws." + dnsConfig.Spec.BaseDomain

	gateway, err := createGatewayWithListeners(gatewayClass, "test-gateway-update", operatorcontroller.DefaultOperandNamespace, []testListener{
		{name: "http-listener1", hostname: ptr.To("foo." + domain)},
		{name: "http-listener2", hostname: ptr.To("bar." + domain)},
	})

	t.Cleanup(func() {
		if err := kclient.Delete(context.TODO(), gateway); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Errorf("failed to delete gateway %q: %v", gateway.Name, err)
		}
	})

	if err != nil {
		t.Fatalf("failed to create gateway with multiple listeners: %v", err)
	}
	t.Logf("Created gateway %s with multiple hostnames", gateway.Name)

	gateway, err = assertGatewaySuccessful(t, operatorcontroller.DefaultOperandNamespace, "test-gateway-update")
	if err != nil {
		t.Fatalf("failed to accept/program gateway test-gateway-update: %v", err)
	}

	if err := assertExpectedDNSRecords(t, map[expectedDnsRecord]bool{
		{dnsName: "foo." + domain + ".", gatewayName: "test-gateway-update"}: true,
	}); err != nil {
		t.Fatalf("DNSRecord %s expectations not met: %v", "foo."+domain+".", err)
	}

	// Fetch the latest version of the Gateway resource.
	if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: operatorcontroller.DefaultOperandNamespace, Name: gateway.Name}, gateway); err != nil {
		t.Fatalf("failed to get gateway resource: %v", err)
	}

	// Modify one gateway listener hostname
	newHostname := gatewayapiv1.Hostname("baz." + domain)
	gateway.Spec.Listeners[0].Hostname = &newHostname

	if err := kclient.Update(context.TODO(), gateway); err != nil {
		t.Fatalf("failed to update gateway %v: %v", gateway.Name, err)
	}
	t.Logf("Modified gateway %s's listener hostname from %s to: %s", gateway.Name, "foo."+domain+".", "baz."+domain+".")

	if err := assertExpectedDNSRecords(t, map[expectedDnsRecord]bool{
		{dnsName: "foo." + domain + ".", gatewayName: "test-gateway-update"}: false,
		{dnsName: "bar." + domain + ".", gatewayName: "test-gateway-update"}: true,
		{dnsName: "baz." + domain + ".", gatewayName: "test-gateway-update"}: true,
	}); err != nil {
		t.Fatalf("DNSRecord %s expectations not met: %v", "baz."+domain+".", err)
	}

	// Fetch the latest version of the Gateway resource.
	if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: operatorcontroller.DefaultOperandNamespace, Name: gateway.Name}, gateway); err != nil {
		t.Fatalf("failed to get gateway resource: %v", err)
	}

	// Delete one of the listeners
	gateway.Spec.Listeners = gateway.Spec.Listeners[0 : len(gateway.Spec.Listeners)-1]

	if err := kclient.Update(context.TODO(), gateway); err != nil {
		t.Fatalf("failed to update gateway with removed listener: %v", err)
	}
	t.Logf("Deleted listener %s from gateway %s.", "http-listener2", gateway.Name)

	t.Logf("Checking that the dnsRecord for %s gets removed after removing the listener.", "bar."+domain+".")

	if err := assertExpectedDNSRecords(t, map[expectedDnsRecord]bool{
		{dnsName: "baz." + domain + ".", gatewayName: "test-gateway-update"}: true,
		{dnsName: "bar." + domain + ".", gatewayName: "test-gateway-update"}: false,
	}); err != nil {
		t.Fatalf("expected bar.%s. to be deleted, but it was not", domain)
	}

	if err := kclient.Delete(context.TODO(), gateway); err != nil {
		t.Errorf("failed to delete gateway %q: %v", gateway.Name, err)
	}

	t.Logf("Checking the remaining DNSRecord %s gets deleted after gateway deletion.", "baz."+domain+".")
	if err := assertExpectedDNSRecords(t, map[expectedDnsRecord]bool{
		{dnsName: "baz." + domain + ".", gatewayName: "test-gateway-update"}: false,
	}); err != nil {
		t.Fatalf("expected baz.%s. to be deleted, but it was not", domain)
	}
	t.Logf("Confirmed DNSRecord removed after gateway deletion.")
}

func testGatewayAPIDNSListenerWithNoHostname(t *testing.T) {
	gatewayClass, err := createGatewayClass(operatorcontroller.OpenShiftDefaultGatewayClassName, operatorcontroller.OpenShiftGatewayClassControllerName)
	if err != nil {
		t.Fatalf("failed to create gatewayclass: %v", err)
	}

	gateway, err := createGatewayWithListeners(gatewayClass, "test-nohost-gateway", operatorcontroller.DefaultOperandNamespace, []testListener{
		{name: "http-listener-no-host", hostname: nil},
	})

	t.Cleanup(func() {
		if err := kclient.Delete(context.TODO(), gateway); err != nil {
			t.Errorf("failed to delete gateway %q: %v", gateway.Name, err)
		}
	})

	if err != nil {
		t.Fatalf("failed to create gateway with a listener with no hostname: %v", err)
	}

	gateway, err = assertGatewaySuccessful(t, operatorcontroller.DefaultOperandNamespace, "test-nohost-gateway")
	if err != nil {
		t.Fatalf("failed to accept/program gateway test-nohost-gateway: %v", err)
	}

	t.Logf("Created gateway %s with a listener with no hostname", gateway.Name)

	err = wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 1*time.Minute, false, func(context context.Context) (bool, error) {
		dnsRecords := &iov1.DNSRecordList{}
		// List all DNSRecords from the default operand namespace.
		if err := kclient.List(context, dnsRecords, client.InNamespace(operatorcontroller.DefaultOperandNamespace)); err != nil {
			t.Logf("failed to list DNSRecords: %v; retrying...", err)
			return false, nil
		}

		for _, record := range dnsRecords.Items {
			if record.Labels["gateway.networking.k8s.io/gateway-name"] == gateway.Name {
				t.Fatalf("dnsrecord found while not expected: %v", err)
			}
		}
		t.Logf("No DNSRecord for gateway listener with no hostname, continuing polling...")
		return false, nil
	})
	t.Logf("Confirmed no DNSRecord created for gateway listener with no hostname.")
}

// testOperatorDegradedCondition verifies that unmanaged (e.g. experimental)
// Gateway API CRDs affect the ingress cluster operator's Degraded status.
func testOperatorDegradedCondition(t *testing.T) {
	// Ensure that the ingress operator is not in a Degraded state
	// to prevent conflicts with the unmanaged Gateway API CRDs logic.
	expectedDegraded := []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
			Reason: "IngressNotDegraded",
		},
	}
	if err := waitForClusterOperatorConditions(t, kclient, expectedDegraded...); err != nil {
		t.Fatalf("Operator should be Degraded=False: %v", err)
	}

	// Create experimental CRDs to check if the ingress cluster operator
	// transitions to the Degraded state.
	bypassVAP(t, ensureExperimentalCRDs)
	expectedDegraded = []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionTrue,
			Reason: "GatewayAPICRDsDegraded",
		},
	}
	if err := waitForClusterOperatorConditions(t, kclient, expectedDegraded...); err != nil {
		t.Errorf("Did not get expected Degraded=True condition: %v", err)
	}

	// Remove the experimental CRDs to checks that the ingress cluster operator
	// recovers from the Degraded state.
	bypassVAP(t, deleteExperimentalCRDs)
	expectedDegraded = []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
			Reason: "IngressNotDegraded",
		},
	}
	if err := waitForClusterOperatorConditions(t, kclient, expectedDegraded...); err != nil {
		t.Errorf("Did not get expected Degraded=False condition: %v", err)
	}
}

// ensureCRDs tests that the Gateway API custom resource definitions exist.
func ensureCRDs(t *testing.T) {
	t.Helper()
	for _, crdName := range crdNames {
		crdVersions, err := assertCRDExists(t, crdName)
		if err != nil {
			t.Fatalf("failed to find crd %s: %v", crdName, err)
		}
		t.Logf("Found CRD %s with the following served versions: %s", crdName, strings.Join(crdVersions, ", "))
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

// deleteExperimentalCRDs deletes experimental Gateway API custom resource definitions.
func deleteExperimentalCRDs(t *testing.T) {
	t.Helper()

	for _, crdName := range xcrdNames {
		err := deleteExistingCRD(t, crdName)
		if err != nil {
			t.Errorf("failed to delete crd %s: %v", crdName, err)
		}
	}
}

// ensureExperimentalCRDs creates experimental Gateway API custom resource definitions.
func ensureExperimentalCRDs(t *testing.T) {
	for _, crdName := range xcrdNames {
		if _, err := createCRD(crdName); err != nil {
			t.Fatalf("failed to create experimental crd %q: %v", crdName, err)
		} else {
			t.Logf("created experimental crd %q", crdName)
		}
	}
}

// ensureGatewayObjectCreation tests that gateway class, gateway, and http route objects can be created.
func ensureGatewayObjectCreation(ns *corev1.Namespace) error {
	var domain string

	gatewayClass, err := createGatewayClass(operatorcontroller.OpenShiftDefaultGatewayClassName, operatorcontroller.OpenShiftGatewayClassControllerName)
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

	_, err = createHttpRoute(ns.Name, "test-httproute", operatorcontroller.DefaultOperandNamespace, defaultRoutename, testGatewayName+"-"+operatorcontroller.OpenShiftDefaultGatewayClassName, testGateway)
	if err != nil {
		return fmt.Errorf("feature gate was enabled, but http route object could not be created: %v", err)
	}
	// The http route is cleaned up when the namespace is deleted.

	return nil
}

// ensureGatewayObjectSuccess tests that gateway class, gateway, and http route objects were accepted as valid,
// and that a curl to the application via the http route returns with a valid response.
func ensureGatewayObjectSuccess(t *testing.T, ns *corev1.Namespace) []string {
	errs := []string{}
	gateway := &gatewayapiv1.Gateway{}

	t.Log("Making sure the gatewayclass is created and accepted...")
	_, err := assertGatewayClassSuccessful(t, operatorcontroller.OpenShiftDefaultGatewayClassName)
	if err != nil {
		errs = append(errs, error.Error(err))
	}

	t.Log("Making sure the gateway is created and accepted...")
	gateway, err = assertGatewaySuccessful(t, operatorcontroller.DefaultOperandNamespace, testGatewayName)
	if err != nil {
		errs = append(errs, error.Error(err))
	}

	t.Log("Making sure the httproute is created and accepted...")
	_, err = assertHttpRouteSuccessful(t, ns.Name, "test-httproute", gateway)
	if err != nil {
		errs = append(errs, error.Error(err))
	} else {
		t.Log("Validating the connectivity to the backend application via the httproute...")
		err = assertHttpRouteConnection(t, defaultRoutename, gateway)
		if err != nil {
			errs = append(errs, error.Error(err))
		}
	}

	return errs
}
