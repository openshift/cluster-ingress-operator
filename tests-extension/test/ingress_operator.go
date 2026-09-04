package test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var _ = g.Describe("[sig-network-edge][Feature:IngressOperator]", func() {
	var (
		ctx           context.Context
		kubeClient    kubernetes.Interface
		dynamicClient dynamic.Interface
		restConfig    *rest.Config
		baseDomain    string
	)

	g.BeforeEach(func() {
		ctx = context.Background()
		var err error
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		restConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to build REST config from KUBECONFIG")
		kubeClient, err = kubernetes.NewForConfig(restConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kubernetes client")
		dynamicClient, err = dynamic.NewForConfig(restConfig)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create dynamic client")
		baseDomain, err = getBaseDomain(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get base domain")
	})

	g.It("Author:shudili-ROSA-OSD_CCS-ARO-Medium-26150-misc tests for ingress operator", func() {
		var (
			namespace          = operatorNamespace
			servicemonitorName = "ingress-operator"
			rolebindingName    = "prometheus-k8s"
		)

		g.By(fmt.Sprintf("1. Check whether servicemonitor %s exists or not", servicemonitorName))
		sm, err := dynamicClient.Resource(servicemonitorGVR).Namespace(namespace).Get(ctx, servicemonitorName, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "ServiceMonitor should exist")
		o.Expect(sm.GetName()).To(o.Equal(servicemonitorName))

		g.By(fmt.Sprintf("2. Check whether rolebinding %s exists or not", rolebindingName))
		rb, err := kubeClient.RbacV1().RoleBindings(namespace).Get(ctx, rolebindingName, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "RoleBinding should exist")
		o.Expect(rb.Name).To(o.Equal(rolebindingName))

		g.By(fmt.Sprintf("3. Check the openshift.io/cluster-monitoring label of the namespace %s, which should be true", namespace))
		ns, err := kubeClient.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(ns.Labels).To(o.HaveKeyWithValue("openshift.io/cluster-monitoring", "true"))
	})

	g.It("Author:hongli-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-Medium-22633-The nodeSelector and tolerations of router deployment are controlled by ingresscontrolle", func() {
		var (
			icName = "ocp22633"
		)

		placement, err := getDefaultPlacement(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred())
		if placement == "ControlPlane" {
			g.Skip("Skip since nodeSelector is set to ControlPlane by default on this cluster")
		}

		icDomain := icName + "." + baseDomain
		defer deleteIC(ctx, dynamicClient, icName)

		g.By("1. Create one custom ingresscontroller")
		err = createIC(ctx, dynamicClient, icName, icDomain, withNodePort())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName, 1, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("2. Ensure the nodeSelector and tolerations of router deployment is the default")
		deploy, err := getRouterDeployment(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(deploy.Spec.Template.Spec.NodeSelector).To(o.HaveKey("node-role.kubernetes.io/worker"))
		for _, t := range deploy.Spec.Template.Spec.Tolerations {
			o.Expect(t.Effect).NotTo(o.Equal("NoSchedule"), "default tolerations should not contain NoSchedule")
		}

		g.By("3. Update the ingresscontroller nodeSelector and tolerations to deploy router pod to control-plane node")
		patch := `{"spec":{"nodePlacement":{"nodeSelector":{"matchLabels":{"node-role.kubernetes.io/control-plane":""}},"tolerations":[{"effect":"NoSchedule","operator":"Exists"}]}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName, 2, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("4. Ensure the nodeSelector and tolerations of router deployment is updated")
		deploy, err = getRouterDeployment(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(deploy.Spec.Template.Spec.NodeSelector).To(o.HaveKey("node-role.kubernetes.io/control-plane"))
		foundNoSchedule := false
		for _, t := range deploy.Spec.Template.Spec.Tolerations {
			if string(t.Effect) == "NoSchedule" && string(t.Operator) == "Exists" {
				foundNoSchedule = true
				break
			}
		}
		o.Expect(foundNoSchedule).To(o.BeTrue(), "tolerations should contain NoSchedule/Exists")
	})

	g.It("Author:mjoseph-ROSA-OSD_CCS-ARO-Critical-22636-The namespaceSelector of router is controlled by ingresscontroller", func() {
		var (
			icName  = "ocp22636"
			srvName = "service-unsecure"
			testNS  = "ocp22636-test-ns"
		)

		icDomain := icName + "." + baseDomain
		routeHost := srvName + "." + icDomain
		defer deleteIC(ctx, dynamicClient, icName)
		defer deleteTestNamespace(ctx, kubeClient, testNS)

		g.By("1. Create one custom ingresscontroller")
		err := createTestNamespace(ctx, kubeClient, testNS)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createIC(ctx, dynamicClient, icName, icDomain, withNodePort())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName, 1, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("2. Create a server pod and expose an unsecure service")
		err = createWebServerPodAndService(ctx, kubeClient, testNS)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodReady(ctx, kubeClient, testNS, "name=web-server-deploy", defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createRoute(ctx, dynamicClient, testNS, srvName, srvName, routeHost)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("3. Label the namespace to 'namespace=router-test'")
		err = labelNamespace(ctx, kubeClient, testNS, "namespace", "router-test")
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("4. Patch the custom ingresscontroller with the namespaceSelector")
		oldPod, err := getRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		patch := `{"spec":{"namespaceSelector":{"matchLabels":{"namespace":"router-test"}}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName, 2, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		newPod, err := waitForNewRouterPod(ctx, kubeClient, icName, oldPod, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("5. Check the haproxy config on the custom router pod to find the backend details of the " + testNS + " route")
		checkoutput, err := execInRouterPod(ctx, restConfig, kubeClient, newPod, `cat haproxy.config | grep "service-unsecure"`)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(checkoutput).To(o.ContainSubstring("backend be_http:" + testNS + ":" + srvName))

		g.By("6. Check the haproxy config on the custom router to confirm no backend details of other routes are present")
		_, canaryErr := execInRouterPod(ctx, restConfig, kubeClient, newPod, "cat haproxy.config | grep canary")
		o.Expect(canaryErr).To(o.HaveOccurred())
	})

	g.It("Author:mjoseph-ROSA-OSD_CCS-ARO-High-22637-The routeSelector of router is controlled by ingresscontroller", func() {
		var (
			icName  = "ocp22637"
			srvName = "service-unsecure"
			testNS  = "ocp22637-test-ns"
		)

		icDomain := icName + "." + baseDomain
		routeHost := srvName + "." + icDomain
		defer deleteIC(ctx, dynamicClient, icName)
		defer deleteTestNamespace(ctx, kubeClient, testNS)

		g.By("1. Create one custom ingresscontroller")
		err := createTestNamespace(ctx, kubeClient, testNS)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createIC(ctx, dynamicClient, icName, icDomain, withNodePort())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName, 1, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("2. Create a server pod and expose an unsecure service")
		err = createWebServerPodAndService(ctx, kubeClient, testNS)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodReady(ctx, kubeClient, testNS, "name=web-server-deploy", defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createRoute(ctx, dynamicClient, testNS, srvName, srvName, routeHost)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("3. Label the route to 'route=router-test'")
		err = labelRoute(ctx, dynamicClient, testNS, srvName, "route", "router-test")
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("4. Patch the custom ingresscontroller with the routeSelector")
		oldPod, err := getRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		patch := `{"spec":{"routeSelector":{"matchLabels":{"route":"router-test"}}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName, 2, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		newPod, err := waitForNewRouterPod(ctx, kubeClient, icName, oldPod, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("5. Check the haproxy config on the custom router pod to find the backend details of the route for " + srvName)
		checkoutput, err := execInRouterPod(ctx, restConfig, kubeClient, newPod, `cat haproxy.config | grep "service-unsecure"`)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(checkoutput).To(o.ContainSubstring("backend be_http:" + testNS + ":" + srvName))

		g.By("6. Check the haproxy config on the custom router to confirm no backend details of other routes are present")
		_, canaryErr := execInRouterPod(ctx, restConfig, kubeClient, newPod, "cat haproxy.config | grep canary")
		o.Expect(canaryErr).To(o.HaveOccurred())
	})

	// bug: 2007246
	g.It("Author:shudili-Medium-56772-Ingress Controller does not set allowPrivilegeEscalation in the router deployment [Serial]", func() {
		var (
			icName = "ocp56772"
		)

		icDomain := icName + "." + baseDomain
		defer deleteIC(ctx, dynamicClient, icName)

		g.By("1. Create a custom ingresscontroller")
		err := createIC(ctx, dynamicClient, icName, icDomain, withNodePort())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForICAvailable(ctx, dynamicClient, icName, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("2. Get router pods and then delete one router pod")
		podNames1, err := getRouterPodNames(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(podNames1)).To(o.BeNumerically(">", 0))
		routerPod := podNames1[0]
		err = kubeClient.CoreV1().Pods(ingressNamespace).Delete(ctx, routerPod, metav1.DeleteOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodDisappear(ctx, kubeClient, ingressNamespace, routerPod, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("3. Get router pods again, and check if it is different with the previous router pod list")
		podNames2, err := getRouterPodNames(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(podNames2)).To(o.Equal(len(podNames1)))
		o.Expect(strings.Join(podNames2, ",")).NotTo(o.Equal(strings.Join(podNames1, ",")))
	})

	g.It("Author:shudili-ROSA-OSD_CCS-ARO-NonPreRelease-Medium-60012-matchExpressions for routeSelector defined in an ingress-controller", func() {
		var (
			icName  = "ocp60012"
			srvName = "service-unsecure"
			testNS  = "ocp60012-test-ns"
		)

		icDomain := icName + "." + baseDomain
		defer deleteIC(ctx, dynamicClient, icName)
		defer deleteTestNamespace(ctx, kubeClient, testNS)

		g.By("1. Create one custom ingresscontroller")
		err := createTestNamespace(ctx, kubeClient, testNS)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createWebServerPodAndService(ctx, kubeClient, testNS)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createIC(ctx, dynamicClient, icName, icDomain, withNodePort())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName, 1, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("2. Create 4 routes for the testing")
		for _, rn := range []string{"unsrv-1", "unsrv-2", "unsrv-3", "unsrv-4"} {
			err = createRoute(ctx, dynamicClient, testNS, rn, srvName, "")
			o.Expect(err).NotTo(o.HaveOccurred())
		}

		g.By("3. Add labels to 3 routes")
		err = labelRoute(ctx, dynamicClient, testNS, "unsrv-1", "test", "aaa")
		o.Expect(err).NotTo(o.HaveOccurred())
		err = labelRoute(ctx, dynamicClient, testNS, "unsrv-2", "test", "bbb")
		o.Expect(err).NotTo(o.HaveOccurred())
		err = labelRoute(ctx, dynamicClient, testNS, "unsrv-3", "test", "ccc")
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("4. Patch the custom ingress-controllers with In matchExpressions routeSelector")
		routerPod, err := waitForRouterPod(ctx, kubeClient, icName, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		patch := `{"spec":{"routeSelector":{"matchExpressions":[{"key":"test","operator":"In","values":["aaa","bbb"]}]}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodDisappear(ctx, kubeClient, ingressNamespace, routerPod, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("5. Check if route unsrv-1 and unsrv-2 are admitted by the custom IC with In matchExpressions routeSelector, while route unsrv-3 and unsrv-4 not")
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-1", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-2", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-3", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-4", icName, defaultTimeout)).NotTo(o.HaveOccurred())

		g.By("6. Patch the custom ingress-controllers with NotIn matchExpressions routeSelector")
		routerPod, err = getRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		patch = `{"spec":{"routeSelector":{"matchExpressions":[{"key":"test","operator":"NotIn","values":["aaa","bbb"]}]}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodDisappear(ctx, kubeClient, ingressNamespace, routerPod, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("7. Check if route unsrv-3 and unsrv-4 are admitted by the custom IC with NotIn matchExpressions routeSelector, while route unsrv-1 and unsrv-2 not")
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-3", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-1", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-2", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-4", icName, defaultTimeout)).NotTo(o.HaveOccurred())

		g.By("8. Patch the custom ingress-controllers with Exists matchExpressions routeSelector")
		routerPod, err = getRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		patch = `{"spec":{"routeSelector":{"matchExpressions":[{"key":"test","operator":"Exists"}]}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodDisappear(ctx, kubeClient, ingressNamespace, routerPod, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("9. Check if route unsrv-1, unsrv-2 and unsrv-3 are admitted by the custom IC with Exists matchExpressions routeSelector, while route unsrv-4 not")
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-1", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-2", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-3", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-4", icName, defaultTimeout)).NotTo(o.HaveOccurred())

		g.By("10. Patch the custom ingress-controllers with DoesNotExist matchExpressions routeSelector")
		routerPod, err = getRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		patch = `{"spec":{"routeSelector":{"matchExpressions":[{"key":"test","operator":"DoesNotExist"}]}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodDisappear(ctx, kubeClient, ingressNamespace, routerPod, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("11. Check if route unsrv-4 is admitted by the custom IC with DoesNotExist matchExpressions routeSelector, while route unsrv-1, unsrv-2 and unsrv-3 not")
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-4", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-1", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-2", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, testNS, "unsrv-3", icName, defaultTimeout)).NotTo(o.HaveOccurred())
	})

	g.It("Author:shudili-ROSA-OSD_CCS-ARO-NonPreRelease-Medium-60013-matchExpressions for namespaceSelector defined in an ingress-controller", func() {
		var (
			icName   = "ocp60013"
			srvName  = "service-unsecure"
			projects = []string{"ocp60013-ns1", "ocp60013-ns2", "ocp60013-ns3", "ocp60013-ns4"}
		)

		icDomain := icName + "." + baseDomain
		defer deleteIC(ctx, dynamicClient, icName)
		for _, p := range projects {
			defer deleteTestNamespace(ctx, kubeClient, p)
		}

		g.By("1. Create one custom ingresscontroller")
		err := createIC(ctx, dynamicClient, icName, icDomain, withNodePort())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName, 1, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("2. Create an unsecure service and its backend pod, create the route in each of the 4 projects")
		for i, ns := range projects {
			err = createTestNamespace(ctx, kubeClient, ns)
			o.Expect(err).NotTo(o.HaveOccurred())
			err = createWebServerPodAndService(ctx, kubeClient, ns)
			o.Expect(err).NotTo(o.HaveOccurred())
			routeName := fmt.Sprintf("shard-ns%d", i+1)
			err = createRoute(ctx, dynamicClient, ns, routeName, srvName, "")
			o.Expect(err).NotTo(o.HaveOccurred())
		}

		g.By("3. Add labels to 3 projects")
		err = labelNamespace(ctx, kubeClient, projects[0], "test", "aaa")
		o.Expect(err).NotTo(o.HaveOccurred())
		err = labelNamespace(ctx, kubeClient, projects[1], "test", "bbb")
		o.Expect(err).NotTo(o.HaveOccurred())
		err = labelNamespace(ctx, kubeClient, projects[2], "test", "ccc")
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("4. Patch the custom ingresscontroller with In matchExpressions namespaceSelector")
		routerPod, err := waitForRouterPod(ctx, kubeClient, icName, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		patch := `{"spec":{"namespaceSelector":{"matchExpressions":[{"key":"test","operator":"In","values":["aaa","bbb"]}]}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodDisappear(ctx, kubeClient, ingressNamespace, routerPod, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("5. Check if route shard-ns1 and shard-ns2 are admitted by the custom IC with In matchExpressions namespaceSelector, while route shard-ns3 and shard-ns4 not")
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, projects[0], "shard-ns1", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, projects[1], "shard-ns2", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, projects[2], "shard-ns3", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, projects[3], "shard-ns4", icName, defaultTimeout)).NotTo(o.HaveOccurred())

		g.By("6. Patch the custom ingresscontroller with NotIn matchExpressions namespaceSelector")
		routerPod, err = getRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		patch = `{"spec":{"namespaceSelector":{"matchExpressions":[{"key":"test","operator":"NotIn","values":["aaa","bbb"]}]}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodDisappear(ctx, kubeClient, ingressNamespace, routerPod, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("7. Check if route shard-ns3 and shard-ns4 are admitted by the custom IC with NotIn matchExpressions namespaceSelector, while route shard-ns1 and shard-ns2 not")
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, projects[2], "shard-ns3", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, projects[0], "shard-ns1", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, projects[1], "shard-ns2", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, projects[3], "shard-ns4", icName, defaultTimeout)).NotTo(o.HaveOccurred())

		g.By("8. Patch the custom ingresscontroller with Exists matchExpressions namespaceSelector")
		routerPod, err = getRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		patch = `{"spec":{"namespaceSelector":{"matchExpressions":[{"key":"test","operator":"Exists"}]}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodDisappear(ctx, kubeClient, ingressNamespace, routerPod, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("9. Check if route shard-ns1, shard-ns2 and shard-ns3 are admitted by the custom IC with Exists matchExpressions namespaceSelector, while route shard-ns4 not")
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, projects[0], "shard-ns1", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, projects[1], "shard-ns2", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, projects[2], "shard-ns3", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, projects[3], "shard-ns4", icName, defaultTimeout)).NotTo(o.HaveOccurred())

		g.By("10. Patch the custom ingresscontroller with DoesNotExist matchExpressions namespaceSelector")
		routerPod, err = getRouterPodName(ctx, kubeClient, icName)
		o.Expect(err).NotTo(o.HaveOccurred())
		patch = `{"spec":{"namespaceSelector":{"matchExpressions":[{"key":"test","operator":"DoesNotExist"}]}}}`
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodDisappear(ctx, kubeClient, ingressNamespace, routerPod, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("11. Check if route shard-ns4 is admitted by the custom IC with DoesNotExist matchExpressions namespaceSelector, while route shard-ns1, shard-ns2 and shard-ns3 not")
		o.Expect(waitForRouteAdmittedByIC(ctx, dynamicClient, projects[3], "shard-ns4", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, projects[0], "shard-ns1", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, projects[1], "shard-ns2", icName, defaultTimeout)).NotTo(o.HaveOccurred())
		o.Expect(waitForRouteNotAdmittedByIC(ctx, dynamicClient, projects[2], "shard-ns3", icName, defaultTimeout)).NotTo(o.HaveOccurred())
	})

	// OCPBUGS-853
	g.It("Author:shudili-ROSA-OSD_CCS-ARO-Critical-62530-openshift ingress operator is failing to update router-certs [Serial]", func() {
		var (
			icName        = "ocp62530"
			tlsSecretName = "custom-cert-62530"
			dirname       = "/tmp/OCP-62530-ca/"
		)

		defer deleteIC(ctx, dynamicClient, icName)
		defer deleteSecret(ctx, kubeClient, ingressNamespace, tlsSecretName)
		defer func() {
			if rmErr := os.RemoveAll(dirname); rmErr != nil {
				_, _ = fmt.Fprintf(g.GinkgoWriter, "failed to remove %s: %v\n", dirname, rmErr)
			}
		}()

		g.By("1. Try to create custom key and custom certification by openssl, create a new self-signed CA at first, creating the CA key")
		err := os.MkdirAll(dirname, 0755)
		o.Expect(err).NotTo(o.HaveOccurred())

		caKey := dirname + "ca.key"
		caCrt := dirname + "ca.crt"
		usrKey := dirname + "usr.key"
		usrCsr := dirname + "usr.csr"
		usrCrt := dirname + "usr.crt"

		_, err = exec.Command("bash", "-c", fmt.Sprintf("openssl genrsa -out %s 4096", caKey)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("2. Create the CA certificate")
		_, err = exec.Command("bash", "-c", fmt.Sprintf("openssl req -x509 -new -nodes -key %s -sha256 -days 30 -out %s -subj /CN=NE-Test-Root-CA", caKey, caCrt)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("3. Create a new user certificate, creating the user CSR with the private user key")
		_, err = exec.Command("bash", "-c", fmt.Sprintf("openssl req -nodes -newkey rsa:2048 -keyout %s -subj /CN=*.ocp62530.example.com -out %s", usrKey, usrCsr)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("4. Sign the user CSR and generate the certificate")
		_, err = exec.Command("bash", "-c", fmt.Sprintf(`openssl x509 -extfile <(printf "subjectAltName = DNS:*.ocp62530.example.com") -req -in %s -CA %s -CAkey %s -CAcreateserial -out %s -days 30 -sha256`, usrCsr, caCrt, caKey, usrCrt)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("5. Create a tls secret in openshift-ingress ns")
		certPEM, err := os.ReadFile(usrCrt)
		o.Expect(err).NotTo(o.HaveOccurred())
		keyPEM, err := os.ReadFile(usrKey)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createTLSSecret(ctx, kubeClient, ingressNamespace, tlsSecretName, certPEM, keyPEM)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("6. Record the router-certs data count before creating the custom IC")
		secretBefore, err := kubeClient.CoreV1().Secrets("openshift-config-managed").Get(ctx, "router-certs", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		routerCertsCountBefore := len(secretBefore.Data)

		g.By("7. Create a custom ingresscontroller")
		err = createIC(ctx, dynamicClient, icName, icName+".example.com", withNodePort())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName, 1, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("8. Patch defaultCertificate with custom secret to the IC")
		patch := fmt.Sprintf(`{"spec":{"defaultCertificate":{"name":"%s"}}}`, tlsSecretName)
		err = patchICMerge(ctx, dynamicClient, icName, patch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName, 2, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("9. Verify the IC spec has the correct certificate name")
		ic, err := dynamicClient.Resource(ingressControllerGVR).Namespace(operatorNamespace).Get(ctx, icName, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		certName := nestedString(ic.Object, "spec", "defaultCertificate", "name")
		o.Expect(certName).To(o.ContainSubstring(tlsSecretName))

		g.By("10. Check the router-certs in the openshift-config-managed namespace, the data count should not increase")
		secretAfter, err := kubeClient.CoreV1().Secrets("openshift-config-managed").Get(ctx, "router-certs", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(secretAfter.Data)).To(o.Equal(routerCertsCountBefore), "router-certs data count should not increase with custom IC cert")
	})

	// bug: https://issues.redhat.com/browse/OCPBUGS-6013
	g.It("Author:asood-NonHyperShiftHOST-ConnectedOnly-ROSA-OSD_CCS-Medium-63832-Cluster ingress health checks and routes fail on swapping application router between public and private", func() {
		var (
			icName = "ocp63832"
		)

		platform, err := getClusterPlatform(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred())
		if platform != "aws" {
			g.Skip("Test cases should be run on AWS cluster, skip for other platforms!!")
		}

		defer deleteIC(ctx, dynamicClient, icName)

		g.By("1. Create a custom ingress controller")
		err = createIC(ctx, dynamicClient, icName, "63832.test.com", withCLB())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForICAvailable(ctx, dynamicClient, icName, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("2. Annotate ingress controller")
		annotatePatch := `{"metadata":{"annotations":{"ingress.operator.openshift.io/auto-delete-load-balancer":""}}}`
		err = patchICMerge(ctx, dynamicClient, icName, annotatePatch)
		o.Expect(err).NotTo(o.HaveOccurred())

		strategyScope := []string{
			`{"spec":{"endpointPublishingStrategy":{"loadBalancer":{"scope":"Internal"},"type":"LoadBalancerService"}}}`,
			`{"spec":{"endpointPublishingStrategy":{"loadBalancer":{"scope":"External"},"type":"LoadBalancerService"}}}`,
		}

		g.By("3. Get the health check node port")
		svc, err := kubeClient.CoreV1().Services(ingressNamespace).Get(ctx, "router-"+icName, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		prevPort := svc.Spec.HealthCheckNodePort

		for i := 0; i < len(strategyScope); i++ {
			g.By("3. Change the endpoint publishing strategy")
			err = patchICMerge(ctx, dynamicClient, icName, strategyScope[i])
			o.Expect(err).NotTo(o.HaveOccurred())

			g.By("3.1 Check the state of custom ingress operator")
			err = waitForICAvailable(ctx, dynamicClient, icName, longTimeout)
			o.Expect(err).NotTo(o.HaveOccurred())

			g.By("3.4. Get new health check node port")
			var newPort int32
			waitErr := wait.PollUntilContextTimeout(ctx, 30*time.Second, longTimeout, true, func(ctx context.Context) (bool, error) {
				svc, err := kubeClient.CoreV1().Services(ingressNamespace).Get(ctx, "router-"+icName, metav1.GetOptions{})
				if err != nil {
					return false, nil
				}
				if svc.Spec.HealthCheckNodePort != prevPort {
					newPort = svc.Spec.HealthCheckNodePort
					return true, nil
				}
				return false, nil
			})
			o.Expect(waitErr).NotTo(o.HaveOccurred(), "health check node port should change after scope swap")
			prevPort = newPort
		}
	})

	g.It("Author:mjoseph-NonHyperShiftHOST-Critical-64611-Ingress operator support for private hosted zones in Shared VPC clusters", func() {
		var (
			icExternal = "ocp64611external"
			icCLB      = "ocp64611clb"
		)

		g.By("Pre-flight check for the platform type")
		platform, err := getClusterPlatform(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred())
		if platform != "aws" {
			g.Skip("Test requires AWS platform")
		}

		g.By("Pre-flight check for the shared VPC platform")
		dnsConfig, err := dynamicClient.Resource(dnsConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		privateZoneIAMRole, _, _ := unstructured.NestedString(dnsConfig.Object, "spec", "platform", "aws", "privateZoneIAMRole")
		if privateZoneIAMRole == "" {
			g.Skip("Skip since this is not a shared vpc cluster")
		}

		g.By("1. Check the STS Role in the cluster")
		cr, err := dynamicClient.Resource(credentialsReqGVR).Namespace("openshift-cloud-credential-operator").Get(ctx, "openshift-ingress", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		crJSON := toJSON(cr.Object)
		o.Expect(crJSON).To(o.ContainSubstring("sts:AssumeRole"))

		g.By("2. Check whether the privateZoneIAMRole is created using the ARN")
		arnRegex := regexp.MustCompile(`arn:(aws|aws-cn|aws-us-gov):iam::[0-9]{12}:role/.*`)
		o.Expect(arnRegex.MatchString(privateZoneIAMRole)).To(o.BeTrue(), "privateZoneIAMRole should be a valid ARN")

		g.By("3. Check the default DNS management status")
		defaultDNSRecord, err := dynamicClient.Resource(dnsRecordGVR).Namespace(operatorNamespace).Get(ctx, "default-wildcard", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		policy := nestedString(defaultDNSRecord.Object, "spec", "dnsManagementPolicy")
		o.Expect(policy).To(o.Equal("Managed"))

		g.By("4. Collecting the public zone and private zone id from dns config")
		privateZoneId, _, _ := unstructured.NestedString(dnsConfig.Object, "spec", "privateZone", "id")
		publicZoneId, _, _ := unstructured.NestedString(dnsConfig.Object, "spec", "publicZone", "id")

		g.By("5. Collecting zone details from default ingress controller and cross checking it with dns config details")
		defaultZones, _, _ := unstructured.NestedSlice(defaultDNSRecord.Object, "status", "zones")
		zonesJSON := toJSON(defaultZones)
		o.Expect(zonesJSON).To(o.ContainSubstring(privateZoneId))
		if publicZoneId != "" {
			o.Expect(zonesJSON).To(o.ContainSubstring(publicZoneId))
		}

		g.By("6. Check the default dnsrecord of the ingress operator to confirm there is no degrades")
		for _, z := range defaultZones {
			zm, ok := z.(map[string]interface{})
			if !ok {
				continue
			}
			zConds, _ := zm["conditions"].([]interface{})
			for _, c := range zConds {
				cm, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				if cm["type"] == "Published" {
					o.Expect(cm["status"]).To(o.Equal("True"))
					o.Expect(cm["reason"]).To(o.Equal("ProviderSuccess"))
				}
			}
		}

		g.By("7. Create two custom ingresscontrollers")
		defer deleteIC(ctx, dynamicClient, icExternal)
		defer deleteIC(ctx, dynamicClient, icCLB)
		err = createIC(ctx, dynamicClient, icExternal, icExternal+"."+baseDomain, withExternalLB())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createIC(ctx, dynamicClient, icCLB, icCLB+"."+baseDomain, withCLB())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForICAvailable(ctx, dynamicClient, icExternal, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForICAvailable(ctx, dynamicClient, icCLB, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("8. Check the custom DNS management status")
		for _, recordName := range []string{icExternal + "-wildcard", icCLB + "-wildcard"} {
			record, err := dynamicClient.Resource(dnsRecordGVR).Namespace(operatorNamespace).Get(ctx, recordName, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())
			p := nestedString(record.Object, "spec", "dnsManagementPolicy")
			o.Expect(p).To(o.Equal("Managed"))
		}

		g.By("9. Collecting zone details from custom ingress controller and cross checking it with dns zone details")
		for _, recordName := range []string{icExternal + "-wildcard", icCLB + "-wildcard"} {
			record, err := dynamicClient.Resource(dnsRecordGVR).Namespace(operatorNamespace).Get(ctx, recordName, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())
			zones, _, _ := unstructured.NestedSlice(record.Object, "status", "zones")
			zJSON := toJSON(zones)
			o.Expect(zJSON).To(o.ContainSubstring(privateZoneId))
			if publicZoneId != "" {
				o.Expect(zJSON).To(o.ContainSubstring(publicZoneId))
			}
		}

		g.By("10. Check the custom dnsrecord of the ingress operator to confirm there is no degrades")
		for _, recordName := range []string{icExternal + "-wildcard", icCLB + "-wildcard"} {
			record, err := dynamicClient.Resource(dnsRecordGVR).Namespace(operatorNamespace).Get(ctx, recordName, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())
			zones, _, _ := unstructured.NestedSlice(record.Object, "status", "zones")
			for _, z := range zones {
				zm, ok := z.(map[string]interface{})
				if !ok {
					continue
				}
				zConds, _ := zm["conditions"].([]interface{})
				for _, c := range zConds {
					cm, ok := c.(map[string]interface{})
					if !ok {
						continue
					}
					if cm["type"] == "Published" {
						o.Expect(cm["status"]).To(o.Equal("True"))
						o.Expect(cm["reason"]).To(o.Equal("ProviderSuccess"))
					}
				}
			}
		}
	})

	// Including OCPBUGS-33657,OCPBUGS-35027 and OCPBUGS-35454 in OCP-75907
	// No ingress operator namespace on HyperShift guest cluster so this case is not available
	g.It("Author:shudili-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-High-75907-Ingress Operator should not always remain in the progressing state [Disruptive]", func() {
		workerNodeCount, err := getWorkerNodeCount(ctx, kubeClient)
		o.Expect(err).NotTo(o.HaveOccurred())
		if workerNodeCount < 1 {
			g.Skip("Skipping as we at least need one Linux worker node")
		}

		err = waitForClusterOperatorNormal(ctx, dynamicClient, "ingress", clusterOperatorTimeout)
		o.Expect(err).NotTo(o.HaveOccurred(), "ingress ClusterOperator should be normal before test")

		// OCPBUGS-35027
		g.By("1. Create a configmap with empty configuration")
		cmName := "custom-ca35027"
		defer deleteConfigMap(ctx, kubeClient, "openshift-config", cmName)
		err = createConfigMap(ctx, kubeClient, "openshift-config", cmName)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("2. Create a ingresscontroller for OCPBUGS-35027")
		icName35027 := "bug35027"
		defer deleteIC(ctx, dynamicClient, icName35027)
		err = createIC(ctx, dynamicClient, icName35027, icName35027+"."+baseDomain, withPrivateLB(), withClientTLS("client-ca-cert"))
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("3. Check the custom router pod should not be created for the custom ingresscontroller was abnormal")
		o.Consistently(func() ([]corev1.Pod, error) {
			return getPodsByLabel(ctx, kubeClient, ingressNamespace, "ingresscontroller.operator.openshift.io/deployment-ingresscontroller="+icName35027)
		}, 30*time.Second, 5*time.Second).Should(o.BeEmpty(), "no router pod should be created for abnormal IC")

		g.By("4. Delete the custom ingress controller, and then check the logs that clientca-configmap finalizer log should not appear")
		deleteIC(ctx, dynamicClient, icName35027)
		logs, err := getOperatorLogs(ctx, kubeClient, 20)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(logs).NotTo(o.ContainSubstring("failed to add " + cmName + "-configmap finalizer"))

		// OCPBUGS-35454
		g.By("5. Create the custom ingress controller for OCPBUGS-35454")
		icName35454 := "bug35454"
		defer deleteIC(ctx, dynamicClient, icName35454)
		err = createIC(ctx, dynamicClient, icName35454, icName35454+"."+baseDomain, withHostNetwork(22080, 22443, 22936))
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForICAvailable(ctx, dynamicClient, icName35454, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("6. Check the service's spec ports of http/https/metrics")
		svc, err := kubeClient.CoreV1().Services(ingressNamespace).Get(ctx, "router-internal-"+icName35454, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		svcJSON := toJSON(svc.Spec.Ports)
		o.Expect(svcJSON).To(o.ContainSubstring(`"port":80`))
		o.Expect(svcJSON).To(o.ContainSubstring(`"port":443`))
		o.Expect(svcJSON).To(o.ContainSubstring(`"port":1936`))

		g.By("7. Check the service's ep of http/https/metrics")
		ep, err := kubeClient.CoreV1().Endpoints(ingressNamespace).Get(ctx, "router-internal-"+icName35454, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		epJSON := toJSON(ep.Subsets)
		o.Expect(epJSON).To(o.ContainSubstring(`"port":22080`))
		o.Expect(epJSON).To(o.ContainSubstring(`"port":22443`))
		o.Expect(epJSON).To(o.ContainSubstring(`"port":22936`))

		g.By("8. Check the configuration update for the custom router deployment and the internal service")
		deploy, err := getRouterDeployment(ctx, kubeClient, icName35454)
		o.Expect(err).NotTo(o.HaveOccurred())
		rc, rcErr := routerContainer(deploy)
		o.Expect(rcErr).NotTo(o.HaveOccurred())
		deployJSON := toJSON(rc.LivenessProbe)
		o.Expect(deployJSON).To(o.ContainSubstring(`"failureThreshold":3`))
		o.Expect(deployJSON).To(o.ContainSubstring(`"scheme":"HTTP"`))
		deployJSON = toJSON(rc.ReadinessProbe)
		o.Expect(deployJSON).To(o.ContainSubstring(`"failureThreshold":3`))
		o.Expect(deployJSON).To(o.ContainSubstring(`"scheme":"HTTP"`))
		deployJSON = toJSON(rc.StartupProbe)
		o.Expect(deployJSON).To(o.ContainSubstring(`"scheme":"HTTP"`))
		deployJSON = toJSON(deploy.Spec.Template.Spec.Volumes)
		o.Expect(deployJSON).To(o.ContainSubstring(`"defaultMode":420`))

		g.By("9. Check the service's sessionAffinity, which should be None")
		o.Expect(string(svc.Spec.SessionAffinity)).To(o.Equal("None"))

		g.By("10. Patch the custom ingress controller with other http/https/metrics ports")
		portPatch := `{"spec":{"endpointPublishingStrategy":{"hostNetwork":{"httpPort":23080,"httpsPort":23443,"statsPort":23936}}}}`
		err = patchICMerge(ctx, dynamicClient, icName35454, portPatch)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForDeployGeneration(ctx, kubeClient, icName35454, 2, defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("11. Check the service's ep of http/https/metrics, which should be updated to the specified ports")
		ep, err = kubeClient.CoreV1().Endpoints(ingressNamespace).Get(ctx, "router-internal-"+icName35454, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		epJSON = toJSON(ep.Subsets)
		o.Expect(epJSON).To(o.ContainSubstring(`"port":23080`))
		o.Expect(epJSON).To(o.ContainSubstring(`"port":23443`))
		o.Expect(epJSON).To(o.ContainSubstring(`"port":23936`))
	})

	// OCPBUGS-29373
	g.It("Author:shudili-ROSA-OSD_CCS-ARO-High-75908-http2 connection coalescing component routing should not be broken with single certificate [Disruptive]", func() {
		var (
			dirname  = "/tmp/OCP-75908-ca/"
			validity = 30
			caSubj   = "/CN=NE-Test-Root-CA"
			caCrt    = dirname + "75908-ca.crt"
			caKey    = dirname + "75908-ca.key"
			usrCrt   = dirname + "75908-usr.crt"
			usrKey   = dirname + "75908-usr.key"
			usrCsr   = dirname + "75908-usr.csr"
		)

		g.By("1. skip for http2 not enabled clusters")
		routerPod, err := getRouterPodName(ctx, kubeClient, "default")
		o.Expect(err).NotTo(o.HaveOccurred())
		deploy, err := getRouterDeployment(ctx, kubeClient, "default")
		o.Expect(err).NotTo(o.HaveOccurred())
		rc, rcErr := routerContainer(deploy)
		o.Expect(rcErr).NotTo(o.HaveOccurred())
		http2Enabled := false
		for _, env := range rc.Env {
			if env.Name == "ROUTER_DISABLE_HTTP2" && env.Value == "false" {
				http2Enabled = true
				break
			}
		}
		if !http2Enabled {
			g.Skip("OCPBUGS-29373 occur on ROSA/OSD cluster, skip for http2 not enabled clusters!")
		}

		g.By("2. Get some info including hostnames of console/oauth route for the testing")
		ingressDomain, err := getIngressDomain(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred())
		consoleRoute, err := dynamicClient.Resource(routeGVR).Namespace("openshift-console").Get(ctx, "console", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		consoleHost := nestedString(consoleRoute.Object, "spec", "host")
		oauthRoute, err := dynamicClient.Resource(routeGVR).Namespace("openshift-authentication").Get(ctx, "oauth-openshift", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		oauthHost := nestedString(oauthRoute.Object, "spec", "host")
		defaultRoute := "foo.apps." + baseDomain

		g.By("3. Use openssl to create the certification and key")
		defer func() {
			if rmErr := os.RemoveAll(dirname); rmErr != nil {
				_, _ = fmt.Fprintf(g.GinkgoWriter, "failed to remove %s: %v\n", dirname, rmErr)
			}
		}()
		err = os.MkdirAll(dirname, 0755)
		o.Expect(err).NotTo(o.HaveOccurred())

		_, err = exec.Command("bash", "-c", fmt.Sprintf("openssl req -x509 -newkey rsa:2048 -days %d -keyout %s -out %s -nodes -subj %s", validity, caKey, caCrt, caSubj)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		_, err = exec.Command("bash", "-c", fmt.Sprintf(`openssl req -newkey rsa:2048 -nodes -keyout %s -out %s -subj '/CN=*.%s'`, usrKey, usrCsr, ingressDomain)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		_, err = exec.Command("bash", "-c", fmt.Sprintf(`openssl x509 -extfile <(printf 'subjectAltName = DNS.1:%s,DNS.2:%s') -req -in %s -CA %s -CAkey %s -CAcreateserial -out %s -days %d`, consoleHost, oauthHost, usrCsr, caCrt, caKey, usrCrt, validity)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("4. Create the custom secret on the cluster with the created user certification and user key")
		secretName := "custom-cert75908"
		defer deleteSecret(ctx, kubeClient, "openshift-config", secretName)
		certPEM, err := os.ReadFile(usrCrt)
		o.Expect(err).NotTo(o.HaveOccurred())
		keyPEM, err := os.ReadFile(usrKey)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createTLSSecret(ctx, kubeClient, "openshift-config", secretName, certPEM, keyPEM)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("5. Patch the ingress with the console route host and the custom secret")
		ingressCfg, err := dynamicClient.Resource(ingressConfigGVR).Get(ctx, "cluster", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		origComponentRoutes, _, _ := unstructured.NestedSlice(ingressCfg.Object, "spec", "componentRoutes")
		defer func() {
			restorePatch := fmt.Sprintf(`{"spec":{"componentRoutes":%s}}`, toJSON(origComponentRoutes))
			_ = patchClusterResourceMerge(ctx, dynamicClient, ingressConfigGVR, "cluster", restorePatch)
		}()

		patchContent := fmt.Sprintf(`{"spec":{"componentRoutes":[{"hostname":"%s","name":"console","namespace":"openshift-console","servingCertKeyPairSecret":{"name":"%s"}}]}}`, consoleHost, secretName)
		err = patchClusterResourceMerge(ctx, dynamicClient, ingressConfigGVR, "cluster", patchContent)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("6. Check the console route has HTTP/2 enabled")
		o.Eventually(func() (string, error) {
			return execInRouterPod(ctx, restConfig, kubeClient, routerPod, "cat cert_config.map")
		}, longTimeout, pollInterval).Should(
			o.ContainSubstring(fmt.Sprintf("console.pem [alpn h2,http/1.1] %s", consoleHost)))

		g.By("7. Check console certificate has different SHA1 Fingerprint with OAuth certificate and default certificate, by using openssl command")
		opPods, err := getPodsByLabel(ctx, kubeClient, operatorNamespace, "name=ingress-operator")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(opPods)).To(o.BeNumerically(">", 0))
		opPodName := opPods[0].Name

		consoleFP, _, err := execInPod(ctx, restConfig, kubeClient, operatorNamespace, opPodName, "ingress-operator",
			[]string{"bash", "-c", fmt.Sprintf(`openssl s_client -connect '%s':443 </dev/null 2>/dev/null | openssl x509 -sha1 -in /dev/stdin -noout -fingerprint`, consoleHost)})
		o.Expect(err).NotTo(o.HaveOccurred())
		oauthFP, _, err := execInPod(ctx, restConfig, kubeClient, operatorNamespace, opPodName, "ingress-operator",
			[]string{"bash", "-c", fmt.Sprintf(`openssl s_client -connect '%s':443 </dev/null 2>/dev/null | openssl x509 -sha1 -in /dev/stdin -noout -fingerprint`, oauthHost)})
		o.Expect(err).NotTo(o.HaveOccurred())
		defaultFP, _, err := execInPod(ctx, restConfig, kubeClient, operatorNamespace, opPodName, "ingress-operator",
			[]string{"bash", "-c", fmt.Sprintf(`openssl s_client -connect '%s':443 </dev/null 2>/dev/null | openssl x509 -sha1 -in /dev/stdin -noout -fingerprint`, defaultRoute)})
		o.Expect(err).NotTo(o.HaveOccurred())

		o.Expect(strings.TrimSpace(consoleFP)).NotTo(o.ContainSubstring(strings.TrimSpace(oauthFP)), "console and oauth should use different certificates")
		o.Expect(strings.TrimSpace(oauthFP)).To(o.ContainSubstring(strings.TrimSpace(defaultFP)), "oauth and default should use the same certificate")
	})

	// Including OCPBUGS-34757, OCPBUGS-34110 and OCPBUGS-34888 in OCP-75909
	// No ingress operator namespace on HyperShift guest cluster so this case is not available
	g.It("Author:shudili-NonHyperShiftHOST-ROSA-OSD_CCS-ARO-High-75909-Ingress Operator should not always remain in the progressing state [Disruptive]", func() {
		var (
			ic34757one = "bug34757one"
			ic34757two = "bug34757two"
			ic34888one = "bug34888one"
			ic34888two = "bug34888two"
		)

		workerNodeCount, err := getWorkerNodeCount(ctx, kubeClient)
		o.Expect(err).NotTo(o.HaveOccurred())
		if workerNodeCount < 1 {
			g.Skip("Skipping as we at least need one Linux worker node")
		}

		// OCPBUGS-34757
		g.By("1. after the cluster is ready, check openshift-ingress-operator logs, which should not contain updated internal service")
		logs, err := getOperatorLogs(ctx, kubeClient, 100)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(strings.Contains(logs, "updated internal service")).NotTo(o.BeTrue())

		g.By("2. Create two custom ingresscontrollers for OCPBUGS-34757")
		defer deleteIC(ctx, dynamicClient, ic34757one)
		err = createIC(ctx, dynamicClient, ic34757one, ic34757one+"."+baseDomain, withNodePort())
		o.Expect(err).NotTo(o.HaveOccurred())
		defer deleteIC(ctx, dynamicClient, ic34757two)
		err = createIC(ctx, dynamicClient, ic34757two, ic34757two+"."+baseDomain, withPrivateLB())
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForICAvailable(ctx, dynamicClient, ic34757one, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForICAvailable(ctx, dynamicClient, ic34757two, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("3. Check the logs again after the custom ingresscontrollers are ready, which should not contain updated internal service")
		logs, err = getOperatorLogs(ctx, kubeClient, 100)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(strings.Contains(logs, "updated internal service")).NotTo(o.BeTrue())
		deleteIC(ctx, dynamicClient, ic34757one)
		deleteIC(ctx, dynamicClient, ic34757two)

		// OCPBUGS-34110
		g.By("4. delete the ingress-operator pod")
		err = deleteAllPodsByLabel(ctx, kubeClient, operatorNamespace, "name=ingress-operator")
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForPodReady(ctx, kubeClient, operatorNamespace, "name=ingress-operator", longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForClusterOperatorNormal(ctx, dynamicClient, "ingress", clusterOperatorTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		logs, err = getOperatorLogs(ctx, kubeClient, 100)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(strings.Contains(logs, "updated IngressClass")).NotTo(o.BeTrue())

		// OCPBUGS-34888
		g.By("5. Create two custom ingresscontrollers for OCPBUGS-34888")
		defer deleteIC(ctx, dynamicClient, ic34888one)
		err = createIC(ctx, dynamicClient, ic34888one, ic34888one+"."+baseDomain, withHostNetwork(10080, 10443, 10936))
		o.Expect(err).NotTo(o.HaveOccurred())
		defer deleteIC(ctx, dynamicClient, ic34888two)
		err = createIC(ctx, dynamicClient, ic34888two, ic34888two+"."+baseDomain, withHostNetwork(11080, 11443, 11936))
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForICAvailable(ctx, dynamicClient, ic34888one, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForICAvailable(ctx, dynamicClient, ic34888two, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("6. Check there was not the updated router deployment log")
		logs, err = getOperatorLogs(ctx, kubeClient, 100)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(logs).NotTo(o.ContainSubstring("updated router deployment"))
	})

	// [OCPBUGS-42480](https://issues.redhat.com/browse/OCPBUGS-42480)
	// [OCPBUGS-43063](https://issues.redhat.com/browse/OCPBUGS-43063)
	g.It("Author:shudili-ROSA-OSD_CCS-ARO-Critical-77283-Router should support SHA1 CA certificates in the default certificate chain", func() {
		var (
			icName         = "77283"
			tlsSecretName  = "custom-cert77283"
			dirname        = "/tmp/OCP-77283-ca/"
			validity       = 30
			caSubj         = "/C=US/ST=SC/L=Default City/O=Default Company Ltd/OU=Test CA/CN=www.exampleca.com/emailAddress=example@example.com"
			caCrt          = dirname + "77283-ca.crt"
			caKey          = dirname + "77283-ca.key"
			usrSubj        = "/CN=www.example.com/ST=SC/C=US/emailAddress=example@example.com/O=Example/OU=Example"
			usrCrt         = dirname + "77283-usr.crt"
			usrKey         = dirname + "77283-usr.key"
			usrCsr         = dirname + "77283-usr.csr"
			ext            = dirname + "77283-extfile"
			combinationCrt = dirname + "77283-combo.crt"
		)

		g.By("1. Use openssl to create the certification and key")
		defer func() {
			if rmErr := os.RemoveAll(dirname); rmErr != nil {
				_, _ = fmt.Fprintf(g.GinkgoWriter, "failed to remove %s: %v\n", dirname, rmErr)
			}
		}()
		err := os.MkdirAll(dirname, 0755)
		o.Expect(err).NotTo(o.HaveOccurred())
		ingressDomain, err := getIngressDomain(ctx, dynamicClient)
		o.Expect(err).NotTo(o.HaveOccurred())
		wildcard := "*." + ingressDomain

		g.By("1.1 Create a new self-signed sha1 root CA including the ca certification and ca key")
		_, err = exec.Command("bash", "-c", fmt.Sprintf(`openssl req -x509 -sha1 -newkey rsa:2048 -days %d -keyout %s -out %s -nodes -subj '%s'`, validity, caKey, caCrt, caSubj)).Output()
		if err != nil {
			g.Skip("Skipping as openssl under the OS doesn't support sha1 certification")
		}

		g.By("1.2 Create the user CSR and the user key")
		_, err = exec.Command("bash", "-c", fmt.Sprintf(`openssl req -newkey rsa:2048 -nodes -keyout %s -out %s -subj %s`, usrKey, usrCsr, usrSubj)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("1.3 Create the extension file, then create the user certification")
		_, err = exec.Command("bash", "-c", fmt.Sprintf(`echo $'[ext]\nbasicConstraints = CA:FALSE\nsubjectKeyIdentifier = none\nauthorityKeyIdentifier = none\nextendedKeyUsage=serverAuth,clientAuth\nkeyUsage=nonRepudiation, digitalSignature, keyEncipherment\nsubjectAltName = DNS:'%s > %s`, wildcard, ext)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		_, err = exec.Command("bash", "-c", fmt.Sprintf(`openssl x509 -req -days %d -sha256 -in %s -CA %s -CAcreateserial -CAkey %s -extfile %s -extensions ext -out %s`, validity, usrCsr, caCrt, caKey, ext, usrCrt)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("1.4 Create the file including the sha1 certification and user certification")
		_, err = exec.Command("bash", "-c", fmt.Sprintf("cat %s %s > %s", usrCrt, caCrt, combinationCrt)).Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("2. Create the custom secret on the cluster with the combination certifications and user key")
		defer deleteSecret(ctx, kubeClient, ingressNamespace, tlsSecretName)
		comboPEM, err := os.ReadFile(combinationCrt)
		o.Expect(err).NotTo(o.HaveOccurred())
		keyPEM, err := os.ReadFile(usrKey)
		o.Expect(err).NotTo(o.HaveOccurred())
		err = createTLSSecret(ctx, kubeClient, ingressNamespace, tlsSecretName, comboPEM, keyPEM)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("3. Create the custom ingresscontroller for the testing")
		defer deleteIC(ctx, dynamicClient, icName)
		err = createIC(ctx, dynamicClient, icName, icName+"."+baseDomain, withNodePort(), withDefaultCertificate(tlsSecretName))
		o.Expect(err).NotTo(o.HaveOccurred())
		err = waitForICAvailable(ctx, dynamicClient, icName, longTimeout)
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("4. Check the ingress co, it should be upgradable")
		co, err := dynamicClient.Resource(clusterOperatorGVR).Get(ctx, "ingress", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		conditions, _, _ := unstructured.NestedSlice(co.Object, "status", "conditions")
		for _, c := range conditions {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if cm["type"] == "Upgradeable" {
				o.Expect(cm["status"]).To(o.Equal("True"))
			}
		}

		g.By("5. The canary route is accessible")
		err = waitForRouteAdmittedByIC(ctx, dynamicClient, "openshift-ingress-canary", "canary", "default", defaultTimeout)
		o.Expect(err).NotTo(o.HaveOccurred(), "canary route should be admitted by default ingress controller")
	})
})
