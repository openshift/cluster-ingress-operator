//go:build e2e
// +build e2e

package e2e

import (
	"context"
	random "crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	awsLBEIPAllocationAnnotation = "service.beta.kubernetes.io/aws-load-balancer-eip-allocations"
)

// TestAWSEIPAllocationsForNLB creates an IngressController with various eipAllocations in AWS.
// The test verifies the provisioning of the LB-type Service, confirms ingress connectivity, and
// confirms that the service.beta.kubernetes.io/aws-load-balancer-eip-allocations as well as the IngressController status is
// correctly configured.
func TestAWSEIPAllocationsForNLB(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}
	if enabled, err := isFeatureGateEnabled(features.FeatureGateSetEIPForNLBIngressController); err != nil {
		t.Fatalf("failed to get feature gate: %v", err)
	} else if !enabled {
		t.Skipf("test skipped because %q feature gate is not enabled", features.FeatureGateSetEIPForNLBIngressController)
	}

	// Create an ingress controller with EIPs mentioned in the Ingress Controller CR.
	var eipAllocations []operatorv1.EIPAllocation

	ec2ServiceClient := createEC2ServiceClient(t, infraConfig)
	clusterName, err := getClusterName(infraConfig)
	if err != nil {
		t.Fatal(err)
	}
	vpcID, err := getVPCId(ec2ServiceClient, clusterName)
	if err != nil {
		t.Fatalf("failed to get VPC ID due to error: %v", err)
	}
	validEIPAllocations, err := createAWSEIPs(t, ec2ServiceClient, clusterName, vpcID)
	if err != nil {
		t.Fatalf("failed to create EIPs due to error: %v", err)
	}
	t.Cleanup(func() { assertEIPAllocationDeleted(t, ec2ServiceClient, 5*time.Minute, clusterName) })

	for _, validEIPAllocation := range validEIPAllocations {
		eipAllocations = append(eipAllocations, operatorv1.EIPAllocation(validEIPAllocation))
	}

	t.Logf("creating ingresscontroller with valid EIPs: %s", validEIPAllocations)
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "eiptest"}
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
					EIPAllocations: eipAllocations,
				},
			},
		},
	}

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	t.Cleanup(func() {
		assertIngressControllerDeleted(t, kclient, ic)
		serviceName := controller.LoadBalancerServiceName(ic)
		// Waits for the service to clean up so EIPs can be released in the next t.Cleanup.
		if err := waitForIngressControllerServiceDeleted(t, ic, 4*time.Minute); err != nil {
			t.Errorf("failed to delete IngressController service %s/%s due to error: %v", serviceName.Namespace, serviceName.Name, err)
		}
	})

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Get ELB host name from the service status for use with verifyExternalIngressController.
	// Using the ELB host name (not the IngressController wildcard domain) results in much quicker DNS resolution.
	elbHostname := getIngressControllerLBAddress(t, ic)

	// Ensure the expected eipAllocation annotation is on the service.
	waitForLBAnnotation(t, ic, awsLBEIPAllocationAnnotation, true, ingress.JoinAWSEIPAllocations(eipAllocations, ","))

	// Verify the eipAllocations status field is configured to what we expect.
	verifyIngressControllerEIPAllocationStatus(t, name)

	// Verify we can reach the NLB with the provided eipAllocations.
	externalTestPodName := types.NamespacedName{Name: name.Name + "-external-verify", Namespace: name.Namespace}
	testHostname := "apps." + ic.Spec.Domain
	t.Logf("verifying external connectivity for ingresscontroller %q using an NLB with specified eipAllocations", ic.Name)
	verifyExternalIngressController(t, externalTestPodName, testHostname, elbHostname)

	// Now, update the IngressController to use invalid (non-existent) eipAllocations.
	t.Logf("updating ingresscontroller %q to use invalid eipAllocations", ic.Name)
	invalidEIPAllocations, err := createInvalidEIPAllocations(t, validEIPAllocations)
	if err != nil {
		t.Fatalf("failed to create invalid AWS EIPs: %v", err)
	}
	if err = updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.EIPAllocations = invalidEIPAllocations
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Since the eipAllocations are invalid, the load balancer will fail to provision and set LoadBalancerReady=False, but
	// it shouldn't be Progressing either.
	loadBalancerNotReadyNotProgressing := []operatorv1.OperatorCondition{
		{
			Type:   operatorv1.LoadBalancerReadyIngressConditionType,
			Status: operatorv1.ConditionFalse,
		},
		{
			Type:   operatorv1.OperatorStatusTypeProgressing,
			Status: operatorv1.ConditionFalse,
		},
	}
	effectuateIngressControllerEIPAllocations(t, ic, invalidEIPAllocations, loadBalancerNotReadyNotProgressing...)

	// Now, update the IngressController to not specify eipAllocations, but let's use the
	// auto-delete-load-balancer annotation, so we don't have to manually delete the service.
	t.Logf("updating ingresscontroller %q to remove the eipAllocations while using the auto-delete-load-balancer annotation", ic.Name)
	if err = updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		if ic.Annotations == nil {
			ic.Annotations = map[string]string{}
		}
		ic.Annotations["ingress.operator.openshift.io/auto-delete-load-balancer"] = ""
		// Remove eipAllocations by not specifying them.
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.EIPAllocations = nil
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Verify the eipAllocation annotation is removed on the service.
	waitForLBAnnotation(t, ic, awsLBEIPAllocationAnnotation, false, "")

	// Expect the load balancer to provision successfully with the eipAllocations removed.
	if err = waitForIngressControllerCondition(t, kclient, 10*time.Minute, name, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the eipAllocations status field is configured to what we expect.
	verifyIngressControllerEIPAllocationStatus(t, name)

	// Refresh ELB host name since it's been recreated.
	elbHostname = getIngressControllerLBAddress(t, ic)

	t.Logf("verifying external connectivity for ingresscontroller %q using an NLB with specified eipAllocations", ic.Name)
	verifyExternalIngressController(t, externalTestPodName, testHostname, elbHostname)
}

