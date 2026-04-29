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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
)

const (
	awsLBTargetGroupAttributesAnnotation = "service.beta.kubernetes.io/aws-load-balancer-target-group-attributes"
)

// TestAWSNLBProtocol verifies that the protocol field on
// AWSNetworkLoadBalancerParameters correctly configures the NLB target
// group attributes and router proxy protocol settings.
func TestAWSNLBProtocol(t *testing.T) {
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
					Protocol: operatorv1.NLBProtocolProxy,
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
	if err := waitForDeploymentEnvVar(t, deployment, 1*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", "true"); err != nil {
		t.Fatalf("expected ROUTER_USE_PROXY_PROTOCOL=true on router deployment: %v", err)
	}

	// Verify the status has PROXY protocol.
	if err := waitForIngressControllerNLBProtocol(t, name, operatorv1.NLBProtocolProxy); err != nil {
		t.Fatalf("expected status protocol to be PROXY: %v", err)
	}

	// Verify connectivity through the NLB.
	elbHostname := getIngressControllerLBAddress(t, ic)
	namespace := createNamespace(t, names.SimpleNameGenerator.GenerateName("nlb-pp-"))
	testPodName := types.NamespacedName{Name: name.Name + "-verify", Namespace: namespace.Name}
	testHostname := "apps." + ic.Spec.Domain
	t.Logf("verifying external connectivity for ingresscontroller %q with PROXY protocol", ic.Name)
	verifyExternalIngressController(t, testPodName, testHostname, elbHostname)

	// Switch to TCP and verify the changes.
	t.Logf("switching ingresscontroller %q to TCP protocol", ic.Name)
	if err := updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.Protocol = operatorv1.NLBProtocolTCP
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Verify the target-group-attributes annotation is updated with TCP values.
	waitForLBAnnotation(t, ic, awsLBTargetGroupAttributesAnnotation, true, "preserve_client_ip.enabled=true,proxy_protocol_v2.enabled=false")

	// Verify ROUTER_USE_PROXY_PROTOCOL is removed.
	if err := waitForDeploymentEnvVar(t, deployment, 1*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", ""); err != nil {
		t.Fatalf("expected ROUTER_USE_PROXY_PROTOCOL to be removed from router deployment: %v", err)
	}

	// Verify the status has TCP.
	if err := waitForIngressControllerNLBProtocol(t, name, operatorv1.NLBProtocolTCP); err != nil {
		t.Fatalf("expected status protocol to be TCP: %v", err)
	}

	// Verify connectivity still works after switching to TCP.
	t.Logf("verifying external connectivity for ingresscontroller %q with TCP protocol", ic.Name)
	verifyExternalIngressController(t, testPodName, testHostname, elbHostname)

	// Clear the spec protocol field and verify the operator reverts to
	// the controller-managed default (PROXY). Clearing the field means
	// "use the platform default," and the operator detects this by seeing
	// an empty spec with a non-empty status.
	t.Logf("clearing spec protocol for ingresscontroller %q to verify revert to default", ic.Name)
	if err := updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.Protocol = ""
	}); err != nil {
		t.Fatalf("failed to clear spec protocol: %v", err)
	}

	// Verify the status reverts to PROXY (the controller-managed default).
	if err := waitForIngressControllerNLBProtocol(t, name, operatorv1.NLBProtocolProxy); err != nil {
		t.Fatalf("expected status protocol to revert to PROXY after clearing spec: %v", err)
	}

	// Verify the target-group-attributes annotation is updated with PROXY values.
	waitForLBAnnotation(t, ic, awsLBTargetGroupAttributesAnnotation, true, "preserve_client_ip.enabled=false,proxy_protocol_v2.enabled=true")

	// Verify ROUTER_USE_PROXY_PROTOCOL is re-enabled.
	if err := waitForDeploymentEnvVar(t, deployment, 1*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", "true"); err != nil {
		t.Fatalf("expected ROUTER_USE_PROXY_PROTOCOL=true after reverting to PROXY: %v", err)
	}

	// Verify connectivity works after reverting to PROXY.
	t.Logf("verifying external connectivity for ingresscontroller %q after reverting to PROXY", ic.Name)
	verifyExternalIngressController(t, testPodName, testHostname, elbHostname)
}

