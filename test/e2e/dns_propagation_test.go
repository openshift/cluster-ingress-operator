//go:build e2e
// +build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"net"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDNSPropagation(t *testing.T) {
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	// Get exec execImage
	deployment := &appsv1.Deployment{}
	deploymentName := types.NamespacedName{Namespace: controller.DefaultOperandNamespace, Name: "router-default"}
	if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
		t.Fatalf("failed to get deployment %q: %v", deploymentName, err)
	}
	execImage := deployment.Spec.Template.Spec.Containers[0].Image
	// We need a client-go client in order to execute commands in the client pod.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	cl, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	// Get internal cluster cloud DNS resolver's IP by starting a pod with DNSPolicy=Default and looking at /etc/resolv.conf
	internalCloudDNSResolver := getCloudDNSResolverIP(t, execImage, cl)

	// External cluster (test run) is generally always AWS (build05 at time of writing)
	externalCloudDNSResolver := "10.0.0.2"

	// Resolver for to resolve via 8.8.8.8
	googleResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: time.Millisecond * time.Duration(5000),
			}
			return d.DialContext(ctx, network, "8.8.8.8:53")
		},
	}

	// Resolver for to resolve via internalCloudDNSResolver
	awsResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: time.Millisecond * time.Duration(5000),
			}
			return d.DialContext(ctx, network, fmt.Sprintf("%s:53", internalCloudDNSResolver))
		},
	}

	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "dns-propagate"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	icNameWarmup := types.NamespacedName{Namespace: operatorNamespace, Name: "dns-propagate-warmup"}
	domainWarmup := icNameWarmup.Name + "." + dnsConfig.Spec.BaseDomain

	// We need an execImage that we can use for test clients.  The router execImage
	// has dig, so we can use that execImage.
	podName := "client-local-netlookup"
	clientPod := buildExecPod(podName, "openshift-ingress", execImage)
	clientPodName := types.NamespacedName{
		Name:      clientPod.Name,
		Namespace: clientPod.Namespace,
	}
	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("failed to create pod %q: %v", clientPodName, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientPod); err != nil {
			if apierrors.IsNotFound(err) {
				return
			}
			t.Fatalf("failed to delete pod %q: %v", clientPodName, err)
		}
	}()

	// how long do we want to wait for DNS to propagate before giving up?
	dnsPollingTimeout := 15 * time.Minute

	// Test Cases:
	// Static vs. Dynamic vs. LB Address Domains
	// External (Test Runner Cluster) vs. Internal (Ephemeral Cluster) Resolution
	// External (Test Runner Cluster) CoreDNS vs. Google (8.8.8.8) vs. Default (AWS DNS)

	for i := 1; i <= 10; i++ {
		t.Logf("------------------------------------------------")
		t.Logf("Starting Test Round %d", i)
		ic := newLoadBalancerController(icName, domain)
		if err := waitForIngressControllerServiceDeleted(t, ic, 3*time.Minute); err != nil {
			t.Fatalf("failed to wait for ingresscontroller service to be deleted: %v", err)
		}
		icWarmup := newLoadBalancerController(icNameWarmup, domainWarmup)
		if err := waitForIngressControllerServiceDeleted(t, icWarmup, 3*time.Minute); err != nil {
			t.Fatalf("failed to wait for ingresscontroller service to be deleted: %v", err)
		}

		icCreateStart := time.Now()
		if err := kclient.Create(context.Background(), ic); err != nil {
			t.Fatalf("expected ingresscontroller creation failed: %v", err)
		}
		if err := kclient.Create(context.Background(), icWarmup); err != nil {
			t.Fatalf("expected ingresscontroller creation failed: %v", err)
		}
		t.Cleanup(func() {
			assertIngressControllerDeleted(t, kclient, ic)
			assertIngressControllerDeleted(t, kclient, icWarmup)
		})

		staticDomain := "static." + domain
		staticDomainWarmup := "static." + domainWarmup

		domainsToResolve := map[string]func() string{
			"STATIC": func() string { return staticDomain },
			"DYNAMIC": func() string {
				// Generate 8 digit long random number
				rand.Seed(time.Now().UnixNano())
				randomNumber := rand.Intn(99999999-10000000+1) + 10000000
				return fmt.Sprintf("dyn%d.%s", randomNumber, domain)
			},
		}

		var lbAddress, lbAddressWarmup string
		var lbProvisionFinish, lbProvisionFinishWarmup time.Time
		// Once the LB Address is set, we'll start querying that.
		domainsToResolve["LB_ADDRESS"] = func() string {
			// Already have it.
			if lbAddress != "" {
				return lbAddress
			}
			lbService := &corev1.Service{}
			if err := kclient.Get(context.Background(), controller.LoadBalancerServiceName(ic), lbService); err != nil {
				t.Logf("failed to get %q service: %v, retrying ...", controller.LoadBalancerServiceName(ic), err)
				return ""
			}
			if len(lbService.Status.LoadBalancer.Ingress) > 0 && len(lbService.Status.LoadBalancer.Ingress[0].Hostname) > 0 {
				lbAddress = lbService.Status.LoadBalancer.Ingress[0].Hostname
				lbProvisionFinish = time.Now()
				t.Logf("lb took %s to provision: %s", time.Now().Sub(icCreateStart), lbAddress)
			}
			return lbAddress
		}

		domainsToResolveWarmup := map[string]func() string{
			"STATIC": func() string { return staticDomainWarmup },
		}

		domainsToResolveWarmup["LB_ADDRESS"] = func() string {
			// Already have it.
			if lbAddressWarmup != "" {
				return lbAddressWarmup
			}
			lbService := &corev1.Service{}
			if err := kclient.Get(context.Background(), controller.LoadBalancerServiceName(icWarmup), lbService); err != nil {
				t.Logf("failed to get %q service: %v, retrying ...", controller.LoadBalancerServiceName(icWarmup), err)
				return ""
			}
			if len(lbService.Status.LoadBalancer.Ingress) > 0 && len(lbService.Status.LoadBalancer.Ingress[0].Hostname) > 0 {
				lbAddressWarmup = lbService.Status.LoadBalancer.Ingress[0].Hostname
				lbProvisionFinishWarmup = time.Now()
				t.Logf("lb took %s to provision: %s", time.Now().Sub(icCreateStart), lbAddressWarmup)
			}
			return lbAddressWarmup
		}

		// Set the number of goroutines to wait for
		var wg sync.WaitGroup

		for label, getDomain := range domainsToResolve {
			// CoreDNS External
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				t.Logf("[External %s @CoreDNS] starting DNS query polling", label)
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, dnsPollingTimeout, false, func(ctx context.Context) (bool, error) {
					queryStart := time.Now()
					domain := getDomain()
					if domain == "" {
						t.Logf("[External %s @CoreDNS] waiting for domain to populate...", label)
						return false, nil
					}
					ips, err := net.LookupIP(domain)
					if err == nil {
						//	t.Logf("[External %s @CoreDNS] query_time=%s total_time=%s FAILED: %v", time.Now().Sub(queryStart), time.Now().Sub(start), label, err)
						//} else {
						t.Logf("[External %s @CoreDNS] query_time=%s total_time=%s after_lb_provision=%s SUCCESS: resolved %s to %v", label, time.Now().Sub(queryStart), time.Now().Sub(start), time.Now().Sub(lbProvisionFinish), domain, ips)
						return true, nil
					}
					return false, nil
				})
				if err != nil {
					t.Logf("[External %s @CoreDNS] failed to resolve DNS", label)
				}
			}()

			// Cluster DNS Resolver (Default) External
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				t.Logf("[External %s @%s] starting DNS query polling", label, externalCloudDNSResolver)
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, dnsPollingTimeout, false, func(ctx context.Context) (bool, error) {
					queryStart := time.Now()
					domain := getDomain()
					if domain == "" {
						t.Logf("[External %s @%s] waiting for domain to populate...", label, externalCloudDNSResolver)
						return false, nil
					}
					ips, err := awsResolver.LookupHost(ctx, domain)
					if err == nil {
						//	t.Logf("[External %s @10.0.0.2] query_time=%s total_time=%s FAILED: %v", time.Now().Sub(queryStart), time.Now().Sub(start), label, err)
						//} else {
						t.Logf("[External %s @%s] query_time=%s total_time=%s after_lb_provision=%s SUCCESS: resolved %s to %v", label, externalCloudDNSResolver, time.Now().Sub(queryStart), time.Now().Sub(start), time.Now().Sub(lbProvisionFinish), domain, ips)
						return true, nil
					}
					return false, nil
				})
				if err != nil {
					t.Logf("[External %s @%s] failed to resolve DNS", label, externalCloudDNSResolver)
				}
			}()

			// Google External
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				t.Logf("[External %s @8.8.8.8] starting DNS query polling", label)
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, dnsPollingTimeout, false, func(ctx context.Context) (bool, error) {
					queryStart := time.Now()
					domain := getDomain()
					if domain == "" {
						t.Logf("[External %s @8.8.8.8] waiting for domain to populate...", label)
						return false, nil
					}
					ips, err := googleResolver.LookupHost(ctx, domain)
					if err == nil {
						//	t.Logf("[External %s @8.8.8.8] query_time=%s total_time=%s FAILED: %v", time.Now().Sub(queryStart), time.Now().Sub(start), label, err)
						//} else {
						t.Logf("[External %s @8.8.8.8] query_time=%s total_time=%s after_lb_provision=%s SUCCESS: resolved %s to %v", label, time.Now().Sub(queryStart), time.Now().Sub(start), time.Now().Sub(lbProvisionFinish), domain, ips)
						return true, nil
					}
					return false, nil
				})
				if err != nil {
					t.Logf("[External %s @8.8.8.8] failed to resolve DNS", label)
				}
			}()

			// Internal CoreDNS
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				t.Logf("[Internal %s @CoreDNS] starting DNS query polling", label)
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, dnsPollingTimeout, false, func(ctx context.Context) (bool, error) {
					queryStart := time.Now()
					domain := getDomain()
					if domain == "" {
						t.Logf("[Internal %s @CoreDNS] waiting for domain to populate...", label)
						return false, nil
					}
					stdOut, err := clusterLocalNetLookup(t, cl, kubeConfig, clientPodName, domain, "", false)
					if err != nil {
						t.Logf("[Internal %s @CoreDNS] error with cluster local net lookup: %v", label, err)
						return false, nil
					}
					if stdOut != "" {
						//	t.Logf("[Internal %s @CoreDNS] query_time=%s total_time=%s FAILED: %v", time.Now().Sub(queryStart), time.Now().Sub(start), label, err)
						//} else {
						t.Logf("[Internal %s @CoreDNS] query_time=%s total_time=%s after_lb_provision=%s SUCCESS: resolved %s to %v", label, time.Now().Sub(queryStart), time.Now().Sub(start), time.Now().Sub(lbProvisionFinish), domain, stdOut)
						return true, nil
					}
					return false, nil
				})
				if err != nil {
					t.Logf("[Internal %s @CoreDNS] failed to resolve DNS", label)
				}
			}()

			// Internal CloudDNS Resolver
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				t.Logf("[Internal %s @%s] starting DNS query polling", label, internalCloudDNSResolver)
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, dnsPollingTimeout, false, func(ctx context.Context) (bool, error) {
					queryStart := time.Now()
					domain := getDomain()
					if domain == "" {
						t.Logf("[Internal %s @%s] waiting for domain to populate...", label, internalCloudDNSResolver)
						return false, nil
					}
					stdOut, err := clusterLocalNetLookup(t, cl, kubeConfig, clientPodName, domain, internalCloudDNSResolver, false)
					if err != nil {
						t.Logf("[Internal %s @%s] error with cluster local net lookup: %v", label, internalCloudDNSResolver, err)
						return false, nil
					}
					if stdOut != "" {
						//	t.Logf("[Internal %s @10.0.0.2] query_time=%s total_time=%s FAILED: %v", time.Now().Sub(queryStart), time.Now().Sub(start), label, err)
						//} else {
						t.Logf("[Internal %s @%s] query_time=%s total_time=%s after_lb_provision=%s SUCCESS: resolved %s to %v", label, internalCloudDNSResolver, time.Now().Sub(queryStart), time.Now().Sub(start), time.Now().Sub(lbProvisionFinish), domain, stdOut)
						return true, nil
					}
					return false, nil
				})
				if err != nil {
					t.Logf("[Internal %s @%s] failed to resolve DNS", label, internalCloudDNSResolver)
				}
			}()
		}

		// A separate IngressController & domains for warmup testing. This is just a delay until we try querying.
		//warmupDuration := time.Duration(i) * 60 * time.Second
		warmupDuration := 6 * time.Minute
		for label, getDomain := range domainsToResolveWarmup {
			// Internal CloudDNS Resolver (WARMUP)
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Sleep for the warmup
				time.Sleep(warmupDuration)
				start := time.Now()
				t.Logf("[Internal Warmup=%s %s @%s] starting DNS query polling", warmupDuration, label, internalCloudDNSResolver)
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, dnsPollingTimeout, false, func(ctx context.Context) (bool, error) {
					queryStart := time.Now()
					domain := getDomain()
					if domain == "" {
						t.Logf("[Internal Warmup=%s %s @%s] waiting for domain to populate...", warmupDuration, label, internalCloudDNSResolver)
						return false, nil
					}
					stdOut, err := clusterLocalNetLookup(t, cl, kubeConfig, clientPodName, domain, internalCloudDNSResolver, false)
					if err != nil {
						t.Logf("[Internal Warmup=%s %s @%s] error with cluster local net lookup: %v", warmupDuration, label, internalCloudDNSResolver, err)
						return false, nil
					}
					if stdOut != "" {
						//	t.Logf("[Internal Warmup %s @10.0.0.2] query_time=%s total_time=%s FAILED: %v", time.Now().Sub(queryStart), time.Now().Sub(start), label, err)
						//} else {
						t.Logf("[Internal Warmup=%s %s @%s] query_time=%s total_time=%s after_lb_provision=%s SUCCESS: resolved %s to %v", warmupDuration, label, internalCloudDNSResolver, time.Now().Sub(queryStart), time.Now().Sub(start), time.Now().Sub(lbProvisionFinishWarmup), domain, stdOut)
						return true, nil
					}
					return false, nil
				})
				if err != nil {
					t.Logf("[Internal Warmup=%s %s @%s] failed to resolve DNS", warmupDuration, label, internalCloudDNSResolver)
				}
			}()
		}
		// Wait for everything to finish
		wg.Wait()
		t.Logf("finished round %d...deleting ingresscontroller", i)
		assertIngressControllerDeleted(t, kclient, ic)
		assertIngressControllerDeleted(t, kclient, icWarmup)
	}
	t.Fatal("Failing so that we can observe logs and output!")
}