func TestAWSEIPAllocationsLBTypeChangeFromNLBToClassic(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}
	if enabled, err := isFeatureGateEnabled(features.FeatureGateSetEIPForNLBIngressController); err != nil {
		t.Fatalf("failed to get feature gate: %v", err)
	} else if !enabled {
		t.Skipf("test skipped because %q feature gate is not enabled", features.FeatureGateSetEIPForNLBIngressController)
	}

	// Create an ingress controller with EIPs mentioned in the Ingress Controller CR.
	var eipAllocations []operatorv1.EIPAllocation

	ec2ServiceClient := createEC2ServiceClient(t, infraConfig)
	clusterName, err := getClusterName(infraConfig)
	if err != nil {
		t.Fatal(err)
	}
	vpcID, err := getVPCId(ec2ServiceClient, clusterName)
	if err != nil {
		t.Fatalf("failed to get VPC ID due to error: %v", err)
	}
	validEIPAllocations, err := createAWSEIPs(t, ec2ServiceClient, clusterName, vpcID)
	if err != nil {
		t.Fatalf("failed to create EIPs due to error: %v", err)
	}
	t.Cleanup(func() { assertEIPAllocationDeleted(t, ec2ServiceClient, 5*time.Minute, clusterName) })

	for _, validEIPAllocation := range validEIPAllocations {
		eipAllocations = append(eipAllocations, operatorv1.EIPAllocation(validEIPAllocation))
	}

	t.Logf("creating ingresscontroller with valid EIPs: %s", validEIPAllocations)
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "lbtypechange"}
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
					EIPAllocations: eipAllocations,
				},
			},
		},
	}

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	t.Cleanup(func() {
		assertIngressControllerDeleted(t, kclient, ic)
		serviceName := controller.LoadBalancerServiceName(ic)
		// Waits for the service to clean up so EIPs can be released in the next t.Cleanup.
		if err := waitForIngressControllerServiceDeleted(t, ic, 4*time.Minute); err != nil {
			t.Errorf("failed to delete IngressController service %s/%s due to error: %v", serviceName.Namespace, serviceName.Name, err)
		}
	})

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Get ELB host name from the service status for use with verifyExternalIngressController.
	// Using the ELB host name (not the IngressController wildcard domain) results in much quicker DNS resolution.
	elbHostname := getIngressControllerLBAddress(t, ic)

	// Ensure the expected eipAllocation annotation is on the service.
	waitForLBAnnotation(t, ic, awsLBEIPAllocationAnnotation, true, ingress.JoinAWSEIPAllocations(eipAllocations, ","))

	// Verify the eipAllocations status field is configured to what we expect.
	verifyIngressControllerEIPAllocationStatus(t, name)

	// Verify we can reach the NLB with the provided eipAllocations.
	externalTestPodName := types.NamespacedName{Name: name.Name + "-external-verify", Namespace: name.Namespace}
	testHostname := "apps." + ic.Spec.Domain
	t.Logf("verifying external connectivity for ingresscontroller %q using an NLB with specified eipAllocations", ic.Name)
	verifyExternalIngressController(t, externalTestPodName, testHostname, elbHostname)

	// Now, update the IngressController to change from NLB to Classic LBType.
	t.Logf("updating ingresscontroller %q to Classic LBType", ic.Name)
	if err := updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.Type = operatorv1.AWSClassicLoadBalancer
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	//effectuateIngressControllerEIPAllocations(t, ic, nil, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...)
	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Ensure the expected eipAllocation annotation is on the service.
	waitForLBAnnotation(t, ic, awsLBEIPAllocationAnnotation, false, "")

	// Get ELB host name from the service status for use with verifyExternalIngressController.
	// Using the ELB host name (not the IngressController wildcard domain) results in much quicker DNS resolution.
	elbHostname = getIngressControllerLBAddress(t, ic)

	// Verify we can reach the CLB with the no eipAllocations.
	externalTestPodName = types.NamespacedName{Name: name.Name + "-external-verify", Namespace: name.Namespace}
	testHostname = "apps." + ic.Spec.Domain
	t.Logf("verifying external connectivity for ingresscontroller %q using an NLB with specified eipAllocations", ic.Name)
	verifyExternalIngressController(t, externalTestPodName, testHostname, elbHostname)
}

