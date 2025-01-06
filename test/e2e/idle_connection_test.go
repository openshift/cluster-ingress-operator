//go:build e2e
// +build e2e

package e2e

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
)

// idleConnectionHTTPClient represents a minimal HTTP client with
// explicit connection management.
type idleConnectionHTTPClient struct {
	addr       string
	conn       net.Conn
	reader     *bufio.Reader
	localAddr  string
	remoteAddr string
}

// idleConnectionNewHTTPClient creates a new custom client for the
// specified address.
func idleConnectionNewHTTPClient(addr string) (*idleConnectionHTTPClient, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	tcpConn, _ := conn.(*net.TCPConn)
	if err := tcpConn.SetKeepAlive(true); err != nil {
		return nil, fmt.Errorf("failed to enable keep alive: %w", err)
	}

	return &idleConnectionHTTPClient{
		addr:       addr,
		conn:       conn,
		reader:     bufio.NewReader(conn),
		localAddr:  conn.LocalAddr().String(),
		remoteAddr: conn.RemoteAddr().String(),
	}, nil
}

// sendRequest sends an HTTP GET request to the specified path with a
// custom Host header.
func (c *idleConnectionHTTPClient) sendRequest(path, host string) error {
	if c.conn == nil {
		return fmt.Errorf("connection is not established")
	}

	request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\n\r\n", path, host)
	_, err := c.conn.Write([]byte(request))
	if err != nil {
		return fmt.Errorf("failed to send request: %q: %w", request, err)
	}

	return nil
}

// readResponse parses the HTTP response using http.readResponse.
func (c *idleConnectionHTTPClient) readResponse() (*http.Response, error) {
	if c.reader == nil {
		return nil, fmt.Errorf("no connection reader available")
	}

	resp, err := http.ReadResponse(c.reader, nil)
	if err != nil {
		return nil, fmt.Errorf("error parsing response: %w", err)
	}

	return resp, nil
}

func (c *idleConnectionHTTPClient) String() string {
	return fmt.Sprintf("%s -> %s", c.localAddr, c.remoteAddr)
}

