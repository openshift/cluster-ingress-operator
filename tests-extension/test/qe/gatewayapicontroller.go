package qe

import (
	"context"
	"fmt"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	testutil "github.com/openshift/cluster-ingress-operator/tests-extension/test"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	gatewayAPIControllerName = "openshift.io/gateway-controller/v1"
	gatewayOperandNamespace  = "openshift-ingress"
	gatewayOperatorNamespace = "openshift-ingress-operator"
)

var (
	gwapiGatewayClassGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
	gwapiGatewayGVR      = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	gwapiHTTPRouteGVR    = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	gwapiIngressCRGVR    = schema.GroupVersionResource{Group: "operator.openshift.io", Version: "v1", Resource: "ingresscontrollers"}
	gwapiInfraGVR        = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "infrastructures"}
)

// The Gateway API cases in this file were migrated from
// openshift-tests-private/test/extended/router/gatewayapi.go. Cases already
// covered by origin OTE were not duplicated: OCP-83185 by
// origin/test/extended/router/gatewayapi_upgrade.go and OCP-88552 by
// origin/test/extended/router/gatewayapicontroller.go (LB, Service, and
// DNSRecord creation and recreation). Each case carries its OCP ID as a label.
var _ = g.Describe("[sig-network-edge][OTP][Level0][Feature:Router] Network_Edge Component_GatewayAPI", func() {
	var (
		ctx        context.Context
		kc         kubernetes.Interface
		dc         dynamic.Interface
		restConfig *rest.Config
	)

	g.BeforeEach(func() {
		ctx = context.Background()
		var err error
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		restConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to build REST config from KUBECONFIG")
		kc, err = kubernetes.NewForConfig(restConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kubernetes client")
		dc, err = dynamic.NewForConfig(restConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create dynamic client")
		skipOnUnsupportedGatewayPlatform(ctx, dc)
	})

	//Author:iamin
	g.It("Ensure that RBAC works correctly for gatewayAPI resources for unprivileged users", g.Label("OCP-81976"), func() {
		const (
			testNS    = "ocp81976-gwapi"
			className = "gwapi-ocp81976"
			gateway   = "gateway81976"
			userSA    = "gwapi-unprivileged"
			routeName = "route81976"
		)
		baseDomain := baseDomainForGatewayAPI(ctx, dc)
		gatewayHost := "*.gwapi." + baseDomain
		routeHost := "ocp81976.gwapi." + baseDomain

		g.By("1. Create a test namespace, backend, and namespace-admin service account")
		o.Expect(testutil.CreateTestNamespace(ctx, kc, testNS)).To(o.Succeed(), "failed to create namespace %s", testNS)
		defer testutil.CleanupTestNamespace(ctx, kc, testNS, g.GinkgoWriter)
		o.Expect(testutil.CreateWebServerPodAndService(ctx, kc, testNS)).To(o.Succeed(), "failed to create web server in %s", testNS)
		createNamespaceAdminServiceAccount(ctx, kc, testNS, userSA)
		userClient := impersonatingDynamicClient(restConfig, fmt.Sprintf("system:serviceaccount:%s:%s", testNS, userSA))

		g.By("2. Create a GatewayClass and wait until it is Accepted")
		o.Expect(gwapiCreateGatewayClass(ctx, dc, className)).To(o.Succeed(), "failed to create GatewayClass %s", className)
		defer gwapiDeleteGatewayClass(ctx, dc, className)
		o.Expect(gwapiWaitGatewayClassCondition(ctx, dc, className, "Accepted", "True", 5*time.Minute)).To(o.Succeed(), "GatewayClass %s was never accepted", className)

		g.By("3. Create a Gateway as admin in the openshift-ingress namespace")
		o.Expect(gwapiCreateGateway(ctx, dc, gateway, gatewayOperandNamespace, className, gatewayHost)).To(o.Succeed(), "failed to create Gateway %s/%s", gatewayOperandNamespace, gateway)
		defer gwapiDeleteGateway(ctx, dc, gatewayOperandNamespace, gateway)
		o.Expect(gwapiWaitGatewayCondition(ctx, dc, gatewayOperandNamespace, gateway, "Programmed", "True", 2*time.Minute)).To(o.Succeed(), "Gateway %s/%s was never programmed", gatewayOperandNamespace, gateway)

		g.By("4. An unprivileged user cannot create a Gateway in openshift-ingress")
		_, err := gwapiCreateGatewayFor(ctx, userClient, "user-gateway-ingress", gatewayOperandNamespace, className, "*.user."+baseDomain)
		o.Expect(err).To(o.HaveOccurred(), "unprivileged user unexpectedly created a Gateway in %s", gatewayOperandNamespace)
		o.Expect(err.Error()).To(o.ContainSubstring(`cannot create resource "gateways"`))
		o.Expect(err.Error()).To(o.ContainSubstring(fmt.Sprintf("in the namespace %q", gatewayOperandNamespace)))

		g.By("5. A namespace admin cannot create a Gateway in its own namespace")
		_, err = gwapiCreateGatewayFor(ctx, userClient, "user-gateway", testNS, className, "*.user2."+baseDomain)
		o.Expect(err).To(o.HaveOccurred(), "namespace admin unexpectedly created a Gateway in %s", testNS)
		o.Expect(err.Error()).To(o.ContainSubstring(`cannot create resource "gateways"`))
		o.Expect(err.Error()).To(o.ContainSubstring(fmt.Sprintf("in the namespace %q", testNS)))

		g.By("6. The user can create an HTTPRoute, but it is rejected by the listener selector")
		o.Expect(gwapiCreateHTTPRoute(ctx, userClient, testNS, routeName, gateway, gatewayOperandNamespace, routeHost, "service-unsecure", 8080)).To(o.Succeed(), "failed to create HTTPRoute %s/%s", testNS, routeName)
		defer gwapiDeleteHTTPRoute(ctx, dc, testNS, routeName)
		o.Expect(gwapiWaitHTTPRouteParentCondition(ctx, dc, testNS, routeName, "Accepted", "False", "NotAllowedByListeners", 2*time.Minute)).To(o.Succeed(), "HTTPRoute %s/%s was not rejected by the listener", testNS, routeName)

		g.By("7. Label the namespace with the Gateway selector")
		ns, err := kc.CoreV1().Namespaces().Get(ctx, testNS, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		ns.Labels["app"] = "gwapi"
		_, err = kc.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to label namespace %s", testNS)

		g.By("8. The HTTPRoute is now accepted")
		o.Expect(gwapiWaitHTTPRouteParentCondition(ctx, dc, testNS, routeName, "Accepted", "True", "", 2*time.Minute)).To(o.Succeed(), "HTTPRoute %s/%s was never accepted", testNS, routeName)
	})

	//Author:iamin
	g.It("View metrics showing who is using Gateway API", g.Label("OCP-86110"), func() {
		const (
			className = "gwapi-ocp86110"
			gateway   = "gateway86110"
		)
		baseDomain := baseDomainForGatewayAPI(ctx, dc)

		g.By("1. Create a GatewayClass and wait until it is Accepted")
		o.Expect(gwapiCreateGatewayClass(ctx, dc, className)).To(o.Succeed(), "failed to create GatewayClass %s", className)
		defer gwapiDeleteGatewayClass(ctx, dc, className)
		o.Expect(gwapiWaitGatewayClassCondition(ctx, dc, className, "Accepted", "True", 5*time.Minute)).To(o.Succeed(), "GatewayClass %s was never accepted", className)

		g.By("2. Create a Gateway under the OpenShift GatewayClass")
		o.Expect(gwapiCreateGateway(ctx, dc, gateway, gatewayOperandNamespace, className, "*.gwapi."+baseDomain)).To(o.Succeed(), "failed to create Gateway %s/%s", gatewayOperandNamespace, gateway)
		defer gwapiDeleteGateway(ctx, dc, gatewayOperandNamespace, gateway)
		o.Expect(gwapiWaitGatewayCondition(ctx, dc, gatewayOperandNamespace, gateway, "Programmed", "True", 2*time.Minute)).To(o.Succeed(), "Gateway %s/%s was never programmed", gatewayOperandNamespace, gateway)

		g.By("3. Query the openshift:gateway_api_usage:count metric")
		token, err := kc.CoreV1().ServiceAccounts("openshift-monitoring").CreateToken(ctx, "prometheus-k8s", &authenticationv1.TokenRequest{}, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to request a token for prometheus-k8s")
		pod := gwapiFindPrometheusPod(ctx, kc)
		url := "https://prometheus-k8s.openshift-monitoring.svc:9091/api/v1/"
		query := "query?query=openshift:gateway_api_usage:count"
		command := []string{"curl", "-k", "-s", "-H", "Authorization: Bearer " + token.Status.Token, url + query, "--connect-timeout", "10"}

		g.By("4. Confirm the metric reports the Gateway as OpenShift-managed")
		o.Expect(wait.PollUntilContextTimeout(ctx, 10*time.Second, 5*time.Minute, true, func(context.Context) (bool, error) {
			stdout, stderr, err := testutil.ExecInPod(ctx, restConfig, kc, "openshift-monitoring", pod, "prometheus", command)
			if err != nil {
				g.GinkgoWriter.Printf("prometheus query failed: %v: %s\n", err, stderr)
				return false, nil
			}
			if !strings.Contains(stdout, `"gateway_class_type":"openshift"`) {
				g.GinkgoWriter.Printf("waiting for gateway_class_type=openshift; got %q\n", stdout)
				return false, nil
			}
			return true, nil
		})).To(o.Succeed(), "gateway_class_type=openshift never appeared in the metric")

		g.By("5. Confirm the metric value is 1")
		o.Expect(wait.PollUntilContextTimeout(ctx, 10*time.Second, 2*time.Minute, true, func(context.Context) (bool, error) {
			stdout, stderr, err := testutil.ExecInPod(ctx, restConfig, kc, "openshift-monitoring", pod, "prometheus", command)
			if err != nil {
				g.GinkgoWriter.Printf("prometheus query failed: %v: %s\n", err, stderr)
				return false, nil
			}
			if !strings.Contains(stdout, `"1"`) {
				g.GinkgoWriter.Printf("waiting for metric value 1; got %q\n", stdout)
				return false, nil
			}
			return true, nil
		})).To(o.Succeed(), "gateway usage metric never reported value 1")
	})

	//Author:mjoseph
	g.It("Verify HorizontalPodAutoscaler is configured correctly for Gateway API based on infrastructure topology", g.Label("OCP-88574"), func() {
		const (
			className = "gwapi-ocp88574"
			gateway1  = "gateway-hpa-1"
			gateway2  = "gateway-hpa-2"
		)
		baseDomain := baseDomainForGatewayAPI(ctx, dc)
		expectedMinReplicas := int32(2)
		if topology := gwapiInfrastructureTopology(ctx, dc); topology == "SingleReplica" {
			expectedMinReplicas = 1
		}

		g.By("1. Create a GatewayClass and wait until it is Accepted")
		o.Expect(gwapiCreateGatewayClass(ctx, dc, className)).To(o.Succeed(), "failed to create GatewayClass %s", className)
		defer gwapiDeleteGatewayClass(ctx, dc, className)
		o.Expect(gwapiWaitGatewayClassCondition(ctx, dc, className, "Accepted", "True", 5*time.Minute)).To(o.Succeed(), "GatewayClass %s was never accepted", className)

		g.By(fmt.Sprintf("2. Verify the istiod defaults ConfigMap uses minReplicas %d", expectedMinReplicas))
		cmName := "istio-openshift-gateway-gatewayclass-" + className
		o.Expect(wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(context.Context) (bool, error) {
			cm, err := kc.CoreV1().ConfigMaps(gatewayOperandNamespace).Get(ctx, cmName, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			hpa := cm.Data["horizontalPodAutoscaler"]
			return strings.Contains(hpa, "maxReplicas: 10") && strings.Contains(hpa, fmt.Sprintf("minReplicas: %d", expectedMinReplicas)), nil
		})).To(o.Succeed(), "ConfigMap %s/%s does not contain the expected HPA configuration", gatewayOperandNamespace, cmName)

		g.By("3. Create the first Gateway and wait until it is Programmed")
		o.Expect(gwapiCreateGateway(ctx, dc, gateway1, gatewayOperandNamespace, className, "*.gwapi1."+baseDomain)).To(o.Succeed(), "failed to create Gateway %s/%s", gatewayOperandNamespace, gateway1)
		defer gwapiDeleteGateway(ctx, dc, gatewayOperandNamespace, gateway1)
		o.Expect(gwapiWaitGatewayCondition(ctx, dc, gatewayOperandNamespace, gateway1, "Programmed", "True", 2*time.Minute)).To(o.Succeed(), "Gateway %s/%s was never programmed", gatewayOperandNamespace, gateway1)
		gwapiExpectHPA(ctx, kc, gatewayOperandNamespace, gateway1+"-"+className, expectedMinReplicas)

		g.By("4. Create the second Gateway and wait until it is Programmed")
		o.Expect(gwapiCreateGateway(ctx, dc, gateway2, gatewayOperandNamespace, className, "*.gwapi2."+baseDomain)).To(o.Succeed(), "failed to create Gateway %s/%s", gatewayOperandNamespace, gateway2)
		defer gwapiDeleteGateway(ctx, dc, gatewayOperandNamespace, gateway2)
		o.Expect(gwapiWaitGatewayCondition(ctx, dc, gatewayOperandNamespace, gateway2, "Programmed", "True", 2*time.Minute)).To(o.Succeed(), "Gateway %s/%s was never programmed", gatewayOperandNamespace, gateway2)
		gwapiExpectHPA(ctx, kc, gatewayOperandNamespace, gateway2+"-"+className, expectedMinReplicas)
	})
})

// skipOnUnsupportedGatewayPlatform mirrors the openshift-tests-private guards:
// the cases run on cloud platforms only, and a private (Internal LB scope)
// cluster never accepts the GatewayClass.
func skipOnUnsupportedGatewayPlatform(ctx context.Context, dc dynamic.Interface) {
	platform, err := testutil.GetClusterPlatform(ctx, dc)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the cluster platform")
	switch platform {
	case "aws", "azure", "gcp", "ibmcloud", "powervs":
	default:
		g.Skip(fmt.Sprintf("Gateway API cases require a cloud platform, got %q", platform))
	}

	ic, err := dc.Resource(gwapiIngressCRGVR).Namespace(gatewayOperatorNamespace).Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return
	}
	scope, _, _ := unstructured.NestedString(ic.Object, "spec", "endpointPublishingStrategy", "loadBalancer", "scope")
	if scope == "Internal" {
		g.Skip("GatewayClass is not accepted on private clusters")
	}
}

func baseDomainForGatewayAPI(ctx context.Context, dc dynamic.Interface) string {
	domain, err := testutil.GetBaseDomain(ctx, dc)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get the cluster base domain")
	return domain
}

func gwapiInfrastructureTopology(ctx context.Context, dc dynamic.Interface) string {
	infra, err := dc.Resource(gwapiInfraGVR).Get(ctx, "cluster", metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get infrastructure/cluster")
	topology, _, _ := unstructured.NestedString(infra.Object, "status", "infrastructureTopology")
	return topology
}

func gwapiGatewayClassObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "GatewayClass",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"controllerName": gatewayAPIControllerName},
	}}
}

