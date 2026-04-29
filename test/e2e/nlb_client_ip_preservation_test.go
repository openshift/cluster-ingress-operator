//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
)

const (
	awsLBTargetGroupAttributesAnnotation = "service.beta.kubernetes.io/aws-load-balancer-target-group-attributes"
)

// TestAWSNLBClientIPPreservationMode verifies that the clientIPPreservationMode
// field on AWSNetworkLoadBalancerParameters correctly configures the NLB target
// group attributes and router proxy protocol settings.
func TestAWSNLBClientIPPreservationMode(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}

	name := types.NamespacedName{Namespace: operatorNamespace, Name: "nlb-pp-test"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newLoadBalancerController(name, domain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.ExternalLoadBalancer,
		DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
		ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.AWSLoadBalancerProvider,
			AWS: &operatorv1.AWSLoadBalancerParameters{
				Type: operatorv1.AWSNetworkLoadBalancer,
				NetworkLoadBalancerParameters: &operatorv1.AWSNetworkLoadBalancerParameters{
					ClientIPPreservationMode: operatorv1.ClientIPPreservationProxyProtocol,
				},
			},
		},
	}

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the target-group-attributes annotation is set on the Service.
	waitForLBAnnotation(t, ic, awsLBTargetGroupAttributesAnnotation, true, "preserve_client_ip.enabled=false,proxy_protocol_v2.enabled=true")

	// Verify the CLB proxy protocol annotation is NOT set.
	waitForLBAnnotation(t, ic, "service.beta.kubernetes.io/aws-load-balancer-proxy-protocol", false, "")

	// Verify the router deployment has ROUTER_USE_PROXY_PROTOCOL=true.
	deployment := &appsv1.Deployment{}
	deploymentName := controller.RouterDeploymentName(ic)
	if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
		t.Fatalf("failed to get router deployment: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", "true"); err != nil {
		t.Fatalf("expected ROUTER_USE_PROXY_PROTOCOL=true on router deployment: %v", err)
	}

	// Verify the status has ProxyProtocol.
	if err := waitForIngressControllerClientIPPreservationMode(t, name, operatorv1.ClientIPPreservationProxyProtocol); err != nil {
		t.Fatalf("expected status clientIPPreservationMode to be ProxyProtocol: %v", err)
	}

	// Verify connectivity through the NLB.
	elbHostname := getIngressControllerLBAddress(t, ic)
	namespace := createNamespace(t, names.SimpleNameGenerator.GenerateName("nlb-pp-"))
	testPodName := types.NamespacedName{Name: name.Name + "-verify", Namespace: namespace.Name}
	testHostname := "apps." + ic.Spec.Domain
	t.Logf("verifying external connectivity for ingresscontroller %q with ProxyProtocol", ic.Name)
	verifyExternalIngressController(t, testPodName, testHostname, elbHostname)

	// Switch to Native and verify the changes.
	t.Logf("switching ingresscontroller %q to Native clientIPPreservationMode", ic.Name)
	if err := updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.ClientIPPreservationMode = operatorv1.ClientIPPreservationNative
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Verify the target-group-attributes annotation is removed.
	waitForLBAnnotation(t, ic, awsLBTargetGroupAttributesAnnotation, false, "")

	// Verify ROUTER_USE_PROXY_PROTOCOL is removed.
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", ""); err != nil {
		t.Fatalf("expected ROUTER_USE_PROXY_PROTOCOL to be removed from router deployment: %v", err)
	}

	// Verify the status has Native.
	if err := waitForIngressControllerClientIPPreservationMode(t, name, operatorv1.ClientIPPreservationNative); err != nil {
		t.Fatalf("expected status clientIPPreservationMode to be Native: %v", err)
	}
}

// TestAWSNLBDefaultClientIPPreservationMode verifies that a new NLB
// IngressController defaults to ProxyProtocol for clientIPPreservationMode.
func TestAWSNLBDefaultClientIPPreservationMode(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}

	name := types.NamespacedName{Namespace: operatorNamespace, Name: "nlb-default"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newLoadBalancerController(name, domain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.ExternalLoadBalancer,
		DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
		ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.AWSLoadBalancerProvider,
			AWS: &operatorv1.AWSLoadBalancerParameters{
				Type: operatorv1.AWSNetworkLoadBalancer,
			},
		},
	}

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the status defaults to ProxyProtocol.
	if err := waitForIngressControllerClientIPPreservationMode(t, name, operatorv1.ClientIPPreservationProxyProtocol); err != nil {
		t.Fatalf("expected new NLB to default to ProxyProtocol: %v", err)
	}

	// Verify the target-group-attributes annotation is set.
	waitForLBAnnotation(t, ic, awsLBTargetGroupAttributesAnnotation, true, "preserve_client_ip.enabled=false,proxy_protocol_v2.enabled=true")

	// Verify ROUTER_USE_PROXY_PROTOCOL=true is set.
	deployment := &appsv1.Deployment{}
	deploymentName := controller.RouterDeploymentName(ic)
	if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
		t.Fatalf("failed to get router deployment: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", "true"); err != nil {
		t.Fatalf("expected ROUTER_USE_PROXY_PROTOCOL=true on router deployment: %v", err)
	}
}

// waitForIngressControllerClientIPPreservationMode waits for the
// IngressController status to have the expected clientIPPreservationMode.
func waitForIngressControllerClientIPPreservationMode(t *testing.T, name types.NamespacedName, expected operatorv1.ClientIPPreservationMode) error {
	t.Helper()
	return wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 5*time.Minute, false, func(ctx context.Context) (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := kclient.Get(ctx, name, ic); err != nil {
			t.Logf("failed to get ingresscontroller %s: %v", name.Name, err)
			return false, nil
		}
		if ic.Status.EndpointPublishingStrategy == nil ||
			ic.Status.EndpointPublishingStrategy.LoadBalancer == nil ||
			ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters == nil ||
			ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS == nil ||
			ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters == nil {
			t.Logf("waiting for NLB parameters in status for %s", name.Name)
			return false, nil
		}
		actual := ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.ClientIPPreservationMode
		if actual != expected {
			t.Logf("expected clientIPPreservationMode %q, got %q for %s", expected, actual, name.Name)
			return false, nil
		}
		return true, nil
	})
}