// TestAWSNLBDefaultProtocol verifies that a new NLB IngressController
// defaults to PROXY protocol.
func TestAWSNLBDefaultProtocol(t *testing.T) {
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

	// Verify the status defaults to PROXY protocol.
	if err := waitForIngressControllerNLBProtocol(t, name, operatorv1.NLBProtocolProxy); err != nil {
		t.Fatalf("expected new NLB to default to PROXY protocol: %v", err)
	}

	// Verify the target-group-attributes annotation is set.
	waitForLBAnnotation(t, ic, awsLBTargetGroupAttributesAnnotation, true, "preserve_client_ip.enabled=false,proxy_protocol_v2.enabled=true")

	// Verify ROUTER_USE_PROXY_PROTOCOL=true is set.
	deployment := &appsv1.Deployment{}
	deploymentName := controller.RouterDeploymentName(ic)
	if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
		t.Fatalf("failed to get router deployment: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, deployment, 1*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", "true"); err != nil {
		t.Fatalf("expected ROUTER_USE_PROXY_PROTOCOL=true on router deployment: %v", err)
	}
}

// TestAWSNLBUpgradeAnnotationPreservation simulates upgrading a pre-existing
// NLB IngressController and verifies:
//  1. The operator does not re-default protocol for already-admitted ICs
//     (status NLB protocol stays empty after clearing).
//  2. The operator does not add the target-group-attributes annotation when
//     status NLB protocol is empty (common upgrade case: no manual annotation).
//  3. The operator does not stomp a user-set target-group-attributes annotation
//     (hairpin workaround case).
//  4. After the user opts in by setting spec...nlbParameters.protocol=PROXY, the operator takes
//     over annotation management correctly.
func TestAWSNLBUpgradeAnnotationPreservation(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}

	name := types.NamespacedName{Namespace: operatorNamespace, Name: "nlb-upgrade"}
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

	t.Logf("creating NLB ingresscontroller %q (will default to PROXY protocol)", ic.Name)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the new IC defaulted to PROXY protocol.
	if err := waitForIngressControllerNLBProtocol(t, name, operatorv1.NLBProtocolProxy); err != nil {
		t.Fatalf("expected new NLB to default to PROXY protocol: %v", err)
	}
	waitForLBAnnotation(t, ic, awsLBTargetGroupAttributesAnnotation, true, "preserve_client_ip.enabled=false,proxy_protocol_v2.enabled=true")

	// --- Simulate pre-upgrade state ---
	// On a pre-upgrade cluster, the NLB protocol field didn't exist, so:
	//   - status NLB protocol was never set
	//   - the target-group-attributes annotation was never on the service
	//   - ROUTER_USE_PROXY_PROTOCOL was not set on the router
	//   - the NLB target group had default AWS settings (no proxy protocol)
	//
	// We replicate this by clearing the status protocol and deleting the
	// service so the operator recreates it. The new service gets no
	// target-group-attributes annotation (empty status protocol), and the
	// new NLB target group gets clean AWS defaults (no proxy protocol).

	t.Logf("clearing NLB protocol from status to simulate pre-upgrade IC")
	clearIngressControllerNLBProtocolStatus(t, name)

	// Recreate the LB service so the NLB target group gets clean AWS
	// defaults. We can't undo proxy protocol on the target group by
	// just removing the annotation.
	t.Logf("recreating LB service to reset NLB target group")
	if err := recreateIngressControllerService(t, ic); err != nil {
		t.Fatalf("failed to recreate LB service: %v", err)
	}

	// Wait for the operator to recreate the service and become available.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions after service recreation: %v", err)
	}

	// Verify the operator does not re-default protocol or add the annotation.
	t.Logf("verifying status NLB protocol and annotation stay empty across reconciles")
	assertAnnotationStable(t, ic, awsLBTargetGroupAttributesAnnotation, false, "")
	assertIngressControllerNLBProtocolEmpty(t, name)

	// Verify ROUTER_USE_PROXY_PROTOCOL is not set.
	deployment := &appsv1.Deployment{}
	deploymentName := controller.RouterDeploymentName(ic)
	if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
		t.Fatalf("failed to get router deployment: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, deployment, 1*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", ""); err != nil {
		t.Fatalf("expected ROUTER_USE_PROXY_PROTOCOL to be unset: %v", err)
	}

	// Verify connectivity with the simulated pre-upgrade state.
	elbHostname := getIngressControllerLBAddress(t, ic)
	namespace := createNamespace(t, names.SimpleNameGenerator.GenerateName("nlb-upgrade-"))
	testPodName := types.NamespacedName{Name: name.Name + "-verify", Namespace: namespace.Name}
	testHostname := "apps." + ic.Spec.Domain
	t.Logf("verifying connectivity after simulated upgrade")
	verifyExternalIngressController(t, testPodName, testHostname, elbHostname)

	// --- Set manual annotation (hairpin workaround) ---
	// Simulate a user who applies the hairpin workaround by setting the
	// target-group-attributes annotation to disable client IP preservation.
	t.Logf("setting manual target-group-attributes annotation to simulate hairpin workaround")
	userAnnotationValue := "preserve_client_ip.enabled=false"
	if err := updateLBServiceAnnotationWithRetryOnConflict(t, ic, awsLBTargetGroupAttributesAnnotation, userAnnotationValue, 5*time.Minute); err != nil {
		t.Fatalf("failed to set manual annotation on LB service: %v", err)
	}

	// Verify the operator does not stomp the user's manual annotation.
	t.Logf("verifying operator preserves user-set annotation")
	assertAnnotationStable(t, ic, awsLBTargetGroupAttributesAnnotation, true, userAnnotationValue)
	t.Logf("confirmed: operator preserved user-set annotation %q", userAnnotationValue)

	// Verify end-to-end connectivity with the manual annotation.
	t.Logf("verifying connectivity with manual hairpin workaround annotation")
	verifyExternalIngressController(t, testPodName, testHostname, elbHostname)

	// --- Phase 4: Opt-in to PROXY after upgrade ---
	// User sees the hairpin alert, decides to opt in by setting the NLB protocol to PROXY.
	// The operator should take over annotation management and set the correct values.
	t.Logf("opting in to PROXY protocol after upgrade")
	if err := updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		if ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters == nil {
			ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{}
		}
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.Protocol = operatorv1.NLBProtocolProxy
	}); err != nil {
		t.Fatalf("failed to set spec.protocol=PROXY: %v", err)
	}

	// Verify the operator sets PROXY protocol in status.
	if err := waitForIngressControllerNLBProtocol(t, name, operatorv1.NLBProtocolProxy); err != nil {
		t.Fatalf("expected status protocol to be PROXY after opt-in: %v", err)
	}

	// Verify the operator takes over the annotation with PROXY values,
	// overwriting the user's manual hairpin workaround value.
	waitForLBAnnotation(t, ic, awsLBTargetGroupAttributesAnnotation, true, "preserve_client_ip.enabled=false,proxy_protocol_v2.enabled=true")

	// Verify ROUTER_USE_PROXY_PROTOCOL is set.
	if err := waitForDeploymentEnvVar(t, deployment, 3*time.Minute, "ROUTER_USE_PROXY_PROTOCOL", "true"); err != nil {
		t.Fatalf("expected ROUTER_USE_PROXY_PROTOCOL=true after opt-in: %v", err)
	}

	// Verify connectivity after opt-in.
	t.Logf("verifying connectivity after opt-in to PROXY protocol")
	verifyExternalIngressController(t, testPodName, testHostname, elbHostname)
}