func gwapiCreateGatewayClass(ctx context.Context, dc dynamic.Interface, name string) error {
	_, err := dc.Resource(gwapiGatewayClassGVR).Create(ctx, gwapiGatewayClassObject(name), metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func gwapiDeleteGatewayClass(ctx context.Context, dc dynamic.Interface, name string) {
	err := dc.Resource(gwapiGatewayClassGVR).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		g.GinkgoWriter.Printf("failed to delete GatewayClass %s: %v\n", name, err)
	}
}

func gwapiCreateGateway(ctx context.Context, dc dynamic.Interface, name, namespace, className, hostname string) error {
	_, err := gwapiCreateGatewayFor(ctx, dc, name, namespace, className, hostname)
	return err
}

func gwapiCreateGatewayFor(ctx context.Context, dc dynamic.Interface, name, namespace, className, hostname string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"gatewayClassName": className,
			"listeners": []any{map[string]any{
				"name":     "demo",
				"hostname": hostname,
				"port":     int64(80),
				"protocol": "HTTP",
				"allowedRoutes": map[string]any{
					"namespaces": map[string]any{
						"from":     "Selector",
						"selector": map[string]any{"matchLabels": map[string]any{"app": "gwapi"}},
					},
				},
			}},
		},
	}}
	return dc.Resource(gwapiGatewayGVR).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
}

