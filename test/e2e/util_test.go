//go:build e2e
// +build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/utils/pointer"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// buildEchoPod returns a pod definition for an socat-based echo server.
func buildEchoPod(name, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"app": name,
			},
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					// Note that HTTP/1.0 will strip the HSTS response header
					Args: []string{
						"TCP4-LISTEN:8080,reuseaddr,fork",
						`EXEC:'/bin/bash -c \"printf \\\"HTTP/1.0 200 OK\r\n\r\n\\\"; sed -e \\\"/^\r/q\\\"\"'`,
					},
					Command: []string{"/bin/socat"},
					Image:   "image-registry.openshift-image-registry.svc:5000/openshift/tools:latest",
					Name:    "echo",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: int32(8080),
							Protocol:      corev1.ProtocolTCP,
						},
					},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: pointer.Bool(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
						RunAsNonRoot: pointer.Bool(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
				},
			},
		},
	}
}

// generateUnprivilegedSecurityContext returns a SecurityContext with the minimum possible privileges that satisfy
// restricted pod security requirements
func generateUnprivilegedSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: pointer.Bool(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		RunAsNonRoot: pointer.Bool(true),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

func waitForHTTPClientCondition(t *testing.T, httpClient *http.Client, req *http.Request, interval, timeout time.Duration, compareFunc func(*http.Response) bool) error {
	t.Helper()
	return wait.PollImmediate(interval, timeout, func() (done bool, err error) {
		resp, err := httpClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			return compareFunc(resp), nil
		} else {
			t.Logf("retrying client call due to: %+v", err)
			return false, nil
		}
	})
}

// buildEchoService returns a service definition for an HTTP service.
func buildEchoService(name, namespace string, labels map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       int32(80),
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromInt(8080),
				},
			},
			Selector: labels,
		},
	}
}

// buildCurlPod returns a pod definition for a pod with the given name and image
// and in the given namespace that curls the specified host and address.
func buildCurlPod(name, namespace, image, host, address string, extraArgs ...string) *corev1.Pod {
	curlArgs := []string{
		"-s",
		"--retry", "300", "--retry-delay", "1", "--max-time", "2",
	}
	curlArgs = append(curlArgs, extraArgs...)
	curlArgs = append(curlArgs, "http://"+host)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: pointer.Int64(0),
			Containers: []corev1.Container{
				{
					Name:    "curl",
					Image:   image,
					Command: []string{"/bin/curl"},
					Args:    curlArgs,
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: pointer.Bool(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
						RunAsNonRoot: pointer.Bool(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

// buildExecPod returns a pod definition for a pod with the given name and image
// and in the given namespace that sleeps for 4 hours (or until it is deleted),
// which can be used to exec commands inside the pod.
func buildExecPod(name, namespace, image string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "execpod",
					Image:   image,
					Command: []string{"/bin/sleep"},
					Args:    []string{"4h"},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: pointer.Bool(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
						RunAsNonRoot: pointer.Bool(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

// buildSlowHTTPDPod returns a pod that responds to HTTP requests slowly.
func buildSlowHTTPDPod(name, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"app": "slow-httpd",
			},
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Args: []string{
						"TCP4-LISTEN:8080,reuseaddr,fork",
						`EXEC:'/bin/bash -c \"sleep 40; printf \\\"HTTP/1.0 200 OK\r\n\r\nfin\r\n\\\"\"'`,
					},
					Command: []string{"/bin/socat"},
					Image:   "image-registry.openshift-image-registry.svc:5000/openshift/tools:latest",
					Name:    "echo",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: int32(8080),
							Protocol:      corev1.ProtocolTCP,
						},
					},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: pointer.Bool(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
						RunAsNonRoot: pointer.Bool(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
				},
			},
		},
	}
}

// buildRoute returns a route definition targeting the specified service.
func buildRoute(name, namespace, serviceName string) *routev1.Route {
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: serviceName,
			},
		},
	}
}

// buildRoute returns a route definition targeting the specified service.
func buildRouteWithHost(name, namespace, serviceName, host string) *routev1.Route {
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: routev1.RouteSpec{
			Host: host,
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: serviceName,
			},
		},
	}
}

