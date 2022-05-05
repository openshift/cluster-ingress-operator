package e2e

import (
	"os"
	"testing"
)

// TestAll is the entrypoint for `make test-e2e` unless you override
// with TEST=Test<foo> when invoking make. The goal of this test is to
// run as many tests in parallel before plodding through those tests
// that must run in series.
func TestAll(t *testing.T) {
	// This call to Run() will not return until all of its
	// parallel subtests complete. Each "parallel" test must
	// invoke t.Parallel().
	t.Run("parallel", func(t *testing.T) {
		if os.Getenv("E2E_SKIP_PARALLEL_TESTS") == "1" {
			t.Skip()
		}
		// t.Run("TestClientTLS", TestClientTLS)
		t.Run("TestContainerLogging", TestContainerLogging)
		t.Run("TestCreateIngressControllerThenSecret", TestCreateIngressControllerThenSecret)
		t.Run("TestCreateSecretThenIngressController", TestCreateSecretThenIngressController)
		t.Run("TestCustomErrorpages", TestCustomErrorpages)
		t.Run("TestCustomIngressClass", TestCustomIngressClass)
		t.Run("TestDynamicConfigManagerUnsupportedConfigOverride", TestDynamicConfigManagerUnsupportedConfigOverride)
		t.Run("TestForwardedHeaderPolicyAppend", TestForwardedHeaderPolicyAppend)
		t.Run("TestForwardedHeaderPolicyIfNone", TestForwardedHeaderPolicyIfNone)
		t.Run("TestForwardedHeaderPolicyNever", TestForwardedHeaderPolicyNever)
		t.Run("TestForwardedHeaderPolicyReplace", TestForwardedHeaderPolicyReplace)
		t.Run("TestHAProxyTimeouts", TestHAProxyTimeouts)
		t.Run("TestHAProxyTimeoutsRejection", TestHAProxyTimeoutsRejection)
		t.Run("TestHTTPCookieCapture", TestHTTPCookieCapture)
		t.Run("TestHTTPHeaderBufferSize", TestHTTPHeaderBufferSize)
		t.Run("TestHTTPHeaderCapture", TestHTTPHeaderCapture)
		t.Run("TestHeaderNameCaseAdjustment", TestHeaderNameCaseAdjustment)
		t.Run("TestHealthCheckIntervalIngressController", TestHealthCheckIntervalIngressController)
		t.Run("TestHostNetworkEndpointPublishingStrategy", TestHostNetworkEndpointPublishingStrategy)
		t.Run("TestHostNetworkPortBinding", TestHostNetworkPortBinding)
		t.Run("TestIngressControllerScale", TestIngressControllerScale)
		t.Run("TestIngressControllerServiceNameCollision", TestIngressControllerServiceNameCollision)
		// t.Run("TestInternalLoadBalancer", TestInternalLoadBalancer)
		t.Run("TestInternalLoadBalancerGlobalAccessGCP", TestInternalLoadBalancerGlobalAccessGCP)
		t.Run("TestLoadBalancingAlgorithmUnsupportedConfigOverride", TestLoadBalancingAlgorithmUnsupportedConfigOverride)
		t.Run("TestMaxConnectionsUnsupportedConfigOverride", TestMaxConnectionsUnsupportedConfigOverride)
		t.Run("TestNetworkLoadBalancer", TestNetworkLoadBalancer)
		t.Run("TestNodePortServiceEndpointPublishingStrategy", TestNodePortServiceEndpointPublishingStrategy)
		t.Run("TestProxyProtocolAPI", TestProxyProtocolAPI)
		t.Run("TestReloadIntervalUnsupportedConfigOverride", TestReloadIntervalUnsupportedConfigOverride)
		t.Run("TestRouteAdmissionPolicy", TestRouteAdmissionPolicy)
		t.Run("TestRouterCompressionParsing", TestRouterCompressionParsing)
		t.Run("TestScopeChange", TestScopeChange)
		// t.Run("TestTLSSecurityProfile", TestTLSSecurityProfile)
		t.Run("TestTunableRouterKubeletProbesForCustomIngressController", TestTunableRouterKubeletProbesForCustomIngressController)
		t.Run("TestUniqueDomainRejection", TestUniqueDomainRejection)
		t.Run("TestUniqueIdHeader", TestUniqueIdHeader)
		t.Run("TestUserDefinedIngressController", TestUserDefinedIngressController)
	})

	t.Run("serial", func(t *testing.T) {
		if os.Getenv("E2E_SKIP_SERIAL_TESTS") == "1" {
			t.Skip()
		}
		t.Run("TestCanaryRouteRotationAnnotation", TestCanaryRouteRotationAnnotation)
		t.Run("TestClusterOperatorStatusRelatedObjects", TestClusterOperatorStatusRelatedObjects)
		t.Run("TestConfigurableRouteNoConsumingUserNoRBAC", TestConfigurableRouteNoConsumingUserNoRBAC)
		t.Run("TestConfigurableRouteNoSecretNoRBAC", TestConfigurableRouteNoSecretNoRBAC)
		t.Run("TestConfigurableRouteRBAC", TestConfigurableRouteRBAC)
		t.Run("TestDefaultIngressCertificate", TestDefaultIngressCertificate)
		t.Run("TestDefaultIngressClass", TestDefaultIngressClass)
		t.Run("TestDefaultIngressControllerSteadyConditions", TestDefaultIngressControllerSteadyConditions) // maybe first test
		t.Run("TestHstsPolicyWorks", TestHstsPolicyWorks)
		t.Run("TestIngressControllerCustomEndpoints", TestIngressControllerCustomEndpoints)
		t.Run("TestIngressStatus", TestIngressStatus)
		t.Run("TestLocalWithFallbackOverrideForLoadBalancerService", TestLocalWithFallbackOverrideForLoadBalancerService)
		t.Run("TestLocalWithFallbackOverrideForNodePortService", TestLocalWithFallbackOverrideForNodePortService) // parallel candidate
		t.Run("TestOperatorSteadyConditions", TestOperatorSteadyConditions)
		t.Run("TestPodDisruptionBudgetExists", TestPodDisruptionBudgetExists)
		t.Run("TestProxyProtocolOnAWS", TestProxyProtocolOnAWS)
		t.Run("TestRouteHTTP2EnableAndDisableIngressConfig", TestRouteHTTP2EnableAndDisableIngressConfig)
		t.Run("TestRouteHTTP2EnableAndDisableIngressController", TestRouteHTTP2EnableAndDisableIngressController)
		t.Run("TestRouteHardStopAfterEnableOnIngressConfig", TestRouteHardStopAfterEnableOnIngressConfig)
		t.Run("TestRouteHardStopAfterEnableOnIngressController", TestRouteHardStopAfterEnableOnIngressController)
		t.Run("TestRouteHardStopAfterEnableOnIngressControllerHasPriorityOverIngressConfig", TestRouteHardStopAfterEnableOnIngressControllerHasPriorityOverIngressConfig)
		t.Run("TestRouteHardStopAfterTestInvalidDuration", TestRouteHardStopAfterTestInvalidDuration)
		t.Run("TestRouteHardStopAfterTestOneDayDuration", TestRouteHardStopAfterTestOneDayDuration)
		t.Run("TestRouteHardStopAfterTestZeroLengthDuration", TestRouteHardStopAfterTestZeroLengthDuration)
		t.Run("TestRouteNbthreadIngressController", TestRouteNbthreadIngressController) // could be parallel
		t.Run("TestRouterCompressionOperation", TestRouterCompressionOperation)
		t.Run("TestSyslogLogging", TestSyslogLogging) // parallel
		t.Run("TestUpdateDefaultIngressController", TestUpdateDefaultIngressController)
		t.Run("TestCanaryRoute", TestCanaryRoute)
	})
}