func gwapiDeleteGateway(ctx context.Context, dc dynamic.Interface, namespace, name string) {
	err := dc.Resource(gwapiGatewayGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		g.GinkgoWriter.Printf("failed to delete Gateway %s/%s: %v\n", namespace, name, err)
	}
}

func gwapiCreateHTTPRoute(ctx context.Context, dc dynamic.Interface, namespace, name, parentName, parentNamespace, hostname, backend string, port int64) error {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"parentRefs": []any{map[string]any{"name": parentName, "namespace": parentNamespace}},
			"hostnames":  []any{hostname},
			"rules": []any{map[string]any{
				"backendRefs": []any{map[string]any{"name": backend, "port": port}},
			}},
		},
	}}
	_, err := dc.Resource(gwapiHTTPRouteGVR).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

func gwapiDeleteHTTPRoute(ctx context.Context, dc dynamic.Interface, namespace, name string) {
	err := dc.Resource(gwapiHTTPRouteGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		g.GinkgoWriter.Printf("failed to delete HTTPRoute %s/%s: %v\n", namespace, name, err)
	}
}

func gwapiWaitGatewayClassCondition(ctx context.Context, dc dynamic.Interface, name, condType, want string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(context.Context) (bool, error) {
		obj, err := dc.Resource(gwapiGatewayClassGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return gwapiConditionHasStatus(obj, condType, want), nil
	})
}

