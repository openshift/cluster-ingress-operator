package qe

import (
	"context"
	"fmt"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	testutil "github.com/openshift/cluster-ingress-operator/tests-extension/test"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	awsLBTypeAnnotation              = "service.beta.kubernetes.io/aws-load-balancer-type"
	awsLBProxyProtocolAnnotation     = "service.beta.kubernetes.io/aws-load-balancer-proxy-protocol"
	awsLBSubnetsAnnotation           = "service.beta.kubernetes.io/aws-load-balancer-subnets"
	awsLBEIPAllocationsAnnotation    = "service.beta.kubernetes.io/aws-load-balancer-eip-allocations"
	autoDeleteLoadBalancerAnnotation = "ingress.operator.openshift.io/auto-delete-load-balancer"
)

var _ = g.Describe("[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-CLB", func() {
	var (
		ctx           context.Context
		kubeClient    kubernetes.Interface
		dynamicClient dynamic.Interface
	)

	g.BeforeEach(func() {
		ctx = context.Background()
		var err error
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to build REST config from KUBECONFIG")
		kubeClient, err = kubernetes.NewForConfig(restConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kubernetes client")
		dynamicClient, err = dynamic.NewForConfig(restConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create dynamic client")
	})

	baseDomain := func() string {
		domain, err := testutil.GetBaseDomain(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get base domain")
		return domain
	}

	//Author:hongli
	g.It("IngressController sets the expected endpoint publishing strategy for the cluster platform", g.Label("OCP-21599"), func() {
		platform, err := testutil.GetClusterPlatform(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get cluster platform")

		cloudPlatforms := map[string]bool{
			"aws":      true,
			"azure":    true,
			"gcp":      true,
			"ibmcloud": true,
			"powervs":  true,
		}
		explicitStrategy, err := testutil.DefaultICHasExplicitEndpointPublishingStrategy(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to inspect default ingresscontroller endpoint publishing strategy")
		if explicitStrategy {
			g.Skip("default ingresscontroller has an explicit endpointPublishingStrategy; platform default cannot be inferred")
		}
		strategyType, err := testutil.WaitForDefaultEndpointPublishingStrategy(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get default endpointPublishingStrategy")
		if cloudPlatforms[platform] {
			o.Expect(strategyType).To(o.Equal("LoadBalancerService"), "expected LoadBalancerService default on platform %s", platform)
		} else {
			o.Expect(strategyType).To(o.Equal("HostNetwork"), "expected HostNetwork default on platform %s", platform)
		}
	})

	//Author:hongli
	g.It("Switching an AWS load balancer between Classic and NLB requires service recreation", g.Label("OCP-52837"), func() {
		testutil.SkipUnlessPlatform(ctx, dynamicClient, "aws")

		icName := "ocp52837"
		defer testutil.CleanupICAndWaitForLBService(ctx, dynamicClient, kubeClient, icName, g.GinkgoWriter)
		o.Expect(testutil.CreateIC(ctx, dynamicClient, icName, icName+"."+baseDomain(), testutil.WithCLB())).To(o.Succeed(), "failed to create Classic LB ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True"}, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not become available", icName)
		o.Expect(testutil.WaitForLBProvisioned(ctx, kubeClient, icName, testutil.LongTimeout)).To(o.Succeed(), "load balancer for ingresscontroller %s was not provisioned", icName)

		g.By("Patch the IngressController to use an NLB")
		patch := `{"spec":{"endpointPublishingStrategy":{"loadBalancer":{"providerParameters":{"type":"AWS","aws":{"type":"NLB"}}}}}}`
		o.Expect(testutil.PatchICMerge(ctx, dynamicClient, icName, patch)).To(o.Succeed(), "failed to patch ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICProgressingMessage(ctx, dynamicClient, icName)).To(o.Succeed(), "ingresscontroller %s did not report that the service must be recreated", icName)
		o.Expect(testutil.DeleteLBService(ctx, kubeClient, icName)).To(o.Succeed(), "failed to recreate load balancer service for ingresscontroller %s", icName)
		o.Expect(testutil.WaitForLBAnnotation(ctx, kubeClient, icName, awsLBTypeAnnotation, true, "nlb", testutil.LongTimeout)).To(o.Succeed(), "NLB annotation was not set for ingresscontroller %s", icName)
		o.Expect(testutil.WaitForLBAnnotation(ctx, kubeClient, icName, awsLBProxyProtocolAnnotation, false, "", testutil.LongTimeout)).To(o.Succeed(), "stale proxy-protocol annotation remained on NLB service for ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True", "Progressing": "False"}, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not settle as available and not progressing", icName)

		g.By("Patch the IngressController to use a Classic load balancer")
		patch = `{"spec":{"endpointPublishingStrategy":{"loadBalancer":{"providerParameters":{"type":"AWS","aws":{"type":"Classic"}}}}}}`
		o.Expect(testutil.PatchICMerge(ctx, dynamicClient, icName, patch)).To(o.Succeed(), "failed to patch ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICProgressingMessage(ctx, dynamicClient, icName)).To(o.Succeed(), "ingresscontroller %s did not report that the service must be recreated", icName)
		o.Expect(testutil.DeleteLBService(ctx, kubeClient, icName)).To(o.Succeed(), "failed to recreate load balancer service for ingresscontroller %s", icName)
		o.Expect(testutil.WaitForLBAnnotation(ctx, kubeClient, icName, awsLBTypeAnnotation, false, "", testutil.LongTimeout)).To(o.Succeed(), "NLB annotation remained on Classic LB service for ingresscontroller %s", icName)
		o.Expect(testutil.WaitForLBAnnotation(ctx, kubeClient, icName, awsLBProxyProtocolAnnotation, true, "*", testutil.LongTimeout)).To(o.Succeed(), "proxy-protocol annotation was not set on Classic LB service for ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True", "Progressing": "False"}, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not settle as available and not progressing", icName)
	})

	//Author:hongli
	// OCP-72126 is Azure-only and intentionally IPv4-specific: it exercises
	// Azure's 4000 source-IP NSG limit with IPv4 /32 ranges.
	g.It("IngressController supports multiple CIDR blocks in one Azure NSG rule", g.Label("OCP-72126"), func() {
		testutil.SkipUnlessPlatform(ctx, dynamicClient, "azure")

		icName := "ocp72126"
		defer testutil.CleanupICAndWaitForLBService(ctx, dynamicClient, kubeClient, icName, g.GinkgoWriter)
		cidrs := testutil.AzureMaximumSourceRanges(3995)
		o.Expect(testutil.CreateIC(ctx, dynamicClient, icName, icName+"."+baseDomain(), testutil.WithExternalLB(), testutil.WithAllowedSourceRanges(cidrs))).To(o.Succeed(), "failed to create Azure ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True"}, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not become available", icName)
		o.Expect(testutil.WaitForServiceEvent(ctx, kubeClient, icName, "Ensured load balancer", testutil.DefaultTimeout)).To(o.Succeed(), "load balancer service did not report a successful ensure event")

		g.By("Patch the IngressController to exceed the Azure source IP address limit")
		additions := []string{
			"1.1.32.118/32", "1.1.32.120/32", "1.1.32.122/32",
			"1.1.32.124/32", "1.1.32.126/32", "1.1.32.128/32",
		}
		operations := make([]string, 0, len(additions))
		for _, cidr := range additions {
			operations = append(operations, fmt.Sprintf(`{"op":"add","path":"/spec/endpointPublishingStrategy/loadBalancer/allowedSourceRanges/-","value":%q}`, cidr))
		}
		o.Expect(testutil.PatchICJSON(ctx, dynamicClient, icName, "["+strings.Join(operations, ",")+"]")).To(o.Succeed(), "failed to add source ranges to ingresscontroller %s", icName)
		o.Expect(testutil.WaitForServiceEventPattern(ctx, kubeClient, icName, testutil.MaxAzureSourceRangePattern, 2*time.Minute)).To(o.Succeed(), "service did not report the expected Azure source IP address limit")
	})

	//Author:hongli
	g.It("AWS NLB supports selecting subnets", g.Label("OCP-75440"), func() {
		testutil.SkipUnlessPlatform(ctx, dynamicClient, "aws")

		publicSubnets, err := testutil.PublicSubnetNames(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to discover public subnets")
		if len(publicSubnets) == 0 {
			g.Skip("Skipping since no public subnet found")
		}

		icName := "ocp75440"
		defer testutil.CleanupICAndWaitForLBService(ctx, dynamicClient, kubeClient, icName, g.GinkgoWriter)
		o.Expect(testutil.CreateIC(ctx, dynamicClient, icName, icName+"."+baseDomain(), testutil.WithCLB())).To(o.Succeed(), "failed to create Classic LB ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True"}, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not become available", icName)

		g.By("Annotate the IngressController so the old service is deleted automatically")
		annotationPatch := fmt.Sprintf(`{"metadata":{"annotations":{%q:""}}}`, autoDeleteLoadBalancerAnnotation)
		o.Expect(testutil.PatchICMerge(ctx, dynamicClient, icName, annotationPatch)).To(o.Succeed(), "failed to annotate ingresscontroller %s", icName)
		patch := fmt.Sprintf(`{"spec":{"endpointPublishingStrategy":{"loadBalancer":{"providerParameters":{"type":"AWS","aws":{"type":"NLB","networkLoadBalancer":{"subnets":{"ids":null,"names":[%s]}}}}}}}}`, testutil.QuoteJoin(publicSubnets))
		o.Expect(testutil.PatchICMerge(ctx, dynamicClient, icName, patch)).To(o.Succeed(), "failed to patch ingresscontroller %s", icName)
		o.Expect(testutil.WaitForLBAnnotation(ctx, kubeClient, icName, awsLBSubnetsAnnotation, true, strings.Join(publicSubnets, ","), testutil.LongTimeout)).To(o.Succeed(), "subnet annotation did not match selected subnets for ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True", "Progressing": "False"}, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not settle as available and not progressing", icName)
		hostname, err := testutil.GetLBHostname(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get load balancer hostname")
		o.Expect(hostname).To(o.MatchRegexp(`.*\.elb\..*\.amazonaws\.com`), "unexpected load balancer hostname %q for ingresscontroller %s", hostname, icName)
	})

	//Author:mjoseph
	g.It("AWS NLB allocates and updates Elastic IP allocations", g.Label("OCP-75499"), func() {
		testutil.SkipUnlessPlatform(ctx, dynamicClient, "aws")

		icName := "ocp75499"
		publicSubnets, err := testutil.PublicSubnetNames(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to discover public subnets")
		if len(publicSubnets) == 0 {
			g.Skip("Skipping since no public subnet found")
		}

		ec2Client, err := testutil.NewEC2Client(ctx)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create EC2 client")
		firstEIPs, err := testutil.AllocateElasticIPs(ctx, ec2Client, len(publicSubnets))
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), testutil.LongTimeout)
			defer cancel()
			o.Expect(testutil.ReleaseElasticIPs(cleanupCtx, ec2Client, firstEIPs, g.GinkgoWriter)).To(o.Succeed(), "failed to release first Elastic IP allocation set for ingresscontroller %s", icName)
		}()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to allocate first set of Elastic IPs for ingresscontroller %s", icName)
		secondEIPs, err := testutil.AllocateElasticIPs(ctx, ec2Client, len(publicSubnets))
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), testutil.LongTimeout)
			defer cancel()
			o.Expect(testutil.ReleaseElasticIPs(cleanupCtx, ec2Client, secondEIPs, g.GinkgoWriter)).To(o.Succeed(), "failed to release second Elastic IP allocation set for ingresscontroller %s", icName)
		}()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to allocate second set of Elastic IPs for ingresscontroller %s", icName)

		defer testutil.CleanupICAndWaitForLBService(ctx, dynamicClient, kubeClient, icName, g.GinkgoWriter)
		o.Expect(testutil.CreateIC(ctx, dynamicClient, icName, icName+"."+baseDomain(), testutil.WithNLB())).To(o.Succeed(), "failed to create NLB ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True"}, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not become available", icName)

		g.By("Patch the IngressController with the first set of Elastic IPs")
		subnets := fmt.Sprintf(`"subnets":{"ids":null,"names":[%s]}`, testutil.QuoteJoin(publicSubnets))
		patch := fmt.Sprintf(`{"spec":{"endpointPublishingStrategy":{"loadBalancer":{"providerParameters":{"type":"AWS","aws":{"type":"NLB","networkLoadBalancer":{%s,"eipAllocations":[%s]}}}}}}}`, subnets, testutil.QuoteJoin(firstEIPs))
		o.Expect(testutil.PatchICMerge(ctx, dynamicClient, icName, patch)).To(o.Succeed(), "failed to patch ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True", "Progressing": "True", "Degraded": "False"}, testutil.DefaultTimeout)).To(o.Succeed(), "ingresscontroller %s did not enter the expected progressing state", icName)
		o.Expect(testutil.WaitForICProgressingMessage(ctx, dynamicClient, icName)).To(o.Succeed(), "ingresscontroller %s did not report that the service must be recreated", icName)
		o.Expect(testutil.DeleteLBService(ctx, kubeClient, icName)).To(o.Succeed(), "failed to recreate load balancer service for ingresscontroller %s", icName)
		o.Expect(testutil.WaitForLBAnnotation(ctx, kubeClient, icName, awsLBEIPAllocationsAnnotation, true, strings.Join(firstEIPs, ","), testutil.LongTimeout)).To(o.Succeed(), "first Elastic IP allocation annotation did not match ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True", "Progressing": "False"}, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not settle as available and not progressing", icName)

		g.By("Patch the IngressController with the second set of Elastic IPs")
		patch = fmt.Sprintf(`{"spec":{"endpointPublishingStrategy":{"loadBalancer":{"providerParameters":{"type":"AWS","aws":{"type":"NLB","networkLoadBalancer":{%s,"eipAllocations":[%s]}}}}}}}`, subnets, testutil.QuoteJoin(secondEIPs))
		o.Expect(testutil.PatchICMerge(ctx, dynamicClient, icName, patch)).To(o.Succeed(), "failed to patch ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True", "Progressing": "True", "Degraded": "False"}, testutil.DefaultTimeout)).To(o.Succeed(), "ingresscontroller %s did not enter the expected progressing state", icName)
		o.Expect(testutil.WaitForICProgressingMessage(ctx, dynamicClient, icName)).To(o.Succeed(), "ingresscontroller %s did not report that the service must be recreated", icName)
		o.Expect(testutil.DeleteLBService(ctx, kubeClient, icName)).To(o.Succeed(), "failed to recreate load balancer service for ingresscontroller %s", icName)
		o.Expect(testutil.WaitForLBAnnotation(ctx, kubeClient, icName, awsLBEIPAllocationsAnnotation, true, strings.Join(secondEIPs, ","), testutil.LongTimeout)).To(o.Succeed(), "second Elastic IP allocation annotation did not match ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICConditions(ctx, dynamicClient, icName, map[string]string{"Available": "True", "Progressing": "False"}, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not settle as available and not progressing", icName)
		hostname, err := testutil.GetLBHostname(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get load balancer hostname")
		o.Expect(hostname).To(o.MatchRegexp(`.*\.elb\..*\.amazonaws\.com`), "unexpected load balancer hostname %q for ingresscontroller %s", hostname, icName)
	})
})