func clusterLocalNetLookup(t *testing.T, cl *kubernetes.Clientset, kubeConfig *rest.Config, clientPodName types.NamespacedName, host string, resolver string, useTcp bool) (string, error) {
	clientPod := &corev1.Pod{}
	if err := kclient.Get(context.TODO(), clientPodName, clientPod); err != nil {
		return "", fmt.Errorf("failed to get client pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
	}
	if clientPod.Status.Phase != corev1.PodRunning {
		return "", fmt.Errorf("client pod %s/%s is not running yet, it is %v", clientPod.Namespace, clientPod.Name, clientPod.Status.Phase)
	}
	req := cl.CoreV1().RESTClient().Post().Resource("pods").
		Namespace(clientPod.Namespace).Name(clientPod.Name).
		SubResource("exec").
		Param("container", clientPod.Spec.Containers[0].Name)
	cmd := []string{
		"/bin/dig", "+short", host,
	}
	if resolver != "" {
		cmd = append(cmd, "@"+resolver)
	}
	if useTcp {
		cmd = append(cmd, "+tcp")
	}
	req.VersionedParams(&corev1.PodExecOptions{
		Command: cmd,
		Stdout:  true,
		Stderr:  true,
	}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(kubeConfig, "POST", req.URL())
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	err = exec.Stream(remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	stdoutStr := stdout.String()
	//t.Logf("command: %s\nstdout:\n%s\nstderr:\n%s\n", strings.Join(cmd, " "), stdoutStr, stderr.String())
	if err != nil {
		return "", err
	}
	return stdoutStr, nil
}

// getCloudDNSResolverIP determines the IP of the DNS resolver for the cloud.
func getCloudDNSResolverIP(t *testing.T, image string, client *kubernetes.Clientset) string {
	podName := "cloud-dns-ip"
	cloudDNSIPPod := buildExecPod(podName, "openshift-ingress", image)
	cloudDNSIPPod.Spec.Containers[0].Command = []string{"cat"}
	cloudDNSIPPod.Spec.Containers[0].Args = []string{"/etc/resolv.conf"}
	cloudDNSIPPod.Spec.DNSPolicy = corev1.DNSDefault

	cloudDNSIPPodName := types.NamespacedName{
		Name:      cloudDNSIPPod.Name,
		Namespace: cloudDNSIPPod.Namespace,
	}
	if err := kclient.Create(context.TODO(), cloudDNSIPPod); err != nil {
		t.Fatalf("failed to create pod %q: %v", cloudDNSIPPodName, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), cloudDNSIPPod); err != nil {
			if apierrors.IsNotFound(err) {
				return
			}
			t.Fatalf("failed to delete pod %q: %v", cloudDNSIPPodName, err)
		}
	}()
	var cloudDNSResolverIP string
	err := wait.PollImmediate(2*time.Second, 3*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), cloudDNSIPPodName, cloudDNSIPPod); err != nil {
			t.Logf("failed to get pod %q: %v", cloudDNSIPPodName, err)
			return false, nil
		}
		// First check if client curl pod is still starting or not running.
		if cloudDNSIPPod.Status.Phase == corev1.PodPending {
			t.Logf("waiting for pod %q to start", cloudDNSIPPodName)
			return false, nil
		}
		readCloser, err := client.CoreV1().Pods(cloudDNSIPPod.Namespace).GetLogs(cloudDNSIPPod.Name, &corev1.PodLogOptions{
			Container: "execpod",
			Follow:    false,
		}).Stream(context.Background())
		if err != nil {
			t.Logf("failed to read output from pod %s: %v", cloudDNSIPPod.Name, err)
			return false, nil
		}
		scanner := bufio.NewScanner(readCloser)
		defer func() {
			if err := readCloser.Close(); err != nil {
				t.Errorf("failed to close reader for pod %s: %v", cloudDNSIPPod.Name, err)
			}
		}()
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "nameserver") {
				cloudDNSResolverIP = strings.Split(line, " ")[1]
				t.Logf("cloud DNS Resolver IP is: %s", cloudDNSResolverIP)
				return true, nil
			} else {
				t.Logf("cloud-dns-ip pod logs aren't an IP: %s", line)
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for pod %q to become ready: %v", cloudDNSIPPodName, err)
	}
	return cloudDNSResolverIP
}