// waitForIngressControllerNLBProtocol waits for the IngressController status
// to have the expected NLB protocol.
func waitForIngressControllerNLBProtocol(t *testing.T, name types.NamespacedName, expected operatorv1.NLBProtocol) error {
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
		actual := ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.Protocol
		if actual != expected {
			t.Logf("expected protocol %q, got %q for %s", expected, actual, name.Name)
			return false, nil
		}
		return true, nil
	})
}

// updateLBServiceAnnotationWithRetryOnConflict updates an annotation on the
// IngressController's LoadBalancer service, retrying on conflict errors.
func updateLBServiceAnnotationWithRetryOnConflict(t *testing.T, ic *operatorv1.IngressController, annotation, value string, timeout time.Duration) error {
	t.Helper()
	serviceName := controller.LoadBalancerServiceName(ic)
	return wait.PollUntilContextTimeout(context.Background(), 1*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		svc := &corev1.Service{}
		if err := kclient.Get(ctx, serviceName, svc); err != nil {
			t.Logf("error getting service %v: %v, retrying...", serviceName, err)
			return false, nil
		}
		if svc.Annotations == nil {
			svc.Annotations = map[string]string{}
		}
		svc.Annotations[annotation] = value
		if err := kclient.Update(ctx, svc); err != nil {
			t.Logf("conflict updating service %v: %v, retrying...", serviceName, err)
			return false, nil
		}
		return true, nil
	})
}

