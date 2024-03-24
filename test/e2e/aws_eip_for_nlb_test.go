//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"strings"
	"testing"
	"time"
)

func CreateAWSEIPs() ([]string, error) {
	kubeClient, err := createClient()
	if err != nil {
		return nil, fmt.Errorf("test skipped: %v", err)
	}
	decodedAccessKeyID, decodedSecretAccessKey, err := getIDAndKey(kubeClient)
	if err != nil {
		return nil, fmt.Errorf("test skipped: %v", err)
	}
	if infraConfig.Status.PlatformStatus == nil {
		return nil, fmt.Errorf("test skipped on nil platform")
	}
	region, err := getRegion()
	if err != nil {
		return nil, fmt.Errorf("test skipped: %v", err)
	}
	clusterName, err := getClusterName()
	if err != nil {
		return nil, fmt.Errorf("test skipped: %v", err)
	}

	// Set up an AWS session
	sess, err := createSession(region, decodedAccessKeyID, decodedSecretAccessKey)
	if err != nil {
		return nil, fmt.Errorf("test skipped: %v", err)
	}

	// Create an EC2 service client
	svc := ec2.New(sess)

	vpcID, err := getVPCID(clusterName, svc)
	if err != nil {
		return nil, fmt.Errorf("test skipped: %v", err)
	}

	publicSubnets, err := getPublicSubnets(vpcID, svc)
	if err != nil {
		return nil, fmt.Errorf("test skipped: %v", err)
	}

	// Set up random seed
	rand.Seed(time.Now().UnixNano())

	// Set tag key and value
	tagKeyEIP := fmt.Sprintf("NE1398-%s", os.Getenv("USER"))
	tagValueEIP := clusterName

	// Allocate EIPs
	var allocationIDs []string
	for i := 0; i < len(publicSubnets); i++ {
		// Allocate EIP
		result, err := svc.AllocateAddress(&ec2.AllocateAddressInput{
			Domain: aws.String("vpc"),
		})
		if err != nil {
			return nil, fmt.Errorf("Error allocating EIP: %v", err)
		}
		allocationID := aws.StringValue(result.AllocationId)
		fmt.Println("EIP allocated successfully. Allocation ID:", allocationID)

		// Generate random name
		randomName := fmt.Sprintf("%s-EIP-%d", clusterName, rand.Int())

		// Tag EIP
		_, err = svc.CreateTags(&ec2.CreateTagsInput{
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

func getClusterName() (string, error) {
	var clusterName string
	if len(infraConfig.Status.InfrastructureName) != 0 {
		clusterName = infraConfig.Status.InfrastructureName
	} else {
		return "", fmt.Errorf("Cluster Name not found")
	}
	return clusterName, nil
}

func getRegion() (string, error) {
	var region string
	if infraConfig.Status.PlatformStatus.AWS != nil && len(infraConfig.Status.PlatformStatus.AWS.Region) != 0 {
		region = infraConfig.Status.PlatformStatus.AWS.Region
	} else {
		return "", fmt.Errorf("Region not found")
	}
	return region, nil
}

func getIDAndKey(kubeClient client.Client) (string, string, error) {
	awsSecret := corev1.Secret{}
	var decodedAccessKeyID, decodedSecretAccessKey string
	var err error
	if err = kubeClient.Get(context.TODO(), controller.AWSSecretNameInKubeSystemNS(), &awsSecret); err != nil {
		return "", "", fmt.Errorf("failed to get secret: %w", err)
	}
	if aws_access_key_id, ok := awsSecret.Data["aws_access_key_id"]; ok || (ok && len(aws_access_key_id) != 0) {
		decodedAccessKeyID = string(awsSecret.Data["aws_access_key_id"])
	}
	if aws_secret_access_key, ok := awsSecret.Data["aws_secret_access_key"]; ok || (ok && len(aws_secret_access_key) != 0) {
		decodedSecretAccessKey = string(awsSecret.Data["aws_secret_access_key"])
	}
	return decodedAccessKeyID, decodedSecretAccessKey, err
}

func createClient() (client.Client, error) {
	kubeConfig, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kube config: %s\n", err)
	}
	kubeClient, err := operatorclient.NewClient(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %s\n", err)
	}
	return kubeClient, nil
}

func getPublicSubnets(vpcID string, svc *ec2.EC2) (map[string]struct{}, error) {
	// Get the Internet Gateway associated with the VPC
	input := &ec2.DescribeInternetGatewaysInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []*string{aws.String(vpcID)},
			},
		},
	}

	resultIG, err := svc.DescribeInternetGateways(input)
	if err != nil {
		return nil, fmt.Errorf("Error describing Internet Gateways: %v", err)
	}

	// Extract Internet Gateway IDs
	igws := make([]string, 0)
	for _, igw := range resultIG.InternetGateways {
		igws = append(igws, aws.StringValue(igw.InternetGatewayId))
	}

	// Find public subnets associated with the Internet Gateways
	publicSubnets := make(map[string]struct{})
	for _, igw := range igws {
		input := &ec2.DescribeRouteTablesInput{
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("route.gateway-id"),
					Values: []*string{aws.String(igw)},
				},
			},
		}

		result, err := svc.DescribeRouteTables(input)
		if err != nil {
			return nil, fmt.Errorf("Error describing Route Tables: %v", err)
		}

		for _, rt := range result.RouteTables {
			for _, assoc := range rt.Associations {
				if assoc != nil && assoc.SubnetId != nil {
					publicSubnets[aws.StringValue(assoc.SubnetId)] = struct{}{}
				}
			}
		}
	}
	return publicSubnets, nil
}

