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
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apiserver/pkg/storage/names"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
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
func idleConnectionNewHTTPClient(t *testing.T, addr string) (*idleConnectionHTTPClient, error) {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	tcpConn, _ := conn.(*net.TCPConn)
	if err := tcpConn.SetKeepAlive(true); err != nil {
		return nil, fmt.Errorf("failed to enable keep alive: %w", err)
	}

	c := idleConnectionHTTPClient{
		addr:       addr,
		conn:       conn,
		reader:     bufio.NewReader(conn),
		localAddr:  conn.LocalAddr().String(),
		remoteAddr: conn.RemoteAddr().String(),
	}

	t.Logf("New connection: %v", c)

	return &c, nil
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
						InitialDelaySeconds: 1, // Delay before readiness checks start to allow for initialisation.
						PeriodSeconds:       2, // Perform readiness checks every 2 seconds.
						TimeoutSeconds:      1, // Each readiness check must respond within 1 second.
						FailureThreshold:    2, // Allow up to 2 consecutive failures before marking the pod as "not ready".
						SuccessThreshold:    1, // Mark the pod as "ready" after one successful probe.
					},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path:   "/healthz",
								Port:   intstr.FromInt32(8080),
								Scheme: corev1.URISchemeHTTP,
							},
						},
						InitialDelaySeconds: 1, // Delay before starting liveness checks to avoid premature restarts.
						PeriodSeconds:       5, // Perform liveness checks every 5 seconds.
						TimeoutSeconds:      2, // Each liveness check must respond within 2 seconds.
						FailureThreshold:    3, // Restart the container after 3 consecutive liveness probe failures.
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
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
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
	const (
		webService1 = "web-service-1"
		webService2 = "web-service-2"
	)

	type testConfig struct {
		elbHostname       string
		ingressController *operatorv1.IngressController
		routeHost         string
		routeName         types.NamespacedName
	}

	type testAction struct {
		description      string
		fetchResponse    func(httpClient **idleConnectionHTTPClient, testConfig *testConfig) (string, error)
		expectedResponse string
	}

	canaryImageReference := func(t *testing.T) (string, error) {
		t.Helper()

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

	createTestServices := func(t *testing.T, ns *corev1.Namespace, image string) error {
		t.Helper()

		var g errgroup.Group

		g.Go(func() error {
			return idleConnectionCreateBackendService(context.Background(), t, ns.Name, webService1, image)
		})

		g.Go(func() error {
			return idleConnectionCreateBackendService(context.Background(), t, ns.Name, webService2, image)
		})

		return g.Wait()
	}

	// immediateActions contains the test steps that validate the
	// Immediate termination policy. This policy immediately
	// terminates existing connections when the route switches
	// to a new service.
	//
	// Steps:
	//
	// 1. Verify that the initial response routes to web-service-1.
	//
	// 2. Switch the route to web-service-2 and confirm that the
	//    Immediate policy terminates the existing connection.
	//    Establish a new connection and ensure the response
	//    routes to web-service-2.
	//
	// 3. Confirm that all subsequent responses on the new
	//    connection route to web-service-2.
	immediateActions := []testAction{
		{
			description: "Verify initial response is correctly routed to web-service-1",
			fetchResponse: func(httpClient **idleConnectionHTTPClient, cfg *testConfig) (string, error) {
				c, err := idleConnectionNewHTTPClient(t, cfg.elbHostname+":80")
				if err != nil {
					return "", fmt.Errorf("failed to establish connection: %w", err)
				}
				*httpClient = c
				return idleConnectionFetchResponse(*httpClient, cfg.routeHost)
			},
			expectedResponse: webService1,
		},
		{
			description: "Switch route to web-service-2 and verify Immediate policy terminates existing connections and routes new connections to web-service-2",
			fetchResponse: func(httpClient **idleConnectionHTTPClient, cfg *testConfig) (string, error) {
				if err := idleConnectionSwitchRouteService(t, cfg.routeName, cfg.ingressController.Name, webService2); err != nil {
					return "", err
				}

				resp, err := idleConnectionFetchResponse(*httpClient, cfg.routeHost)
				if err == nil {
					return "", fmt.Errorf("expected connection error but got none; response %q", resp)
				}

				c, err := idleConnectionNewHTTPClient(t, cfg.elbHostname+":80")
				if err != nil {
					return "", fmt.Errorf("failed to establish connection: %w", err)
				}
				*httpClient = c

				return idleConnectionFetchResponse(*httpClient, cfg.routeHost)
			},
			expectedResponse: webService2,
		},
		{
			description: "Verify response is from web-service-2",
			fetchResponse: func(httpClient **idleConnectionHTTPClient, cfg *testConfig) (string, error) {
				return idleConnectionFetchResponse(*httpClient, cfg.routeHost)
			},
			expectedResponse: webService2,
		},
	}

	// deferredActions contains the test steps that validate the
	// Deferred termination policy. This policy allows an existing
	// connection to serve one final response from the previous
	// backend even after switching the route to a new service.
	// Subsequent requests on the same connection fail, requiring
	// the client to establish a new connection.
	//
	// Steps:
	//
	// 1. Verify that the initial response routes to web-service-1.
	//
	// 2. Switch the route to web-service-2 and confirm that the
	//    Deferred policy allows one final response from
	//    web-service-1 on the existing connection.
	//
	// 3. Confirm that the existing connection closes and that a new
	//    connection routes the response to web-service-2, reflecting
	//    the updated route configuration.
	deferredActions := []testAction{
		{
			description: "Verify initial response is correctly routed to web-service-1",
			fetchResponse: func(httpClient **idleConnectionHTTPClient, cfg *testConfig) (string, error) {
				c, err := idleConnectionNewHTTPClient(t, cfg.elbHostname+":80")
				if err != nil {
					return "", fmt.Errorf("failed to establish connection: %w", err)
				}
				*httpClient = c
				return idleConnectionFetchResponse(*httpClient, cfg.routeHost)
			},
			expectedResponse: webService1,
		},
		{
			description: "Switch route to web-service-2 and validate Deferred policy allows one final response from web-service-1 before closing the connection",
			fetchResponse: func(httpClient **idleConnectionHTTPClient, cfg *testConfig) (string, error) {
				if err := idleConnectionSwitchRouteService(t, cfg.routeName, cfg.ingressController.Name, webService2); err != nil {
					return "", err
				}

				return idleConnectionFetchResponse(*httpClient, cfg.routeHost)
			},
			expectedResponse: webService1,
		},
		{
			description: "Ensure old connection fails and new connection routes correctly to web-service-2",
			fetchResponse: func(httpClient **idleConnectionHTTPClient, cfg *testConfig) (string, error) {
				resp, err := idleConnectionFetchResponse(*httpClient, cfg.routeHost)
				if err == nil {
					return "", fmt.Errorf("expected connection error but got none; response=%q", resp)
				}

				c, err := idleConnectionNewHTTPClient(t, cfg.elbHostname+":80")
				if err != nil {
					return "", fmt.Errorf("failed to establish new connection: %w", err)
				}
				*httpClient = c
				return idleConnectionFetchResponse(*httpClient, cfg.routeHost)
			},
			expectedResponse: webService2,
		},
	}

	createTestServicesAndTestRoute := func(ns *corev1.Namespace, ic *operatorv1.IngressController) (*testConfig, error) {
		canaryImage, err := canaryImageReference(t)
		if err != nil {
			return nil, fmt.Errorf("failed to get canary image reference: %w", err)
		}

		if err := createTestServices(t, ns, canaryImage); err != nil {
			return nil, fmt.Errorf("failed to create backend services: %w", err)
		}

		routeName := types.NamespacedName{Namespace: ns.Name, Name: "test"}
		route := buildRoute(routeName.Name, routeName.Namespace, webService1)
		if err := kclient.Create(context.Background(), route); err != nil {
			return nil, fmt.Errorf("failed to create route: %w", err)
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

		return &testConfig{
			elbHostname:       getIngressControllerLBAddress(t, ic),
			ingressController: ic,
			routeHost:         routeHost,
			routeName:         routeName,
		}, nil
	}

	createIngressController := func(t *testing.T, policy operatorv1.IngressControllerConnectionTerminationPolicy, testName string) (*operatorv1.IngressController, error) {
		t.Helper()

		icName := types.NamespacedName{
			Namespace: operatorNamespace,
			Name:      testName,
		}

		ic := newLoadBalancerController(icName, icName.Name+"."+dnsConfig.Spec.BaseDomain)
		t.Logf("Creating IngressController %s...", icName)

		ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
			Scope:               operatorv1.ExternalLoadBalancer,
			DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
		}
		ic.Spec.IdleConnectionTerminationPolicy = policy

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
		externalTestPodName := types.NamespacedName{Name: icName.Name + "-external-verify", Namespace: icName.Namespace}
		verifyExternalIngressController(t, externalTestPodName, "apps."+ic.Spec.Domain, elbHostname)

		return ic, nil
	}

	testCases := []struct {
		policy  operatorv1.IngressControllerConnectionTerminationPolicy
		actions []testAction
	}{
		{
			policy:  operatorv1.IngressControllerConnectionTerminationPolicyImmediate,
			actions: immediateActions,
		},
		{
			policy:  operatorv1.IngressControllerConnectionTerminationPolicyDeferred,
			actions: deferredActions,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(string(tc.policy), func(t *testing.T) {
			testName := names.SimpleNameGenerator.GenerateName(strings.ToLower(fmt.Sprintf("idle-connection-%s-", tc.policy)))
			ns := createNamespace(t, testName)

			ic, err := createIngressController(t, tc.policy, testName)
			if err != nil {
				t.Fatalf("failed to create IngressController for test %s", testName)
			}

			testResources, err := createTestServicesAndTestRoute(ns, ic)
			if err != nil {
				t.Fatalf("test setup failed for policy %s: %v", tc.policy, err)
			}

			// httpClient is created and established in
			// the respective test case actions.
			var httpClient *idleConnectionHTTPClient

			for step, action := range tc.actions {
				t.Logf("[IdleConnectionTerminationPolicy %s] step %d: %q", tc.policy, step+1, action.description)
				if got, err := action.fetchResponse(&httpClient, testResources); err != nil {
					t.Fatalf("[IdleConnectionTerminationPolicy %s] step %d: failed: %v", tc.policy, step+1, err)
				} else if want := action.expectedResponse; got != want {
					t.Fatalf("[IdleConnectionTerminationPolicy %s] step %d: unexpected response: got %q, want %q", tc.policy, step+1, got, want)
				}
			}
		})
	}
}