// removeLBServiceAnnotationWithRetryOnConflict removes an annotation from the
// IngressController's LoadBalancer service, retrying on conflict errors.
func removeLBServiceAnnotationWithRetryOnConflict(t *testing.T, ic *operatorv1.IngressController, annotation string, timeout time.Duration) error {
	t.Helper()
	serviceName := controller.LoadBalancerServiceName(ic)
	return wait.PollUntilContextTimeout(context.Background(), 1*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		svc := &corev1.Service{}
		if err := kclient.Get(ctx, serviceName, svc); err != nil {
			t.Logf("error getting service %v: %v, retrying...", serviceName, err)
			return false, nil
		}
		if _, ok := svc.Annotations[annotation]; !ok {
			return true, nil
		}
		delete(svc.Annotations, annotation)
		if err := kclient.Update(ctx, svc); err != nil {
			t.Logf("conflict updating service %v: %v, retrying...", serviceName, err)
			return false, nil
		}
		return true, nil
	})
}

// assertAnnotationStable polls the LB service for 2 minutes and fatally fails
// if the annotation changes from the expected state. When expectedExist is
// false, the annotation must remain absent. When true, the value must remain
// equal to expectedValue.
func assertAnnotationStable(t *testing.T, ic *operatorv1.IngressController, annotation string, expectedExist bool, expectedValue string) {
	t.Helper()
	serviceName := controller.LoadBalancerServiceName(ic)
	if err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
		svc := &corev1.Service{}
		if err := kclient.Get(ctx, serviceName, svc); err != nil {
			t.Logf("failed to get LB service: %v, retrying...", err)
			return false, nil
		}
		val, ok := svc.Annotations[annotation]
		if expectedExist {
			if !ok {
				t.Fatalf("annotation %q was removed by the operator", annotation)
			}
			if val != expectedValue {
				t.Fatalf("annotation %q changed from %q to %q", annotation, expectedValue, val)
			}
		} else {
			if ok {
				t.Fatalf("annotation %q appeared unexpectedly with value %q", annotation, val)
			}
		}
		return false, nil
	}); err == nil {
		t.Fatalf("polling unexpectedly succeeded")
	}
}

// clearIngressControllerNLBProtocolStatus clears the NLB protocol from the
// IngressController's status to simulate a pre-upgrade IC that never had the
// protocol field.
func clearIngressControllerNLBProtocolStatus(t *testing.T, name types.NamespacedName) {
	t.Helper()
	if err := updateIngressControllerStatusWithRetryOnConflict(t, name, 5*time.Minute, func(status *operatorv1.IngressControllerStatus) {
		if status.EndpointPublishingStrategy != nil &&
			status.EndpointPublishingStrategy.LoadBalancer != nil &&
			status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters != nil &&
			status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS != nil &&
			status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters != nil {
			status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.Protocol = ""
		}
	}); err != nil {
		t.Fatalf("failed to clear NLB protocol from status: %v", err)
	}
}

// assertIngressControllerNLBProtocolEmpty verifies that the IngressController's
// status NLB protocol is empty.
func assertIngressControllerNLBProtocolEmpty(t *testing.T, name types.NamespacedName) {
	t.Helper()
	var protocol operatorv1.NLBProtocol
	if err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := kclient.Get(ctx, name, ic); err != nil {
			t.Logf("error getting ingresscontroller %v: %v, retrying...", name, err)
			return false, nil
		}
		if ic.Status.EndpointPublishingStrategy != nil &&
			ic.Status.EndpointPublishingStrategy.LoadBalancer != nil &&
			ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters != nil &&
			ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS != nil &&
			ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters != nil {
			protocol = ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.Protocol
		}
		return true, nil
	}); err != nil {
		t.Fatalf("failed to get ingresscontroller %v: %v", name, err)
	}
	if protocol != "" {
		t.Fatalf("expected status NLB protocol to be empty, got %q", protocol)
	}
}
