//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
)

const (
	awsLBSecurityGroupAnnotation = "service.beta.kubernetes.io/aws-load-balancer-security-groups"
)

// TestAWSSecurityGroupsForNLB creates an IngressController with BYO security groups in AWS.
// The test verifies the provisioning of the LB-type Service, confirms ingress connectivity, and
// confirms that the service.beta.kubernetes.io/aws-load-balancer-security-groups annotation as well as
// the IngressController status is correctly configured.
func TestAWSSecurityGroupsForNLB(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}

	ec2ServiceClient := createEC2ServiceClient(t, infraConfig)
	clusterName, err := getClusterName(infraConfig)
	if err != nil {
		t.Fatal(err)
	}
	vpcID, err := getVPCId(ec2ServiceClient, clusterName)
	if err != nil {
		t.Fatalf("failed to get VPC ID due to error: %v", err)
	}

	// Create a security group in the cluster VPC.
	sgID, err := createAWSSecurityGroup(t, ec2ServiceClient, clusterName, vpcID)
	if err != nil {
		t.Fatalf("failed to create security group due to error: %v", err)
	}
	t.Cleanup(func() { assertSecurityGroupDeleted(t, ec2ServiceClient, sgID, 5*time.Minute) })

	securityGroups := []operatorv1.SecurityGroupID{operatorv1.SecurityGroupID(sgID)}

	t.Logf("creating ingresscontroller with security group: %s", sgID)
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "sgtest"}
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
					SecurityGroups: securityGroups,
				},
			},
		},
	}

	if err := createWithRetryOnError(t, context.Background(), ic, DefaultRetryTimeout); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	t.Cleanup(func() {
		assertIngressControllerDeleted(t, kclient, ic)
		serviceName := controller.LoadBalancerServiceName(ic)
		if err := waitForIngressControllerServiceDeleted(t, ic, 4*time.Minute); err != nil {
			t.Errorf("failed to delete IngressController service %s/%s due to error: %v", serviceName.Namespace, serviceName.Name, err)
		}
	})

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Get ELB host name from the service status for use with verifyExternalIngressController.
	elbHostname := getIngressControllerLBAddress(t, ic)

	// Ensure the expected security groups annotation is on the service.
	waitForLBAnnotation(t, ic, awsLBSecurityGroupAnnotation, true, ingress.JoinAWSSecurityGroups(securityGroups, ","))

	// Verify the securityGroups status field is configured to what we expect.
	verifyIngressControllerSecurityGroupStatus(t, name)

	// Verify we can reach the NLB with the provided security groups.
	namespace := createNamespace(t, names.SimpleNameGenerator.GenerateName("security-groups-"))
	externalTestPodName := types.NamespacedName{Name: name.Name + "-external-verify", Namespace: namespace.Name}
	testHostname := "apps." + ic.Spec.Domain
	t.Logf("verifying external connectivity for ingresscontroller %q using an NLB with specified securityGroups", ic.Name)
	verifyExternalIngressController(t, externalTestPodName, testHostname, elbHostname)

	// Create a second security group and update the IngressController.
	sgID2, err := createAWSSecurityGroup(t, ec2ServiceClient, clusterName, vpcID)
	if err != nil {
		t.Fatalf("failed to create second security group due to error: %v", err)
	}
	t.Cleanup(func() { assertSecurityGroupDeleted(t, ec2ServiceClient, sgID2, 5*time.Minute) })

	updatedSecurityGroups := []operatorv1.SecurityGroupID{
		operatorv1.SecurityGroupID(sgID),
		operatorv1.SecurityGroupID(sgID2),
	}

	t.Logf("updating ingresscontroller %q to use updated securityGroups", ic.Name)
	if err = updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.SecurityGroups = updatedSecurityGroups
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Effectuate the security groups update.
	effectuateIngressControllerSecurityGroups(t, ic, updatedSecurityGroups, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...)

	// Refresh ELB host name since it's been recreated.
	elbHostname = getIngressControllerLBAddress(t, ic)

	t.Logf("verifying external connectivity for ingresscontroller %q using an NLB with updated securityGroups", ic.Name)
	verifyExternalIngressController(t, externalTestPodName, testHostname, elbHostname)

	// Now, update the IngressController to remove securityGroups using the auto-delete-load-balancer annotation.
	t.Logf("updating ingresscontroller %q to remove the securityGroups while using the auto-delete-load-balancer annotation", ic.Name)
	if err = updateIngressControllerWithRetryOnConflict(t, name, 5*time.Minute, func(ic *operatorv1.IngressController) {
		if ic.Annotations == nil {
			ic.Annotations = map[string]string{}
		}
		ic.Annotations["ingress.operator.openshift.io/auto-delete-load-balancer"] = ""
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.SecurityGroups = nil
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Verify the security groups annotation is removed on the service.
	waitForLBAnnotation(t, ic, awsLBSecurityGroupAnnotation, false, "")

	// Expect the load balancer to provision successfully with the securityGroups removed.
	if err = waitForIngressControllerCondition(t, kclient, 10*time.Minute, name, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the securityGroups status field is configured to what we expect.
	verifyIngressControllerSecurityGroupStatus(t, name)

	// Refresh ELB host name since it's been recreated.
	elbHostname = getIngressControllerLBAddress(t, ic)

	t.Logf("verifying external connectivity for ingresscontroller %q after removing securityGroups", ic.Name)
	verifyExternalIngressController(t, externalTestPodName, testHostname, elbHostname)
}

// TestUnmanagedAWSSecurityGroups tests compatibility for unmanaged security groups annotations
// on the IngressController service. This is done by directly configuring the annotation on the service
// and then updating the IngressController to match the unmanaged security group annotation.
func TestUnmanagedAWSSecurityGroups(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform: %q", infraConfig.Status.PlatformStatus.Type)
	}

	// Create a NLB IngressController.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "unmanaged-aws-securitygroups"}
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

	if err := createWithRetryOnError(t, context.Background(), ic, DefaultRetryTimeout); err != nil {
		t.Fatalf("expected ingresscontroller creation failed: %v", err)
	}
	t.Cleanup(func() {
		assertIngressControllerDeleted(t, kclient, ic)
		serviceName := controller.LoadBalancerServiceName(ic)
		if err := waitForIngressControllerServiceDeleted(t, ic, 4*time.Minute); err != nil {
			t.Errorf("failed to delete IngressController service %s/%s due to error: %v", serviceName.Namespace, serviceName.Name, err)
		}
	})

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 10*time.Minute, icName, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Ensure there is no security groups annotation on the service.
	waitForLBAnnotation(t, ic, awsLBSecurityGroupAnnotation, false, "")

	// Create a security group in the cluster VPC.
	ec2ServiceClient := createEC2ServiceClient(t, infraConfig)
	clusterName, err := getClusterName(infraConfig)
	if err != nil {
		t.Fatalf("cluster name not found due to error: %v", err)
	}
	vpcID, err := getVPCId(ec2ServiceClient, clusterName)
	if err != nil {
		t.Fatalf("failed to get VPC ID due to error: %v", err)
	}
	sgID, err := createAWSSecurityGroup(t, ec2ServiceClient, clusterName, vpcID)
	if err != nil {
		t.Fatalf("failed to create security group: %v", err)
	}
	t.Cleanup(func() { assertSecurityGroupDeleted(t, ec2ServiceClient, sgID, 5*time.Minute) })

	securityGroups := []operatorv1.SecurityGroupID{operatorv1.SecurityGroupID(sgID)}

	// Now, update the security groups annotation directly on the service.
	serviceName := controller.LoadBalancerServiceName(ic)
	t.Logf("updating service %s/%s directly to add unmanaged security groups annotation", serviceName.Namespace, serviceName.Name)
	lbService := &corev1.Service{}
	if err := kclient.Get(context.Background(), serviceName, lbService); err != nil {
		t.Fatalf("failed to get service: %v", err)
	}
	lbService.Annotations[awsLBSecurityGroupAnnotation] = ingress.JoinAWSSecurityGroups(securityGroups, ",")
	if err := kclient.Update(context.Background(), lbService); err != nil {
		t.Fatalf("failed to update service: %v", err)
	}

	// LoadBalancerProgressing should become True because the security groups annotation
	// doesn't match the IngressController spec.
	loadBalancerProgressingTrue := operatorv1.OperatorCondition{
		Type:   ingress.IngressControllerLoadBalancerProgressingConditionType,
		Status: operatorv1.ConditionTrue,
	}
	if err = waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, loadBalancerProgressingTrue); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the security groups annotation didn't get removed.
	waitForLBAnnotation(t, ic, awsLBSecurityGroupAnnotation, true, ingress.JoinAWSSecurityGroups(securityGroups, ","))

	// Now, update the IngressController to specify the same unmanaged security groups.
	t.Logf("updating ingresscontroller %q to specify the securityGroups in the unmanaged security groups annotation", ic.Name)
	if err = updateIngressControllerWithRetryOnConflict(t, icName, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{
			SecurityGroups: securityGroups,
		}
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// The LoadBalancerProgressing=True condition should be resolved and the IngressController should be available.
	if err = waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableNotProgressingConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Verify the security groups annotation is still the same.
	waitForLBAnnotation(t, ic, awsLBSecurityGroupAnnotation, true, ingress.JoinAWSSecurityGroups(securityGroups, ","))
}

// verifyIngressControllerSecurityGroupStatus verifies that the IngressController securityGroups status fields are
// equal to the appropriate securityGroups spec fields when an IngressController is in a LoadBalancerProgressing=False state.
func verifyIngressControllerSecurityGroupStatus(t *testing.T, icName types.NamespacedName) {
	t.Helper()
	t.Logf("verifying ingresscontroller %q securityGroups status field match the appropriate securityGroups spec field", icName.Name)
	loadBalancerProgressingFalse := operatorv1.OperatorCondition{
		Type:   ingress.IngressControllerLoadBalancerProgressingConditionType,
		Status: operatorv1.ConditionFalse,
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, loadBalancerProgressingFalse); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
	err := wait.PollUntilContextTimeout(t.Context(), 10*time.Second, 3*time.Minute, false, func(ctx context.Context) (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := kclient.Get(ctx, icName, ic); err != nil {
			t.Logf("failed to get ingresscontroller: %v, retrying...", err)
			return false, nil
		}
		var sgSpec, sgStatus []operatorv1.SecurityGroupID
		if networkLoadBalancerParametersSpecExists(ic) {
			sgSpec = ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.SecurityGroups
		}
		if networkLoadBalancerParametersStatusExist(ic) {
			sgStatus = ic.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.SecurityGroups
		}
		if !reflect.DeepEqual(sgSpec, sgStatus) {
			t.Logf("expected ingresscontroller securityGroups status to be %q, got %q, retrying...", sgSpec, sgStatus)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("error verifying securityGroups status for IngressController %q: %v", icName.Name, err)
	}
}

// effectuateIngressControllerSecurityGroups manually effectuates updated IngressController security groups by
// confirming IngressController is in a progressing state, deleting the service, waiting for
// the expected security groups to appear on the service, and confirming the IngressController
// securityGroups status is accurate.
func effectuateIngressControllerSecurityGroups(t *testing.T, ic *operatorv1.IngressController, expectedSecurityGroups []operatorv1.SecurityGroupID, expectedOperatorConditions ...operatorv1.OperatorCondition) {
	t.Helper()
	t.Logf("effectuating securityGroups for IngressController %s", ic.Name)
	icName := types.NamespacedName{Name: ic.Name, Namespace: ic.Namespace}
	progressingTrue := operatorv1.OperatorCondition{
		Type:   ingress.IngressControllerLoadBalancerProgressingConditionType,
		Status: operatorv1.ConditionTrue,
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, progressingTrue); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	t.Logf("recreating the service to effectuate the securityGroups: %s/%s", controller.LoadBalancerServiceName(ic).Namespace, controller.LoadBalancerServiceName(ic).Name)
	if err := recreateIngressControllerService(t, ic); err != nil {
		t.Fatalf("failed to delete and recreate service: %v", err)
	}

	waitForLBAnnotation(t, ic, awsLBSecurityGroupAnnotation, true, ingress.JoinAWSSecurityGroups(expectedSecurityGroups, ","))

	if err := waitForIngressControllerCondition(t, kclient, 10*time.Minute, icName, expectedOperatorConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	verifyIngressControllerSecurityGroupStatus(t, icName)
}

// createAWSSecurityGroup creates a security group in the specified VPC with a tag for identification.
func createAWSSecurityGroup(t *testing.T, ec2ServiceClient *ec2.Client, clusterName, vpcID string) (string, error) {
	t.Helper()
	groupName := fmt.Sprintf("%s-e2e-sg-%s", clusterName, rand.String(5))
	tagKey, tagValue := getTagKeyAndValue(t)

	result, err := ec2ServiceClient.CreateSecurityGroup(context.TODO(), &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(groupName),
		Description: aws.String("E2E test security group for NLB ingress controller"),
		VpcId:       aws.String(vpcID),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeSecurityGroup,
				Tags: []ec2types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(groupName),
					},
					{
						Key:   aws.String(tagKey),
						Value: aws.String(tagValue),
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create security group: %v", err)
	}

	sgID := aws.ToString(result.GroupId)
	t.Logf("security group created successfully: %s", sgID)

	// Allow all inbound traffic so the NLB can function.
	_, err = ec2ServiceClient.AuthorizeSecurityGroupIngress(context.TODO(), &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(sgID),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("-1"),
				IpRanges: []ec2types.IpRange{
					{
						CidrIp:      aws.String("0.0.0.0/0"),
						Description: aws.String("Allow all inbound traffic for e2e test"),
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to authorize security group ingress: %v", err)
	}

	return sgID, nil
}

// assertSecurityGroupDeleted deletes the specified security group and waits for it to be deleted.
func assertSecurityGroupDeleted(t *testing.T, ec2ServiceClient *ec2.Client, sgID string, timeout time.Duration) {
	t.Helper()
	t.Logf("deleting security group %s", sgID)
	err := wait.PollUntilContextTimeout(context.Background(), 15*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := ec2ServiceClient.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
			GroupId: aws.String(sgID),
		})
		if err != nil {
			t.Logf("failed to delete security group %s: %v, retrying...", sgID, err)
			return false, nil
		}
		t.Logf("security group %s deleted successfully", sgID)
		return true, nil
	})
	if err != nil {
		t.Errorf("failed to delete security group %s within %v: %v", sgID, timeout, err)
	}
}