// buildRouteWithHSTS returns a route definition with the specified HSTS annotation.
// Overwrites Spec.Host and TLS
func buildRouteWithHSTS(podName, namespace, serviceName, domain, annotation string) *routev1.Route {
	route := buildRoute(podName, namespace, serviceName)
	route.Spec.Host = domain
	route.Spec.TLS = &routev1.TLSConfig{Termination: routev1.TLSTerminationEdge}
	if route.Annotations == nil {
		route.Annotations = map[string]string{}
	}
	route.Annotations["haproxy.router.openshift.io/hsts_header"] = annotation

	return route
}

func getIngressController(t *testing.T, client client.Client, name types.NamespacedName, timeout time.Duration) (*operatorv1.IngressController, error) {
	t.Helper()
	ic := operatorv1.IngressController{}
	if err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, &ic); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to get %q: %v", name, err)
	}
	return &ic, nil
}

func getDeployment(t *testing.T, client client.Client, name types.NamespacedName, timeout time.Duration) (*appsv1.Deployment, error) {
	t.Helper()
	dep := appsv1.Deployment{}
	if err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, &dep); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, fmt.Errorf("Failed to get %q: %v", name, err)
	}
	return &dep, nil
}

func podExec(t *testing.T, pod corev1.Pod, stdout, stderr *bytes.Buffer, cmd []string) error {
	t.Helper()
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	cl, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}
	req := cl.CoreV1().RESTClient().Post().Resource("pods").
		Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").
		Param("container", pod.Spec.Containers[0].Name).
		VersionedParams(&corev1.PodExecOptions{
			Command: cmd,
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(kubeConfig, "POST", req.URL())
	if err != nil {
		return err
	}
	return exec.Stream(remotecommand.StreamOptions{
		Stdout: stdout,
		Stderr: stderr,
	})
}

// cmpProbes compares two probes on their timeoutSeconds, periodSeconds,
// successThreshold, and failureThreshold parameters.
func cmpProbes(a, b *corev1.Probe) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.TimeoutSeconds != b.TimeoutSeconds {
		return false
	}
	if a.PeriodSeconds != b.PeriodSeconds {
		return false
	}
	if a.SuccessThreshold != b.SuccessThreshold {
		return false
	}
	if a.FailureThreshold != b.FailureThreshold {
		return false
	}
	return true
}

// probe returns a Probe with the specified parameters.
func probe(timeout, period, success, failure int) *corev1.Probe {
	return &corev1.Probe{
		TimeoutSeconds:   int32(timeout),
		PeriodSeconds:    int32(period),
		SuccessThreshold: int32(success),
		FailureThreshold: int32(failure),
	}
}

