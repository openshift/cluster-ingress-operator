package test

import (
	"context"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
)

const (
	lbProvisionTimeout = 10 * time.Minute
	conditionTimeout   = 5 * time.Minute
	annotationTimeout  = 5 * time.Minute
	updateTimeout      = 2 * time.Minute
	// sgDeleteTimeout is longer because the SG stays attached to the NLB ENI until teardown completes.
	sgDeleteTimeout = 5 * time.Minute
)

// skipUnlessAWS skips the suite unless the cluster is AWS. The
// [OCPFeatureGate:IngressControllerLBSecurityGroupsAWS] tag on the Describe already
// ensures these specs only run when the feature gate is enabled, and continue to run
// once the gate is promoted to default and removed.
func skipUnlessAWS(ctx context.Context, cl *clients) *configv1.Infrastructure {
	infra, err := cl.getInfrastructure(ctx)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get infrastructure config")
	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		g.Skip("test skipped on non-AWS platform")
	}
	return infra
}

var _ = g.Describe("[sig-network-edge][OCPFeatureGate:IngressControllerLBSecurityGroupsAWS][Feature:Router][apigroup:operator.openshift.io] AWS NLB security groups", func() {
	g.Describe("managed through the IngressController spec", func() {
		var (
			cl     *clients
			icName string
			sgID   string
			sgID2  string
		)

		g.BeforeEach(func(ctx context.Context) {
			var err error
			cl, err = newClients()
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to build clients")

			infra := skipUnlessAWS(ctx, cl)

			clusterName, err := getClusterName(infra)
			o.Expect(err).NotTo(o.HaveOccurred())
			domain, err := cl.getBaseDomain(ctx)
			o.Expect(err).NotTo(o.HaveOccurred())

			ec2Client, err := cl.createEC2Client(ctx, infra)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create EC2 client")

			vpcID, err := getVPCID(ctx, ec2Client, clusterName)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to get VPC ID")

			// Create both security groups up front. Registering their cleanups before the
			// IngressController's guarantees (via DeferCleanup's LIFO ordering) that the
			// IngressController - and its NLB - is torn down before we delete the security
			// groups, which would otherwise fail while still attached to the NLB ENI.
			sgID, err = createSecurityGroup(ctx, ec2Client, clusterName, vpcID)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create security group")
			g.DeferCleanup(func(ctx context.Context) {
				o.Expect(deleteSecurityGroup(ctx, ec2Client, sgID, sgDeleteTimeout)).To(o.Succeed())
			})
			sgID2, err = createSecurityGroup(ctx, ec2Client, clusterName, vpcID)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create second security group")
			g.DeferCleanup(func(ctx context.Context) {
				o.Expect(deleteSecurityGroup(ctx, ec2Client, sgID2, sgDeleteTimeout)).To(o.Succeed())
			})

			icName = uniqueICName("sgtest-ote")
			ic := newNLBIngressController(icName, icName+"."+domain, []operatorv1.SecurityGroupID{operatorv1.SecurityGroupID(sgID)})
			o.Expect(cl.createWithRetryOnError(ctx, ic, updateTimeout)).To(o.Succeed(), "failed to create ingresscontroller")
			g.DeferCleanup(func(ctx context.Context) {
				o.Expect(cl.deleteIngressController(ctx, icName, conditionTimeout)).To(o.Succeed())
			})
		})

		g.It("provisions, updates, and removes the security groups over the LB lifecycle", func(ctx context.Context) {
			o.Expect(cl.waitForLBProvisioned(ctx, icName, lbProvisionTimeout)).To(o.Succeed(), "LB service was not provisioned")
			o.Expect(cl.waitForLBAnnotation(ctx, icName, awsLBSecurityGroupsAnnotation, true, sgID, annotationTimeout)).To(o.Succeed(), "security groups annotation not set")
			o.Expect(cl.waitForICCondition(ctx, icName, loadBalancerProgressingConditionType, operatorv1.ConditionFalse, conditionTimeout)).To(o.Succeed())
			o.Expect(cl.waitForStatusMatchesSpec(ctx, icName, conditionTimeout)).To(o.Succeed(), "status securityGroups did not match spec")

			// Recreating the Service is what propagates an updated securityGroups list to the annotation.
			updated := []operatorv1.SecurityGroupID{operatorv1.SecurityGroupID(sgID), operatorv1.SecurityGroupID(sgID2)}
			o.Expect(cl.updateSecurityGroups(ctx, icName, updated, false, updateTimeout)).To(o.Succeed(), "failed to update securityGroups")
			o.Expect(cl.waitForICCondition(ctx, icName, loadBalancerProgressingConditionType, operatorv1.ConditionTrue, conditionTimeout)).To(o.Succeed())
			o.Expect(cl.recreateLBService(ctx, icName, conditionTimeout)).To(o.Succeed(), "failed to recreate LB service")
			o.Expect(cl.waitForLBAnnotation(ctx, icName, awsLBSecurityGroupsAnnotation, true, joinSecurityGroups(updated, ","), annotationTimeout)).To(o.Succeed())
			o.Expect(cl.waitForICCondition(ctx, icName, loadBalancerProgressingConditionType, operatorv1.ConditionFalse, conditionTimeout)).To(o.Succeed())
			o.Expect(cl.waitForStatusMatchesSpec(ctx, icName, conditionTimeout)).To(o.Succeed(), "status securityGroups did not match spec")

			o.Expect(cl.updateSecurityGroups(ctx, icName, nil, true, updateTimeout)).To(o.Succeed(), "failed to remove securityGroups")
			o.Expect(cl.waitForLBAnnotation(ctx, icName, awsLBSecurityGroupsAnnotation, false, "", annotationTimeout)).To(o.Succeed(), "security groups annotation not removed")
			o.Expect(cl.waitForICCondition(ctx, icName, loadBalancerProgressingConditionType, operatorv1.ConditionFalse, conditionTimeout)).To(o.Succeed())
			o.Expect(cl.waitForStatusMatchesSpec(ctx, icName, conditionTimeout)).To(o.Succeed(), "status securityGroups did not match spec after removal")
		})
	})

	g.Describe("set directly on the LoadBalancer service (unmanaged)", func() {
		var (
			cl     *clients
			icName string
			sgID   string
		)

		g.BeforeEach(func(ctx context.Context) {
			var err error
			cl, err = newClients()
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to build clients")

			infra := skipUnlessAWS(ctx, cl)

			clusterName, err := getClusterName(infra)
			o.Expect(err).NotTo(o.HaveOccurred())
			domain, err := cl.getBaseDomain(ctx)
			o.Expect(err).NotTo(o.HaveOccurred())

			ec2Client, err := cl.createEC2Client(ctx, infra)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create EC2 client")
			vpcID, err := getVPCID(ctx, ec2Client, clusterName)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to get VPC ID")

			// Create the security group first so its cleanup runs (LIFO) after the
			// IngressController and its NLB have been torn down.
			sgID, err = createSecurityGroup(ctx, ec2Client, clusterName, vpcID)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create security group")
			g.DeferCleanup(func(ctx context.Context) {
				o.Expect(deleteSecurityGroup(ctx, ec2Client, sgID, sgDeleteTimeout)).To(o.Succeed())
			})

			icName = uniqueICName("sgtest-unmanaged-ote")
			ic := newNLBIngressController(icName, icName+"."+domain, nil)
			o.Expect(cl.createWithRetryOnError(ctx, ic, updateTimeout)).To(o.Succeed(), "failed to create ingresscontroller")
			g.DeferCleanup(func(ctx context.Context) {
				o.Expect(cl.deleteIngressController(ctx, icName, conditionTimeout)).To(o.Succeed())
			})
		})

		g.It("preserves a security groups annotation and reconciles once the spec matches", func(ctx context.Context) {
			o.Expect(cl.waitForLBProvisioned(ctx, icName, lbProvisionTimeout)).To(o.Succeed(), "LB service was not provisioned")
			o.Expect(cl.waitForLBAnnotation(ctx, icName, awsLBSecurityGroupsAnnotation, false, "", annotationTimeout)).To(o.Succeed(), "unexpected security groups annotation")

			o.Expect(cl.setServiceSecurityGroupsAnnotation(ctx, icName, sgID, updateTimeout)).To(o.Succeed(), "failed to set annotation on service")

			o.Expect(cl.waitForICCondition(ctx, icName, loadBalancerProgressingConditionType, operatorv1.ConditionTrue, conditionTimeout)).To(o.Succeed())
			// The operator must not remove the unmanaged annotation.
			o.Consistently(func() bool {
				return cl.waitForLBAnnotation(ctx, icName, awsLBSecurityGroupsAnnotation, true, sgID, 5*time.Second) == nil
			}, 30*time.Second, 5*time.Second).Should(o.BeTrue(), "operator removed the unmanaged annotation")

			o.Expect(cl.updateSecurityGroups(ctx, icName, []operatorv1.SecurityGroupID{operatorv1.SecurityGroupID(sgID)}, false, updateTimeout)).To(o.Succeed())
			o.Expect(cl.waitForICCondition(ctx, icName, loadBalancerProgressingConditionType, operatorv1.ConditionFalse, conditionTimeout)).To(o.Succeed())
			o.Expect(cl.waitForLBAnnotation(ctx, icName, awsLBSecurityGroupsAnnotation, true, sgID, annotationTimeout)).To(o.Succeed())
		})
	})
})