func createSession(region, decodedAccessKeyID, decodedSecretAccessKey string) (*session.Session, error) {
	sess, err := session.NewSession(&aws.Config{Region: aws.String(region),
		Credentials: credentials.NewStaticCredentials(string(decodedAccessKeyID), string(decodedSecretAccessKey), "")})
	if err != nil {
		return nil, fmt.Errorf("Error creating session: %v", err)
	}
	return sess, nil
}

func getVPCID(clusterName string, svc *ec2.EC2) (string, error) {
	// Define the filter to search for instances with the specified tag
	tagKey := "kubernetes.io/cluster/" + clusterName
	tagValue := "owned"
	filters := []*ec2.Filter{
		{
			Name:   aws.String("tag:" + tagKey),
			Values: []*string{aws.String(tagValue)},
		},
	}

	// Describe instances that match the filter
	result, err := svc.DescribeInstances(&ec2.DescribeInstancesInput{
		Filters: filters,
	})
	if err != nil {
		return "", fmt.Errorf("Error describing instances: %v", err)
	}

	// Check if any instances were found
	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return "", fmt.Errorf("No instances found with the specified tag, or unable to determine VPC ID.")
	}

	// Extract and print the VPC ID
	return aws.StringValue(result.Reservations[0].Instances[0].VpcId), nil
}

