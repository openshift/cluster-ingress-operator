package qe

import (
	"context"
	"errors"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	testutil "github.com/openshift/cluster-ingress-operator/tests-extension/test"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var _ = g.Describe("[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-TLS", func() {
	var (
		ctx           context.Context
		kubeClient    kubernetes.Interface
		dynamicClient dynamic.Interface
		restConfig    *rest.Config
	)

	g.BeforeEach(func() {
		ctx = context.Background()
		var err error
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		restConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to build REST config from KUBECONFIG")
		restConfig.Timeout = 30 * time.Second
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
	g.It("IngressController tlsSecurityProfile can be updated", g.Label("OCP-25702"), func() {
		const icName = "ocp25702"
		icDomain := icName + "." + baseDomain()
		defer testutil.CleanupIC(ctx, dynamicClient, icName, g.GinkgoWriter)

		g.By("1. Create an IngressController with the default Intermediate TLS profile")
		o.Expect(testutil.CreateIC(ctx, dynamicClient, icName, icDomain, testutil.WithNodePort())).To(o.Succeed(), "failed to create ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICAvailable(ctx, dynamicClient, icName, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not become available", icName)
		o.Expect(testutil.WaitForDeployGeneration(ctx, kubeClient, icName, 1, testutil.DefaultTimeout)).To(o.Succeed(), "ingresscontroller %s did not reach deployment generation 1", icName)
		pod, err := testutil.GetRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get a router pod for ingresscontroller %s", icName)

		g.By("2. Verify the default Intermediate profile")
		deployment, err := testutil.GetRouterDeployment(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get router deployment for ingresscontroller %s", icName)
		expectRouterTLSProfile(deployment, *configv1.TLSProfiles[configv1.TLSProfileIntermediateType])

		g.By("3. Update the IngressController to the Old profile")
		o.Expect(testutil.PatchICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName,
			`{"spec":{"tlsSecurityProfile":{"type":"Old"}}}`, 2, pod)).NotTo(o.BeEmpty(),
			"failed to roll out the Old TLS profile for ingresscontroller %s", icName)
		deployment, err = testutil.GetRouterDeployment(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get router deployment after setting the Old profile")
		expectRouterTLSProfile(deployment, *configv1.TLSProfiles[configv1.TLSProfileOldType])

		g.By("4. Update the IngressController back to the Intermediate profile")
		pod, err = testutil.GetRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the Old-profile router pod")
		o.Expect(testutil.PatchICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName,
			`{"spec":{"tlsSecurityProfile":{"type":"Intermediate"}}}`, 3, pod)).NotTo(o.BeEmpty(),
			"failed to roll out the Intermediate TLS profile for ingresscontroller %s", icName)
		deployment, err = testutil.GetRouterDeployment(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get router deployment after restoring the Intermediate profile")
		expectRouterTLSProfile(deployment, *configv1.TLSProfiles[configv1.TLSProfileIntermediateType])

		g.By("5. Update the IngressController to a Custom profile")
		pod, err = testutil.GetRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the Intermediate-profile router pod")
		o.Expect(testutil.PatchICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName,
			`{"spec":{"tlsSecurityProfile":{"type":"Custom","custom":{"ciphers":["DHE-RSA-AES256-GCM-SHA384","ECDHE-ECDSA-AES256-GCM-SHA384"],"minTLSVersion":"VersionTLS12"}}}}`,
			4, pod)).NotTo(o.BeEmpty(), "failed to roll out the Custom TLS profile for ingresscontroller %s", icName)
		deployment, err = testutil.GetRouterDeployment(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get router deployment after setting the Custom profile")
		expectRouterTLSProfile(deployment, configv1.TLSProfileSpec{
			Ciphers:       []string{"DHE-RSA-AES256-GCM-SHA384", "ECDHE-ECDSA-AES256-GCM-SHA384"},
			MinTLSVersion: configv1.VersionTLS12,
		})
	})

	//Author:mjoseph
	g.It("IngressController tlsSecurityProfile can be set to Modern TLSv1.3", g.Label("OCP-43284"), func() {
		const icName = "ocp43284"
		icDomain := icName + "." + baseDomain()
		defer testutil.CleanupIC(ctx, dynamicClient, icName, g.GinkgoWriter)

		g.By("1. Create an IngressController and update it to the Modern profile")
		o.Expect(testutil.CreateIC(ctx, dynamicClient, icName, icDomain, testutil.WithNodePort())).To(o.Succeed(), "failed to create ingresscontroller %s", icName)
		o.Expect(testutil.WaitForDeployGeneration(ctx, kubeClient, icName, 1, testutil.DefaultTimeout)).To(o.Succeed(), "ingresscontroller %s did not reach deployment generation 1", icName)
		oldPod, err := testutil.GetRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the initial router pod for ingresscontroller %s", icName)
		pod := testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName,
			`{"spec":{"tlsSecurityProfile":{"type":"Modern"}}}`, 2, oldPod)
		o.Expect(pod).NotTo(o.BeEmpty(), "failed to roll out the Modern TLS profile for ingresscontroller %s", icName)

		g.By("2. Verify the Modern profile in the router deployment")
		deployment, err := testutil.GetRouterDeployment(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get router deployment for ingresscontroller %s", icName)
		expectRouterTLSProfile(deployment, *configv1.TLSProfiles[configv1.TLSProfileModernType])

		g.By("3. Verify TLSv1.3 and its cipher suites in HAProxy")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "ssl-default-bind-options ssl-min-ver TLSv1.3")
		fips := routerDeploymentFIPS(deployment)
		cipherSuites := expectedCipherSuites(*configv1.TLSProfiles[configv1.TLSProfileModernType], fips)
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "ssl-default-bind-ciphersuites "+cipherSuites)
	})

	//Author:mjoseph
	g.It("ROUTER_CURVES configures post-quantum cryptography curves", g.Label("OCP-88875"), func() {
		g.By("1. Read ROUTER_CURVES from the default router deployment")
		deployment, err := testutil.GetRouterDeployment(ctx, kubeClient, "default")
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the default router deployment")
		curves, found := deploymentEnv(deployment, "ROUTER_CURVES")
		o.Expect(found).To(o.BeTrue(), "default router deployment does not set ROUTER_CURVES")
		fips := !strings.Contains(curves, "X25519MLKEM768")
		expectedCurves := "X25519MLKEM768:X25519:secp256r1:secp384r1"
		if fips {
			expectedCurves = "P-256:P-384:P-521"
		}
		o.Expect(curves).To(o.Equal(expectedCurves), "unexpected ROUTER_CURVES value")

		g.By("2. Verify the curve settings in HAProxy")
		pod, err := testutil.GetRouterPodName(ctx, kubeClient, "default")
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the default router pod")
		output, err := testutil.ExecInRouterPod(ctx, restConfig, kubeClient, pod, "cat haproxy.config | grep curve")
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to read curve settings from the default router HAProxy configuration")
		o.Expect(output).To(o.ContainSubstring("ssl-default-bind-curves "+expectedCurves), "unexpected HAProxy curve settings")
	})
})

// deploymentEnv returns the value of an environment variable in the router
// container.
func deploymentEnv(deployment *appsv1.Deployment, name string) (string, bool) {
	container, err := testutil.RouterContainer(deployment)
	if err != nil {
		return "", false
	}
	for _, env := range container.Env {
		if env.Name == name {
			return env.Value, true
		}
	}
	return "", false
}

// routerDeploymentFIPS infers whether the router omits non-FIPS curve and
// cipher support. ROUTER_CURVES is the most direct signal on current routers;
// older images that do not set it can be inferred from TLS 1.3 cipher suites.
func routerDeploymentFIPS(deployment *appsv1.Deployment) bool {
	if curves, found := deploymentEnv(deployment, "ROUTER_CURVES"); found && curves != "" {
		return !strings.Contains(curves, "X25519MLKEM768")
	}
	suites, _ := deploymentEnv(deployment, "ROUTER_CIPHERSUITES")
	return !strings.Contains(suites, "TLS_CHACHA20_POLY1305_SHA256")
}

// expectRouterTLSProfile verifies the router deployment environment generated
// for a TLS profile. Profile cipher lists contain TLS 1.3 suites followed by
// TLS 1.2 and earlier ciphers, matching the two router environment variables.
func expectRouterTLSProfile(deployment *appsv1.Deployment, profile configv1.TLSProfileSpec) {
	version, err := tlsVersion(profile.MinTLSVersion)
	o.Expect(err).NotTo(o.HaveOccurred(), "unsupported TLS version %q", profile.MinTLSVersion)
	sslMinVersion, found := deploymentEnv(deployment, "SSL_MIN_VERSION")
	o.Expect(found).To(o.BeTrue(), "router deployment does not set SSL_MIN_VERSION")
	o.Expect(sslMinVersion).To(o.Equal(version))

	fips := routerDeploymentFIPS(deployment)
	if suites := expectedCipherSuites(profile, fips); suites != "" {
		cipherSuites, found := deploymentEnv(deployment, "ROUTER_CIPHERSUITES")
		o.Expect(found).To(o.BeTrue(), "router deployment does not set ROUTER_CIPHERSUITES")
		o.Expect(cipherSuites).To(o.Equal(suites))
	}
	if ciphers := expectedCiphers(profile); ciphers != "" {
		routerCiphers, found := deploymentEnv(deployment, "ROUTER_CIPHERS")
		o.Expect(found).To(o.BeTrue(), "router deployment does not set ROUTER_CIPHERS")
		o.Expect(routerCiphers).To(o.Equal(ciphers))
	}
}

// expectedCipherSuites returns the colon-joined TLS 1.3 suites, excluding
// ChaCha20-Poly1305 in FIPS mode.
func expectedCipherSuites(profile configv1.TLSProfileSpec, fips bool) string {
	suites := make([]string, 0, len(profile.Ciphers))
	for _, cipher := range profile.Ciphers {
		if !strings.HasPrefix(cipher, "TLS_") {
			continue
		}
		if fips && cipher == "TLS_CHACHA20_POLY1305_SHA256" {
			continue
		}
		suites = append(suites, cipher)
	}
	return strings.Join(suites, ":")
}

// expectedCiphers returns the colon-joined TLS 1.2 and earlier cipher names.
func expectedCiphers(profile configv1.TLSProfileSpec) string {
	ciphers := make([]string, 0, len(profile.Ciphers))
	for _, cipher := range profile.Ciphers {
		if !strings.HasPrefix(cipher, "TLS_") {
			ciphers = append(ciphers, cipher)
		}
	}
	return strings.Join(ciphers, ":")
}

// tlsVersion maps an OpenShift TLS protocol version to HAProxy's spelling.
func tlsVersion(version configv1.TLSProtocolVersion) (string, error) {
	switch version {
	case configv1.VersionTLS10:
		// The router maps TLS 1.0 to TLS 1.1 because HAProxy does not support TLS 1.0.
		return "TLSv1.1", nil
	case configv1.VersionTLS11:
		return "TLSv1.1", nil
	case configv1.VersionTLS12:
		return "TLSv1.2", nil
	case configv1.VersionTLS13:
		return "TLSv1.3", nil
	default:
		return "", errors.New("unsupported TLS protocol version")
	}
}