// updateIngressControllerSpecWithRetryOnConflict gets a fresh copy of
// the named ingresscontroller, calls mutateSpecFn() where callers can
// modify fields of the spec, and then updates the ingresscontroller
// object. If there is a conflict error on update then the complete
// sequence of get, mutate, and update is retried until timeout is
// reached.
func updateIngressControllerSpecWithRetryOnConflict(t *testing.T, name types.NamespacedName, timeout time.Duration, mutateSpecFn func(*operatorv1.IngressControllerSpec)) error {
	ic := operatorv1.IngressController{}
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, &ic); err != nil {
			t.Logf("error getting ingress controller %v: %v, retrying...", name, err)
			return false, nil
		}
		mutateSpecFn(&ic.Spec)
		if err := kclient.Update(context.TODO(), &ic); err != nil {
			if errors.IsConflict(err) {
				t.Logf("conflict when updating ingress controller %v: %v, retrying...", name, err)
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

// updateIngressConfigSpecWithRetryOnConflict gets a fresh copy of the
// name ingress config, calls updateSpecFn() where callers can modify
// fields of the spec, and then updates the ingress config object. If
// there is a conflict error on update then the complete operation of
// get, mutate, and update is retried until timeout is reached.
func updateIngressConfigSpecWithRetryOnConflict(t *testing.T, name types.NamespacedName, timeout time.Duration, mutateSpecFn func(*configv1.IngressSpec)) error {
	ingressConfig := configv1.Ingress{}
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, &ingressConfig); err != nil {
			t.Logf("error getting ingress config %v: %v, retrying...", name, err)
			return false, nil
		}
		mutateSpecFn(&ingressConfig.Spec)
		if err := kclient.Update(context.TODO(), &ingressConfig); err != nil {
			if errors.IsConflict(err) {
				t.Logf("conflict when updating ingress config %v: %v, retrying...", name, err)
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

// updateInfrastructureConfigSpecWithRetryOnConflict gets a fresh copy
// of the named infrastructure object, calls updateSpecFn() where
// callers can modify fields of the spec, and then updates the infrastructure
// config object. If there is a conflict error on update then the
// complete operation of get, mutate, and update is retried until
// timeout is reached.
func updateInfrastructureConfigSpecWithRetryOnConflict(t *testing.T, name types.NamespacedName, timeout time.Duration, mutateSpecFn func(*configv1.InfrastructureSpec)) error {
	infraConfig := configv1.Infrastructure{}
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, &infraConfig); err != nil {
			t.Logf("error getting infrastructure config %v: %v, retrying...", name, err)
			return false, nil
		}
		mutateSpecFn(&infraConfig.Spec)
		if err := kclient.Update(context.TODO(), &infraConfig); err != nil {
			if errors.IsConflict(err) {
				t.Logf("conflict when updating infrastructure config %v: %v, retrying...", name, err)
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

// verifyExternalIngressController verifies connectivity between the router
// and a test workload by making a http call using the hostname passed to it.
// This hostname must be the domain associated with the ingresscontroller under test.
// This function overrides the HTTP HOST header with hostname argument, which is needed
// when no DNS records have been created on the cloud provider for said hostname.
func verifyExternalIngressController(t *testing.T, name types.NamespacedName, hostname, address string) {
	t.Helper()
	echoPod := buildEchoPod(name.Name, name.Namespace)
	if err := kclient.Create(context.TODO(), echoPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", echoPod.Namespace, echoPod.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoPod); err != nil {
			t.Fatalf("failed to delete pod %s/%s: %v", echoPod.Namespace, echoPod.Name, err)
		}
	}()

	echoService := buildEchoService(echoPod.Name, echoPod.Namespace, echoPod.ObjectMeta.Labels)
	if err := kclient.Create(context.TODO(), echoService); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", echoService.Namespace, echoService.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoService); err != nil {
			t.Fatalf("failed to delete service %s/%s: %v", echoService.Namespace, echoService.Name, err)
		}
	}()

	echoRoute := buildRouteWithHost(echoPod.Name, echoPod.Namespace, echoService.Name, hostname)
	if err := kclient.Create(context.TODO(), echoRoute); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoRoute); err != nil {
			t.Fatalf("failed to delete route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
		}
	}()

	// If we have a DNS as an external IP address, make sure we can resolve it before moving on.
	// This just limits the number of "could not resolve host" errors which can be confusing.
	if net.ParseIP(address) == nil {
		if err := wait.PollImmediate(10*time.Second, 5*time.Minute, func() (bool, error) {
			_, err := net.LookupIP(address)
			if err != nil {
				t.Logf("waiting for loadbalancer domain %s to resolve...", address)
				return false, nil
			}
			return true, nil
		}); err != nil {
			t.Fatalf("loadbalancer domain %s was unable to resolve:", address)
		}
	}

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s", address), nil)
	if err != nil {
		t.Fatalf("failed to build client request: %v", err)
	}
	// we use HOST header to map to the domain associated on the ingresscontroller.
	// This ensures our http call is routed to the correct router.
	req.Host = hostname

	httpClient := http.Client{Timeout: 5 * time.Second}
	err = waitForHTTPClientCondition(t, &httpClient, req, 10*time.Second, 10*time.Minute, func(r *http.Response) bool {
		if r.StatusCode == http.StatusOK {
			t.Logf("verified connectivity with workload with req %v and response %v", req.URL, r.StatusCode)
			return true
		}
		return false
	})
	if err != nil {
		t.Fatalf("failed to verify connectivity with workload with reqURL %s using external client: %v", req.URL, err)
	}
}

// verifyInternalIngressController verifies connectivity between the router
// and a test workload by spawning a curl pod on the internal network.
// The hostname must be the domain associated with the ingresscontroller under test.
// This function overrides the HTTP HOST header to the hostname argument, which ensures
// correct routing of the curl request.
func verifyInternalIngressController(t *testing.T, name types.NamespacedName, hostname, address, image string) {
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	echoPod := buildEchoPod(name.Name, name.Namespace)
	if err := kclient.Create(context.TODO(), echoPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", echoPod.Namespace, echoPod.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoPod); err != nil {
			t.Fatalf("failed to delete pod %s/%s: %v", echoPod.Namespace, echoPod.Name, err)
		}
	}()

	echoService := buildEchoService(echoPod.Name, echoPod.Namespace, echoPod.ObjectMeta.Labels)
	if err := kclient.Create(context.TODO(), echoService); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", echoService.Namespace, echoService.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoService); err != nil {
			t.Fatalf("failed to delete service %s/%s: %v", echoService.Namespace, echoService.Name, err)
		}
	}()

	echoRoute := buildRouteWithHost(echoPod.Name, echoPod.Namespace, echoService.Name, hostname)
	if err := kclient.Create(context.TODO(), echoRoute); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoRoute); err != nil {
			t.Fatalf("failed to delete route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
		}
	}()

	extraArgs := []string{
		"--header", "HOST:" + echoRoute.Spec.Host,
		"-v",
		"--retry-delay", "20",
		"--max-time", "10",
	}
	clientPodName := types.NamespacedName{Namespace: name.Namespace, Name: "curl-" + name.Name}
	clientPodSpec := buildCurlPod(clientPodName.Name, clientPodName.Namespace, image, address, echoRoute.Spec.Host, extraArgs...)
	clientPod := clientPodSpec.DeepCopy()
	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("failed to create pod %q: %v", clientPodName, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientPod); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Fatalf("failed to delete pod %q: %v", clientPodName, err)
		}
	}()

	var curlPodLogs string
	err = wait.PollImmediate(10*time.Second, 10*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), clientPodName, clientPod); err != nil {
			t.Logf("error getting client pod %q: %v, retrying...", clientPodName, err)
			return false, nil
		}
		// First check if client curl pod is still starting or not running.
		if clientPod.Status.Phase == corev1.PodPending {
			t.Logf("waiting for client pod %q to start", clientPodName)
			return false, nil
		}
		readCloser, err := client.CoreV1().Pods(clientPod.Namespace).GetLogs(clientPod.Name, &corev1.PodLogOptions{
			Container: "curl",
			Follow:    false,
		}).Stream(context.TODO())
		if err != nil {
			t.Logf("failed to read output from pod %s: %v", clientPod.Name, err)
			return false, nil
		}
		scanner := bufio.NewScanner(readCloser)
		defer func() {
			if err := readCloser.Close(); err != nil {
				t.Errorf("failed to close reader for pod %s: %v", clientPod.Name, err)
			}
		}()
		curlPodLogs = ""
		for scanner.Scan() {
			line := scanner.Text()
			curlPodLogs += line + "\n"
			if strings.Contains(line, "HTTP/1.0 200 OK") {
				t.Logf("verified connectivity with workload with address: %s with response %s", address, line)
				return true, nil
			}
		}
		// If failed or succeeded, the pod is stopped, but didn't provide us 200 response, let's try again.
		if clientPod.Status.Phase == corev1.PodFailed || clientPod.Status.Phase == corev1.PodSucceeded {
			t.Logf("client pod %q has stopped...restarting. Curl Pod Logs:\n%s", clientPodName, curlPodLogs)
			if err := kclient.Delete(context.TODO(), clientPod); err != nil && errors.IsNotFound(err) {
				t.Fatalf("failed to delete pod %q: %v", clientPodName, err)
			}
			// Wait for deletion to prevent a race condition. Use PollInfinite since we are already in a Poll.
			wait.PollInfinite(5*time.Second, func() (bool, error) {
				err = kclient.Get(context.TODO(), clientPodName, clientPod)
				if !errors.IsNotFound(err) {
					t.Logf("waiting for %q: to be deleted", clientPodName)
					return false, nil
				}
				return true, nil
			})
			clientPod = clientPodSpec.DeepCopy()
			if err := kclient.Create(context.TODO(), clientPod); err != nil {
				t.Fatalf("failed to create pod %q: %v", clientPodName, err)
			}
			return false, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("failed to verify connectivity with workload with address: %s using internal curl client. Curl Pod Logs:\n%s", address, curlPodLogs)
	}
}

