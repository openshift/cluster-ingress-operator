package test

import (
	"context"
	"fmt"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
)

var _ = g.Describe("[sig-network-edge][OCPFeatureGate:AWSDualStackInstall][Feature:Router][apigroup:route.openshift.io][apigroup:operator.openshift.io][apigroup:config.openshift.io]", func() {
	defer g.GinkgoRecover()

	var oc = NewCLI("router-dualstack", WithPodSecurityLevel("baseline"))
	var baseDomain string

	g.BeforeEach(func() {
		requireAWSDualStack(context.Background(), oc)

		domain, err := getDefaultIngressDomain(context.Background(), oc)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to find default domain name")
		baseDomain = strings.TrimPrefix(domain, "apps.")
	})

	g.AfterEach(func() {
		if g.CurrentSpecReport().Failed() {
			dumpRouterPodLogs(oc, oc.Namespace())
		}
	})

	g.It("should be reachable via IPv4 and IPv6 through a dual-stack ingress controller", func() {
		ctx := context.Background()

		shardFQDN := "nlb." + baseDomain

		// Deploy the shard first so DNS and LB can provision while we set up the backend.
		g.By("Deploying a new router shard with NLB")
		shardIC, err := deployNewRouterShard(ctx, oc, 10*time.Minute, shardConfig{
			Domain: shardFQDN,
			Type:   oc.Namespace(),
			LoadBalancer: &operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSNetworkLoadBalancer,
					},
				},
			},
		})
		defer func() {
			if shardIC != nil {
				if err := oc.AdminOperatorClient().OperatorV1().IngressControllers(shardIC.Namespace).Delete(ctx, shardIC.Name, metav1.DeleteOptions{}); err != nil {
					fmt.Fprintf(g.GinkgoWriter, "deleting ingress controller failed: %v\n", err)
				}
			}
		}()
		o.Expect(err).NotTo(o.HaveOccurred(), "new router shard did not rollout")

		g.By("Disabling client IP preservation on the NLB target group to avoid hairpin issues (OCPBUGS-63219)")
		routerSvcName := "router-" + shardIC.Name
		err = oc.AsAdmin().Run("annotate").Args("service", "-n", "openshift-ingress", routerSvcName,
			"service.beta.kubernetes.io/aws-load-balancer-target-group-attributes=preserve_client_ip.enabled=false").Execute()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Labelling the namespace for the shard")
		err = oc.AsAdmin().Run("label").Args("namespace", oc.Namespace(), "type="+oc.Namespace()).Execute()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Creating backend service and pod")
		createBackendServiceAndPod(ctx, oc, "dualstack-backend")

		g.By("Creating an edge-terminated route")
		routeHost := "dualstack-test." + shardFQDN
		createEdgeRoute(ctx, oc, "dualstack-route", routeHost, "dualstack-backend", oc.Namespace())

		g.By("Waiting for the route to be admitted")
		waitForRouteAdmitted(ctx, oc, "dualstack-route", routeHost, 5*time.Minute)

		g.By("Creating exec pod for curl tests")
		execPodName := createExecPod(ctx, oc, "execpod")
		defer func() {
			_ = oc.AdminKubeClient().CoreV1().Pods(oc.Namespace()).Delete(ctx, execPodName, *metav1.NewDeleteOptions(1))
		}()

		g.By("Waiting for DNS resolution of the route host")
		err = waitForDNSResolution(oc, execPodName, routeHost, 10*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred(), "DNS resolution failed")

		g.By("Verifying route is reachable over IPv4")
		err = waitForRouteResponse(oc, execPodName, routeHost, "-4", 5*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred(), "route not reachable over IPv4")

		g.By("Verifying route is reachable over IPv6")
		err = waitForRouteResponse(oc, execPodName, routeHost, "-6", 5*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred(), "route not reachable over IPv6")
	})

	g.It("should be reachable via IPv4 through a Classic LB ingress controller on a dual-stack cluster", func() {
		ctx := context.Background()

		shardFQDN := "clb." + baseDomain

		// Deploy the shard first so DNS and LB can provision while we set up the backend.
		g.By("Deploying a new router shard with Classic LB")
		shardIC, err := deployNewRouterShard(ctx, oc, 10*time.Minute, shardConfig{
			Domain: shardFQDN,
			Type:   oc.Namespace(),
			LoadBalancer: &operatorv1.LoadBalancerStrategy{
				Scope: operatorv1.ExternalLoadBalancer,
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSClassicLoadBalancer,
					},
				},
			},
		})
		defer func() {
			if shardIC != nil {
				if err := oc.AdminOperatorClient().OperatorV1().IngressControllers(shardIC.Namespace).Delete(ctx, shardIC.Name, metav1.DeleteOptions{}); err != nil {
					fmt.Fprintf(g.GinkgoWriter, "deleting ingress controller failed: %v\n", err)
				}
			}
		}()
		o.Expect(err).NotTo(o.HaveOccurred(), "new router shard did not rollout")

		g.By("Labelling the namespace for the shard")
		err = oc.AsAdmin().Run("label").Args("namespace", oc.Namespace(), "type="+oc.Namespace()).Execute()
		o.Expect(err).NotTo(o.HaveOccurred())

		g.By("Creating backend service and pod")
		createBackendServiceAndPod(ctx, oc, "classic-backend")

		g.By("Creating an edge-terminated route")
		routeHost := "classic-test." + shardFQDN
		createEdgeRoute(ctx, oc, "classic-route", routeHost, "classic-backend", oc.Namespace())

		g.By("Waiting for the route to be admitted")
		waitForRouteAdmitted(ctx, oc, "classic-route", routeHost, 5*time.Minute)

		g.By("Creating exec pod for curl tests")
		execPodName := createExecPod(ctx, oc, "execpod")
		defer func() {
			_ = oc.AdminKubeClient().CoreV1().Pods(oc.Namespace()).Delete(ctx, execPodName, *metav1.NewDeleteOptions(1))
		}()

		g.By("Waiting for DNS resolution of the route host")
		err = waitForDNSResolution(oc, execPodName, routeHost, 10*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred(), "DNS resolution failed")

		g.By("Verifying route is reachable over IPv4")
		err = waitForRouteResponse(oc, execPodName, routeHost, "-4", 5*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred(), "route not reachable over IPv4")
	})
})

