//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/storage/names"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
)

// Test_HTTPKeepAliveTimeout verifies that the HTTPKeepAliveTimeout tuning option correctly closes
// a persistent HTTP connection after the configured duration, forcing the client to establish a new one.
func Test_HTTPKeepAliveTimeout(t *testing.T) {
	t.Parallel()

	// Use client timeout value lesser than the http keep alive timeout
	// to make sure we avoid https://github.com/haproxy/haproxy/issues/2334.
	httpKeepAliveTimeoutRunTest(t, 7, 3)
	// Use the default client timeout.
	httpKeepAliveTimeoutRunTest(t, 7, 0)
}

// httpKeepAliveTimeoutRunTest is a helper which runs a single test workflow.
// The test workflow:
// - Create an ingress controller with a set HTTPKeepAliveTimeout.
// - Send multiple requests to confirm the connection reuse.
// - Sleep for the duration of the timeout.
// - Send a final request and verify a new connection is established.
func httpKeepAliveTimeoutRunTest(t *testing.T, httpKATimeoutSeconds, clientTimeoutSeconds int64) {
	t.Helper()

	const (
		webService = "web-service"
		// Number of requests to check the connection is reused.
		numConnReuseAttempts = 5
	)

	createIngressController := func(t *testing.T, testNs *corev1.Namespace) (*operatorv1.IngressController, error) {
		t.Helper()

		icName := types.NamespacedName{
			Namespace: operatorNamespace,
			Name:      testNs.Name,
		}

		t.Logf("Creating IngressController %q...", icName)

		ic := newLoadBalancerController(icName, icName.Name+"."+dnsConfig.Spec.BaseDomain)
		ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
			Scope:               operatorv1.ExternalLoadBalancer,
			DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
		}
		ic.Spec.NamespaceSelector = &metav1.LabelSelector{
			MatchLabels: testNs.Labels,
		}
		ic.Spec.TuningOptions.HTTPKeepAliveTimeout = &metav1.Duration{Duration: time.Duration(httpKATimeoutSeconds) * time.Second}
		if clientTimeoutSeconds != 0 {
			ic.Spec.TuningOptions.ClientTimeout = &metav1.Duration{Duration: time.Duration(clientTimeoutSeconds) * time.Second}
		}

		if err := kclient.Create(context.Background(), ic); err != nil {
			return nil, fmt.Errorf("failed to create IngressController: %w", err)
		}
		t.Cleanup(func() {
			t.Logf("Deleting IngressController %q...", icName)
			assertIngressControllerDeleted(t, kclient, ic)
		})

		if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
			return nil, fmt.Errorf("failed to observe expected conditions: %w", err)
		}

		return ic, nil
	}

	createTestServicesAndTestRoute := func(ns *corev1.Namespace, ic *operatorv1.IngressController) (string, error) {
		canaryImage, err := canaryImageReference(t)
		if err != nil {
			return "", fmt.Errorf("failed to get canary image reference: %w", err)
		}

		t.Logf("Creating test workload in namespace %q...", ns.Name)

		if err := idleConnectionCreateBackendService(context.Background(), t, ns.Name, webService, canaryImage); err != nil {
			return "", fmt.Errorf("failed to create service %s: %w", webService, err)
		}

		routeName := types.NamespacedName{Namespace: ns.Name, Name: webService}
		route := buildRoute(routeName.Name, routeName.Namespace, webService)
		if err := kclient.Create(context.Background(), route); err != nil {
			return "", fmt.Errorf("failed to create route %s: %w", routeName, err)
		}

		if err := waitForRouteIngressConditions(t, kclient, routeName, ic.Name, routev1.RouteIngressCondition{
			Type:   routev1.RouteAdmitted,
			Status: corev1.ConditionTrue,
		}); err != nil {
			return "", fmt.Errorf("failed to observe route admitted condition: %w", err)
		}

		if err := kclient.Get(context.Background(), routeName, route); err != nil {
			return "", fmt.Errorf("failed to get route %s: %w", routeName, err)
		}

		routeHost := getRouteHost(route, ic.Name)
		if routeHost == "" {
			return "", fmt.Errorf("route %s has no host assigned by IngressController %s", routeName, ic.Name)
		}

		return routeHost, nil
	}

	testName := names.SimpleNameGenerator.GenerateName("http-keep-alive-")
	testNs := createNamespace(t, testName)

	ic, err := createIngressController(t, testNs)
	if err != nil {
		t.Fatalf("Failed to create IngressController %s: %v", testName, err)
	}

	routeHost, err := createTestServicesAndTestRoute(testNs, ic)
	if err != nil {
		t.Fatalf("Test setup failed: %v", err)
	}

	elbAddress, err := resolveIngressControllerAddress(t, ic)
	if err != nil {
		t.Fatalf("Test setup failed: %v", err)
	}

	// Create an HTTP client that reuses connections by default.
	httpClient := &http.Client{
		Timeout: time.Minute, // full request roundtrip (client-server-client)
		Transport: &addressCapturingRoundTripper{
			BaseTransport: &http.Transport{
				// Set client's idle timeout high to prevent it from closing the connection.
				IdleConnTimeout: 600 * time.Second,
				// Set connection pool sizes high to ensure the connection reuse.
				MaxConnsPerHost:     100, // active + idle per URL host
				MaxIdleConns:        100, // all idle
				MaxIdleConnsPerHost: 100, // idle per host
			},
		},
	}
	t.Logf("Checking connectivity to test route...")
	if err := checkRouteConnectivity(t, httpClient, elbAddress, routeHost); err != nil {
		t.Fatalf("Failed to check the connectivity to route %q: %v", routeHost, err)
	}

	t.Logf("Checking connection reuse by sending %d requests...", numConnReuseAttempts)
	// Send N requests to make sure the connection keep alive is working.
	prevLocalAddr := ""
	for i := 1; i < (numConnReuseAttempts + 1); i++ {
		response, localAddr, err := idleConnectionFetchResponse(t, httpClient, elbAddress, routeHost)
		if err != nil {
			t.Errorf("Request %d failed: %v", i, err)
		}
		if !strings.HasPrefix(response, webService) {
			t.Errorf("Request %d failed: wrong response: %s", i, response)
		}
		if prevLocalAddr == "" {
			prevLocalAddr = localAddr
		} else if prevLocalAddr != localAddr {
			t.Errorf("Request %d failed: unexpected new connection", i)
		}
	}

	// Don't send requests for longer than HTTPKeepAliveTimeout.
	sleepDuration := time.Duration(httpKATimeoutSeconds+1) * time.Second
	t.Logf("Going to sleep for %v...", sleepDuration)
	time.Sleep(sleepDuration)

	// Send the last request to see that a new connection was created by the client.
	// Old connection is supposed to be reset by the router due to expired http-keep-alive.
	t.Logf("Checking connection reset...")
	response, localAddr, err := idleConnectionFetchResponse(t, httpClient, elbAddress, routeHost)
	if err != nil {
		t.Errorf("Last request failed: %v", err)
	}
	if !strings.HasPrefix(response, webService) {
		t.Errorf("Last request failed: wrong response: %s", response)
	}
	if prevLocalAddr == localAddr {
		t.Errorf("Last request failed: unexpected reused connection after keep alive timeout expired")
	}
}
