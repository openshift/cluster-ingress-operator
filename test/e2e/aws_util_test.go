//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// createEC2ServiceClient creates an ec2 client using aws credentials and region.
func createEC2ServiceClient(t *testing.T, infraConfig configv1.Infrastructure) *ec2.EC2 {
	decodedAccessKeyID, decodedSecretAccessKey, err := getIDAndKey()
	if err != nil {
		t.Fatalf("failed to get access key id and secret access key due to error: %v", err)
	}
	region, err := getRegion(infraConfig)
	if err != nil {
		t.Fatalf("failed to get aws region due to error: %v", err)
	}
	// Set up an AWS session
	sess, err := createSession(region, decodedAccessKeyID, decodedSecretAccessKey)
	if err != nil {
		t.Fatalf("failed to create session due to error: %v", err)
	}
	// Create an EC2 service client
	return ec2.New(sess)
}

// getClusterName fetches cluster name from the infrastructures.config.openshift.io cluster object.
func getClusterName(infraConfig configv1.Infrastructure) (string, error) {
	if len(infraConfig.Status.InfrastructureName) != 0 {
		return infraConfig.Status.InfrastructureName, nil
	}
	return "", fmt.Errorf("cluster name not found")
}

// getRegion fetches region from the infrastructures.config.openshift.io cluster object.
func getRegion(infraConfig configv1.Infrastructure) (string, error) {
	if infraConfig.Status.PlatformStatus.AWS != nil && len(infraConfig.Status.PlatformStatus.AWS.Region) != 0 {
		return infraConfig.Status.PlatformStatus.AWS.Region, nil
	}
	return "", fmt.Errorf("region not found")
}

// AWSCloudCredSecretName returns the name of the secret containing root aws credentials.
func AWSCloudCredSecretName() types.NamespacedName {
	return types.NamespacedName{Namespace: "kube-system", Name: "aws-creds"}
}

// getIDAndKey fetches the aws credentials from the secret containing root aws credentials in kube-system namespace.
func getIDAndKey() (string, string, error) {
	awsSecret := corev1.Secret{}
	var accessKeyID, secretAccessKey string
	if err := kclient.Get(context.TODO(), AWSCloudCredSecretName(), &awsSecret); err != nil {
		return "", "", fmt.Errorf("failed to get secret: %w", err)
	}
	accessKeyID = string(awsSecret.Data["aws_access_key_id"])
	secretAccessKey = string(awsSecret.Data["aws_secret_access_key"])
	return accessKeyID, secretAccessKey, nil
}

// getPublicSubnets fetches public subnets for a AWS VPC ID.
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
	var igws []*ec2.InternetGateway
	for _, igw := range resultIG.InternetGateways {
		igws = append(igws, igw)
	}

	// Find public subnets associated with the Internet Gateways
	publicSubnets := make(map[string]struct{})
	for _, igw := range igws {
		input := &ec2.DescribeRouteTablesInput{
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("route.gateway-id"),
					Values: []*string{igw.InternetGatewayId},
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

// createSession creates a session using decoded AWS credentials.
func createSession(region, decodedAccessKeyID, decodedSecretAccessKey string) (*session.Session, error) {
	return session.NewSession(&aws.Config{
		Region:      aws.String(region),
		Credentials: credentials.NewStaticCredentials(decodedAccessKeyID, decodedSecretAccessKey, ""),
	})
}

// getVPCId return the VPC ID of the cluster
func getVPCId(ec2Client *ec2.EC2, clusterName string) (string, error) {
	tagKey := "kubernetes.io/cluster/" + clusterName
	tagValue := "owned"
	vpcs, err := ec2Client.DescribeVpcs(&ec2.DescribeVpcsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:" + tagKey),
				Values: []*string{aws.String(tagValue)},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to list VPC with tag %s:%s : %w", tagKey, tagValue, err)
	}
	if len(vpcs.Vpcs) == 0 {
		return "", fmt.Errorf("no VPC with tag %s:%s found : %w", tagKey, tagValue, err)
	}
	if len(vpcs.Vpcs) > 1 {
		return "", fmt.Errorf("multiple VPCs with tag %s:%s found", tagKey, tagValue)
	}
	return aws.StringValue(vpcs.Vpcs[0].VpcId), nil
}

func getTagKeyAndValue(t *testing.T) (string, string) {
	tagKeyEIP := fmt.Sprintf("NE1674-%s", t.Name())
	tagValueEIP, err := getClusterName(infraConfig)
	if err != nil {
		t.Fatal(err)
	}
	return tagKeyEIP, tagValueEIP
}