func TestAWSEIPForNLB(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}
	// Create an ingress controller with EIPs mentioned in the Ingress Controller CR.
	var eipAllocations []operatorv1.EIPAllocation

	validEIPAllocations, err := CreateAWSEIPs()
	if err != nil {
		t.Fatalf("AWS EIPs failed to get created %s", err)
	}
	for _, validEIPAllocation := range validEIPAllocations {
		eipAllocations = append(eipAllocations, operatorv1.EIPAllocation(validEIPAllocation))
	}

	t.Logf("Should create an ingresscontroller with spec.endpointPublishingStrategy.loadBalancer.providerParameters.aws.networkLoadBalancer.eip-allocations set to a valid EIPs: %s", validEIPAllocations)
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

	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Poll for a minute and verify that EIPAllocations field is updated as expected.
	lbService := &corev1.Service{}
	err = wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
			return false, fmt.Errorf("failed to get service: %w", err)
		}

		if lbService.Annotations != nil {
			if a, ok := lbService.Annotations[ingress.AwsEIPAllocations]; ok || (ok && len(a) != 0 && a == strings.Join(validEIPAllocations, ",")) {
				t.Logf("AWS EIP of the service annotation was updated as expected and set to %s", strings.Join(validEIPAllocations, ","))
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("expected AWS EIPs to be updated: %v for lbService: %v", err, lbService)
	}

	// Update Ingress Controller EIPs.
	var eipAllocationsForUpdate []operatorv1.EIPAllocation
	validEIPAllocationsForUpdate, err := CreateAWSEIPs()
	if err != nil {
		t.Fatalf("AWS EIPs failed to get created %s", err)
	}
	for _, validEIPAllocationForUpdate := range validEIPAllocationsForUpdate {
		eipAllocationsForUpdate = append(eipAllocationsForUpdate, operatorv1.EIPAllocation(validEIPAllocationForUpdate))
	}
	t.Logf("Should set Progressing and LoadBalancerProgressing Statuses to True while updating an ingresscontroller without `auto-delete-load-balancer` annotation and with spec.endpointPublishingStrategy.loadBalancer.providerParameters.aws.networkLoadBalancer.eip-allocations set to an valid EIPs: %s", validEIPAllocationsForUpdate)

	if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: operatorNamespace, Name: "eiptest"}, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.EIPAllocations = eipAllocationsForUpdate
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	err = wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, ic); err != nil {
			return false, fmt.Errorf("failed to get ingresscontroller: %w", err)
		}

		loadBalancerProgressing := false
		progressing := false
		expectedStatusSubstring := "To effectuate this change, you must delete the service:"
		isExpectedSubstring := false

		for _, cond := range ic.Status.Conditions {
			if cond.Type == ingress.IngressControllerLoadBalancerProgressingConditionType && cond.Status == operatorv1.ConditionTrue {
				loadBalancerProgressing = true
			}
			if cond.Type == operatorv1.OperatorStatusTypeProgressing && cond.Status == operatorv1.ConditionTrue {
				progressing = true
			}
			if strings.Contains(cond.Message, expectedStatusSubstring) {
				isExpectedSubstring = true
			}
			if progressing && loadBalancerProgressing && isExpectedSubstring {
				return true, nil
			}
		}
		return false, fmt.Errorf("got Progressing=%v, LoadBalancerProgressing=%v, and not an expectedMessage substring %v", progressing, loadBalancerProgressing, expectedStatusSubstring)
	})
	if err != nil {
		t.Fatalf("expected ingresscontroller to have Progressing=true and LoadBalancerProgressing=true and message having `To effectuate this change, you must delete the service`  for IC %v but got %v", ic.Name, err)
	}

	// Update Ingress Controller EIPs using auto-delete-load-balancer annotation.

	var eipAllocationsForUpdateUsingAutoDelete []operatorv1.EIPAllocation
	validEIPAllocationsForUpdateUsingAutoDelete, err := CreateAWSEIPs()
	if err != nil {
		t.Fatalf("AWS EIPs failed to get created %s", err)
	}
	for _, validEIPAllocationForUpdateUsingAutoDelete := range validEIPAllocationsForUpdateUsingAutoDelete {
		eipAllocationsForUpdateUsingAutoDelete = append(eipAllocationsForUpdateUsingAutoDelete, operatorv1.EIPAllocation(validEIPAllocationForUpdateUsingAutoDelete))
	}
	t.Logf("Should update an ingresscontroller with annotation `auto-delete-load-balancer` and with spec.endpointPublishingStrategy.loadBalancer.providerParameters.aws.networkLoadBalancer.eip-allocations set to an valid EIPs: %s", validEIPAllocationsForUpdateUsingAutoDelete)

	if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: operatorNamespace, Name: "eiptest"}, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Annotations = map[string]string{"ingress.operator.openshift.io/auto-delete-load-balancer": ""}
	ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.EIPAllocations = eipAllocationsForUpdateUsingAutoDelete
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	// Wait for the load balancer and DNS to be ready.
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Poll for a minute and verify that EIPAllocations field is updated as expected.
	lbService = &corev1.Service{}
	err = wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
			return false, fmt.Errorf("failed to get service: %w", err)
		}

		if lbService.Annotations != nil {
			if a, ok := lbService.Annotations[ingress.AwsEIPAllocations]; ok || (ok && len(a) != 0 && a == strings.Join(validEIPAllocations, ",")) {
				t.Logf("AWS EIP of the service annotation was updated as expected and set to %s", strings.Join(validEIPAllocations, ","))
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("expected AWS EIPs to be updated: %v for lbService: %v", err, lbService)
	}

	// Test case while updating an IC CR with EIPAllocations not equal to number of subnets.

	var eipAllocationsEqualSubnets []operatorv1.EIPAllocation
	eipAllocationsEqualSubnetsString, err := CreateAWSEIPs()
	if err != nil {
		t.Fatalf("AWS EIPs failed to get created %s", err)
	}
	eipAllocationsEqualSubnetsString = delete_at_index(eipAllocationsEqualSubnetsString, 0)
	for _, eipAllocationEqualSubnetsString := range eipAllocationsEqualSubnetsString {
		eipAllocationsEqualSubnets = append(eipAllocationsEqualSubnets, operatorv1.EIPAllocation(eipAllocationEqualSubnetsString))
	}
	t.Logf("Should set Available and LoadBalancerProgressing Statuses to False while updating an ingresscontroller which has `auto-delete-load-balancer` annotation and with spec.endpointPublishingStrategy.loadBalancer.providerParameters.aws.networkLoadBalancer.eip-allocations set to an incorrect number of valid EIPs which is not equal to number of subnets: %s", eipAllocationsEqualSubnetsString)

	if err := kclient.Get(context.TODO(), types.NamespacedName{Namespace: operatorNamespace, Name: "eiptest"}, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Annotations = map[string]string{"ingress.operator.openshift.io/auto-delete-load-balancer": ""}
	ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.EIPAllocations = eipAllocationsEqualSubnets
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	time.Sleep(60 * time.Second)

	err = wait.PollImmediate(10*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, ic); err != nil {
			return false, fmt.Errorf("failed to get ingresscontroller: %w", err)
		}

		loadBalancerNotReady := false
		notAvailable := false
		expectedStatusSubstring := "Must have same number of EIP AllocationIDs"
		isExpectedSubstring := false

		for _, cond := range ic.Status.Conditions {
			if cond.Type == operatorv1.LoadBalancerReadyIngressConditionType && cond.Status == operatorv1.ConditionFalse {
				loadBalancerNotReady = true
			}
			if cond.Type == operatorv1.OperatorStatusTypeAvailable && cond.Status == operatorv1.ConditionFalse {
				notAvailable = true
			}
			if strings.Contains(cond.Message, expectedStatusSubstring) {
				isExpectedSubstring = true
			}
			if loadBalancerNotReady && notAvailable && isExpectedSubstring {
				return true, nil
			}
		}
		return false, fmt.Errorf("got Available=%v, LoadBalancerReady=%v, and not an expectedMessage substring %v", loadBalancerNotReady, notAvailable, expectedStatusSubstring)
	})
	if err != nil {
		t.Fatalf("expected ingresscontroller to have Available=False and LoadBalancerReady=False and message having `Must have same number of EIP AllocationIDs`  for IC %v but got %v", ic.Name, err)
	}
}