// assertDeleted tries to delete a cluster resource, and causes test failure if the delete fails.
func assertDeleted(t *testing.T, cl client.Client, thing client.Object) {
	t.Helper()
	if err := cl.Delete(context.TODO(), thing); err != nil {
		if errors.IsNotFound(err) {
			return
		}
		t.Fatalf("Failed to delete %s: %v", thing.GetName(), err)
	} else {
		t.Logf("Deleted %s", thing.GetName())
	}
}

// assertDeletedWaitForCleanup tries to delete a cluster resource, and waits for it to actually be cleaned up before
// returning. It causes test failure if the delete fails or if the cleanup times out.
func assertDeletedWaitForCleanup(t *testing.T, cl client.Client, thing client.Object) {
	t.Helper()
	thingName := types.NamespacedName{
		Name:      thing.GetName(),
		Namespace: thing.GetNamespace(),
	}
	assertDeleted(t, cl, thing)
	if err := wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
		if err := cl.Get(context.TODO(), thingName, thing); err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	}); err != nil {
		t.Fatalf("Timed out waiting for %s to be cleaned up: %v", thing.GetName(), err)
	}
}

// getRouteHost returns the host name of the given route for the named
// IngressController.  If the IngressController has not added its host name to
// the route's status, this function causes a fatal error.  For simplicity, this
// function does not check the "Admitted" status condition, so it will return
// the host name if it is set even if the IngressController has rejected the
// route.
func getRouteHost(t *testing.T, route *routev1.Route, router string) string {
	t.Helper()

	if route == nil {
		t.Fatal("route is nil")
		return ""
	}

	for _, ingress := range route.Status.Ingress {
		if ingress.RouterName == router {
			return ingress.Host
		}
	}

	t.Fatalf("failed to find host name for default router in route: %#v", route)
	return ""
}