// TestUnmanagedAWSEIPAllocations tests compatibility for unmanaged service.beta.kubernetes.io/aws-load-balancer-eip-allocations
// annotations on the IngressController service. This is done by directly configuring the annotation on the service
// and then updating the IngressController to match the unmanaged eipAllocation annotation.
func TestUnmanagedAWSEIPAllocations(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform: %q", infraConfig.Status.PlatformStatus.Type)
	}
	if enabled, err := isFeatureGateEnabled(features.FeatureGateSetEIPForNLBIngressController); err != nil {
		t.Fatalf("failed to get feature gate: %v", err)
	} else if !enabled {
		t.Skipf("test skipped because %q feature gate is not enabled", features.FeatureGateSetEIPForNLBIngressController)
	}

	// Next, create a NLB IngressController.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "unmanaged-aws-eipallocations"}
	t.Logf("create a NLB ingresscontroller: %q", icName.Name)
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newLoadBalancerController(icName, domain)
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

	if err := kclient.Create(context.Background(), ic); err != nil {
		t.Fatalf("expected ingresscontroller creation failed: %v", err)
	}
	t.Cleanup(func() {
		assertIngressControllerDeleted(t, kclient, ic)
		serviceName := controller.LoadBalancerServiceName(ic)
		// Waits for the service to clean up so EIPs can be released in the next t.Cleanup.
		if err := waitForIngressControllerServiceDeleted(t, ic, 4*time.Minute); err != nil {
			t.Errorf("failed to delete IngressController service %s/%s due to error: %v", serviceName.Namespace, serviceName.Name, err)
		}
	})

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 10*time.Minute, icName, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Ensure there is no eipAllocation annotation on the service.
	waitForLBAnnotation(t, ic, awsLBEIPAllocationAnnotation, false, "")

	// Let's get the list of eipAllocations to use for the LB.
	var eipAllocations []operatorv1.EIPAllocation
	ec2ServiceClient := createEC2ServiceClient(t, infraConfig)
	clusterName, err := getClusterName(infraConfig)
	if err != nil {
		t.Fatalf("cluster name not found due to error: %v", err)
	}
	vpcID, err := getVPCId(ec2ServiceClient, clusterName)
	if err != nil {
		t.Fatalf("failed to get VPC ID due to error: %v", err)
	}
	validEIPAllocations, err := createAWSEIPs(t, ec2ServiceClient, clusterName, vpcID)
	if err != nil {
		t.Fatalf("AWS EIPs failed to get created: %v", err)
	}
	t.Cleanup(func() { assertEIPAllocationDeleted(t, ec2ServiceClient, 5*time.Minute, clusterName) })

	for _, validEIPAllocation := range validEIPAllocations {
		eipAllocations = append(eipAllocations, operatorv1.EIPAllocation(validEIPAllocation))
	}

	// Now, update the eipAllocation annotation directly on the service.
	serviceName := controller.LoadBalancerServiceName(ic)
	t.Logf("updating service %s/%s directly add unmanaged eipAllocation annotation", serviceName.Namespace, serviceName.Name)
	lbService := &corev1.Service{}
	if err := kclient.Get(context.Background(), serviceName, lbService); err != nil {
		t.Fatalf("failed to get service: %v", err)
	}
	lbService.Annotations[awsLBEIPAllocationAnnotation] = ingress.JoinAWSEIPAllocations(eipAllocations, ",")
	if err := kclient.Update(context.Background(), lbService); err != nil {
		t.Fatalf("failed to update service: %v", err)
	}

	// LoadBalancerProgressing should become True because the eipAllocation annotation
	// doesn't match the IngressController spec.
	loadBalancerProgressingTrue := operatorv1.OperatorCondition{
		Type:   ingress.IngressControllerLoadBalancerProgressingConditionType,
		Status: operatorv1.ConditionTrue,
	}
	if err = waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, loadBalancerProgressingTrue); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the eipAllocation annotation didn't get removed.
	waitForLBAnnotation(t, ic, awsLBEIPAllocationAnnotation, true, ingress.JoinAWSEIPAllocations(eipAllocations, ","))

	// Now, update the IngressController to specify the same unmanaged eipAllocations.
	t.Logf("updating ingresscontroller %q to specify the eipAllocations in the unmanaged eipAllocations annotation", ic.Name)
	if err = updateIngressControllerWithRetryOnConflict(t, icName, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{
			EIPAllocations: eipAllocations,
		}
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// The LoadBalancerProgressing=True condition should be resolved and the IngressController should be available.
	if err = waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the eipAllocation annotation is still the same.
	waitForLBAnnotation(t, ic, awsLBEIPAllocationAnnotation, true, ingress.JoinAWSEIPAllocations(eipAllocations, ","))
}