func gwapiWaitGatewayCondition(ctx context.Context, dc dynamic.Interface, namespace, name, condType, want string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(context.Context) (bool, error) {
		obj, err := dc.Resource(gwapiGatewayGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return gwapiConditionHasStatus(obj, condType, want), nil
	})
}

func gwapiConditionHasStatus(obj *unstructured.Unstructured, condType, want string) bool {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, condition := range conditions {
		entry, ok := condition.(map[string]any)
		if !ok {
			continue
		}
		if entry["type"] == condType {
			return entry["status"] == want
		}
	}
	return false
}

func gwapiWaitHTTPRouteParentCondition(ctx context.Context, dc dynamic.Interface, namespace, name, condType, wantStatus, wantReason string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(context.Context) (bool, error) {
		obj, err := dc.Resource(gwapiHTTPRouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		parents, _, _ := unstructured.NestedSlice(obj.Object, "status", "parents")
		for _, parent := range parents {
			entry, ok := parent.(map[string]any)
			if !ok {
				continue
			}
			conditions, _ := entry["conditions"].([]any)
			for _, condition := range conditions {
				c, ok := condition.(map[string]any)
				if !ok || c["type"] != condType || c["status"] != wantStatus {
					continue
				}
				if wantReason == "" || c["reason"] == wantReason {
					return true, nil
				}
			}
		}
		return false, nil
	})
}

