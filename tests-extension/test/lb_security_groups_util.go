package test

import (
	"bytes"
	"context"
	"fmt"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// awsLBSecurityGroupsAnnotation specifies a list of security group IDs for NLBs.
	awsLBSecurityGroupsAnnotation = "service.beta.kubernetes.io/aws-load-balancer-security-groups"
	// autoDeleteLoadBalancerAnnotation tells the operator to recreate the LB Service to effectuate changes.
	autoDeleteLoadBalancerAnnotation = "ingress.operator.openshift.io/auto-delete-load-balancer"
	// loadBalancerProgressingConditionType is the IngressController condition set when the LB Service must be recreated.
	loadBalancerProgressingConditionType = "LoadBalancerProgressing"
)

// joinSecurityGroups joins a SecurityGroupID slice into a string separated by sep, skipping empty IDs.
func joinSecurityGroups(securityGroups []operatorv1.SecurityGroupID, sep string) string {
	var buffer bytes.Buffer
	first := true
	for _, sg := range securityGroups {
		if len(string(sg)) == 0 {
			continue
		}
		if !first {
			buffer.WriteString(sep)
		}
		first = false
		buffer.WriteString(string(sg))
	}
	return buffer.String()
}

// getStatusSecurityGroups returns the effective securityGroups reported in the IngressController status.
func getStatusSecurityGroups(ic *operatorv1.IngressController) []operatorv1.SecurityGroupID {
	eps := ic.Status.EndpointPublishingStrategy
	if eps != nil && eps.LoadBalancer != nil && eps.LoadBalancer.ProviderParameters != nil &&
		eps.LoadBalancer.ProviderParameters.AWS != nil &&
		eps.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters != nil {
		return eps.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.SecurityGroups
	}
	return nil
}

// getSpecSecurityGroups returns the securityGroups configured in the IngressController spec.
func getSpecSecurityGroups(ic *operatorv1.IngressController) []operatorv1.SecurityGroupID {
	eps := ic.Spec.EndpointPublishingStrategy
	if eps != nil && eps.LoadBalancer != nil && eps.LoadBalancer.ProviderParameters != nil &&
		eps.LoadBalancer.ProviderParameters.AWS != nil &&
		eps.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters != nil {
		return eps.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters.SecurityGroups
	}
	return nil
}

// waitForStatusMatchesSpec polls until the IngressController status securityGroups equals the spec securityGroups.
func (c *clients) waitForStatusMatchesSpec(ctx context.Context, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := c.client.Get(ctx, crclient.ObjectKey{Namespace: icNamespace, Name: name}, ic); err != nil {
			return false, nil
		}
		return securityGroupsEqual(getSpecSecurityGroups(ic), getStatusSecurityGroups(ic)), nil
	})
}

// securityGroupsEqual compares two SecurityGroupID slices as multisets, treating nil and empty as equal.
func securityGroupsEqual(a, b []operatorv1.SecurityGroupID) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[operatorv1.SecurityGroupID]int, len(a))
	for _, sg := range a {
		counts[sg]++
	}
	for _, sg := range b {
		counts[sg]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}

// updateSecurityGroups updates the IngressController spec securityGroups, retrying on conflict.
func (c *clients) updateSecurityGroups(ctx context.Context, name string, securityGroups []operatorv1.SecurityGroupID, autoDelete bool, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := c.client.Get(ctx, crclient.ObjectKey{Namespace: icNamespace, Name: name}, ic); err != nil {
			return false, nil
		}
		eps := ic.Spec.EndpointPublishingStrategy
		if eps == nil || eps.LoadBalancer == nil || eps.LoadBalancer.ProviderParameters == nil || eps.LoadBalancer.ProviderParameters.AWS == nil {
			return false, fmt.Errorf("ingresscontroller %s is not configured with AWS load balancer provider parameters", name)
		}
		awsParams := eps.LoadBalancer.ProviderParameters.AWS
		if awsParams.NetworkLoadBalancerParameters == nil {
			awsParams.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{}
		}
		awsParams.NetworkLoadBalancerParameters.SecurityGroups = securityGroups
		if autoDelete {
			if ic.Annotations == nil {
				ic.Annotations = map[string]string{}
			}
			ic.Annotations[autoDeleteLoadBalancerAnnotation] = ""
		}
		if err := c.client.Update(ctx, ic); err != nil {
			if apierrors.IsConflict(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

// setServiceSecurityGroupsAnnotation sets the security groups annotation directly on the LB Service, retrying on conflict.
func (c *clients) setServiceSecurityGroupsAnnotation(ctx context.Context, icName, value string, timeout time.Duration) error {
	key := lbServiceName(icName)
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		svc := &corev1.Service{}
		if err := c.client.Get(ctx, key, svc); err != nil {
			return false, nil
		}
		if svc.Annotations == nil {
			svc.Annotations = map[string]string{}
		}
		svc.Annotations[awsLBSecurityGroupsAnnotation] = value
		if err := c.client.Update(ctx, svc); err != nil {
			if apierrors.IsConflict(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}