// dumpEventsInNamespace gets the events in the specified namespace and logs
// them.
func dumpEventsInNamespace(t *testing.T, ns string) {
	t.Helper()

	eventList := &corev1.EventList{}
	if err := kclient.List(context.TODO(), eventList, client.InNamespace(ns)); err != nil {
		t.Errorf("failed to list events for namespace %s: %v", ns, err)
		return
	}

	for _, e := range eventList.Items {
		t.Log(e.FirstTimestamp, e.Source, e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Reason, e.Message)
	}
}

// createNamespace creates a namespace with the specified name and registers a
// cleanup handler to delete the namespace when the test finishes.
//
// After creating the namespace, this function waits for the "default"
// ServiceAccount and "system:image-pullers" RoleBinding to be created as well,
// which is necessary in order for pods in the new namespace to be able to pull
// images.
func createNamespace(t *testing.T, name string) *corev1.Namespace {
	t.Helper()

	t.Logf("Creating namespace %q...", name)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := kclient.Create(context.TODO(), ns); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	t.Cleanup(func() {
		t.Logf("Dumping events in namespace %q...", name)
		if t.Failed() {
			dumpEventsInNamespace(t, name)
		}
		t.Logf("Deleting namespace %q...", name)
		if err := kclient.Delete(context.TODO(), ns); err != nil {
			t.Errorf("failed to delete namespace %s: %v", ns.Name, err)
		}
	})

	saName := types.NamespacedName{
		Namespace: name,
		Name:      "default",
	}
	t.Logf("Waiting for ServiceAccount %s to be provisioned...", saName)
	if err := wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		var sa corev1.ServiceAccount
		if err := kclient.Get(context.TODO(), saName, &sa); err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		for _, s := range sa.Secrets {
			if strings.Contains(s.Name, "dockercfg") {
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		t.Fatalf(`Timed out waiting for ServiceAccount %s to be provisioned: %v`, saName, err)
	}

	rbName := types.NamespacedName{
		Namespace: name,
		Name:      "system:image-pullers",
	}
	t.Logf("Waiting for RoleBinding %s to be created...", rbName)
	if err := wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		var rb rbacv1.RoleBinding
		if err := kclient.Get(context.TODO(), rbName, &rb); err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}); err != nil {
		t.Fatalf(`Timed out waiting for RoleBinding "default" to be provisioned: %v`, err)
	}

	return ns
}