func createNamespaceAdminServiceAccount(ctx context.Context, kc kubernetes.Interface, namespace, name string) {
	_, err := kc.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create ServiceAccount %s/%s", namespace, name)
	_, err = kc.RbacV1().RoleBindings(namespace).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-admin", Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "admin"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: namespace}},
	}, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to bind admin to ServiceAccount %s/%s", namespace, name)
}

func impersonatingDynamicClient(base *rest.Config, user string) dynamic.Interface {
	cfg := rest.CopyConfig(base)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: user}
	client, err := dynamic.NewForConfig(cfg)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create impersonating client")
	return client
}

func gwapiFindPrometheusPod(ctx context.Context, kc kubernetes.Interface) string {
	pods, err := kc.CoreV1().Pods("openshift-monitoring").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=prometheus"})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to list prometheus pods")
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name
		}
	}
	return "prometheus-k8s-0"
}

func gwapiExpectHPA(ctx context.Context, kc kubernetes.Interface, namespace, name string, minReplicas int32) {
	var hpaErr error
	o.Expect(wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, func(context.Context) (bool, error) {
		hpa, err := kc.AutoscalingV2().HorizontalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			hpaErr = err
			return false, nil
		}
		hpaErr = nil
		if hpa.Spec.MinReplicas == nil || *hpa.Spec.MinReplicas != minReplicas {
			return false, nil
		}
		if hpa.Spec.MaxReplicas != 10 || hpa.Spec.ScaleTargetRef.Name == "" {
			return false, nil
		}
		deploy, err := kc.AppsV1().Deployments(namespace).Get(ctx, hpa.Spec.ScaleTargetRef.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		for _, condition := range deploy.Status.Conditions {
			if condition.Type == "Available" && condition.Status == "True" {
				return true, nil
			}
		}
		return false, nil
	})).To(o.Succeed(), "HPA %s/%s did not converge: %v", namespace, name, hpaErr)
}