// requireAWSDualStack skips the test unless the cluster is running on AWS
// with a dual-stack IP family. Uses AdminConfigClient.
func requireAWSDualStack(ctx context.Context, oc *CLI) {
	infra, err := oc.AdminConfigClient().ConfigV1().Infrastructures().Get(ctx, "cluster", metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get infrastructure CR")

	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		g.Skip("Test requires AWS platform")
	}
	if infra.Status.PlatformStatus.AWS == nil {
		g.Skip("AWS platform status is not set")
	}
	ipFamily := infra.Status.PlatformStatus.AWS.IPFamily
	if ipFamily != configv1.DualStackIPv4Primary && ipFamily != configv1.DualStackIPv6Primary {
		g.Skip(fmt.Sprintf("Test requires DualStack IPFamily, got %q", ipFamily))
	}
}

// createBackendServiceAndPod creates a dual-stack service and a backend pod
// that serves HTTP 200 on port 8080. Uses AdminKubeClient to create
// resources and KubeClient to wait for pod readiness, matching origin's
// split between AdminKubeClient and KubeClient.
func createBackendServiceAndPod(ctx context.Context, oc *CLI, name string) {
	ns := oc.Namespace()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"app": name},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			IPFamilyPolicy: func() *corev1.IPFamilyPolicy {
				p := corev1.IPFamilyPolicyPreferDualStack
				return &p
			}(),
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       8080,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromInt32(8080),
				},
			},
		},
	}
	adminKC := oc.AdminKubeClient()
	_, err := adminKC.CoreV1().Services(ns).Create(ctx, service, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"app": name},
		},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: ptr.To[int64](1),
			Containers: []corev1.Container{
				{
					Name:            "server",
					Image:           shellImage(),
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command: []string{"/bin/bash", "-c", `while true; do
printf "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nContent-Type: text/plain\r\n\r\nOK" | ncat -l 8080 --send-only || true
done`},
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 8080,
							Name:          "http",
							Protocol:      corev1.ProtocolTCP,
						},
					},
				},
			},
		},
	}
	_, err = adminKC.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred())

	// Wait for pod to be running using the regular user client,
	// matching origin's use of oc.KubeClient() for this check.
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		p, err := oc.KubeClient().CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return p.Status.Phase == corev1.PodRunning, nil
	})
	o.Expect(err).NotTo(o.HaveOccurred(), "backend pod not running")
}