// Function that takes two parameters
// a slice which has to be operated on
// the index of the element to be deleted from the slice
// return value as a slice of integers

func delete_at_index(slice []string, index int) []string {

	// Append function used to append elements to a slice
	// first parameter as the slice to which the elements
	// are to be added/appended second parameter is the
	// element(s) to be appended into the slice
	// return value as a slice
	return append(slice[:index], slice[index+1:]...)
}

func TestAWSInvalidEIP(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.PlatformStatus == nil {
		t.Fatal("test skipped on nil platform")
	}
	if infraConfig.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)
	}
	// If incorrect EIPs are provided in the IngressController CR
	kubeClient, err := createClient()
	if err != nil {
		t.Fatalf("Test Skipped %s", err)
	}
	decodedAccessKeyID, decodedSecretAccessKey, err := getIDAndKey(kubeClient)
	t.Logf("decodedAccessKeyID: %v, decodedSecretAccessKey: %v", decodedAccessKeyID, decodedSecretAccessKey)
	if err != nil {
		t.Fatalf("Test Skipped %s", err)
	}
	region, err := getRegion()
	if err != nil {
		t.Fatalf("Test Skipped %s", err)
	}
	clusterName, err := getClusterName()
	if err != nil {
		t.Fatalf("Test Skipped %s", err)
	}
	// Set up an AWS session
	sess, err := createSession(region, decodedAccessKeyID, decodedSecretAccessKey)
	if err != nil {
		t.Fatalf("Test Skipped %s", err)
	}
	// Create an EC2 service client
	svc := ec2.New(sess)
	vpcID, err := getVPCID(clusterName, svc)
	if err != nil {
		t.Fatalf("Test Skipped %s", err)
	}
	publicSubnets, err := getPublicSubnets(vpcID, svc)
	if err != nil {
		t.Fatalf("Test Skipped %s", err)
	}
	invalidEIPAllocations := make([]operatorv1.EIPAllocation, len(publicSubnets))
	for i := 0; i < len(publicSubnets); i++ {
		invalidEIPAllocations[i] = operatorv1.EIPAllocation(fmt.Sprintf("%s-%d", "eip-allocation", rand.Int()))
	}
	t.Logf("Fail to create an ingresscontroller with spec.endpointPublishingStrategy.loadBalancer.providerParameters.aws.networkLoadBalancer.eip-allocations set to an invalid EIP Allocation: %s", invalidEIPAllocations)

	name := types.NamespacedName{Namespace: operatorNamespace, Name: "invalideip"}
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
					EIPAllocations: invalidEIPAllocations,
				},
			},
		},
	}
	t.Logf("\nic %v", ic.Spec.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.EIPAllocations)
	if err := kubeClient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })
	time.Sleep(10 * time.Second)
	err = wait.PollImmediate(10*time.Second, 5*time.Minute, func() (bool, error) {
		if err := kubeClient.Get(context.TODO(), name, ic); err != nil {
			return false, fmt.Errorf("failed to get ingresscontroller: %w", err)
		}
		loadBalancerNotReady := false
		expectedStatusSubstring := "The service-controller component is reporting SyncLoadBalancerFailed events like: Error syncing load balancer: failed to ensure load balancer: error creating load balancer: \"AllocationIdNotFound: The allocation IDs"
		isExpectedSubstring := false

		for _, cond := range ic.Status.Conditions {
			if cond.Type == operatorv1.LoadBalancerReadyIngressConditionType && cond.Status == operatorv1.ConditionFalse {
				loadBalancerNotReady = true
			}
			if strings.Contains(cond.Message, expectedStatusSubstring) {
				isExpectedSubstring = true
			}
			if loadBalancerNotReady && isExpectedSubstring {
				return true, nil
			}
		}
		return false, fmt.Errorf("got LoadBalancerReady=%v, and not an expectedMessage substring %v", loadBalancerNotReady, expectedStatusSubstring)
	})
	if err != nil {
		t.Fatalf("expected ingresscontroller to have LoadBalancerReady=False for IC %v but got %v", ic.Name, err)
	}
}
