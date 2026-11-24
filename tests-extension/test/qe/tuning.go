package qe

import (
	"context"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	testutil "github.com/openshift/cluster-ingress-operator/tests-extension/test"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var _ = g.Describe("[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Route-Tunning", func() {
	var (
		ctx           context.Context
		kubeClient    kubernetes.Interface
		dynamicClient dynamic.Interface
		restConfig    *rest.Config
	)

	g.BeforeEach(func() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Minute)
		g.DeferCleanup(cancel)
		var err error
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		restConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to build REST config from KUBECONFIG")
		kubeClient, err = kubernetes.NewForConfig(restConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kubernetes client")
		dynamicClient, err = dynamic.NewForConfig(restConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create dynamic client")
	})

	//Author:mjoseph
	g.It("IngressController tuningOptions configure HAProxy header buffers", g.Label("OCP-40747"), func() {
		firstIC := "ocp40747a"
		secondIC := "ocp40747b"
		defer testutil.CleanupICWithTimeout(dynamicClient, firstIC, g.GinkgoWriter)
		defer testutil.CleanupICWithTimeout(dynamicClient, secondIC, g.GinkgoWriter)

		initialTuning := testutil.WithTuningOptions(map[string]interface{}{
			"headerBufferBytes":           int64(16385),
			"headerBufferMaxRewriteBytes": int64(4097),
		})

		g.By("Create two custom ingresscontrollers and verify their initial HAProxy buffer settings")
		firstPod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, firstIC, testutil.WithNodePort(), initialTuning)
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, firstPod, "tune.bufsize 16385")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, firstPod, "tune.maxrewrite 4097")

		secondPod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, secondIC, testutil.WithNodePort(), initialTuning)
		secondPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, secondIC, `{"spec":{"tuningOptions":{"headerBufferBytes":18000,"headerBufferMaxRewriteBytes":10000}}}`, 2, secondPod)
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, secondPod, "tune.bufsize 18000")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, secondPod, "tune.maxrewrite 10000")

		g.By("Reject tuningOptions values below the supported HAProxy minimums")
		testutil.ExpectInvalidICPatch(ctx, dynamicClient, secondIC,
			`{"spec":{"tuningOptions":{"headerBufferBytes":8192,"headerBufferMaxRewriteBytes":2048}}}`,
			"spec.tuningOptions.headerBufferMaxRewriteBytes: Invalid value: 2048")
		testutil.ExpectInvalidICPatch(ctx, dynamicClient, secondIC,
			`{"spec":{"tuningOptions":{"headerBufferBytes":8192,"headerBufferMaxRewriteBytes":2048}}}`,
			"spec.tuningOptions.headerBufferBytes: Invalid value: 8192")

		g.By("Update the first ingresscontroller maxrewrite setting")
		firstPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, firstIC, `{"spec":{"tuningOptions":{"headerBufferMaxRewriteBytes":8192}}}`, 2, firstPod)
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, firstPod, "tune.maxrewrite 8192")

		g.By("Update the first ingresscontroller bufsize setting")
		firstPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, firstIC, `{"spec":{"tuningOptions":{"headerBufferBytes":18000}}}`, 3, firstPod)
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, firstPod, "tune.bufsize 18000")
	})

	//Author:shudili
	g.It("The threadCount parameter controls HAProxy nbthread", g.Label("OCP-41110"), func() {
		icName := "ocp41110"
		defer testutil.CleanupICWithTimeout(dynamicClient, icName, g.GinkgoWriter)

		pod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, icName, testutil.WithNodePort())
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_THREADS", "4")

		pod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName, `{"spec":{"tuningOptions":{"threadCount":6}}}`, 2, pod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_THREADS", "6")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "nbthread 6")

		testutil.ExpectInvalidICPatch(ctx, dynamicClient, icName, `{"spec":{"tuningOptions":{"threadCount":-1}}}`,
			"Invalid value: -1: spec.tuningOptions.threadCount in body should be greater than or equal to 1")
		testutil.ExpectInvalidICPatch(ctx, dynamicClient, icName, `{"spec":{"tuningOptions":{"threadCount":512}}}`,
			"Invalid value: 512: spec.tuningOptions.threadCount in body should be less than or equal to 64")
		testutil.ExpectInvalidICPatch(ctx, dynamicClient, icName, `{"spec":{"tuningOptions":{"threadCount":"abc"}}}`,
			`Invalid value: "string": spec.tuningOptions.threadCount in body must be of type integer: "string"`)
	})

	//Author:aiyengar
	g.It("TuningOptions configure TCP client and server timeouts", g.Label("OCP-43105"), func() {
		icName := "ocp43105"
		defer testutil.CleanupICWithTimeout(dynamicClient, icName, g.GinkgoWriter)

		pod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, icName, testutil.WithNodePort())
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "timeout client 30s")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "timeout client-fin 1s")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "timeout server 30s")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "timeout server-fin 1s")

		patch := `{"spec":{"tuningOptions":{"clientFinTimeout":"3s","clientTimeout":"33s","serverFinTimeout":"3s","serverTimeout":"33s"}}}`
		pod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName, patch, 2, pod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_CLIENT_FIN_TIMEOUT", "3s")
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_DEFAULT_CLIENT_TIMEOUT", "33s")
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_DEFAULT_SERVER_TIMEOUT", "33s")
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_DEFAULT_SERVER_FIN_TIMEOUT", "3s")
	})

	//Author:aiyengar
	g.It("TuningOptions configure TLS inspect delay", g.Label("OCP-43113"), func() {
		icName := "ocp43113"
		defer testutil.CleanupICWithTimeout(dynamicClient, icName, g.GinkgoWriter)

		pod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, icName, testutil.WithNodePort())
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "tcp-request inspect-delay 5s")

		pod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName, `{"spec":{"tuningOptions":{"tlsInspectDelay":"15s"}}}`, 2, pod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_INSPECT_DELAY", "15s")
	})

	//Author:shudili
	g.It("TuningOptions configure router backend check interval", g.Label("OCP-50662"), func() {
		firstIC := "ocp50662one"
		secondIC := "ocp50662two"
		defer testutil.CleanupICWithTimeout(dynamicClient, firstIC, g.GinkgoWriter)
		defer testutil.CleanupICWithTimeout(dynamicClient, secondIC, g.GinkgoWriter)

		firstPod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, firstIC, testutil.WithNodePort(), testutil.WithTuningOptions(map[string]interface{}{"healthCheckInterval": "20s"}))
		secondPod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, secondIC, testutil.WithNodePort(), testutil.WithTuningOptions(map[string]interface{}{"healthCheckInterval": "100m"}))
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, firstPod, "ROUTER_BACKEND_CHECK_INTERVAL", "20s")
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, secondPod, "ROUTER_BACKEND_CHECK_INTERVAL", "100m")

		firstPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, firstIC, `{"spec":{"tuningOptions":{"healthCheckInterval":"2147483647ms"}}}`, 2, firstPod)
		secondPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, secondIC, `{"spec":{"tuningOptions":{"healthCheckInterval":"0s"}}}`, 2, secondPod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, firstPod, "ROUTER_BACKEND_CHECK_INTERVAL", "2147483647ms")
		testutil.ExpectRouterPodEnvAbsent(ctx, restConfig, kubeClient, secondPod, "ROUTER_BACKEND_CHECK_INTERVAL")

		g.By("Reset the health-check interval before testing over-maximum clamping")
		firstPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, firstIC, `{"spec":{"tuningOptions":{"healthCheckInterval":"30s"}}}`, 3, firstPod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, firstPod, "ROUTER_BACKEND_CHECK_INTERVAL", "30s")

		g.By("Verify an over-maximum healthCheckInterval is clamped to the supported maximum")
		firstPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, firstIC, `{"spec":{"tuningOptions":{"healthCheckInterval":"2147483900ms"}}}`, 4, firstPod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, firstPod, "ROUTER_BACKEND_CHECK_INTERVAL", "2147483647ms")

		testutil.ExpectInvalidICPatch(ctx, dynamicClient, firstIC, `{"spec":{"tuningOptions":{"healthCheckInterval":"-1s"}}}`, `Invalid value: "-1s"`)
		testutil.ExpectInvalidICPatch(ctx, dynamicClient, firstIC, `{"spec":{"tuningOptions":{"healthCheckInterval":"0abc"}}}`, `Invalid value: "0abc":`)
	})

	//Author:shudili
	g.It("TuningOptions configure ROUTER_MAX_CONNECTIONS", g.Label("OCP-50926"), func() {
		icName := "ocp50926"
		defer testutil.CleanupICWithTimeout(dynamicClient, icName, g.GinkgoWriter)

		pod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, icName, testutil.WithNodePort(), testutil.WithTuningOptions(map[string]interface{}{"maxConnections": int64(-1)}))
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_MAX_CONNECTIONS", "auto")
		testutil.ExpectHAProxyConfigDirectiveAbsent(ctx, restConfig, kubeClient, pod, "maxconn")

		pod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName, `{"spec":{"tuningOptions":{"maxConnections":2000}}}`, 2, pod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_MAX_CONNECTIONS", "2000")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "maxconn 2000")

		pod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName, `{"spec":{"tuningOptions":{"maxConnections":2000000}}}`, 3, pod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_MAX_CONNECTIONS", "2000000")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "maxconn 2000000")

		pod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName, `{"spec":{"tuningOptions":{"maxConnections":0}}}`, 4, pod)
		testutil.ExpectRouterPodEnvAbsent(ctx, restConfig, kubeClient, pod, "ROUTER_MAX_CONNECTIONS")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "maxconn 50000")

		testutil.ExpectInvalidICPatch(ctx, dynamicClient, icName, `{"spec":{"tuningOptions":{"maxConnections":1999}}}`,
			"Invalid value: 1999: maxConnections must be 0, -1, or between 2000 and 2000000")
		testutil.ExpectInvalidICPatch(ctx, dynamicClient, icName, `{"spec":{"tuningOptions":{"maxConnections":2000001}}}`,
			"Invalid value: 2000001: maxConnections must be 0, -1, or between 2000 and 2000000")
		testutil.ExpectInvalidICPatch(ctx, dynamicClient, icName, `{"spec":{"tuningOptions":{"maxConnections":"abc"}}}`,
			`Invalid value: "string": spec.tuningOptions.maxConnections in body must be of type integer`)
	})

	//Author:shudili
	g.It("TuningOptions configure HAProxy reload interval", g.Label("OCP-53605"), func() {
		firstIC := "ocp53605one"
		secondIC := "ocp53605two"
		defer testutil.CleanupICWithTimeout(dynamicClient, firstIC, g.GinkgoWriter)
		defer testutil.CleanupICWithTimeout(dynamicClient, secondIC, g.GinkgoWriter)

		firstPod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, firstIC, testutil.WithNodePort(), testutil.WithTuningOptions(map[string]interface{}{"reloadInterval": "15s"}))
		secondPod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, secondIC, testutil.WithNodePort(), testutil.WithTuningOptions(map[string]interface{}{"reloadInterval": "120s"}))
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, firstPod, "RELOAD_INTERVAL", "15s")
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, secondPod, "RELOAD_INTERVAL", "2m")

		firstPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, firstIC, `{"spec":{"tuningOptions":{"reloadInterval":"1m"}}}`, 2, firstPod)
		secondPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, secondIC, `{"spec":{"tuningOptions":{"reloadInterval":"0s"}}}`, 2, secondPod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, firstPod, "RELOAD_INTERVAL", "1m")
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, secondPod, "RELOAD_INTERVAL", "5s")

		g.By("Verify out-of-range reload intervals are clamped")
		firstPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, firstIC, `{"spec":{"tuningOptions":{"reloadInterval":"121s"}}}`, 3, firstPod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, firstPod, "RELOAD_INTERVAL", "2m")

		firstPod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, firstIC, `{"spec":{"tuningOptions":{"reloadInterval":"0.5s"}}}`, 4, firstPod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, firstPod, "RELOAD_INTERVAL", "1s")

		testutil.ExpectInvalidICPatch(ctx, dynamicClient, firstIC, `{"spec":{"tuningOptions":{"reloadInterval":"-1s"}}}`, `Invalid value: "-1s"`)
		testutil.ExpectInvalidICPatch(ctx, dynamicClient, firstIC, `{"spec":{"tuningOptions":{"reloadInterval":"1abc"}}}`, `Invalid value: "1abc"`)
		testutil.ExpectInvalidICPatch(ctx, dynamicClient, firstIC, `{"spec":{"tuningOptions":{"reloadInterval":"012 s"}}}`, `Invalid value: "012 s"`)
	})

	//Author:shudili
	g.It("Default HAProxy maxconn is 50000", g.Label("OCP-55367"), func() {
		icName := "ocp55367"
		defer testutil.CleanupICWithTimeout(dynamicClient, icName, g.GinkgoWriter)

		pod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, icName, testutil.WithNodePort())
		testutil.ExpectRouterPodEnvAbsent(ctx, restConfig, kubeClient, pod, "ROUTER_MAX_CONNECTIONS")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "maxconn 50000")

		g.By("Verify removing an already absent maxConnections setting does not roll out the custom router")
		before, err := testutil.GetRouterDeployment(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get custom router deployment for ingresscontroller %s", icName)
		o.Expect(testutil.PatchICMerge(ctx, dynamicClient, icName, `{"spec":{"tuningOptions":{"maxConnections":null}}}`)).To(o.Succeed(), "failed to patch maxConnections to null on ingresscontroller %s", icName)
		after, err := testutil.GetRouterDeployment(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get custom router deployment after null maxConnections patch for ingresscontroller %s", icName)
		o.Expect(after.Generation).To(o.Equal(before.Generation), "custom router deployment unexpectedly rolled out")
		o.Expect(after.Status.ObservedGeneration).To(o.Equal(before.Status.ObservedGeneration), "custom router deployment generation was unexpectedly observed")

		pod, err = testutil.GetRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get router pod for ingresscontroller %s", icName)
		testutil.ExpectRouterPodEnvAbsent(ctx, restConfig, kubeClient, pod, "ROUTER_MAX_CONNECTIONS")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "maxconn 50000")

		pod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName, `{"spec":{"tuningOptions":{"maxConnections":500000}}}`, 2, pod)
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_MAX_CONNECTIONS", "500000")
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "maxconn 500000")
	})

	//Author:shudili
	g.It("TuningOptions configure HTTP keep-alive timeout", g.Label("OCP-86153"), func() {
		icName := "ocp86153"
		defer testutil.CleanupICWithTimeout(dynamicClient, icName, g.GinkgoWriter)

		pod := testutil.CreateICAndWaitForRouterPod(ctx, dynamicClient, kubeClient, icName, testutil.WithNodePort())
		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "timeout http-keep-alive 300s")
		testutil.ExpectRouterPodEnvAbsent(ctx, restConfig, kubeClient, pod, "ROUTER_SLOWLORIS_HTTP_KEEPALIVE")

		pod = testutil.UpdateICAndWaitForNewRouterPod(ctx, dynamicClient, kubeClient, icName, `{"spec":{"tuningOptions":{"httpKeepAliveTimeout":"50s"}}}`, 2, pod)

		ic, err := dynamicClient.Resource(testutil.IngressControllerGVR).Namespace(testutil.OperatorNamespace).Get(ctx, icName, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get ingresscontroller %s", icName)
		o.Expect(testutil.NestedString(ic.Object, "spec", "tuningOptions", "httpKeepAliveTimeout")).To(o.Equal("50s"), "unexpected httpKeepAliveTimeout in ingresscontroller %s spec", icName)

		testutil.ExpectHAProxyConfigLine(ctx, restConfig, kubeClient, pod, "timeout http-keep-alive 50s")
		testutil.ExpectRouterPodEnv(ctx, restConfig, kubeClient, pod, "ROUTER_SLOWLORIS_HTTP_KEEPALIVE", "50s")

		testutil.ExpectInvalidICPatch(ctx, dynamicClient, icName, `{"spec":{"tuningOptions":{"httpKeepAliveTimeout":"50m"}}}`,
			"httpKeepAliveTimeout must be less than or equal to 15 minutes")
		testutil.ExpectInvalidICPatch(ctx, dynamicClient, icName, `{"spec":{"tuningOptions":{"httpKeepAliveTimeout":"1h"}}}`,
			"httpKeepAliveTimeout must be a valid duration string composed of an unsigned integer value, optionally followed by a decimal fraction and a unit suffix (ms, s, m)")
	})
})
