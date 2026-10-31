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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	ingressGVR        = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	clusterVersionGVR = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusterversions"}
	crdGVR            = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)

var _ = g.Describe("[sig-network-edge][OTP][Level0] Network_Edge Component_Router", func() {
	var (
		ctx    context.Context
		kc     kubernetes.Interface
		dc     dynamic.Interface
		cfg    *rest.Config
		domain string
		ns     string
	)

	g.BeforeEach(func() {
		ctx = context.Background()
		var err error
		cfg, err = restConfigForQE()
		o.Expect(err).NotTo(o.HaveOccurred())
		kc, err = kubernetes.NewForConfig(cfg)
		o.Expect(err).NotTo(o.HaveOccurred())
		dc, err = dynamic.NewForConfig(cfg)
		o.Expect(err).NotTo(o.HaveOccurred())
		domain, err = testutil.GetBaseDomain(ctx, dc)
		o.Expect(err).NotTo(o.HaveOccurred())
		ns = "qe-router-" + strings.ToLower(randomSuffix())
		err = testutil.CreateTestNamespace(ctx, kc, ns)
		o.Expect(err).NotTo(o.HaveOccurred())
		g.DeferCleanup(testutil.DeleteTestNamespace, ctx, kc, ns)
		err = testutil.CreateWebServerPodAndService(ctx, kc, ns)
		o.Expect(err).NotTo(o.HaveOccurred())
		// Match the service ports used by the private-router fixtures. The
		// Ingress-to-Route controller needs the backend port to exist on Service.
		unsecure, err := kc.CoreV1().Services(ns).Get(ctx, "service-unsecure", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		unsecure.Spec.Ports[0].Port = 27017
		_, err = kc.CoreV1().Services(ns).Update(ctx, unsecure, metav1.UpdateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		_, err = kc.CoreV1().Services(ns).Create(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "service-secure", Namespace: ns},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"name": "web-server-deploy"},
				Ports:    []corev1.ServicePort{{Name: "https", Port: 27443, TargetPort: intstr.FromInt32(8080)}},
			},
		}, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		err = testutil.WaitForPodReady(ctx, kc, ns, "name=web-server-deploy", testutil.DefaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	//Author:mjoseph
	g.It("Setting route.openshift.io/termination annotation to Edge/Passthrough/Reencrypt in ingress resource", g.Label("OCP-33960"), func() {
		cert, key, err := makeTestCertificate()
		o.Expect(err).NotTo(o.HaveOccurred())
		_, err = kc.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ingress-secret", Namespace: ns}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}}, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		for _, tc := range []struct{ name, termination, service, port string }{
			{"edge", "edge", "service-unsecure", "27017"},
			{"passthrough", "passthrough", "service-secure", "27443"},
			{"reencrypt", "reencrypt", "service-secure", "27443"},
			{"random", "abcd", "service-unsecure", "27017"},
		} {
			host := tc.name + "." + ns + ".apps." + domain
			ing := newIngress("ingress-"+tc.name, host, tc.service, tc.port, tc.termination)
			if tc.name != "passthrough" {
				unstructured.SetNestedSlice(ing.Object, []interface{}{map[string]interface{}{"hosts": []interface{}{host}, "secretName": "ingress-secret"}}, "spec", "tls")
			}
			_, err := dc.Resource(ingressGVR).Namespace(ns).Create(ctx, ing, metav1.CreateOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())
			if tc.name == "random" {
				route := waitForIngressRoute(ctx, dc, ns, ing)
				tls, _, _ := unstructured.NestedMap(route.Object, "spec", "tls")
				o.Expect(fmt.Sprint(tls["termination"])).NotTo(o.Equal("abcd"))
				continue
			}
			route := waitForIngressRoute(ctx, dc, ns, ing)
			want := map[string]string{"edge": "edge", "passthrough": "passthrough", "reencrypt": "reencrypt"}[tc.name]
			o.Expect(route.Object["spec"]).Should(o.HaveKey("tls"))
			tls, _, _ := unstructured.NestedMap(route.Object, "spec", "tls")
			o.Expect(tls["termination"]).To(o.Equal(want))
		}
	})

	//Author:hongli
	g.It("use IngressClass controller for ingress-to-route", g.Label("OCP-41109"), func() {
		ing := newIngress("ingress-class-test", "class."+ns+".apps."+domain, "service-unsecure", "27017", "")
		_ = unstructured.SetNestedField(ing.Object, "does-not-match", "spec", "ingressClassName")
		_, err := dc.Resource(ingressGVR).Namespace(ns).Create(ctx, ing, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Consistently(func() bool {
			r, _ := dc.Resource(testutil.RouteGVR).Namespace(ns).Get(ctx, ing.GetName(), metav1.GetOptions{})
			return r == nil
		}, 30*time.Second, 5*time.Second).Should(o.BeTrue())
		err = testutil.PatchResourceMerge(ctx, dc, ingressGVR, ns, ing.GetName(), `{"spec":{"ingressClassName":"openshift-default"}}`)
		o.Expect(err).NotTo(o.HaveOccurred())
		route := waitForIngressRoute(ctx, dc, ns, ing)
		o.Expect(route.GetOwnerReferences()).To(o.ContainElement(o.HaveField("Name", ing.GetName())))
		err = waitForIngressAddress(ctx, dc, ns, ing.GetName(), "router-default.apps."+domain)
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	//Author:mjoseph
	g.It("host name of the route depends on the subdomain if provided", g.Label("OCP-51148"), func() {
		for _, tc := range []struct{ name, subdomain string }{{"with-subdomain", "foo"}, {"without-subdomain", ""}} {
			_, err := createBareRoute(ctx, dc, ns, tc.name, "service-unsecure")
			o.Expect(err).NotTo(o.HaveOccurred())
			if tc.subdomain != "" {
				patch := fmt.Sprintf(`{"spec":{"subdomain":%q}}`, tc.subdomain)
				err = testutil.PatchResourceMerge(ctx, dc, testutil.RouteGVR, ns, tc.name, patch)
				o.Expect(err).NotTo(o.HaveOccurred())
			}
			got, err := dc.Resource(testutil.RouteGVR).Namespace(ns).Get(ctx, tc.name, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())
			if tc.subdomain != "" {
				o.Expect(got.Object["spec"]).Should(o.HaveKey("subdomain"))
			} else {
				o.Expect(got.Object["spec"]).NotTo(o.HaveKey("subdomain"))
			}
		}
	})

	//Author:mjoseph
	g.It("different router deployment with same route using subdomain", g.Label("OCP-51429"), func() {
		icName := "qe51429-" + strings.ToLower(randomSuffix())
		icDomain := icName + "." + domain
		g.DeferCleanup(testutil.DeleteIC, ctx, dc, icName)
		err := testutil.CreateIC(ctx, dc, icName, icDomain, testutil.WithNodePort())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = testutil.WaitForDeployGeneration(ctx, kc, icName, 1, testutil.LongTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		route, err := createBareRoute(ctx, dc, ns, "shared-subdomain", "service-unsecure")
		o.Expect(err).NotTo(o.HaveOccurred())
		err = testutil.PatchResourceMerge(ctx, dc, testutil.RouteGVR, ns, route.GetName(), `{"spec":{"subdomain":"foobar"}}`)
		o.Expect(err).NotTo(o.HaveOccurred())
		for _, router := range []string{"default", icName} {
			if router != "default" {
				err = waitForRouteInStatus(ctx, dc, ns, "shared-subdomain", router)
				o.Expect(err).NotTo(o.HaveOccurred())
			} else {
				err = waitForRouteInStatus(ctx, dc, ns, "shared-subdomain", router)
				o.Expect(err).NotTo(o.HaveOccurred())
			}
		}
	})

	//Author:mjoseph
	g.It("Router deployment using different shard with same subdomain", g.Label("OCP-51437"), func() {
		controllers := []struct{ name, shard string }{{"qe-alpha-" + strings.ToLower(randomSuffix()), "alpha"}, {"qe-beta-" + strings.ToLower(randomSuffix()), "beta"}}
		for _, c := range controllers {
			c := c
			g.DeferCleanup(testutil.DeleteIC, ctx, dc, c.name)
			err := testutil.CreateIC(ctx, dc, c.name, c.name+"."+domain, testutil.WithNodePort())
			o.Expect(err).NotTo(o.HaveOccurred())
			err = testutil.PatchICMerge(ctx, dc, c.name, fmt.Sprintf(`{"spec":{"routeSelector":{"matchLabels":{"shard":%q}}}}`, c.shard))
			o.Expect(err).NotTo(o.HaveOccurred())
			err = testutil.WaitForDeployGeneration(ctx, kc, c.name, 2, testutil.LongTimeout)
			o.Expect(err).NotTo(o.HaveOccurred())
		}
		route, err := createBareRoute(ctx, dc, ns, "sharded-subdomain", "service-unsecure")
		o.Expect(err).NotTo(o.HaveOccurred())
		err = testutil.PatchResourceMerge(ctx, dc, testutil.RouteGVR, ns, route.GetName(), `{"spec":{"subdomain":"bar"}}`)
		o.Expect(err).NotTo(o.HaveOccurred())
		_ = testutil.LabelRoute(ctx, dc, ns, "sharded-subdomain", "shard", "alpha")
		o.Expect(waitForRouteInStatus(ctx, dc, ns, "sharded-subdomain", controllers[0].name)).To(o.Succeed())
		_ = testutil.LabelRoute(ctx, dc, ns, "sharded-subdomain", "shard", "beta")
		o.Expect(waitForRouteInStatus(ctx, dc, ns, "sharded-subdomain", controllers[1].name)).To(o.Succeed())
	})

	//Author:shudili
	g.It("Deletion of default router service is recovered [Disruptive]", g.Label("OCP-56228"), func() {
		svc, err := kc.CoreV1().Services(testutil.IngressNamespace).Get(ctx, "router-default", metav1.GetOptions{})
		if err != nil {
			g.Skip("router-default Service is not present")
		}
		originalUID := svc.UID
		err = kc.CoreV1().Services(testutil.IngressNamespace).Delete(ctx, svc.Name, metav1.DeleteOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		err = wait.PollUntilContextTimeout(ctx, 5*time.Second, testutil.LongTimeout, true, func(ctx context.Context) (bool, error) {
			current, err := kc.CoreV1().Services(testutil.IngressNamespace).Get(ctx, svc.Name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			return current.UID != originalUID, nil
		})
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	//Author:shudili
	g.It("Azure external and internal load balancer services receive IPs", g.Label("OCP-57089"), func() {
		platform, err := testutil.GetClusterPlatform(ctx, dc)
		o.Expect(err).NotTo(o.HaveOccurred())
		if !strings.EqualFold(platform, "Azure") {
			g.Skip("Azure-only case")
		}
		for _, name := range []string{"external-lb", "internal-lb"} {
			svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Selector: map[string]string{"name": "web-server-deploy"}, Ports: []corev1.ServicePort{{Port: 443, TargetPort: intstr.FromInt32(8443)}}}}
			if name == "internal-lb" {
				svc.Annotations = map[string]string{"service.beta.kubernetes.io/azure-load-balancer-internal": "true"}
			}
			_, err := kc.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())
			g.DeferCleanup(func() { _ = kc.CoreV1().Services(ns).Delete(context.Background(), name, metav1.DeleteOptions{}) })
			err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 15*time.Minute, true, func(ctx context.Context) (bool, error) {
				current, e := kc.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
				return e == nil && len(current.Status.LoadBalancer.Ingress) > 0 && (current.Status.LoadBalancer.Ingress[0].IP != "" || current.Status.LoadBalancer.Ingress[0].Hostname != ""), nil
			})
			o.Expect(err).NotTo(o.HaveOccurred())
		}
	})

	//Author:mjoseph
	g.It("component route hostname is RFC compliant [Serial]", g.Label("OCP-57370"), func() {
		if _, err := kc.CoreV1().Namespaces().Get(ctx, "openshift-console", metav1.GetOptions{}); err != nil {
			g.Skip("console namespace is not present")
		}
		ingress, err := dc.Resource(testutil.IngressConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		original, _, _ := unstructured.NestedSlice(ingress.Object, "spec", "componentRoutes")
		defer func() {
			patch := fmt.Sprintf(`{"spec":{"componentRoutes":%s}}`, testutil.ToJSON(original))
			_ = testutil.PatchClusterResourceMerge(context.Background(), dc, testutil.IngressConfigGVR, "cluster", patch)
		}()
		host := "1digit9.apps." + domain
		routes := []interface{}{map[string]interface{}{"name": "downloads", "namespace": "openshift-console", "hostname": host}}
		_ = unstructured.SetNestedSlice(ingress.Object, routes, "spec", "componentRoutes")
		_, err = dc.Resource(testutil.IngressConfigGVR).Update(ctx, ingress, metav1.UpdateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		route := waitForRoute(ctx, dc, "openshift-console", "downloads-custom")
		actualHost, _, _ := unstructured.NestedString(route.Object, "spec", "host")
		o.Expect(actualHost).To(o.Equal(host))
	})

	//Author:mjoseph
	g.It("Ingress operator is enabled as an optional component", g.Label("OCP-73619"), func() {
		version, err := dc.Resource(clusterVersionGVR).Get(ctx, "version", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		caps, _, _ := unstructured.NestedStringSlice(version.Object, "status", "capabilities", "enabledCapabilities")
		o.Expect(caps).To(o.ContainElement("Ingress"))
		for _, name := range []string{"dnsrecords.ingress.operator.openshift.io", "ingresscontrollers.operator.openshift.io"} {
			crd, err := dc.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())
			annotations, _, _ := unstructured.NestedStringMap(crd.Object, "metadata", "annotations")
			o.Expect(annotations["capability.openshift.io/name"]).To(o.Equal("Ingress"))
		}
	})

	//Author:shudili
	g.It("router rejects invalid certificate content on ingress [Serial]", g.Label("OCP-85280"), func() {
		key, _ := rsa.GenerateKey(rand.Reader, 2048)
		badDER := []byte("not-a-certificate")
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bad-tls", Namespace: ns}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: badDER, corev1.TLSPrivateKeyKey: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})}}
		_, err := kc.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		ing := newIngress("invalid-cert", "invalid."+ns+".apps."+domain, "service-unsecure", "27017", "")
		_ = unstructured.SetNestedSlice(ing.Object, []interface{}{map[string]interface{}{"hosts": []interface{}{"invalid." + ns + ".apps." + domain}, "secretName": "bad-tls"}}, "spec", "tls")
		_, err = dc.Resource(ingressGVR).Namespace(ns).Create(ctx, ing, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		route := waitForIngressRoute(ctx, dc, ns, ing)
		status, _, _ := unstructured.NestedSlice(route.Object, "status", "ingress")
		o.Expect(fmt.Sprint(status)).To(o.ContainSubstring("ExtendedValidationFailed"))
	})

	//Author:shudili
	g.It("Propagate ingress labels to generated routes", g.Label("OCP-85950"), func() {
		host := "labels." + ns + ".apps." + domain
		ing := newIngress("label-ingress", host, "service-unsecure", "27017", "")
		_, err := dc.Resource(ingressGVR).Namespace(ns).Create(ctx, ing, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		route := waitForIngressRoute(ctx, dc, ns, ing)
		_ = testutil.PatchResourceMerge(ctx, dc, ingressGVR, ns, ing.GetName(), `{"metadata":{"annotations":{"route.openshift.io/reconcile-labels":"true"},"labels":{"shard":"alpha"}}}`)
		err = waitForRouteLabel(ctx, dc, ns, route.GetName(), "shard", "alpha")
		o.Expect(err).NotTo(o.HaveOccurred())
		_ = testutil.PatchResourceMerge(ctx, dc, ingressGVR, ns, ing.GetName(), `{"metadata":{"annotations":{"route.openshift.io/reconcile-labels":"false"},"labels":{"custom":"internal"}}}`)
		time.Sleep(5 * time.Second)
		current, err := dc.Resource(testutil.RouteGVR).Namespace(ns).Get(ctx, route.GetName(), metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(current.GetLabels()).NotTo(o.HaveKey("custom"))
		_ = testutil.PatchResourceMerge(ctx, dc, ingressGVR, ns, ing.GetName(), `{"metadata":{"annotations":{"route.openshift.io/reconcile-labels":"true"},"labels":{"shard":"beta","custom":"internal"}}}`)
		err = waitForRouteLabel(ctx, dc, ns, route.GetName(), "shard", "beta")
		o.Expect(err).NotTo(o.HaveOccurred())
		_ = testutil.PatchResourceMerge(ctx, dc, ingressGVR, ns, ing.GetName(), `{"metadata":{"labels":{"custom":null}}}`)
		err = waitForRouteLabelRemoved(ctx, dc, ns, route.GetName(), "custom")
		o.Expect(err).NotTo(o.HaveOccurred())
		err = dc.Resource(testutil.RouteGVR).Namespace(ns).Delete(ctx, route.GetName(), metav1.DeleteOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		_ = waitForIngressRoute(ctx, dc, ns, ing)
	})
})

func restConfigForQE() (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

func createBareRoute(ctx context.Context, dc dynamic.Interface, namespace, name, service string) (*unstructured.Unstructured, error) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "route.openshift.io/v1", "kind": "Route", "metadata": map[string]interface{}{"name": name}, "spec": map[string]interface{}{"to": map[string]interface{}{"kind": "Service", "name": service}}}}
	return dc.Resource(testutil.RouteGVR).Namespace(namespace).Create(ctx, route, metav1.CreateOptions{})
}

func newIngress(name, host, service, port, termination string) *unstructured.Unstructured {
	annotations := map[string]interface{}{}
	if termination != "" {
		annotations["route.openshift.io/termination"] = termination
	}
	backend := map[string]interface{}{
		"service": map[string]interface{}{
			"name": service,
			"port": map[string]interface{}{"number": mustAtoi(port)},
		},
	}
	pathValue, pathType := "/", "Prefix"
	if termination == "passthrough" {
		// Passthrough Routes cannot have a path. ImplementationSpecific allows
		// an empty Ingress path, which converts to a pathless Route.
		pathValue, pathType = "", "ImplementationSpecific"
	}
	path := map[string]interface{}{"path": pathValue, "pathType": pathType, "backend": backend}
	rule := map[string]interface{}{"host": host, "http": map[string]interface{}{"paths": []interface{}{path}}}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "Ingress",
		"metadata":   map[string]interface{}{"name": name, "annotations": annotations},
		"spec":       map[string]interface{}{"ingressClassName": "openshift-default", "rules": []interface{}{rule}},
	}}
}

func waitForRoute(ctx context.Context, dc dynamic.Interface, namespace, name string) *unstructured.Unstructured {
	var result *unstructured.Unstructured
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		r, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		result = r
		return true, nil
	})
	o.Expect(err).NotTo(o.HaveOccurred(), "route %s/%s was not created", namespace, name)
	return result
}

func waitForIngressRoute(ctx context.Context, dc dynamic.Interface, namespace string, ingress *unstructured.Unstructured) *unstructured.Unstructured {
	liveIngress, err := dc.Resource(ingressGVR).Namespace(namespace).Get(ctx, ingress.GetName(), metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Ingress %s/%s was not found", namespace, ingress.GetName())
	ingressUID := liveIngress.GetUID()

	var result *unstructured.Unstructured
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		routes, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for i := range routes.Items {
			for _, owner := range routes.Items[i].GetOwnerReferences() {
				if owner.Kind == "Ingress" && owner.Name == ingress.GetName() && owner.UID == ingressUID {
					result = &routes.Items[i]
					return true, nil
				}
			}
		}
		return false, nil
	})
	o.Expect(err).NotTo(o.HaveOccurred(), "route owned by Ingress %s/%s was not created", namespace, ingress.GetName())
	return result
}

func waitForIngressAddress(ctx context.Context, dc dynamic.Interface, namespace, name, want string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		ing, err := dc.Resource(ingressGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		addresses, _, _ := unstructured.NestedSlice(ing.Object, "status", "loadBalancer", "ingress")
		for _, a := range addresses {
			m, _ := a.(map[string]interface{})
			if strings.Contains(fmt.Sprint(m["hostname"]), want) || strings.Contains(fmt.Sprint(m["ip"]), want) {
				return true, nil
			}
		}
		return false, nil
	})
}

func waitForRouteInStatus(ctx context.Context, dc dynamic.Interface, namespace, name, router string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.LongTimeout, true, func(ctx context.Context) (bool, error) {
		r, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		items, _, _ := unstructured.NestedSlice(r.Object, "status", "ingress")
		for _, item := range items {
			m, _ := item.(map[string]interface{})
			if m["routerName"] == router {
				return true, nil
			}
		}
		return false, nil
	})
}

func waitForRouteLabel(ctx context.Context, dc dynamic.Interface, namespace, name, key, value string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		r, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return r.GetLabels()[key] == value, nil
	})
}

func waitForRouteLabelRemoved(ctx context.Context, dc dynamic.Interface, namespace, name, key string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, testutil.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		r, err := dc.Resource(testutil.RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		_, ok := r.GetLabels()[key]
		return !ok, nil
	})
}

func randomSuffix() string    { b := make([]byte, 4); _, _ = rand.Read(b); return fmt.Sprintf("%x", b) }
func mustAtoi(s string) int64 { var n int64; _, _ = fmt.Sscan(s, &n); return n }

func makeTestCertificate() ([]byte, []byte, error) {
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
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return cert, privateKey, nil
}
