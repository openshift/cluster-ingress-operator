package test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// getRegion fetches the AWS region from the infrastructure config.
func getRegion(infra *configv1.Infrastructure) (string, error) {
	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil && len(infra.Status.PlatformStatus.AWS.Region) != 0 {
		return infra.Status.PlatformStatus.AWS.Region, nil
	}
	return "", fmt.Errorf("region not found")
}

// createEC2Client creates an EC2 client using the cluster's root AWS credentials and region.
func (c *clients) createEC2Client(ctx context.Context, infra *configv1.Infrastructure) (*ec2.Client, error) {
	secret := &corev1.Secret{}
	if err := c.client.Get(ctx, crclient.ObjectKey{Namespace: "kube-system", Name: "aws-creds"}, secret); err != nil {
		return nil, fmt.Errorf("failed to get aws-creds secret: %w", err)
	}
	accessKeyID := string(secret.Data["aws_access_key_id"])
	secretAccessKey := string(secret.Data["aws_secret_access_key"])

	region, err := getRegion(infra)
	if err != nil {
		return nil, err
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return ec2.NewFromConfig(cfg), nil
}

// getVPCID returns the VPC ID of the cluster.
func getVPCID(ctx context.Context, ec2Client *ec2.Client, clusterName string) (string, error) {
	tagKey := "kubernetes.io/cluster/" + clusterName
	vpcs, err := ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + tagKey), Values: []string{"owned"}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to list VPC with tag %s: %w", tagKey, err)
	}
	switch len(vpcs.Vpcs) {
	case 0:
		return "", fmt.Errorf("no VPC with tag %s:owned found", tagKey)
	case 1:
		return aws.ToString(vpcs.Vpcs[0].VpcId), nil
	default:
		return "", fmt.Errorf("multiple VPCs with tag %s:owned found", tagKey)
	}
}

// createSecurityGroup creates a security group in the given VPC allowing inbound HTTP/HTTPS so the NLB can function.
func createSecurityGroup(ctx context.Context, ec2Client *ec2.Client, clusterName, vpcID string) (string, error) {
	groupName := fmt.Sprintf("%s-ote-sg-%s", clusterName, rand.String(5))
	result, err := ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(groupName),
		Description: aws.String("OTE test security group for NLB ingress controller"),
		VpcId:       aws.String(vpcID),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeSecurityGroup,
				Tags: []ec2types.Tag{
					{Key: aws.String("Name"), Value: aws.String(groupName)},
					{Key: aws.String("kubernetes.io/cluster/" + clusterName), Value: aws.String("owned")},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create security group: %w", err)
	}
	sgID := aws.ToString(result.GroupId)

	if _, err := ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(sgID),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(80),
				ToPort:     aws.Int32(80),
				IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String("Allow inbound HTTP traffic for OTE test")}},
			},
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(443),
				ToPort:     aws.Int32(443),
				IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String("Allow inbound HTTPS traffic for OTE test")}},
			},
		},
	}); err != nil {
		return "", fmt.Errorf("failed to authorize security group ingress: %w", err)
	}
	return sgID, nil
}

// deleteSecurityGroup deletes the security group, treating a missing group as success and
// retrying transient errors (e.g. the SG is still attached to the NLB ENI) until the timeout.
func deleteSecurityGroup(ctx context.Context, ec2Client *ec2.Client, sgID string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 15*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(sgID)})
		if err != nil {
			if isAWSErrorCode(err, "InvalidGroup.NotFound") {
				return true, nil
			}
			// Retry transient errors such as DependencyViolation while the NLB is torn down.
			return false, nil
		}
		return true, nil
	})
}

// isAWSErrorCode reports whether err is (or wraps) a smithy API error with the given code.
func isAWSErrorCode(err error, code string) bool {
	var apiErr interface{ ErrorCode() string }
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == code
	}
	return false
}
