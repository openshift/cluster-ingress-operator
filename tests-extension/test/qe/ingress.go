package qe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	testutil "github.com/openshift/cluster-ingress-operator/tests-extension/test"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// apiRequestTimeout bounds each client-go request. The specs use
// context.Background(), so without a timeout a stalled API response could
// block a spec until the runner kills it and skips deferred cleanup.
const apiRequestTimeout = 30 * time.Second

var (
	clusterVersionGVR = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusterversions"}
	crdGVR            = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)

// The Ingress-to-Route cases in this file were migrated from
// openshift-tests-private/test/extended/router/ingress.go. They follow the
// same structure as ingress_operator.go: each case creates and cleans up its
// own namespace and fixtures, documents its steps with g.By, and carries its
// OCP case ID as a Ginkgo label.
var _ = g.Describe("[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_Ingress", func() {
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
		restConfig.Timeout = apiRequestTimeout
		kubeClient, err = kubernetes.NewForConfig(restConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kubernetes client")
		dynamicClient, err = dynamic.NewForConfig(restConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create dynamic client")
	})

	// baseDomain resolves the cluster's base domain at the point of use.
	//
	// Deliberately not resolved in BeforeEach: dns.config/cluster does not
	// exist on MicroShift, so looking it up during shared setup would fail
	// every spec in this file with a 404 before its own assertions ran
	// (OCPBUGS-132070). The API-group environment selector in
	// cmd/environment_selectors.go already keeps these specs off MicroShift.
	baseDomain := func() string {
		domain, err := testutil.GetBaseDomain(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get base domain")
		return domain
	}

	// defaultRouterDomain resolves the domain the default ingresscontroller
	// uses for generated route hosts. The installer sets
	// ingress.config/cluster's spec.domain to "apps.<base domain>", but it can
	// be configured independently, so the DNS base domain is not a substitute.
	defaultRouterDomain := func() string {
		domain, err := testutil.GetIngressDomain(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get ingress domain")
		if domain != "" {
			return domain
		}
		return "apps." + baseDomain()
	}

	//Author:mjoseph
	g.It("Setting route.openshift.io/termination annotation to Edge/Passthrough/Reencrypt in ingress resource", g.Label("OCP-33960"), func() {
		const testNS = "ocp33960-test-ns"

		g.By("1. Create a test namespace, web server pod, and unsecure and secure services")
		o.Expect(testutil.CreateTestNamespace(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create namespace %s", testNS)
		defer testutil.CleanupTestNamespace(ctx, kubeClient, testNS, g.GinkgoWriter)
		o.Expect(testutil.CreateWebServerPodAndService(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create web server pod and service in namespace %s", testNS)
		_, err := kubeClient.CoreV1().Services(testNS).Create(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "service-secure", Namespace: testNS},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"name": "web-server-deploy"},
				Ports:    []corev1.ServicePort{{Name: "https", Port: 8443, TargetPort: intstr.FromInt32(8080)}},
			},
		}, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create service %q in namespace %s", "service-secure", testNS)
		o.Expect(testutil.WaitForPodReady(ctx, kubeClient, testNS, "name=web-server-deploy", testutil.DefaultTimeout)).To(o.Succeed(), "web server pod did not become ready in namespace %s", testNS)

		g.By("2. Create a TLS secret for the edge and reencrypt ingress resources")
		domain := baseDomain()
		edgeHost := "edge." + testNS + ".apps." + domain
		passthroughHost := "passthrough." + testNS + ".apps." + domain
		reencryptHost := "reencrypt." + testNS + ".apps." + domain
		randomHost := "random." + testNS + ".apps." + domain
		cert, key, err := makeTestCertificate(edgeHost, reencryptHost)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to generate test certificate")
		o.Expect(testutil.CreateTLSSecret(ctx, kubeClient, testNS, "ingress-secret", cert, key)).To(o.Succeed(), "failed to create TLS secret %q in namespace %s", "ingress-secret", testNS)

		g.By("3. Create ingress resources for edge, passthrough, reencrypt, and an unsupported termination value")
		cases := []struct {
			name            string
			host            string
			service         string
			port            int32
			termination     string
			secret          string
			wantTLS         bool
			wantTermination string
		}{
			{name: "ingress-edge", host: edgeHost, service: "service-unsecure", port: 8080, termination: "edge", secret: "ingress-secret", wantTLS: true, wantTermination: "edge"},
			{name: "ingress-passthrough", host: passthroughHost, service: "service-secure", port: 8443, termination: "passthrough", wantTLS: true, wantTermination: "passthrough"},
			{name: "ingress-reencrypt", host: reencryptHost, service: "service-secure", port: 8443, termination: "reencrypt", secret: "ingress-secret", wantTLS: true, wantTermination: "reencrypt"},
			{name: "ingress-random", host: randomHost, service: "service-unsecure", port: 8080, termination: "abcd"},
		}
		ingresses := make([]*networkingv1.Ingress, 0, len(cases))
		for _, tc := range cases {
			ing, err := createIngress(ctx, kubeClient, testNS, tc.name, tc.host, tc.service, tc.port, tc.termination, tc.secret)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create ingress %s in namespace %s", tc.name, testNS)
			ingresses = append(ingresses, ing)
		}

		g.By("4. Verify each generated route carries the expected TLS termination")
		for i, tc := range cases {
			route := waitForIngressRoute(ctx, dynamicClient, testNS, ingresses[i], testutil.DefaultTimeout)
			specTLS, found, err := unstructured.NestedMap(route.Object, "spec", "tls")
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to read TLS configuration from route %s", route.GetName())
			if !tc.wantTLS {
				o.Expect(found).To(o.BeFalse(), "route %s should not have a TLS configuration", route.GetName())
				continue
			}
			o.Expect(found).To(o.BeTrue(), "route %s should have a TLS configuration", route.GetName())
			o.Expect(specTLS["termination"]).To(o.Equal(tc.wantTermination))
		}
	})

	//Author:hongli
	g.It("use IngressClass controller for ingress-to-route", g.Label("OCP-41109"), func() {
		const testNS = "ocp41109-test-ns"

		g.By("1. Create a test namespace, web server, and an ingress with a non-matching IngressClass")
		o.Expect(testutil.CreateTestNamespace(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create namespace %s", testNS)
		defer testutil.CleanupTestNamespace(ctx, kubeClient, testNS, g.GinkgoWriter)
		o.Expect(testutil.CreateWebServerPodAndService(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create web server pod and service in namespace %s", testNS)
		o.Expect(testutil.WaitForPodReady(ctx, kubeClient, testNS, "name=web-server-deploy", testutil.DefaultTimeout)).To(o.Succeed(), "web server pod did not become ready in namespace %s", testNS)

		domain := baseDomain()
		ing, err := createIngressWithClass(ctx, kubeClient, testNS, "ingress-class-test", "class."+testNS+".apps."+domain, "service-unsecure", 8080, "", "", "does-not-match")
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create ingress %q in namespace %s", "ingress-class-test", testNS)

		g.By("2. Confirm no route is created while the IngressClass does not match")
		o.Consistently(func() (bool, error) {
			return routeOwnedByIngress(ctx, dynamicClient, testNS, ing)
		}, 30*time.Second, 5*time.Second).Should(o.BeFalse())

		g.By("3. Patch the ingress to use the default IngressClass")
		o.Expect(patchIngress(ctx, kubeClient, testNS, ing.Name, []byte(`{"spec":{"ingressClassName":"openshift-default"}}`))).To(o.Succeed(), "failed to set the IngressClass on ingress %s/%s", testNS, ing.Name)

		g.By("4. Confirm the route is created and owned by the ingress")
		route := waitForIngressRoute(ctx, dynamicClient, testNS, ing, testutil.DefaultTimeout)
		o.Expect(route.GetOwnerReferences()).To(o.ContainElement(o.HaveField("Name", ing.GetName())))

		g.By("5. Confirm the ingress address is populated with the default router domain")
		o.Expect(waitForIngressAddress(ctx, kubeClient, testNS, ing.Name, "router-default."+defaultRouterDomain())).To(o.Succeed(), "ingress %s/%s was not populated with the default router address", testNS, ing.Name)
	})

	//Author:mjoseph
	g.It("host name of the route depends on the subdomain if provided", g.Label("OCP-51148"), func() {
		const testNS = "ocp51148-test-ns"

		g.By("1. Create a test namespace, web server pod, and unsecure service")
		o.Expect(testutil.CreateTestNamespace(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create namespace %s", testNS)
		defer testutil.CleanupTestNamespace(ctx, kubeClient, testNS, g.GinkgoWriter)
		o.Expect(testutil.CreateWebServerPodAndService(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create web server pod and service in namespace %s", testNS)
		o.Expect(testutil.WaitForPodReady(ctx, kubeClient, testNS, "name=web-server-deploy", testutil.DefaultTimeout)).To(o.Succeed(), "web server pod did not become ready in namespace %s", testNS)

		domain := baseDomain()
		defaultDomain := defaultRouterDomain()
		routes := []struct {
			name      string
			subdomain string
			host      string
			wantHost  string
		}{
			{name: "foo-unsecure1", subdomain: "foo", wantHost: "foo." + defaultDomain},
			{name: "foo-unsecure2", wantHost: "foo-unsecure2-" + testNS + "." + defaultDomain},
			{name: "foo-unsecure3", subdomain: "foo", host: "man-" + testNS + ".apps." + domain, wantHost: "man-" + testNS + ".apps." + domain},
			{name: "foo-unsecure4", host: "bar-" + testNS + ".apps." + domain, wantHost: "bar-" + testNS + ".apps." + domain},
		}

		g.By("2. Create routes with and without a subdomain and host")
		for _, tc := range routes {
			spec := map[string]interface{}{
				"to": map[string]interface{}{"kind": "Service", "name": "service-unsecure"},
			}
			if tc.subdomain != "" {
				spec["subdomain"] = tc.subdomain
			}
			if tc.host != "" {
				spec["host"] = tc.host
			}
			_, err := createRoute(ctx, dynamicClient, testNS, tc.name, map[string]string{"name": "service-unsecure"}, spec)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create route %s in namespace %s", tc.name, testNS)
		}

		g.By("3. Verify each route retains the subdomain and host it was created with")
		for _, tc := range routes {
			got, err := dynamicClient.Resource(testutil.RouteGVR).Namespace(testNS).Get(ctx, tc.name, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to get route %s/%s", testNS, tc.name)
			spec, _, err := unstructured.NestedMap(got.Object, "spec")
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to read spec from route %s/%s", testNS, tc.name)
			if tc.subdomain != "" {
				o.Expect(spec).To(o.HaveKeyWithValue("subdomain", tc.subdomain))
			} else {
				o.Expect(spec).NotTo(o.HaveKey("subdomain"))
			}
			if tc.host != "" {
				o.Expect(spec).To(o.HaveKeyWithValue("host", tc.host))
			}
		}

		g.By("4. Verify the default router reports the expected host for each route")
		for _, tc := range routes {
			o.Expect(waitForRouteIngressStatus(ctx, dynamicClient, testNS, tc.name, "default", tc.wantHost)).To(o.Succeed(), "default router did not report host %s for route %s/%s", tc.wantHost, testNS, tc.name)
		}
	})

	//Author:mjoseph
	g.It("different router deployment with same route using subdomain", g.Label("OCP-51429"), func() {
		const (
			testNS = "ocp51429-test-ns"
			icName = "ocp51429"
		)

		g.By("1. Create a test namespace and web server pod")
		o.Expect(testutil.CreateTestNamespace(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create namespace %s", testNS)
		defer testutil.CleanupTestNamespace(ctx, kubeClient, testNS, g.GinkgoWriter)
		o.Expect(testutil.CreateWebServerPodAndService(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create web server pod and service in namespace %s", testNS)
		o.Expect(testutil.WaitForPodReady(ctx, kubeClient, testNS, "name=web-server-deploy", testutil.DefaultTimeout)).To(o.Succeed(), "web server pod did not become ready in namespace %s", testNS)

		g.By("2. Create a custom ingresscontroller")
		domain := baseDomain()
		defer testutil.CleanupIC(ctx, dynamicClient, icName, g.GinkgoWriter)
		o.Expect(testutil.CreateIC(ctx, dynamicClient, icName, icName+"."+domain, testutil.WithNodePort())).To(o.Succeed(), "failed to create ingresscontroller %s", icName)
		o.Expect(testutil.WaitForICAvailable(ctx, dynamicClient, icName, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not become available", icName)

		g.By("3. Create a route with a subdomain")
		_, err := createRoute(ctx, dynamicClient, testNS, "foobar-unsecure", map[string]string{"name": "service-unsecure"}, map[string]interface{}{
			"subdomain": "foobar",
			"to":        map[string]interface{}{"kind": "Service", "name": "service-unsecure"},
		})
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create route %q in namespace %s", "foobar-unsecure", testNS)

		g.By("4. Verify the default and custom routers both report the subdomain-based host")
		o.Expect(waitForRouteIngressStatus(ctx, dynamicClient, testNS, "foobar-unsecure", "default", "foobar."+defaultRouterDomain())).To(o.Succeed(), "default router did not report the subdomain-based host for route %s/%s", testNS, "foobar-unsecure")
		o.Expect(waitForRouteIngressStatus(ctx, dynamicClient, testNS, "foobar-unsecure", icName, "foobar."+icName+"."+domain)).To(o.Succeed(), "ingresscontroller %s did not report the subdomain-based host for route %s/%s", icName, testNS, "foobar-unsecure")

		g.By("5. Verify the route backend is present in the haproxy configuration of both routers")
		defaultPod, err := testutil.GetRouterPodName(ctx, kubeClient, "default")
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the default router pod")
		customPod, err := testutil.GetRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the router pod for ingresscontroller %s", icName)
		backend := "backend be_http:" + testNS + ":foobar-unsecure"
		o.Expect(eventuallyRouterConfigContains(ctx, restConfig, kubeClient, defaultPod, backend, testutil.LongTimeout)).To(o.Succeed(), "default router configuration does not contain backend %s", backend)
		o.Expect(eventuallyRouterConfigContains(ctx, restConfig, kubeClient, customPod, backend, testutil.LongTimeout)).To(o.Succeed(), "router configuration for ingresscontroller %s does not contain backend %s", icName, backend)
	})

	//Author:mjoseph
	g.It("Router deployment using different shard with same subdomain", g.Label("OCP-51437"), func() {
		const testNS = "ocp51437-test-ns"

		g.By("1. Create a test namespace and web server pod")
		o.Expect(testutil.CreateTestNamespace(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create namespace %s", testNS)
		defer testutil.CleanupTestNamespace(ctx, kubeClient, testNS, g.GinkgoWriter)
		o.Expect(testutil.CreateWebServerPodAndService(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create web server pod and service in namespace %s", testNS)
		o.Expect(testutil.WaitForPodReady(ctx, kubeClient, testNS, "name=web-server-deploy", testutil.DefaultTimeout)).To(o.Succeed(), "web server pod did not become ready in namespace %s", testNS)

		g.By("2. Create one ingresscontroller for each shard")
		domain := baseDomain()
		controllers := []struct{ name, shard string }{
			{name: "ocp51437-alpha", shard: "alpha"},
			{name: "ocp51437-beta", shard: "beta"},
		}
		routerPods := make([]string, 0, len(controllers))
		for _, controller := range controllers {
			name, shard := controller.name, controller.shard
			defer testutil.CleanupIC(ctx, dynamicClient, name, g.GinkgoWriter)
			o.Expect(testutil.CreateIC(ctx, dynamicClient, name, name+"."+domain, testutil.WithNodePort())).To(o.Succeed(), "failed to create ingresscontroller %s", name)
			o.Expect(testutil.WaitForICAvailable(ctx, dynamicClient, name, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not become available", name)
			oldPod, err := testutil.GetRouterPodName(ctx, kubeClient, name)
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the router pod for ingresscontroller %s", name)
			o.Expect(testutil.PatchICMerge(ctx, dynamicClient, name, fmt.Sprintf(`{"spec":{"routeSelector":{"matchLabels":{"shard":%q}}}}`, shard))).To(o.Succeed(), "failed to set the route selector on ingresscontroller %s", name)
			// The selector change rolls the router deployment; read the
			// configuration from the replacement pod, not the one that is
			// terminating.
			newPod, err := testutil.WaitForNewRouterPod(ctx, kubeClient, name, oldPod, testutil.LongTimeout)
			o.Expect(err).NotTo(o.HaveOccurred(), "ingresscontroller %s did not get a replacement router pod", name)
			o.Expect(testutil.WaitForICAvailable(ctx, dynamicClient, name, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not become available after the route selector change", name)
			routerPods = append(routerPods, newPod)
		}

		g.By("3. Create a route in the alpha shard with a subdomain")
		_, err := createRoute(ctx, dynamicClient, testNS, "bar-unsecure", map[string]string{"shard": "alpha"}, map[string]interface{}{
			"subdomain": "bar",
			"to":        map[string]interface{}{"kind": "Service", "name": "service-unsecure"},
		})
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create route %q in namespace %s", "bar-unsecure", testNS)

		alphaPod, betaPod := routerPods[0], routerPods[1]
		backend := "backend be_http:" + testNS + ":bar-unsecure"

		g.By("4. Verify the default and alpha routers use the subdomain-based host")
		o.Expect(waitForRouteIngressStatus(ctx, dynamicClient, testNS, "bar-unsecure", "default", "bar."+defaultRouterDomain())).To(o.Succeed(), "default router did not report the subdomain-based host for route %s/%s", testNS, "bar-unsecure")
		o.Expect(waitForRouteIngressStatus(ctx, dynamicClient, testNS, "bar-unsecure", controllers[0].name, "bar."+controllers[0].name+"."+domain)).To(o.Succeed(), "ingresscontroller %s did not report the subdomain-based host for route %s/%s", controllers[0].name, testNS, "bar-unsecure")
		o.Expect(eventuallyRouterConfigContains(ctx, restConfig, kubeClient, alphaPod, backend, testutil.LongTimeout)).To(o.Succeed(), "alpha router configuration does not contain backend %s", backend)

		g.By("5. Move the route to the beta shard and verify the beta router takes it over")
		o.Expect(testutil.LabelRoute(ctx, dynamicClient, testNS, "bar-unsecure", "shard", "beta")).To(o.Succeed(), "failed to label route %s/%s for the beta shard", testNS, "bar-unsecure")
		o.Expect(waitForRouteIngressStatus(ctx, dynamicClient, testNS, "bar-unsecure", controllers[1].name, "bar."+controllers[1].name+"."+domain)).To(o.Succeed(), "ingresscontroller %s did not report the subdomain-based host for route %s/%s", controllers[1].name, testNS, "bar-unsecure")
		o.Expect(eventuallyRouterConfigContains(ctx, restConfig, kubeClient, betaPod, backend, testutil.LongTimeout)).To(o.Succeed(), "beta router configuration does not contain backend %s", backend)

		g.By("6. Verify the alpha router drops the backend it no longer selects")
		o.Expect(eventuallyRouterConfigAbsent(ctx, restConfig, kubeClient, alphaPod, backend, testutil.LongTimeout)).To(o.Succeed(),
			"alpha router still serves backend %s after the route moved to the beta shard", backend)
	})

	//Author:shudili
	g.It("Deletion of default router service is recovered [Disruptive]", g.Label("OCP-56228"), func() {
		g.By("1. Confirm the default router service exists")
		svc, err := kubeClient.CoreV1().Services(testutil.IngressNamespace).Get(ctx, "router-default", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			g.Skip("router-default Service is not present")
		}
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get service %s/%s", testutil.IngressNamespace, "router-default")
		originalUID := svc.UID

		g.By("2. Delete the default router service")
		o.Expect(kubeClient.CoreV1().Services(testutil.IngressNamespace).Delete(ctx, svc.Name, metav1.DeleteOptions{})).To(o.Succeed(), "failed to delete service %s/%s", testutil.IngressNamespace, svc.Name)

		g.By("3. Wait for the operator to recreate the service")
		o.Expect(wait.PollUntilContextTimeout(ctx, testutil.PollInterval, testutil.LongTimeout, true, func(ctx context.Context) (bool, error) {
			current, err := kubeClient.CoreV1().Services(testutil.IngressNamespace).Get(ctx, svc.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return current.UID != originalUID, nil
		})).To(o.Succeed(), "the operator did not recreate service %s/%s", testutil.IngressNamespace, svc.Name)

		g.By("4. Confirm the ingress clusteroperator returns to a normal state")
		o.Expect(testutil.WaitForClusterOperatorNormal(ctx, dynamicClient, "ingress", testutil.LongTimeout)).To(o.Succeed(), "the ingress clusteroperator did not return to a normal state")
	})

	//Author:shudili
	g.It("Azure external and internal load balancer services receive IPs", g.Label("OCP-57089"), func() {
		const testNS = "ocp57089-test-ns"

		g.By("1. Confirm the cluster runs on Azure")
		platform, err := testutil.GetClusterPlatform(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get cluster platform")
		if !strings.EqualFold(platform, "azure") {
			g.Skip("Azure-only case")
		}

		g.By("2. Create a test namespace and web server pod")
		o.Expect(testutil.CreateTestNamespace(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create namespace %s", testNS)
		defer testutil.CleanupTestNamespace(ctx, kubeClient, testNS, g.GinkgoWriter)
		o.Expect(testutil.CreateWebServerPodAndService(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create web server pod and service in namespace %s", testNS)
		o.Expect(testutil.WaitForPodReady(ctx, kubeClient, testNS, "name=web-server-deploy", testutil.DefaultTimeout)).To(o.Succeed(), "web server pod did not become ready in namespace %s", testNS)

		g.By("3. Create external and internal load balancer services")
		services := []struct {
			name        string
			annotations map[string]string
		}{
			{name: "external-lb-57089"},
			{name: "internal-lb-57089", annotations: map[string]string{"service.beta.kubernetes.io/azure-load-balancer-internal": "true"}},
		}
		for _, tc := range services {
			_, err := kubeClient.CoreV1().Services(testNS).Create(ctx, &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: tc.name, Namespace: testNS, Annotations: tc.annotations},
				Spec: corev1.ServiceSpec{
					Type:     corev1.ServiceTypeLoadBalancer,
					Selector: map[string]string{"name": "web-server-deploy"},
					Ports:    []corev1.ServicePort{{Port: 443, TargetPort: intstr.FromInt32(8080)}},
				},
			}, metav1.CreateOptions{})
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create service %s in namespace %s", tc.name, testNS)
		}

		g.By("4. Confirm both load balancer services receive an IP address or hostname")
		for _, tc := range services {
			o.Expect(wait.PollUntilContextTimeout(ctx, testutil.PollInterval, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
				current, err := kubeClient.CoreV1().Services(testNS).Get(ctx, tc.name, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				for _, ingress := range current.Status.LoadBalancer.Ingress {
					if ingress.IP != "" || ingress.Hostname != "" {
						return true, nil
					}
				}
				return false, nil
			})).To(o.Succeed(), "load balancer service %s did not receive an address", tc.name)
		}
	})

	//Author:mjoseph
	g.It("component route hostname is RFC compliant [Serial]", g.Label("OCP-57370"), func() {
		const (
			consoleNamespace = "openshift-console"
			host             = "1digit9"
		)

		g.By("1. Skip when the console operator is not installed")
		if _, err := kubeClient.CoreV1().Namespaces().Get(ctx, consoleNamespace, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			g.Skip("console namespace is not present")
		} else {
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to get namespace %s", consoleNamespace)
		}

		wantHost := host + "." + defaultRouterDomain()

		g.By("2. Add a component route whose hostname starts with a digit")
		testEntry := map[string]interface{}{
			"name":      "downloads",
			"namespace": consoleNamespace,
			"hostname":  wantHost,
		}
		var (
			originalEntry interface{}
			cleanupNeeded bool
		)
		// Register cleanup before the update. The API server can commit an
		// update and still return an error, for example when the response is
		// lost; without the guard the test hostname would survive in shared
		// cluster configuration.
		defer func() {
			if !cleanupNeeded {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(context.Background(), testutil.LongTimeout)
			defer cancel()
			o.Expect(updateIngressConfig(cleanupCtx, dynamicClient, func(ingress *unstructured.Unstructured) error {
				current, _, err := unstructured.NestedSlice(ingress.Object, "spec", "componentRoutes")
				if err != nil {
					return err
				}
				// Remove only the entry this test added and put back any entry
				// it replaced, so edits by other writers survive.
				return unstructured.SetNestedSlice(ingress.Object,
					testutil.WithoutComponentRoute(current, consoleNamespace, "downloads", testEntry, originalEntry),
					"spec", "componentRoutes")
			})).To(o.Succeed(), "failed to restore spec.componentRoutes in ingress.config/cluster")
			o.Expect(waitForComponentRouteReconciled(cleanupCtx, dynamicClient, consoleNamespace, "downloads", "downloads-custom", wantHost, testutil.LongTimeout)).To(o.Succeed(),
				"component route %s/%s did not reconcile after restoring ingress.config/cluster", consoleNamespace, "downloads-custom")
		}()

		o.Expect(updateIngressConfig(ctx, dynamicClient, func(ingress *unstructured.Unstructured) error {
			current, _, err := unstructured.NestedSlice(ingress.Object, "spec", "componentRoutes")
			if err != nil {
				return err
			}
			// Record the baseline from this attempt. updateIngressConfig
			// retries with a fresh read after a conflict, and a snapshot from
			// a rejected attempt can omit another writer's accepted entry.
			originalEntry = testutil.ComponentRouteEntry(current, consoleNamespace, "downloads")
			cleanupNeeded = true
			componentRoutes := testutil.WithComponentRoute(current, consoleNamespace, "downloads", testEntry)
			return unstructured.SetNestedSlice(ingress.Object, componentRoutes, "spec", "componentRoutes")
		})).To(o.Succeed(), "failed to add the downloads component route to ingress.config/cluster")

		g.By("3. Confirm the component route is created with the requested hostname")
		o.Expect(waitForRouteHost(ctx, dynamicClient, consoleNamespace, "downloads-custom", wantHost, testutil.LongTimeout)).To(o.Succeed(),
			"route %s/%s did not get host %s", consoleNamespace, "downloads-custom", wantHost)

		g.By("4. Confirm the default router loads the component route")
		pod, err := testutil.GetRouterPodName(ctx, kubeClient, "default")
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the default router pod")
		o.Expect(eventuallyRouterConfigContains(ctx, restConfig, kubeClient, pod, "backend be_edge_http:"+consoleNamespace+":downloads-custom", testutil.LongTimeout)).To(o.Succeed(), "default router configuration does not contain the downloads-custom backend")
	})

	//Author:mjoseph
	g.It("Ingress operator is enabled as an optional component", g.Label("OCP-73619"), func() {
		g.By("1. Confirm the Ingress capability is enabled in the cluster version")
		version, err := dynamicClient.Resource(clusterVersionGVR).Get(ctx, "version", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get clusterversion version")
		capabilities, _, err := unstructured.NestedStringSlice(version.Object, "status", "capabilities", "enabledCapabilities")
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to read enabled capabilities from clusterversion version")
		o.Expect(capabilities).To(o.ContainElement("Ingress"))

		g.By("2. Confirm the ingress CRDs carry the Ingress capability annotation")
		for _, name := range []string{"dnsrecords.ingress.operator.openshift.io", "ingresscontrollers.operator.openshift.io"} {
			crd, err := dynamicClient.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to get CRD %s", name)
			annotations, _, err := unstructured.NestedStringMap(crd.Object, "metadata", "annotations")
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to read annotations from CRD %s", name)
			o.Expect(annotations["capability.openshift.io/name"]).To(o.Equal("Ingress"))
		}
	})

	//Author:shudili
	g.It("router rejects invalid certificate content on ingress [Serial]", g.Label("OCP-85280"), func() {
		const testNS = "ocp85280-test-ns"

		g.By("1. Create a test namespace, web server pod, and unsecure service")
		o.Expect(testutil.CreateTestNamespace(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create namespace %s", testNS)
		defer testutil.CleanupTestNamespace(ctx, kubeClient, testNS, g.GinkgoWriter)
		o.Expect(testutil.CreateWebServerPodAndService(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create web server pod and service in namespace %s", testNS)
		o.Expect(testutil.WaitForPodReady(ctx, kubeClient, testNS, "name=web-server-deploy", testutil.DefaultTimeout)).To(o.Succeed(), "web server pod did not become ready in namespace %s", testNS)

		g.By("2. Create a TLS secret whose certificate content is invalid")
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to generate RSA private key")
		privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		o.Expect(testutil.CreateTLSSecret(ctx, kubeClient, testNS, "bad-tls", []byte("not-a-certificate"), privateKey)).To(o.Succeed(), "failed to create TLS secret %q in namespace %s", "bad-tls", testNS)

		g.By("3. Create an ingress that references the invalid secret")
		domain := baseDomain()
		host := "invalid." + testNS + ".apps." + domain
		ing, err := createIngress(ctx, kubeClient, testNS, "invalid-cert", host, "service-unsecure", 8080, "", "bad-tls")
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create ingress %q in namespace %s", "invalid-cert", testNS)

		g.By("4. Confirm the generated route is rejected with ExtendedValidationFailed")
		route := waitForIngressRoute(ctx, dynamicClient, testNS, ing, testutil.DefaultTimeout)
		o.Expect(waitForRouteStatusContains(ctx, dynamicClient, testNS, route.GetName(), "ExtendedValidationFailed")).To(o.Succeed(), "route %s/%s was not rejected with ExtendedValidationFailed", testNS, route.GetName())
	})

	//Author:shudili
	g.It("Propagate ingress labels to generated routes", g.Label("OCP-85950"), func() {
		const testNS = "ocp85950-test-ns"

		g.By("1. Create a test namespace, web server pod, and ingress")
		o.Expect(testutil.CreateTestNamespace(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create namespace %s", testNS)
		defer testutil.CleanupTestNamespace(ctx, kubeClient, testNS, g.GinkgoWriter)
		o.Expect(testutil.CreateWebServerPodAndService(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create web server pod and service in namespace %s", testNS)
		o.Expect(testutil.WaitForPodReady(ctx, kubeClient, testNS, "name=web-server-deploy", testutil.DefaultTimeout)).To(o.Succeed(), "web server pod did not become ready in namespace %s", testNS)
		domain := baseDomain()
		ing, err := createIngress(ctx, kubeClient, testNS, "label-ingress", "labels."+testNS+".apps."+domain, "service-unsecure", 8080, "", "")
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create ingress %q in namespace %s", "label-ingress", testNS)
		route := waitForIngressRoute(ctx, dynamicClient, testNS, ing, testutil.DefaultTimeout)

		g.By("2. Enable label reconciliation and add a label to the ingress")
		o.Expect(patchIngress(ctx, kubeClient, testNS, ing.Name,
			[]byte(`{"metadata":{"annotations":{"route.openshift.io/reconcile-labels":"true"},"labels":{"shard":"alpha"}}}`))).To(o.Succeed(), "failed to enable label reconciliation on ingress %s/%s", testNS, ing.Name)
		o.Expect(waitForRouteLabel(ctx, dynamicClient, testNS, route.GetName(), "shard", "alpha")).To(o.Succeed(), "route %s/%s did not get label shard=alpha", testNS, route.GetName())
		o.Expect(waitForRouteAnnotation(ctx, dynamicClient, testNS, route.GetName(), "route.openshift.io/reconcile-labels", "true")).To(o.Succeed(), "route %s/%s did not get annotation route.openshift.io/reconcile-labels=true", testNS, route.GetName())

		g.By("3. Set an invalid reconciliation value and confirm new labels are not propagated")
		o.Expect(patchIngress(ctx, kubeClient, testNS, ing.Name,
			[]byte(`{"metadata":{"annotations":{"route.openshift.io/reconcile-labels":"ffalse"},"labels":{"custom":"internal"}}}`))).To(o.Succeed(), "failed to set an invalid label reconciliation value on ingress %s/%s", testNS, ing.Name)
		o.Expect(waitForRouteAnnotation(ctx, dynamicClient, testNS, route.GetName(), "route.openshift.io/reconcile-labels", "ffalse")).To(o.Succeed(), "route %s/%s did not get annotation route.openshift.io/reconcile-labels=ffalse", testNS, route.GetName())
		o.Consistently(func() (bool, error) {
			current, err := dynamicClient.Resource(testutil.RouteGVR).Namespace(testNS).Get(ctx, route.GetName(), metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			_, found := current.GetLabels()["custom"]
			return found, nil
		}, 10*time.Second, 2*time.Second).Should(o.BeFalse())

		g.By("4. Update the label and re-enable reconciliation, and confirm all labels are propagated")
		o.Expect(patchIngress(ctx, kubeClient, testNS, ing.Name,
			[]byte(`{"metadata":{"annotations":{"route.openshift.io/reconcile-labels":"true"},"labels":{"shard":"beta","custom":"internal"}}}`))).To(o.Succeed(), "failed to restore label reconciliation and update labels on ingress %s/%s", testNS, ing.Name)
		o.Expect(waitForRouteLabel(ctx, dynamicClient, testNS, route.GetName(), "shard", "beta")).To(o.Succeed(), "route %s/%s did not get label shard=beta", testNS, route.GetName())
		o.Expect(waitForRouteLabel(ctx, dynamicClient, testNS, route.GetName(), "custom", "internal")).To(o.Succeed(), "route %s/%s did not get label custom=internal", testNS, route.GetName())

		g.By("5. Add a label with uppercase characters and confirm it is propagated")
		o.Expect(patchIngress(ctx, kubeClient, testNS, ing.Name,
			[]byte(`{"metadata":{"labels":{"Market_01":"No1-In-APEC"}}}`))).To(o.Succeed(), "failed to add the Market_01 label to ingress %s/%s", testNS, ing.Name)
		o.Expect(waitForRouteLabel(ctx, dynamicClient, testNS, route.GetName(), "Market_01", "No1-In-APEC")).To(o.Succeed(), "route %s/%s did not get label Market_01=No1-In-APEC", testNS, route.GetName())

		g.By("6. Remove a label from the ingress and confirm it is removed from the route")
		o.Expect(patchIngress(ctx, kubeClient, testNS, ing.Name,
			[]byte(`{"metadata":{"labels":{"custom":null}}}`))).To(o.Succeed(), "failed to remove the custom label from ingress %s/%s", testNS, ing.Name)
		o.Expect(waitForRouteLabelAbsent(ctx, dynamicClient, testNS, route.GetName(), "custom")).To(o.Succeed(), "route %s/%s still has label custom after it was removed from the ingress", testNS, route.GetName())

		g.By("7. Delete the route and confirm it is recreated with the ingress labels")
		originalUID := route.GetUID()
		o.Expect(dynamicClient.Resource(testutil.RouteGVR).Namespace(testNS).Delete(ctx, route.GetName(), metav1.DeleteOptions{})).To(o.Succeed(), "failed to delete route %s/%s", testNS, route.GetName())
		recreated := waitForIngressRouteRecreated(ctx, dynamicClient, testNS, ing, originalUID)
		o.Expect(waitForRouteLabel(ctx, dynamicClient, testNS, recreated.GetName(), "shard", "beta")).To(o.Succeed(), "recreated route %s/%s did not get label shard=beta", testNS, recreated.GetName())
		o.Expect(waitForRouteLabel(ctx, dynamicClient, testNS, recreated.GetName(), "Market_01", "No1-In-APEC")).To(o.Succeed(), "recreated route %s/%s did not get label Market_01=No1-In-APEC", testNS, recreated.GetName())
	})

	//Author:mjoseph
	g.It("destination-ca-certificate-secret annotation uses an Opaque destination CA certificate", g.Label("OCP-50842"), func() {
		suffix := utilrand.String(5)
		g.By("1. Create an Ingress with an Opaque destination CA certificate secret")
		runDestinationCACertificateCase(ctx, kubeClient, dynamicClient, restConfig, baseDomain(), "ocp50842-"+suffix, "ingress-dca-opq-"+suffix, corev1.SecretTypeOpaque)
	})

	//Author:mjoseph
	g.It("destination-ca-certificate-secret annotation uses a TLS destination CA certificate", g.Label("OCP-51980"), func() {
		suffix := utilrand.String(5)
		g.By("1. Create an Ingress with a TLS destination CA certificate secret")
		runDestinationCACertificateCase(ctx, kubeClient, dynamicClient, restConfig, baseDomain(), "ocp51980-"+suffix, "ingress-dca-tls-"+suffix, corev1.SecretTypeTLS)
	})
})

// runDestinationCACertificateCase verifies that an Ingress annotation can
// select a destination CA secret of either Opaque or kubernetes.io/tls type.
func runDestinationCACertificateCase(
	ctx context.Context,
	kubeClient kubernetes.Interface,
	dynamicClient dynamic.Interface,
	restConfig *rest.Config,
	baseDomain, testNS, ingressName string,
	secretType corev1.SecretType,
) {
	icName := "destination-ca-" + ingressName

	g.By("2. Create a test namespace, CA, backend certificate, and TLS backend")
	o.Expect(testutil.CreateTestNamespace(ctx, kubeClient, testNS)).To(o.Succeed(), "failed to create namespace %s", testNS)
	defer testutil.CleanupTestNamespace(ctx, kubeClient, testNS, g.GinkgoWriter)

	caPEM, caKeyPEM, certPEM, keyPEM, err := testutil.MakeTestCAAndCertificateWithKey(
		"service-secure", "service-secure", "service-secure."+testNS+".svc")
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create the destination CA and backend certificate")
	o.Expect(testutil.CreateTLSSecret(ctx, kubeClient, testNS, "backend-cert", certPEM, keyPEM)).To(o.Succeed(), "failed to create backend TLS secret in namespace %s", testNS)
	o.Expect(createTLSServerAndService(ctx, kubeClient, testNS, "backend-cert")).To(o.Succeed(), "failed to create TLS backend in namespace %s", testNS)

	g.By("3. Create the destination CA secret")
	destinationSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "service-secret", Namespace: testNS},
		Type:       secretType,
		Data:       map[string][]byte{"tls.crt": caPEM},
	}
	if secretType == corev1.SecretTypeTLS {
		destinationSecret.Data["tls.key"] = caKeyPEM
	}
	_, err = kubeClient.CoreV1().Secrets(testNS).Create(ctx, destinationSecret, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create destination CA secret in namespace %s", testNS)

	g.By("4. Create a custom ingresscontroller")
	icDomain := icName + "." + baseDomain
	defer testutil.CleanupIC(ctx, dynamicClient, icName, g.GinkgoWriter)
	o.Expect(testutil.CreateIC(ctx, dynamicClient, icName, icDomain, testutil.WithNodePort())).To(o.Succeed(), "failed to create ingresscontroller %s", icName)
	o.Expect(testutil.WaitForICAvailable(ctx, dynamicClient, icName, testutil.LongTimeout)).To(o.Succeed(), "ingresscontroller %s did not become available", icName)
	o.Expect(testutil.WaitForDeployGeneration(ctx, kubeClient, icName, 1, testutil.DefaultTimeout)).To(o.Succeed(), "ingresscontroller %s did not reach deployment generation 1", icName)
	routerPod, err := testutil.GetRouterPodName(ctx, kubeClient, icName)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get router pod for ingresscontroller %s", icName)

	g.By("5. Create a reencrypt Ingress that references the destination CA secret")
	host := "service-secure-" + testNS + "." + icDomain
	ingress := newDestinationCAIngress(testNS, ingressName, host, "service-secure", 8443)
	_, err = kubeClient.NetworkingV1().Ingresses(testNS).Create(ctx, ingress, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create ingress %s/%s", testNS, ingressName)
	// The route controller can lag while reconciling multiple ingress
	// controllers on a busy cluster; use the same long timeout as the later
	// router-config and reachability checks.
	route := waitForIngressRoute(ctx, dynamicClient, testNS, ingress, testutil.LongTimeout)

	g.By("6. Verify the generated route carries the destination CA annotation and expected host")
	annotations, _, err := unstructured.NestedStringMap(route.Object, "metadata", "annotations")
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to read annotations from route %s/%s", testNS, route.GetName())
	o.Expect(annotations).To(o.HaveKeyWithValue("route.openshift.io/destination-ca-certificate-secret", "service-secret"))
	routeHost, _, err := unstructured.NestedString(route.Object, "spec", "host")
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to read host from route %s/%s", testNS, route.GetName())
	o.Expect(routeHost).To(o.Equal(host))
	o.Expect(waitForRouteIngressStatus(ctx, dynamicClient, testNS, route.GetName(), icName, host)).To(o.Succeed(), "ingresscontroller %s did not report the expected host", icName)

	g.By("7. Verify the custom router loads the route backend")
	backend := "backend be_secure:" + testNS + ":" + route.GetName()
	o.Expect(eventuallyRouterConfigContains(ctx, restConfig, kubeClient, routerPod, backend, testutil.LongTimeout)).To(o.Succeed(), "router configuration does not contain backend %s", backend)

	g.By("8. Verify the route is reachable through the custom router")
	curlPod := createCurlPod(ctx, kubeClient, testNS, ingressName+"-curl")
	o.Expect(testutil.WaitForPodReady(ctx, kubeClient, testNS, "name="+curlPod.Name, testutil.DefaultTimeout)).To(o.Succeed(), "curl pod %s/%s did not become ready", testNS, curlPod.Name)
	routerAddress := routerPodAddress(ctx, kubeClient, routerPod)
	var status string
	o.Expect(wait.PollUntilContextTimeout(ctx, testutil.PollInterval, testutil.LongTimeout, true, func(ctx context.Context) (bool, error) {
		current, err := curlRouteStatus(ctx, restConfig, kubeClient, testNS, curlPod.Name, host, routerAddress)
		if err != nil {
			return false, err
		}
		status = current
		return status == "200", nil
	})).To(o.Succeed(), "route %s did not become reachable through ingresscontroller %s; last HTTP status: %s", host, icName, status)
}

// newDestinationCAIngress builds a reencrypt Ingress whose generated Route
// uses the service-secret destination CA secret.
func newDestinationCAIngress(namespace, name, host, service string, port int32) *networkingv1.Ingress {
	ingress := newIngress(namespace, name, host, service, port, "", "", "openshift-default")
	// The QE fixture and the documented destination-CA Ingress examples leave
	// ingressClassName unset. Keep that behavior exactly; explicitly selecting
	// openshift-default is not required for Ingress-to-Route conversion and
	// diverges from the case being migrated.
	ingress.Spec.IngressClassName = nil
	ingress.Annotations["route.openshift.io/destination-ca-certificate-secret"] = "service-secret"
	ingress.Annotations["route.openshift.io/termination"] = "reencrypt"
	return ingress
}

// createTLSServerAndService creates an openssl TLS backend and a service named
// service-secure. The backend certificate is supplied by secretName.
func createTLSServerAndService(ctx context.Context, kc kubernetes.Interface, namespace, secretName string) error {
	labels := map[string]string{"name": "tls-web-server"}
	allowEscalation := false
	runAsNonRoot := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "tls-web-server", Namespace: namespace, Labels: labels},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: &runAsNonRoot,
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Containers: []corev1.Container{{
				Name:  "tls-web-server",
				Image: testutil.WebServerImage,
				Command: []string{
					"openssl", "s_server", "-accept", "8443",
					"-cert", "/certs/tls.crt", "-key", "/certs/tls.key", "-www",
				},
				Ports: []corev1.ContainerPort{{ContainerPort: 8443, Name: "https"}},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "backend-cert",
					MountPath: "/certs",
					ReadOnly:  true,
				}},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &allowEscalation,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
			Volumes: []corev1.Volume{{
				Name: "backend-cert",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: secretName},
				},
			}},
		},
	}
	if _, err := kc.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return err
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "service-secure", Namespace: namespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name:       "https",
				Port:       8443,
				TargetPort: intstr.FromInt32(8443),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	if _, err := kc.CoreV1().Services(namespace).Create(ctx, service, metav1.CreateOptions{}); err != nil {
		return err
	}
	return testutil.WaitForPodReady(ctx, kc, namespace, "name=tls-web-server", testutil.DefaultTimeout)
}

// createCurlPod creates a long-lived tools pod used to execute curl commands.
func createCurlPod(ctx context.Context, kc kubernetes.Interface, namespace, name string) *corev1.Pod {
	allowEscalation := false
	runAsNonRoot := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"name": name},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: &runAsNonRoot,
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Containers: []corev1.Container{{
				Name:    "curl",
				Image:   testutil.WebServerImage,
				Command: []string{"sleep", "600"},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &allowEscalation,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	_, err := kc.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create curl pod %s/%s", namespace, name)
	return pod
}

// routerPodAddress returns the primary IP address of a router pod.
func routerPodAddress(ctx context.Context, kc kubernetes.Interface, podName string) string {
	pod, err := kc.CoreV1().Pods(testutil.IngressNamespace).Get(ctx, podName, metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get router pod %s/%s", testutil.IngressNamespace, podName)
	o.Expect(pod.Status.PodIP).NotTo(o.BeEmpty(), "router pod %s has no IP address", podName)
	return pod.Status.PodIP
}

// curlRouteStatus curls a route through a router pod address and returns the
// HTTP status emitted by curl.
func curlRouteStatus(ctx context.Context, restConfig *rest.Config, kc kubernetes.Interface, namespace, podName, host, routerAddress string) (string, error) {
	command := []string{
		"curl", "-sk", "-o", "/dev/null", "-w", "%{http_code}",
		"--resolve", curlResolve(host, 443, routerAddress), "https://" + host,
	}
	stdout, _, err := testutil.ExecInPod(ctx, restConfig, kc, namespace, podName, "curl", command)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout), nil
}

// curlResolve formats a curl --resolve value and brackets IPv6 addresses.
func curlResolve(host string, port int, address string) string {
	if strings.Contains(address, ":") {
		address = "[" + address + "]"
	}
	return fmt.Sprintf("%s:%d:%s", host, port, address)
}

// createIngress creates an Ingress in the default IngressClass.
func createIngress(ctx context.Context, kc kubernetes.Interface, namespace, name, host, service string, port int32, termination, tlsSecret string) (*networkingv1.Ingress, error) {
	return createIngressWithClass(ctx, kc, namespace, name, host, service, port, termination, tlsSecret, "openshift-default")
}

// createIngressWithClass creates an Ingress in the given IngressClass.
func createIngressWithClass(ctx context.Context, kc kubernetes.Interface, namespace, name, host, service string, port int32, termination, tlsSecret, className string) (*networkingv1.Ingress, error) {
	return kc.NetworkingV1().Ingresses(namespace).Create(ctx, newIngress(namespace, name, host, service, port, termination, tlsSecret, className), metav1.CreateOptions{})
}

// newIngress builds an Ingress with a single rule and optional TLS
// termination.
func newIngress(namespace, name, host, service string, port int32, termination, tlsSecret, className string) *networkingv1.Ingress {
	path := "/"
	pathType := networkingv1.PathTypePrefix
	if termination == "passthrough" {
		// Passthrough routes cannot have a path. ImplementationSpecific allows
		// an empty Ingress path, which converts to a pathless Route.
		path = ""
		pathType = networkingv1.PathTypeImplementationSpecific
	}

	annotations := map[string]string{}
	if termination != "" {
		annotations["route.openshift.io/termination"] = termination
	}
	var tls []networkingv1.IngressTLS
	if tlsSecret != "" {
		tls = []networkingv1.IngressTLS{{Hosts: []string{host}, SecretName: tlsSecret}}
	}

	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: annotations},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &className,
			TLS:              tls,
			Rules: []networkingv1.IngressRule{{
				Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     path,
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: service,
									Port: networkingv1.ServiceBackendPort{Number: port},
								},
							},
						}},
					},
				},
			}},
		},
	}
}

func patchIngress(ctx context.Context, kc kubernetes.Interface, namespace, name string, patch []byte) error {
	_, err := kc.NetworkingV1().Ingresses(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func createRoute(ctx context.Context, dc dynamic.Interface, namespace, name string, labels map[string]string, spec map[string]interface{}) (*unstructured.Unstructured, error) {
	metadata := map[string]interface{}{"name": name}
	if len(labels) > 0 {
		labelMap := make(map[string]interface{}, len(labels))
		for key, value := range labels {
			labelMap[key] = value
		}
		metadata["labels"] = labelMap
	}
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "route.openshift.io/v1",
		"kind":       "Route",
		"metadata":   metadata,
		"spec":       spec,
	}}
	return dc.Resource(testutil.RouteGVR).Namespace(namespace).Create(ctx, route, metav1.CreateOptions{})
}

func routeOwnedByIngress(ctx context.Context, dc dynamic.Interface, namespace string, ingress *networkingv1.Ingress) (bool, error) {
	routes, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	for i := range routes.Items {
		for _, owner := range routes.Items[i].GetOwnerReferences() {
			if owner.Kind == "Ingress" && owner.Name == ingress.Name && owner.UID == ingress.UID {
				return true, nil
			}
		}
	}
	return false, nil
}

func waitForIngressRoute(ctx context.Context, dc dynamic.Interface, namespace string, ingress *networkingv1.Ingress, timeout time.Duration) *unstructured.Unstructured {
	var result *unstructured.Unstructured
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		routes, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for i := range routes.Items {
			for _, owner := range routes.Items[i].GetOwnerReferences() {
				if owner.Kind == "Ingress" && owner.Name == ingress.Name && owner.UID == ingress.UID {
					result = &routes.Items[i]
					return true, nil
				}
			}
		}
		return false, nil
	})
	o.Expect(err).NotTo(o.HaveOccurred(), "route owned by Ingress %s/%s was not created", namespace, ingress.Name)
	return result
}

// waitForIngressRouteRecreated waits for a route owned by ingress whose UID is
// not oldUID. The ingress-to-route controller appends a generated suffix to
// route names, so a replacement route can have a different name than the one
// that was deleted.
func waitForIngressRouteRecreated(ctx context.Context, dc dynamic.Interface, namespace string, ingress *networkingv1.Ingress, oldUID types.UID) *unstructured.Unstructured {
	var result *unstructured.Unstructured
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.LongTimeout, true, func(ctx context.Context) (bool, error) {
		routes, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for i := range routes.Items {
			if routes.Items[i].GetUID() == oldUID {
				continue
			}
			for _, owner := range routes.Items[i].GetOwnerReferences() {
				if owner.Kind == "Ingress" && owner.Name == ingress.Name && owner.UID == ingress.UID {
					result = &routes.Items[i]
					return true, nil
				}
			}
		}
		return false, nil
	})
	o.Expect(err).NotTo(o.HaveOccurred(), "route owned by Ingress %s/%s was not recreated", namespace, ingress.Name)
	return result
}

// isNonRetryableRequestError reports whether a polling callback should stop
// instead of retrying. Authorization failures will not resolve with time.
func isNonRetryableRequestError(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
}

// withLastError adds the last retryable error observed while polling to a
// timeout, so the failure reports why the wait never succeeded instead of only
// the timeout.
func withLastError(err, lastErr error) error {
	if err == nil || lastErr == nil || !wait.Interrupted(err) {
		return err
	}
	return fmt.Errorf("%w (last observed error: %v)", err, lastErr)
}

func waitForIngressAddress(ctx context.Context, kc kubernetes.Interface, namespace, name, want string) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		ingress, err := kc.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		for _, address := range ingress.Status.LoadBalancer.Ingress {
			if address.Hostname == want {
				return true, nil
			}
		}
		return false, nil
	})
	return withLastError(err, lastErr)
}

// waitForRouteHost waits until the route exists and its spec.host equals
// wantHost.
func waitForRouteHost(ctx context.Context, dc dynamic.Interface, namespace, name, wantHost string, timeout time.Duration) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		route, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		host, _, err := unstructured.NestedString(route.Object, "spec", "host")
		if err != nil {
			return false, err
		}
		return host == wantHost, nil
	})
	return withLastError(err, lastErr)
}

func waitForRouteIngressStatus(ctx context.Context, dc dynamic.Interface, namespace, name, routerName, wantHost string) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.LongTimeout, true, func(ctx context.Context) (bool, error) {
		route, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		statuses, _, err := unstructured.NestedSlice(route.Object, "status", "ingress")
		if err != nil {
			return false, err
		}
		for _, status := range statuses {
			entry, _ := status.(map[string]interface{})
			if entry["routerName"] != routerName {
				continue
			}
			if host, _ := entry["host"].(string); host == wantHost {
				return true, nil
			}
		}
		return false, nil
	})
	return withLastError(err, lastErr)
}

func waitForRouteStatusContains(ctx context.Context, dc dynamic.Interface, namespace, name, want string) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.LongTimeout, true, func(ctx context.Context) (bool, error) {
		route, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		statuses, _, err := unstructured.NestedSlice(route.Object, "status", "ingress")
		if err != nil {
			return false, err
		}
		return strings.Contains(fmt.Sprint(statuses), want), nil
	})
	return withLastError(err, lastErr)
}

func waitForRouteLabel(ctx context.Context, dc dynamic.Interface, namespace, name, key, value string) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		route, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		return route.GetLabels()[key] == value, nil
	})
	return withLastError(err, lastErr)
}

func waitForRouteLabelAbsent(ctx context.Context, dc dynamic.Interface, namespace, name, key string) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		route, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		_, found := route.GetLabels()[key]
		return !found, nil
	})
	return withLastError(err, lastErr)
}

func waitForRouteAnnotation(ctx context.Context, dc dynamic.Interface, namespace, name, key, value string) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		route, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		return route.GetAnnotations()[key] == value, nil
	})
	return withLastError(err, lastErr)
}

// waitForComponentRouteReconciled waits until the route matches the current
// componentRoutes entry for componentName. Re-reading the entry on every poll
// keeps the expectation current when another writer changes it. When the entry
// is gone, the route must disappear or stop using testHost.
func waitForComponentRouteReconciled(ctx context.Context, dc dynamic.Interface, namespace, componentName, routeName, testHost string, timeout time.Duration) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		config, err := dc.Resource(testutil.IngressConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		componentRoutes, _, err := unstructured.NestedSlice(config.Object, "spec", "componentRoutes")
		if err != nil {
			return false, err
		}
		expectedHost := ""
		if entry, ok := testutil.ComponentRouteEntry(componentRoutes, namespace, componentName).(map[string]interface{}); ok {
			expectedHost, _ = entry["hostname"].(string)
		}

		route, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, routeName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return expectedHost == "", nil
		}
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		host, _, err := unstructured.NestedString(route.Object, "spec", "host")
		if err != nil {
			return false, err
		}
		if expectedHost != "" {
			return host == expectedHost, nil
		}
		return host != testHost, nil
	})
	return withLastError(err, lastErr)
}

// updateIngressConfig retries a read-modify-write of ingress.config/cluster on
// resource-version conflicts. The API server rejects an update that races the
// ingress operator, which reconciles the same object.
func updateIngressConfig(ctx context.Context, dc dynamic.Interface, mutate func(*unstructured.Unstructured) error) error {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		ingress, err := dc.Resource(testutil.IngressConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := mutate(ingress); err != nil {
			return err
		}
		if _, err := dc.Resource(testutil.IngressConfigGVR).Update(ctx, ingress, metav1.UpdateOptions{}); err != nil {
			if !apierrors.IsConflict(err) {
				return err
			}
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		return nil
	}
	return lastErr
}

func eventuallyRouterConfigContains(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName, want string, timeout time.Duration) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, testutil.PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		output, err := testutil.ExecInRouterPod(ctx, restCfg, kc, podName, "cat haproxy.config")
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		return strings.Contains(output, want), nil
	})
	return withLastError(err, lastErr)
}

// eventuallyRouterConfigAbsent waits until the router pod's configuration no
// longer contains want.
func eventuallyRouterConfigAbsent(ctx context.Context, restCfg *rest.Config, kc kubernetes.Interface, podName, want string, timeout time.Duration) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, testutil.PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		output, err := testutil.ExecInRouterPod(ctx, restCfg, kc, podName, "cat haproxy.config")
		if err != nil {
			if isNonRetryableRequestError(err) {
				return false, err
			}
			lastErr = err
			return false, nil
		}
		return !strings.Contains(output, want), nil
	})
	return withLastError(err, lastErr)
}

func makeTestCertificate(hosts ...string) ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "qe-router-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              hosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return cert, privateKey, nil
}
