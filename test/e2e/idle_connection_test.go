//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
)

// addressCapturingRoundTripper is a custom RoundTripper
// implementation designed to capture the local and remote addresses
// of network connections used during HTTP requests.
type addressCapturingRoundTripper struct {
	BaseTransport *http.Transport
	LocalAddr     string
	RemoteAddr    string
	mu            sync.Mutex
}

// RoundTrip executes a single HTTP transaction while capturing the
// local and remote addresses of the underlying network connection.
//
// This method temporarily wraps the DialContext of the BaseTransport
// to intercept the connection establishment phase of the HTTP
// request. During this phase, the local and remote addresses of the
// connection are captured and stored in the LocalAddr and RemoteAddr
// fields of the addressCapturingRoundTripper instance. Once the
// connection is established, the original DialContext is restored to
// avoid perturbing subsequent transport behaviour.
//
// Connection Reuse / Behaviour:
//   - If the underlying connection is reused (e.g., due to HTTP
//     keep-alive), the previously captured local and remote addresses
//     remain unchanged, as no new connection is established.
//   - If the underlying connection has been closed (e.g., due to a
//     timeout or server behaviour), a new connection is established for
//     the request. The wrapped DialContext ensures the local and remote
//     addresses of the new connection are captured dynamically and
//     stored in the respective fields.
//   - This behaviour ensures that the captured addresses are always
//     up-to-date whenever a new connection is established, while
//     preserving normal HTTP transport functionality.
//
// This method assumes the BaseTransport is a properly configured
// *http.Transport. It does not modify other aspects of the HTTP
// request or response lifecycle.
func (c *addressCapturingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	originalDialContext := c.BaseTransport.DialContext
	if originalDialContext == nil {
		originalDialContext = (&net.Dialer{}).DialContext
	}

	c.BaseTransport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := originalDialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.LocalAddr = conn.LocalAddr().String()
		c.RemoteAddr = conn.RemoteAddr().String()
		c.mu.Unlock()
		return conn, nil
	}

	defer func() {
		c.BaseTransport.DialContext = originalDialContext
	}()

	return c.BaseTransport.RoundTrip(req)
}

// ConnectionEndpoints returns the local and remote addresses of the
// most recent connection used by this round tripper. If no connection
// has been established or the connection information is not
// available, empty strings are returned.
func (c *addressCapturingRoundTripper) ConnectionEndpoints() (localAddr, remoteAddr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.LocalAddr, c.RemoteAddr
}

type idleConnectionTestConfig struct {
	ingressController *operatorv1.IngressController // The IngressController being tested.
	routeHost         string                        // The host assigned to the route.
	routeName         types.NamespacedName          // The name and namespace of the route.
}

type idleConnectionTestAction struct {
	description      string                                                                                                  // A human-readable description of the step
	fetchResponse    func(httpClient *http.Client, elbAddress string, cfg *idleConnectionTestConfig) (string, string, error) // Function to fetch a response
	expectedResponse string                                                                                                  // The expected response value for verification
}

func idleConnectionCreateBackendService(ctx context.Context, t *testing.T, namespace, name, image string) error {
	labels := map[string]string{
		"idle-close-on-response": name,
	}

	_, err := idleConnectionCreateService(ctx, namespace, name, labels)
	if err != nil {
		return fmt.Errorf("failed to create service %s/%s: %w", namespace, name, err)
	}

	pod, err := idleConnectionCreatePod(ctx, namespace, name, image, labels)
	if err != nil {
		return fmt.Errorf("failed to create pod %s: %w", name, err)
	}

	if err := waitForPodReady(t, kclient, pod, 2*time.Minute); err != nil {
		return fmt.Errorf("pod %s is not ready: %w", name, err)
	}

	return nil
}

func idleConnectionCreateService(ctx context.Context, namespace, name string, labels map[string]string) (*corev1.Service, error) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       8080,
				TargetPort: intstr.FromInt32(8080),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}

	if err := kclient.Create(ctx, service); err != nil {
		return nil, fmt.Errorf("failed to create service %s/%s: %w", service.Namespace, service.Name, err)
	}

	return service, nil
}

