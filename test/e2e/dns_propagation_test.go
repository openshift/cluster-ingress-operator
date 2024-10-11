//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	configv1 "github.com/openshift/api/config/v1"
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
	"sync"
	"testing"
	"time"
)

func TestDNSPropagation(t *testing.T) {
	if infraConfig.Status.PlatformStatus == nil {
		t.Skip("test skipped on nil platform")
	}
	var internalCloudDNSResolver string
	switch infraConfig.Status.PlatformStatus.Type {
	case configv1.AWSPlatformType:
		internalCloudDNSResolver = "10.0.0.2"
	case configv1.IBMCloudPlatformType:
		internalCloudDNSResolver = "10.0.80.11"
	default:
		t.Skipf("test skipped on platform %q", infraConfig.Status.PlatformStatus.Type)

	}

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
	// We need a
	//client-go client in order to execute commands in the client
	// pod.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	cl, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "dns-propagate"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain

	deployment := &appsv1.Deployment{}
	deploymentName := types.NamespacedName{Namespace: controller.DefaultOperandNamespace, Name: "router-default"}
	if err := kclient.Get(context.TODO(), deploymentName, deployment); err != nil {
		t.Fatalf("failed to get deployment %q: %v", deploymentName, err)
	}

	// We need an image that we can use for test clients.  The router image
	// has dig, so we can use that image.
	podName := "client-local-netlookup"
	image := deployment.Spec.Template.Spec.Containers[0].Image
	clientPod := buildExecPod(podName, "openshift-ingress", image)
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

		icCreateStart := time.Now()
		if err := kclient.Create(context.Background(), ic); err != nil {
			t.Fatalf("expected ingresscontroller creation failed: %v", err)
		}
		t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

		staticDomain := "static." + domain

		domainsToResolve := map[string]func() string{
			"STATIC": func() string { return staticDomain },
			"DYNAMIC": func() string {
				// Generate 8 digit long random number
				rand.Seed(time.Now().UnixNano())
				randomNumber := rand.Intn(99999999-10000000+1) + 10000000
				return fmt.Sprintf("dyn%d.%s", randomNumber, domain)
			},
		}

		lbAddress := ""
		var lbProvisionFinish time.Time
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
				t.Logf("lb took %s to provision", time.Now().Sub(icCreateStart))
			}
			return lbAddress
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
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 20*time.Minute, false, func(ctx context.Context) (bool, error) {
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
				t.Logf("[External %s @%s] starting DNS query polling", label, internalCloudDNSResolver)
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 20*time.Minute, false, func(ctx context.Context) (bool, error) {
					queryStart := time.Now()
					domain := getDomain()
					if domain == "" {
						t.Logf("[External %s @%s] waiting for domain to populate...", label, internalCloudDNSResolver)
						return false, nil
					}
					ips, err := awsResolver.LookupHost(ctx, domain)
					if err == nil {
						//	t.Logf("[External %s @10.0.0.2] query_time=%s total_time=%s FAILED: %v", time.Now().Sub(queryStart), time.Now().Sub(start), label, err)
						//} else {
						t.Logf("[External %s @%s] query_time=%s total_time=%s after_lb_provision=%s SUCCESS: resolved %s to %v", label, internalCloudDNSResolver, time.Now().Sub(queryStart), time.Now().Sub(start), time.Now().Sub(lbProvisionFinish), domain, ips)
						return true, nil
					}
					return false, nil
				})
				if err != nil {
					t.Logf("[External %s @%s] failed to resolve DNS", label, internalCloudDNSResolver)
				}
			}()

			// Google External
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				t.Logf("[External %s @8.8.8.8] starting DNS query polling", label)
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 20*time.Minute, false, func(ctx context.Context) (bool, error) {
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

			// CoreDNS Internal
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				t.Logf("[Internal %s @CoreDNS] starting DNS query polling", label)
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 20*time.Minute, false, func(ctx context.Context) (bool, error) {
					queryStart := time.Now()
					domain := getDomain()
					if domain == "" {
						t.Logf("[Internal %s @CoreDNS] waiting for domain to populate...", label)
						return false, nil
					}
					stdOut, err := clusterLocalNetLookup(t, cl, kubeConfig, *clientPod, domain, "", false)
					if err != nil {
						t.Logf("[Internal %s @CoreDNS] error with cluster local net lookup: %v", label, err)
						return true, nil
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
					t.Logf("[Internal %s CoreDNS] failed to resolve DNS", label)
				}
			}()

			// CoreDNS Internal 10.0.0.2
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				t.Logf("[Internal %s @%s] starting DNS query polling", label, internalCloudDNSResolver)
				err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 20*time.Minute, false, func(ctx context.Context) (bool, error) {
					queryStart := time.Now()
					domain := getDomain()
					if domain == "" {
						t.Logf("[Internal %s @%s] waiting for domain to populate...", label, internalCloudDNSResolver)
						return false, nil
					}
					stdOut, err := clusterLocalNetLookup(t, cl, kubeConfig, *clientPod, domain, internalCloudDNSResolver, false)
					if err != nil {
						t.Logf("[Internal %s @%s] error with cluster local net lookup: %v", label, internalCloudDNSResolver, err)
						return true, nil
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
					t.Logf("[Internal %s %s] failed to resolve DNS", label, internalCloudDNSResolver)
				}
			}()
		}

		// Wait for everything to finish
		wg.Wait()
		t.Logf("finished round %d...deleting ingresscontroller", i)
		assertIngressControllerDeleted(t, kclient, ic)
	}
	t.Fatal("Failing so that we can observe logs and output!")
}

func clusterLocalNetLookup(t *testing.T, cl *kubernetes.Clientset, kubeConfig *rest.Config, clientPod corev1.Pod, host string, resolver string, useTcp bool) (string, error) {
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
