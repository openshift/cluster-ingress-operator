//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// createEC2ServiceClient creates an ec2 client using aws credentials and region.
func createEC2ServiceClient(t *testing.T, infraConfig configv1.Infrastructure) *ec2.Client {
	decodedAccessKeyID, decodedSecretAccessKey, err := getIDAndKey()
	if err != nil {
		t.Fatalf("failed to get access key id and secret access key due to error: %v", err)
	}
	region, err := getRegion(infraConfig)
	if err != nil {
		t.Fatalf("failed to get aws region due to error: %v", err)
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.TODO(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(decodedAccessKeyID, decodedSecretAccessKey, "")),
	)
	if err != nil {
		t.Fatalf("failed to load AWS config due to error: %v", err)
	}
	return ec2.NewFromConfig(cfg)
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
func getPublicSubnets(vpcID string, svc *ec2.Client) (map[string]struct{}, error) {
	// Get the Internet Gateway associated with the VPC
	input := &ec2.DescribeInternetGatewaysInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []string{vpcID},
			},
		},
	}

	resultIG, err := svc.DescribeInternetGateways(context.TODO(), input)
	if err != nil {
		return nil, fmt.Errorf("Error describing Internet Gateways: %v", err)
	}

	// Find public subnets associated with the Internet Gateways
	publicSubnets := make(map[string]struct{})
	for _, igw := range resultIG.InternetGateways {
		input := &ec2.DescribeRouteTablesInput{
			Filters: []ec2types.Filter{
				{
					Name:   aws.String("route.gateway-id"),
					Values: []string{aws.ToString(igw.InternetGatewayId)},
				},
			},
		}

		result, err := svc.DescribeRouteTables(context.TODO(), input)
		if err != nil {
			return nil, fmt.Errorf("Error describing Route Tables: %v", err)
		}

		for _, rt := range result.RouteTables {
			for _, assoc := range rt.Associations {
				if assoc.SubnetId != nil {
					publicSubnets[aws.ToString(assoc.SubnetId)] = struct{}{}
				}
			}
		}
	}
	return publicSubnets, nil
}

// getVPCId return the VPC ID of the cluster
func getVPCId(ec2Client *ec2.Client, clusterName string) (string, error) {
	tagKey := "kubernetes.io/cluster/" + clusterName
	tagValue := "owned"
	vpcs, err := ec2Client.DescribeVpcs(context.TODO(), &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:" + tagKey),
				Values: []string{tagValue},
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
	return aws.ToString(vpcs.Vpcs[0].VpcId), nil
}

func getTagKeyAndValue(t *testing.T) (string, string) {
	tagKeyEIP := fmt.Sprintf("NE1674-%s", t.Name())
	tagValueEIP, err := getClusterName(infraConfig)
	if err != nil {
		t.Fatal(err)
	}
	return tagKeyEIP, tagValueEIP
}