func idleConnectionCreatePod(ctx context.Context, namespace, name, image string, labels map[string]string) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            name,
					Image:           image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"/usr/bin/ingress-operator"},
					Args:            []string{"serve-http2-test-server"},
					Ports: []corev1.ContainerPort{
						{
							Name:          "http",
							ContainerPort: 8080,
						},
					},
					Env: []corev1.EnvVar{
						{
							Name:  "HTTP2_TEST_SERVER_ENABLE_HTTP_LISTENER",
							Value: "true",
						},
						{
							Name:  "HTTP2_TEST_SERVER_ENABLE_HTTPS_LISTENER",
							Value: "false",
						},
						{
							Name: "POD_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.name",
								},
							},
						},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path:   "/healthz",
								Port:   intstr.FromInt32(8080),
								Scheme: corev1.URISchemeHTTP,
							},
						},
					},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path:   "/healthz",
								Port:   intstr.FromInt32(8080),
								Scheme: corev1.URISchemeHTTP,
							},
						},
					},
					StartupProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path:   "/healthz",
								Port:   intstr.FromInt32(8080),
								Scheme: corev1.URISchemeHTTP,
							},
						},
						InitialDelaySeconds: 0,  // Start checking immediately upon container start.
						PeriodSeconds:       5,  // Perform startup checks every 5 seconds.
						TimeoutSeconds:      2,  // Each startup check must respond within 2 seconds.
						FailureThreshold:    12, // Allow up to 12 failures (60 seconds total) before restarting the container.
					},
					SecurityContext: generateUnprivilegedSecurityContext(),
				},
			},
		},
	}

	if err := kclient.Create(ctx, pod); err != nil {
		return nil, fmt.Errorf("failed to create pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}

	return pod, nil
}

func idleConnectionSwitchRouteService(t *testing.T, routeName types.NamespacedName, routerName, serviceName string) error {
	t.Helper()

	if err := updateRouteWithRetryOnConflict(t, routeName, time.Minute, func(route *routev1.Route) {
		route.Spec.To.Name = serviceName
	}); err != nil {
		return fmt.Errorf("failed to update route %s to point to service %q: %w", routeName, serviceName, err)
	}

	routeAdmittedCondition := routev1.RouteIngressCondition{
		Type:   routev1.RouteAdmitted,
		Status: corev1.ConditionTrue,
	}

	if err := waitForRouteIngressConditions(t, kclient, routeName, routerName, routeAdmittedCondition); err != nil {
		return fmt.Errorf("error waiting for route %s to be admitted: %w", routeName, err)
	}

	// Wait for the router deployment to update the HAProxy
	// configuration and also perform a HAProxy soft-reload.
	time.Sleep(20 * time.Second)

	return nil
}

func idleConnectionFetchResponse(t *testing.T, httpClient *http.Client, elbAddr, host string) (string, string, error) {
	t.Helper()

	url := fmt.Sprintf("http://%s", elbAddr)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to build client request %s: %w", url, err)
	}
	req.Host = host

	resp, err := httpClient.Do(req)
	localAddr, remoteAddr := httpClient.Transport.(*addressCapturingRoundTripper).ConnectionEndpoints()
	if err != nil {
		if localAddr != "" && remoteAddr != "" {
			return "", "", fmt.Errorf("[%s -> %s] %s %s failed: %v", localAddr, remoteAddr, req.Method, url, err)
		}
		return "", "", fmt.Errorf("%s %s failed: %v", req.Method, url, err)
	}

	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	// Log separately for grep-ability.
	t.Logf("[%s -> %s] Req: URL=%s, Host=%s", localAddr, remoteAddr, url, host)
	t.Logf("[%s <- %s] Res: Status=%d, Headers=%+v", localAddr, remoteAddr, resp.StatusCode, resp.Header)

	return resp.Header.Get("x-pod-name"), localAddr, nil
}