// verifyIngressControllerEIPAllocationStatus verifies that the IngressController eipAllocation status fields are
// equal to the appropriate eipAllocation spec fields when an IngressController is in a LoadBalancerProgressing=False state.
func verifyIngressControllerEIPAllocationStatus(t *testing.T, icName types.NamespacedName) {
	t.Helper()
	t.Logf("verifying ingresscontroller %q eipAllocation status field match the appropriate eipAllocation spec field", icName.Name)
	// First, ensure the LoadBalancerProgressing is False. If LoadBalancerProgressing is True
	// (due to a non-effectuated eipAllocation update), our eipAllocation status checks will surely fail.
	loadBalancerProgressingFalse := operatorv1.OperatorCondition{
		Type:   ingress.IngressControllerLoadBalancerProgressingConditionType,
		Status: operatorv1.ConditionFalse,
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, loadBalancerProgressingFalse); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
	// Poll until the eipAllocation status is what we expect.
	err := wait.PollUntilContextTimeout(context.Background(), 10*time.Second, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := kclient.Get(context.Background(), icName, ic); err != nil {
			t.Logf("failed to get ingresscontroller: %v, retrying...", err)
			return false, nil
		}
		// Verify the eipAllocation status field is configured to what we expect.
		var eipAllocationSpec, eipAllocationStatus []operatorv1.EIPAllocation
		if networkLoadBalancerParametersSpecExists(ic) {
			eipAllocationSpec = ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.EIPAllocations
		}
		if networkLoadBalancerParametersStatusExist(ic) {
			eipAllocationStatus = ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.EIPAllocations
		}
		// Check if the eipAllocationSpec and eipAllocationStatus are equal.
		if !reflect.DeepEqual(eipAllocationSpec, eipAllocationStatus) {
			t.Logf("expected ingresscontroller eipAllocation status to be %q, got %q, retrying...", eipAllocationSpec, eipAllocationStatus)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("error verifying eipAllocation status for IngressController %q: %v", icName.Name, err)
	}
}

// effectuateIngressControllerEIPAllocations manually effectuates updated IngressController eipAllocations by
// confirming IngressController is in a progressing state, deleting the service, waiting for
// the expected eipAllocations to appear on the service, and confirming the IngressController
// eipAllocation status is accurate. It waits for the provided operator conditions after the eipAllocations
// have been effectuated.
func effectuateIngressControllerEIPAllocations(t *testing.T, ic *operatorv1.IngressController, expectedEIPAllocations []operatorv1.EIPAllocation, expectedOperatorConditions ...operatorv1.OperatorCondition) {
	t.Helper()
	t.Logf("effectuating eipAllocations for IngressController %s", ic.Name)
	icName := types.NamespacedName{Name: ic.Name, Namespace: ic.Namespace}
	progressingTrue := operatorv1.OperatorCondition{
		Type:   ingress.IngressControllerLoadBalancerProgressingConditionType,
		Status: operatorv1.ConditionTrue,
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, progressingTrue); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Delete and recreate the IngressController service to effectuate.
	t.Logf("recreating the service to effectuate the eipAllocations: %s/%s", controller.LoadBalancerServiceName(ic).Namespace, controller.LoadBalancerServiceName(ic).Namespace)
	if err := recreateIngressControllerService(t, ic); err != nil {
		t.Fatalf("failed to delete and recreate service: %v", err)
	}

	// Ensure the service's load-balancer status changes, and verify we get the expected eipAllocation annotation on the service.
	waitForLBAnnotation(t, ic, awsLBEIPAllocationAnnotation, true, ingress.JoinAWSEIPAllocations(expectedEIPAllocations, ","))

	// Expect the load balancer to provision successfully with the new eipAllocations.
	if err := waitForIngressControllerCondition(t, kclient, 10*time.Minute, icName, expectedOperatorConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the subnets status field is configured to what we expect.
	verifyIngressControllerEIPAllocationStatus(t, icName)
}

// createInvalidEIPAllocations fetches the public subnets and creates same number of invalid eipAllocations as that of public subnets.
func createInvalidEIPAllocations(t *testing.T, validEIPAllocations []string) ([]operatorv1.EIPAllocation, error) {
	t.Helper()
	invalidEIPAllocations := make([]operatorv1.EIPAllocation, len(validEIPAllocations))
	var hexString string
	var err error
	for i := 0; i < len(validEIPAllocations); i++ {
		if hexString, err = randomHex(9); err != nil {
			t.Fatalf("failed to create hexadecimal string for eipAllocation due to error: %v", err)
		}
		invalidEIPAllocations[i] = operatorv1.EIPAllocation(fmt.Sprintf("%s-%s", "eipalloc", hexString[:17]))
	}
	return invalidEIPAllocations, nil
}

func randomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := random.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// createAWSEIPs creates valid eipAllocations whose count is equal to the number of public subnets.
func createAWSEIPs(t *testing.T, ec2ServiceClient *ec2.EC2, clusterName string, vpcID string) ([]string, error) {
	publicSubnets, err := getPublicSubnets(vpcID, ec2ServiceClient)
	if err != nil {
		t.Fatalf("failed to get public subnets due to error: %v", err)
	}
	// Set up random seed
	rand.Seed(time.Now().UnixNano())

	// Set tag key and value
	tagKeyEIP, tagValueEIP := getTagKeyAndValue(t)

	// Allocate EIPs
	var allocationIDs []string
	for i := 0; i < len(publicSubnets); i++ {
		// Allocate EIP
		result, err := ec2ServiceClient.AllocateAddress(&ec2.AllocateAddressInput{
			Domain: aws.String("vpc"),
		})
		if err != nil {
			return nil, fmt.Errorf("Error allocating EIP: %v", err)
		}
		allocationID := aws.StringValue(result.AllocationId)
		t.Logf("EIP allocated successfully. Allocation ID: %s", allocationID)

		// Generate random name
		randomName := fmt.Sprintf("%s-EIP-%d", clusterName, rand.Int())

		// Tag EIP
		_, err = ec2ServiceClient.CreateTags(&ec2.CreateTagsInput{
			Resources: []*string{result.AllocationId},
			Tags: []*ec2.Tag{
				{
					Key:   aws.String("Name"),
					Value: aws.String(randomName),
				},
				{
					Key:   aws.String(tagKeyEIP),
					Value: aws.String(tagValueEIP),
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("Error tagging EIP Allocation ID: %v", allocationID)
		}
		allocationIDs = append(allocationIDs, allocationID)
	}
	return allocationIDs, nil
}

// cleanupEIPAllocations clean all unassociated EIPs created during the e2e tests. It returns true if no associated eipAllocations tagged with cluster name are found.
func cleanupEIPAllocations(t *testing.T, svc *ec2.EC2, clusterName string) bool {
	t.Log("Releasing unassociated EIPs")

	tagKeyEIP, tagValueEIP := getTagKeyAndValue(t)

	// Describe addresses to get unassociated EIPs
	describeAddressesInput := &ec2.DescribeAddressesInput{}
	describeAddressesOutput, err := svc.DescribeAddresses(describeAddressesInput)
	if err != nil {
		t.Fatalf("failed to describe addresses: %v", err)
	}

	var unassociatedEIPs, associatedEIPs []*ec2.Address

	for _, address := range describeAddressesOutput.Addresses {
		// Check if the EIP has the specified tag key and value
		hasTag := false
		for _, tag := range address.Tags {
			if *tag.Key == tagKeyEIP && *tag.Value == tagValueEIP {
				hasTag = true
				break
			}
		}
		if hasTag {
			if address.AssociationId == nil {
				unassociatedEIPs = append(unassociatedEIPs, address)
			} else {
				associatedEIPs = append(associatedEIPs, address)
			}
		}
	}

	if len(unassociatedEIPs) == 0 {
		t.Log("No unassociated EIPs found with the specified tag key and value.")
	} else {
		t.Log("Unassociated EIPs with the specified tag key and value:")
		for _, eip := range unassociatedEIPs {
			t.Logf("Public IP: %v, Allocation ID: %v", *eip.PublicIp, *eip.AllocationId)
		}
	}

	// Release each unassociated EIP
	for _, eip := range unassociatedEIPs {
		releaseAddressInput := &ec2.ReleaseAddressInput{
			AllocationId: eip.AllocationId,
		}
		_, err := svc.ReleaseAddress(releaseAddressInput)
		if err != nil {
			t.Errorf("Failed to release EIP %v with Allocation ID %v: %v", *eip.PublicIp, *eip.AllocationId, err)
		} else {
			t.Logf("Released EIP %v with Allocation ID %v", *eip.PublicIp, *eip.AllocationId)
		}
	}

	if len(associatedEIPs) == 0 {
		t.Log("No associated EIPs found with the specified tag key and value.")
		return true
	} else {
		t.Log("Associated EIPs with the specified tag key and value:")
		for _, eip := range associatedEIPs {
			t.Logf("Public IP: %v, Allocation ID: %v", *eip.PublicIp, *eip.AllocationId)
		}
		return false
	}
}