func idleConnectionCreateBackendService(ctx context.Context, t *testing.T, namespace, name, image string) error {
	labels := map[string]string{
		"instance": name,
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
						{
							Name: "POD_NAMESPACE",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.namespace",
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
	// configuration and also perform a haproxy soft-reload.
	time.Sleep(20 * time.Second)

	return nil
}

func idleConnectionFetchResponse(httpClient *idleConnectionHTTPClient, hostname string) (string, error) {
	if err := httpClient.sendRequest("/", hostname); err != nil {
		return "", fmt.Errorf("[%s] failed to send GET request: %w", httpClient, err)
	}

	resp, err := httpClient.readResponse()
	if err != nil {
		return "", fmt.Errorf("[%s] failed to read response: %w", httpClient, err)
	}

	defer func() {
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected response status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	podName := resp.Header.Get("x-pod-name")
	if podName == "" {
		return "", fmt.Errorf("missing 'x-pod-name' header in response")
	}

	return podName, nil
}

func idleConnectionValidateRouterEnvVar(t *testing.T, routerDeployment *appsv1.Deployment, expectValue string) error {
	state := "unset"
	if expectValue != "" {
		state = fmt.Sprintf("set to %q", expectValue)
	}

	if err := waitForDeploymentEnvVar(t, kclient, routerDeployment, 2*time.Minute, "ROUTER_IDLE_CLOSE_ON_RESPONSE", expectValue); err != nil {
		return fmt.Errorf("expected router deployment to have ROUTER_IDLE_CLOSE_ON_RESPONSE %s: %w", state, err)
	}

	return nil
}

func idleConnectionSwitchIdleTerminationPolicy(t *testing.T, ic *operatorv1.IngressController, policy operatorv1.IngressControllerConnectionTerminationPolicy) error {
	icName := types.NamespacedName{Namespace: ic.Namespace, Name: ic.Name}
	t.Logf("Updating IngressController %s: setting IngressControllerConnectionTerminationPolicy to %q", icName, policy)

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.Background(), operatorcontroller.RouterDeploymentName(ic), deployment); err != nil {
		return fmt.Errorf("failed to get initial deployment state: %v", err)
	}
	startingGeneration := deployment.Generation

	if err := updateIngressControllerWithRetryOnConflict(t, icName, 5*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.IdleConnectionTerminationPolicy = policy
	}); err != nil {
		return fmt.Errorf("failed to update IdleConnectionTerminationPolicy to %q for IngressController %s: %w", policy, icName, err)
	}

	if err := waitForDeploymentCompleteAndNoOldPods(t, operatorcontroller.RouterDeploymentName(ic), startingGeneration, 15*time.Second, 3*time.Minute); err != nil {
		return fmt.Errorf("failed to observe router deployment completion for %s: %w", operatorcontroller.RouterDeploymentName(ic), err)
	}

	routerDeployment := appsv1.Deployment{}
	if err := kclient.Get(context.Background(), operatorcontroller.RouterDeploymentName(ic), &routerDeployment); err != nil {
		return fmt.Errorf("failed to get IngressController deployment: %w", err)
	}

	switch policy {
	case operatorv1.IngressControllerConnectionTerminationPolicyDeferred:
		return idleConnectionValidateRouterEnvVar(t, &routerDeployment, "true")
	case operatorv1.IngressControllerConnectionTerminationPolicyImmediate:
		return idleConnectionValidateRouterEnvVar(t, &routerDeployment, "")
	default:
		return fmt.Errorf("unsupported idle connection termination policy: %q", policy)
	}
}

// IngressController correctly handles backend switching under
// different IdleConnectionTerminationPolicy settings.
//
// This test:
//  1. Deploys two backend services (`web-service-1` and `web-service-2`).
//  2. Alternates a Route between the backends.
//  3. Validates that HAProxy routes requests to the correct backend
//     according to the policy (`Immediate` or `Deferred`).
//  4. Ensures router pods correctly apply the expected environment
//     variable (`ROUTER_IDLE_CLOSE_ON_RESPONSE`) for each policy.
//
// Note: In the `Deferred` policy case, due to keep-alive behaviour,
// the first request after switching backends will still be routed to
// the previously active backend. The test accounts for this expected
// behaviour and validates subsequent requests route correctly to the
// new backend.
func Test_IdleConnectionTerminationPolicy(t *testing.T) {
	t.Parallel()

	canaryImageReference := func(t *testing.T) (string, error) {
		ingressOperatorName := types.NamespacedName{
			Name:      "ingress-operator",
			Namespace: operatorNamespace,
		}

		deployment, err := getDeployment(t, kclient, ingressOperatorName, 1*time.Minute)
		if err != nil {
			return "", fmt.Errorf("failed to get deployment %s/%s: %w", ingressOperatorName.Namespace, ingressOperatorName.Name, err)
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

	const (
		webService1 = "web-service-1"
		webService2 = "web-service-2"
	)

	testName := names.SimpleNameGenerator.GenerateName("idle-close-on-response-")

	podImage, err := canaryImageReference(t)
	if err != nil {
		t.Fatalf("failed to get canary image reference: %v", err)
	}

	icName := types.NamespacedName{Namespace: operatorNamespace, Name: testName}
	ns := createNamespace(t, icName.Name)

	ic := newLoadBalancerController(icName, icName.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.ExternalLoadBalancer,
		DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
	}
	// We add logging in case of CI flakes.
	ic.Spec.Logging = &operatorv1.IngressControllerLogging{
		Access: &operatorv1.AccessLogging{
			Destination: operatorv1.LoggingDestination{
				Type: "Container",
			},
		},
	}

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create IngressController: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if ic, err = getIngressController(t, kclient, icName, 1*time.Minute); err != nil {
		t.Fatalf("failed to get IngressController: %v", err)
	}

	initialIdleTerminationPolicy := ic.Spec.IdleConnectionTerminationPolicy
	elbHostname := getIngressControllerLBAddress(t, ic)
	externalTestPodName := types.NamespacedName{Name: icName.Name + "-external-verify", Namespace: icName.Namespace}
	verifyExternalIngressController(t, externalTestPodName, "apps."+ic.Spec.Domain, elbHostname)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := idleConnectionCreateBackendService(context.Background(), t, ns.Name, webService1, podImage); err != nil {
		t.Fatalf("failed to create backend service 1: %v", err)
	}

	if err := idleConnectionCreateBackendService(context.Background(), t, ns.Name, webService2, podImage); err != nil {
		t.Fatalf("failed to create backend service 2: %v", err)
	}

	routeName := types.NamespacedName{Namespace: testName, Name: "test"}
	route := buildRoute(routeName.Name, routeName.Namespace, webService1)
	if err := kclient.Create(context.Background(), route); err != nil {
		t.Fatalf("failed to create route %s: %v", routeName, err)
	}

	routeAdmittedCondition := routev1.RouteIngressCondition{
		Type:   routev1.RouteAdmitted,
		Status: corev1.ConditionTrue,
	}

	if err := waitForRouteIngressConditions(t, kclient, routeName, ic.Name, routeAdmittedCondition); err != nil {
		t.Fatalf("error waiting for route %s to be admitted: %v", routeName, err)
	}

	if err := kclient.Get(context.TODO(), routeName, route); err != nil {
		t.Fatalf("failed to get route %s: %v", routeName, err)
	}

	routeHost := getRouteHost(route, ic.Name)
	if routeHost == "" {
		t.Fatalf("route %s has no host assigned by IngressController %s", routeName, ic.Name)
	}
	t.Logf("test host: %s", routeHost)

	testPolicies := []operatorv1.IngressControllerConnectionTerminationPolicy{
		operatorv1.IngressControllerConnectionTerminationPolicyImmediate,
		operatorv1.IngressControllerConnectionTerminationPolicyDeferred,
	}

	// If the current policy is Deferred, reorder the test cases
	// to start with Deferred. This ensures we avoid an
	// unnecessary policy switch and the associated
	// IngressController rollout at the beginning of the test. By
	// starting with the current policy, we can skip applying it
	// again in the first subtest, improving efficiency. In 4.19+
	// the default is Immediate.
	if initialIdleTerminationPolicy == operatorv1.IngressControllerConnectionTerminationPolicyDeferred {
		t.Log("Reordering test cases to avoid initial policy switch")
		testPolicies = []operatorv1.IngressControllerConnectionTerminationPolicy{
			operatorv1.IngressControllerConnectionTerminationPolicyDeferred,
			operatorv1.IngressControllerConnectionTerminationPolicyImmediate,
		}
	}

	var httpClient *idleConnectionHTTPClient
	var httpClientErr error

	actions := []struct {
		description      string
		fetchResponse    func(policy operatorv1.IngressControllerConnectionTerminationPolicy) (string, error)
		expectedResponse func(policy operatorv1.IngressControllerConnectionTerminationPolicy) string
	}{
		{
			description: "Switch route to web-service-1 and fetch response",
			fetchResponse: func(policy operatorv1.IngressControllerConnectionTerminationPolicy) (string, error) {
				if err := idleConnectionSwitchRouteService(t, routeName, ic.Name, webService1); err != nil {
					return "", err
				}

				httpClient, httpClientErr = idleConnectionNewHTTPClient(elbHostname + ":80")
				if httpClientErr != nil {
					return "", fmt.Errorf("failed to establish connection: %w", httpClientErr)
				}
				return idleConnectionFetchResponse(httpClient, routeHost)
			},
			expectedResponse: func(policy operatorv1.IngressControllerConnectionTerminationPolicy) string {
				return webService1
			},
		},
		{
			description: "Verify response is from web-service-1",
			fetchResponse: func(policy operatorv1.IngressControllerConnectionTerminationPolicy) (string, error) {
				return idleConnectionFetchResponse(httpClient, routeHost)
			},
			expectedResponse: func(policy operatorv1.IngressControllerConnectionTerminationPolicy) string {
				return webService1
			},
		},
		{
			description: "Switch route to web-service-2 and fetch response",
			fetchResponse: func(policy operatorv1.IngressControllerConnectionTerminationPolicy) (string, error) {
				if err := idleConnectionSwitchRouteService(t, routeName, ic.Name, webService2); err != nil {
					return "", err
				}

				if policy == operatorv1.IngressControllerConnectionTerminationPolicyImmediate {
					// In Immediate mode, HAProxy will terminate existing idle connections
					// because the configuration changes to reflect the new route's
					// service, and HAProxy undergoes a soft-reload to apply the updated
					// configuration. This invalidates any pre-existing connections,
					// requiring the client to establish a new connection.

					// Attempt a request using the existing connection to confirm it's
					// been invalidated.
					resp, err := idleConnectionFetchResponse(httpClient, routeHost)
					if err == nil {
						return "", fmt.Errorf("expected connection error but got none; response=%q", resp)
					}

					// Re-establish a new connection to HAProxy for further testing.
					httpClient, httpClientErr = idleConnectionNewHTTPClient(elbHostname + ":80")
					if httpClientErr != nil {
						return "", fmt.Errorf("failed to establish connection: %w", err)
					}
				}

				return idleConnectionFetchResponse(httpClient, routeHost)
			},
			expectedResponse: func(policy operatorv1.IngressControllerConnectionTerminationPolicy) string {
				return map[operatorv1.IngressControllerConnectionTerminationPolicy]string{
					operatorv1.IngressControllerConnectionTerminationPolicyImmediate: webService2,
					operatorv1.IngressControllerConnectionTerminationPolicyDeferred:  webService1,
				}[policy]
			},
		},
		{
			description: "Verify response is from web-service-2",
			fetchResponse: func(policy operatorv1.IngressControllerConnectionTerminationPolicy) (string, error) {
				if policy == operatorv1.IngressControllerConnectionTerminationPolicyDeferred {
					// In Deferred mode, the existing connection may allow one response
					// after the route switch, but subsequent requests on that same
					// connection should result in an error as HAProxy closes the
					// connection to enforce the new configuration.

					// Attempt a request on the existing connection to ensure it errors.
					if _, err := idleConnectionFetchResponse(httpClient, routeHost); err == nil {
						return "", fmt.Errorf("expected connection error but got none")
					}

					// Establish a new connection to ensure traffic routes to the new
					// backend (web-service-2).
					httpClient, httpClientErr = idleConnectionNewHTTPClient(elbHostname + ":80")
					if httpClientErr != nil {
						return "", fmt.Errorf("failed to establish new connection: %w", httpClientErr)
					}
				}

				return idleConnectionFetchResponse(httpClient, routeHost)
			},
			expectedResponse: func(policy operatorv1.IngressControllerConnectionTerminationPolicy) string {
				return webService2
			},
		},
	}

	for i, policy := range testPolicies {
		if i == 0 && policy == initialIdleTerminationPolicy {
			t.Logf("[%s] skipping policy update for IngressController %s: current policy %q matches desired policy %q", policy, icName, initialIdleTerminationPolicy, policy)
		} else {
			if err := idleConnectionSwitchIdleTerminationPolicy(t, ic, policy); err != nil {
				t.Fatalf("failed to set IngressController %s idle termination policy to %q: %v", icName, policy, err)
			}
			if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
				t.Fatalf("failed to observe expected conditions: %v", err)
			}
		}

		for step, action := range actions {
			t.Logf("[%s] step %d: %q", policy, step+1, action.description)
			if got, err := action.fetchResponse(policy); err != nil {
				t.Fatalf("[%s] step %d: failed: %v", policy, step+1, err)
			} else if want := action.expectedResponse(policy); got != want {
				t.Fatalf("[%s] step %d: unexpected response: got %q, want %q", policy, step+1, got, want)
			}
		}
	}
}

// waitForDeploymentCompleteAndNoOldPods waits for a deployment to
// complete a roll-out by watching for the deployment's generation to
// advance beyond a known starting point. This avoids races that could
// occur if we started watching after the roll-out had already begun or
// completed.
//
// The function takes a startingGeneration parameter which represents
// the deployment's generation before any changes were made. It then
// waits until:
//
//  1. The deployment generation advances beyond startingGeneration
//     (indicating a change was detected).
//
//  2. The number of pods exactly matches the deployment's desired
//     replica count, with all pods running and ready.
//
//  3. No pods are in a terminating state.
//
// This ensures we see both the start of the roll-out (generation
// advancing) and its completion (all old pods gone, exact number of
// new pods ready and running).
//
// For cases involving pod termination with grace periods, this
// function will continue to wait until the terminating pods are fully
// removed from the API server.
func waitForDeploymentCompleteAndNoOldPods(t *testing.T, deploymentName types.NamespacedName, startingGeneration int64, interval, timeout time.Duration) error {
	t.Helper()

	startTime := time.Now()
	t.Logf("[%s] Waiting for deployment %s to move past generation %d (timeout: %v)",
		deploymentName.String(), deploymentName, startingGeneration, timeout)

	return wait.PollUntilContextTimeout(context.Background(), interval, timeout, false, func(ctx context.Context) (bool, error) {
		deployment := &appsv1.Deployment{}
		if err := kclient.Get(context.Background(), deploymentName, deployment); err != nil {
			return false, fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
		}

		// If spec.replicas is null, the default value is 1,
		// per the API spec.
		expectedReplicas := ptr.Deref(deployment.Spec.Replicas, 1)

		podList := &corev1.PodList{}
		if err := kclient.List(context.Background(), podList,
			client.InNamespace(deploymentName.Namespace),
			client.MatchingLabels(deployment.Spec.Selector.MatchLabels)); err != nil {
			return false, fmt.Errorf("failed to list pods for deployment %s: %w", deploymentName, err)
		}

		elapsed := time.Since(startTime).Round(time.Second)
		t.Logf("[%v elapsed] Deployment %s status:", elapsed, deploymentName.String())
		t.Logf("  Generation: %d/%d (starting at %d)",
			deployment.Status.ObservedGeneration,
			deployment.Generation,
			startingGeneration)
		t.Logf("  Pods: %d current, %d desired",
			len(podList.Items),
			expectedReplicas)

		// Wait until the deployment moves past our starting
		// generation.
		if deployment.Generation <= startingGeneration {
			t.Logf("Waiting for deployment %s to move past generation %d (currently %d)",
				deploymentName, startingGeneration, deployment.Generation)
			return false, nil
		}

		var (
			readyAndRunning int32
			terminatingPods int32
		)

		for _, pod := range podList.Items {
			if pod.DeletionTimestamp != nil {
				terminatingPods++
				t.Logf("  Pod %s in deployment %s is terminating (grace period: %ds)",
					pod.Name, deploymentName, ptr.Deref(pod.DeletionGracePeriodSeconds, 0))
				continue
			}

			isReady := false
			if pod.Status.Phase == corev1.PodRunning {
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						readyAndRunning++
						isReady = true
						break
					}
				}
			}

			t.Logf("  Pod %s in deployment %s is %s (ready: %v)",
				pod.Name, deploymentName, pod.Status.Phase, isReady)
		}

		if readyAndRunning != expectedReplicas || terminatingPods > 0 {
			t.Logf("Deployment %s: Waiting for pods to be ready and running (%d ready+running, %d terminating, %d desired)",
				deploymentName, readyAndRunning, terminatingPods, expectedReplicas)
			return false, nil
		}

		t.Logf("Deployment %s complete in %s: Moved from generation %d to %d with %d pods ready and running",
			deploymentName, elapsed.Round(time.Second), startingGeneration, deployment.Generation, readyAndRunning)
		return true, nil
	})
}