func idleConnectionTerminationPolicyRunTest(t *testing.T, policy operatorv1.IngressControllerConnectionTerminationPolicy, actions []idleConnectionTestAction) {
	t.Helper()

	const (
		webService1 = "web-service-1"
		webService2 = "web-service-2"
	)

	canaryImageReference := func(t *testing.T) (string, error) {
		t.Helper()

		ingressOperatorName := types.NamespacedName{
			Name:      "ingress-operator",
			Namespace: operatorNamespace,
		}

		deployment, err := getDeployment(t, kclient, ingressOperatorName, 1*time.Minute)
		if err != nil {
			return "", fmt.Errorf("failed to get deployment for IngressController %s: %w", ingressOperatorName, err)
		}

		for _, container := range deployment.Spec.Template.Spec.Containers {
			for _, env := range container.Env {
				if env.Name == "CANARY_IMAGE" {
					return env.Value, nil
				}
			}
		}

		return "", fmt.Errorf("CANARY_IMAGE environment variable not found in deployment %s/%s", ingressOperatorName.Namespace, ingressOperatorName.Name)
	}

	createTestServicesAndTestRoute := func(ns *corev1.Namespace, ic *operatorv1.IngressController) (*idleConnectionTestConfig, error) {
		canaryImage, err := canaryImageReference(t)
		if err != nil {
			return nil, fmt.Errorf("failed to get canary image reference: %w", err)
		}

		if err := idleConnectionCreateBackendService(context.Background(), t, ns.Name, webService1, canaryImage); err != nil {
			return nil, fmt.Errorf("failed to create service %s: %w", webService1, err)
		}

		if err := idleConnectionCreateBackendService(context.Background(), t, ns.Name, webService2, canaryImage); err != nil {
			return nil, fmt.Errorf("failed to create service %s: %w", webService2, err)
		}

		routeName := types.NamespacedName{Namespace: ns.Name, Name: "test"}
		route := buildRoute(routeName.Name, routeName.Namespace, webService1)
		if err := kclient.Create(context.Background(), route); err != nil {
			return nil, fmt.Errorf("failed to create route %s: %w", routeName, err)
		}

		if err := waitForRouteIngressConditions(t, kclient, routeName, ic.Name, routev1.RouteIngressCondition{
			Type:   routev1.RouteAdmitted,
			Status: corev1.ConditionTrue,
		}); err != nil {
			return nil, fmt.Errorf("failed to observe route admitted condition: %w", err)
		}

		if err := kclient.Get(context.Background(), routeName, route); err != nil {
			return nil, fmt.Errorf("failed to get route %s: %w", routeName, err)
		}

		routeHost := getRouteHost(route, ic.Name)
		if routeHost == "" {
			return nil, fmt.Errorf("route %s has no host assigned by IngressController %s", routeName, ic.Name)
		}

		return &idleConnectionTestConfig{
			ingressController: ic,
			routeHost:         routeHost,
			routeName:         routeName,
		}, nil
	}

	createIngressController := func(t *testing.T, testNs *corev1.Namespace, policy operatorv1.IngressControllerConnectionTerminationPolicy) (*operatorv1.IngressController, error) {
		t.Helper()

		icName := types.NamespacedName{
			Namespace: operatorNamespace,
			Name:      testNs.Name,
		}

		t.Logf("Creating IngressController %s...", icName)

		ic := newLoadBalancerController(icName, icName.Name+"."+dnsConfig.Spec.BaseDomain)
		ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
			Scope:               operatorv1.ExternalLoadBalancer,
			DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
		}
		ic.Spec.IdleConnectionTerminationPolicy = policy
		ic.Spec.NamespaceSelector = &metav1.LabelSelector{
			MatchLabels: testNs.Labels,
		}
		if err := kclient.Create(context.Background(), ic); err != nil {
			return nil, fmt.Errorf("failed to create IngressController: %w", err)
		}
		t.Cleanup(func() {
			assertIngressControllerDeleted(t, kclient, ic)
		})

		if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
			return nil, fmt.Errorf("failed to observe expected conditions: %w", err)
		}

		elbHostname := getIngressControllerLBAddress(t, ic)
		externalTestPodName := types.NamespacedName{Name: testNs.Name + "-external-verify", Namespace: testNs.Name}
		verifyExternalIngressController(t, externalTestPodName, "apps."+ic.Spec.Domain, elbHostname)

		return ic, nil
	}

	resolveIngressControllerAddress := func(t *testing.T, ic *operatorv1.IngressController) (string, error) {
		elbHostname := getIngressControllerLBAddress(t, ic)
		elbAddress := elbHostname

		if net.ParseIP(elbHostname) == nil {
			if err := wait.PollUntilContextTimeout(context.Background(), 10*time.Second, 5*time.Minute, false, func(ctx context.Context) (bool, error) {
				addrs, err := net.LookupHost(elbHostname)
				if err != nil {
					t.Logf("%v error resolving %s: %v, retrying...", time.Now(), elbHostname, err)
					return false, nil
				}
				elbAddress = addrs[0]
				return true, nil
			}); err != nil {
				return "", fmt.Errorf("failed to resolve %s: %w", elbHostname, err)
			}
		}

		return elbAddress, nil
	}

	testName := names.SimpleNameGenerator.GenerateName(strings.ToLower(fmt.Sprintf("idle-connection-close-%s-", policy)))
	testNs := createNamespace(t, testName)
	testNs.Labels = map[string]string{testNs.Name + "-shard": testName}

	if err := retry.OnError(retry.DefaultBackoff, func(err error) bool {
		return apierrors.IsConflict(err)
	}, func() error {
		if err := kclient.Get(context.TODO(), client.ObjectKeyFromObject(testNs), testNs); err != nil {
			return fmt.Errorf("error fetching latest version of namespace %s: %w", testNs.Name, err)
		}
		if err := kclient.Update(context.TODO(), testNs); err != nil {
			t.Logf("conflict updating namespace %s with labels %s: %v, retrying...", testName, testNs.Labels, err)
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("error updating namespace %s with labels %s: %v", testName, testNs.Labels, err)
	}

	ic, err := createIngressController(t, testNs, policy)
	if err != nil {
		t.Fatalf("failed to create IngressController %s: %v", testName, err)
	}

	testResources, err := createTestServicesAndTestRoute(testNs, ic)
	if err != nil {
		t.Fatalf("test setup failed for policy %s: %v", policy, err)
	}

	// After the IngressController (IC) has been created and
	// verifyExternalIngressController() returns OK from
	// createIngressController(), CI flakes have been observed where
	// subsequent HTTP GETs using the ELB hostname (i.e., not an IP
	// address) fail to resolve. To mitigate these flakes, the ELB
	// hostname is resolved to an IP address, which is then used
	// exclusively for HTTP transactions.
	elbAddress, err := resolveIngressControllerAddress(t, ic)
	if err != nil {
		t.Fatalf("test setup failed: %v", err)
	}

	httpClient := func() *http.Client {
		baseTransport := &http.Transport{
			// 300s matches http-keep-alive setting in haproxy.config.
			IdleConnTimeout:     300 * time.Second,
			MaxConnsPerHost:     100,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
		}

		return &http.Client{
			Timeout: time.Minute,
			Transport: &addressCapturingRoundTripper{
				BaseTransport: baseTransport,
			},
		}
	}()

	var localAddresses []string

	for step, action := range actions {
		t.Logf("step %d: %s", step+1, action.description)

		response, localAddr, err := action.fetchResponse(httpClient, elbAddress, testResources)
		if err != nil {
			t.Fatalf("step %d: failed: %v", step+1, err)
		}

		if response != action.expectedResponse {
			t.Fatalf("step %d: unexpected response: got %q, want %q", step+1, response, action.expectedResponse)
		}

		localAddresses = append(localAddresses, localAddr)
	}

	switch policy {
	case operatorv1.IngressControllerConnectionTerminationPolicyImmediate:
		if localAddresses[1] != localAddresses[2] {
			t.Fatalf("%s policy: expected steps 2 and 3 to use same connection, but got addresses %s and %s", policy, localAddresses[1], localAddresses[2])
		}
	case operatorv1.IngressControllerConnectionTerminationPolicyDeferred:
		if localAddresses[0] != localAddresses[1] {
			t.Fatalf("%s policy: expected steps 1 and 2 to use same connection, but got addresses %s and %s", policy, localAddresses[0], localAddresses[1])
		}
	}
}

// Test_IdleConnectionTerminationPolicyImmediate verifies that with
// the Immediate policy, new requests use a fresh connection after a
// backend switch, ensuring connection reuse is avoided.
//
// This test:
//
//  1. Deploys two backend services (`web-service-1` and `web-service-2`).
//
//  2. Switches a Route between the backends and makes GET requests:
//
//     Step 1: Verify the initial request is served by `web-service-1`.
//
//     Step 2: Switch the route to `web-service-2`. Verify that the
//     Immediate policy ensures the new connection is established, and
//     the request is served by `web-service-2`.
//
//     Step 3: Verify that subsequent requests are also served by
//     `web-service-2`.
//
//     Verifies that steps 2 and 3 use the same new connection (same
//     local address), demonstrating that the backend switch
//     effectively takes effect without reusing the original
//     connection.
func Test_IdleConnectionTerminationPolicyImmediate(t *testing.T) {
	t.Parallel()

	idleConnectionTerminationPolicyRunTest(t, operatorv1.IngressControllerConnectionTerminationPolicyImmediate, []idleConnectionTestAction{
		{
			description: "Verify the initial response is correctly served by web-service-1",
			fetchResponse: func(httpClient *http.Client, elbAddr string, cfg *idleConnectionTestConfig) (string, string, error) {
				return idleConnectionFetchResponse(t, httpClient, elbAddr, cfg.routeHost)
			},
			expectedResponse: "web-service-1",
		},
		{
			description: "Switch route to web-service-2 and verify Immediate policy ensures new responses are served by web-service-2",
			fetchResponse: func(httpClient *http.Client, elbAddr string, cfg *idleConnectionTestConfig) (string, string, error) {
				if err := idleConnectionSwitchRouteService(t, cfg.routeName, cfg.ingressController.Name, "web-service-2"); err != nil {
					return "", "", err
				}
				return idleConnectionFetchResponse(t, httpClient, elbAddr, cfg.routeHost)
			},
			expectedResponse: "web-service-2",
		},
		{
			description: "Ensure subsequent responses are served by web-service-2",
			fetchResponse: func(httpClient *http.Client, elbAddr string, cfg *idleConnectionTestConfig) (string, string, error) {
				return idleConnectionFetchResponse(t, httpClient, elbAddr, cfg.routeHost)
			},
			expectedResponse: "web-service-2",
		},
	})
}

// Test_IdleConnectionTerminationPolicyDeferred verifies that with the
// Deferred policy, existing connections are reused even after a
// backend switch, ensuring graceful connection reuse.
//
// This test:
//
//  1. Deploys two backend services (`web-service-1` and `web-service-2`).
//
//  2. Switches a Route between the backends and makes GET requests:
//
//     Step 1: Verify the initial request is served by `web-service-1`.
//
//     Step 2: Switch the route to `web-service-2`. Verify that the
//     Deferred policy allows the connection to be reused, and the
//     request is still served by `web-service-1`.
//
//     Step 3: Verify that subsequent requests are served by
//     `web-service-2` after the connection is reset.
//
//     Verifies that steps 1 and 2 use the same original connection
//     (same local address), showing that the original connection
//     persists during the backend switch.
func Test_IdleConnectionTerminationPolicyDeferred(t *testing.T) {
	t.Parallel()

	idleConnectionTerminationPolicyRunTest(t, operatorv1.IngressControllerConnectionTerminationPolicyDeferred, []idleConnectionTestAction{
		{
			description: "Verify the initial response is correctly served by web-service-1",
			fetchResponse: func(httpClient *http.Client, elbAddr string, cfg *idleConnectionTestConfig) (string, string, error) {
				return idleConnectionFetchResponse(t, httpClient, elbAddr, cfg.routeHost)
			},
			expectedResponse: "web-service-1",
		},
		{
			description: "Switch route to web-service-2 and validate Deferred policy allows one final response to be served by web-service-1",
			fetchResponse: func(httpClient *http.Client, elbAddr string, cfg *idleConnectionTestConfig) (string, string, error) {
				if err := idleConnectionSwitchRouteService(t, cfg.routeName, cfg.ingressController.Name, "web-service-2"); err != nil {
					return "", "", err
				}
				return idleConnectionFetchResponse(t, httpClient, elbAddr, cfg.routeHost)
			},
			expectedResponse: "web-service-1",
		},
		{
			description: "Ensure subsequent responses are now served by web-service-2",
			fetchResponse: func(httpClient *http.Client, elbAddr string, cfg *idleConnectionTestConfig) (string, string, error) {
				return idleConnectionFetchResponse(t, httpClient, elbAddr, cfg.routeHost)
			},
			expectedResponse: "web-service-2",
		},
	})
}
