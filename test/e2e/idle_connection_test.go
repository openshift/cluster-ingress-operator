//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apiserver/pkg/storage/names"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
)

const (
	webService1 = "web-service-1"
	webService2 = "web-service-2"
)

// Test_ImmediateIdleConnectionTerminationPolicy tests:
//  1. Deploys two backend services (`web-service-1` and `web-service-2`).
//  2. Alternates a Route between the backends.
//  3. Validates that HAProxy routes requests to the correct backend
//     according to the policy `Immediate`.
func Test_ImmediateIdleConnectionTerminationPolicy(t *testing.T) {
	t.Parallel()

	testName := names.SimpleNameGenerator.GenerateName("idle-close-on-response-immediate-")
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: testName}

	t.Logf("Creating IngressController %q\n", icName.Name)
	ic := newLoadBalancerController(icName, icName.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.IdleConnectionTerminationPolicy = operatorv1.IngressControllerConnectionTerminationPolicyImmediate
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.ExternalLoadBalancer,
		DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
	}
	err := kclient.Create(context.Background(), ic)
	if err != nil {
		t.Fatalf("failed to create IngressController: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if ic, err = getIngressController(t, kclient, icName, 1*time.Minute); err != nil {
		t.Fatalf("failed to get IngressController: %v", err)
	}

	elbHostname := getIngressControllerLBAddress(t, ic)
	t.Logf("Verifying IngressController on host %q", elbHostname)
	externalTestPodName := types.NamespacedName{Name: icName.Name + "-external-verify", Namespace: icName.Namespace}
	verifyExternalIngressController(t, externalTestPodName, "apps."+ic.Spec.Domain, elbHostname)

	podImage, err := getCanaryImageReference(t)
	if err != nil {
		t.Fatalf("failed to get canary image reference: %v", err)
	}

	ns := createNamespace(t, icName.Name)

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

	httpClient := &http.Client{
		Timeout: time.Minute,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			MaxConnsPerHost:     100,
			IdleConnTimeout:     300 * time.Second,
		},
	}

	actions := []struct {
		description      string
		fetchResponse    func() (string, error)
		expectedResponse func() string
	}{
		{
			description: "Verify response is from web-service-1",
			fetchResponse: func() (string, error) {
				return idleConnectionFetchResponse(httpClient, elbHostname, routeHost)
			},
			expectedResponse: func() string {
				return webService1
			},
		},
		{
			description: "Switch route to web-service-2 and fetch response",
			fetchResponse: func() (string, error) {
				if err := idleConnectionSwitchRouteService(t, routeName, ic.Name, webService2); err != nil {
					return "", err
				}
				return idleConnectionFetchResponse(httpClient, elbHostname, routeHost)
			},
			expectedResponse: func() string {
				return webService2
			},
		},
		{
			description: "Verify response is from web-service-2",
			fetchResponse: func() (string, error) {
				return idleConnectionFetchResponse(httpClient, elbHostname, routeHost)
			},
			expectedResponse: func() string {
				return webService2
			},
		},
	}

	for step, action := range actions {
		t.Logf("[Immediate] step %d: %q", step+1, action.description)
		if got, err := action.fetchResponse(); err != nil {
			t.Fatalf("[Immediate] step %d: failed: %v", step+1, err)
		} else if want := action.expectedResponse(); got != want {
			t.Fatalf("[Immediate] step %d: unexpected response: got %q, want %q", step+1, got, want)
		}
	}
}

// Test_DeferredIdleConnectionTerminationPolicy tests:
//  1. Deploys two backend services (`web-service-1` and `web-service-2`).
//  2. Alternates a Route between the backends.
//  3. Validates that HAProxy routes requests to the correct backend
//     according to the policy `Immediate`.
func Test_DeferredIdleConnectionTerminationPolicy(t *testing.T) {
	t.Parallel()

	testName := names.SimpleNameGenerator.GenerateName("idle-close-on-response-deferred-")
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: testName}

	t.Logf("Creating IngressController %q\n", icName.Name)
	ic := newLoadBalancerController(icName, icName.Name+"."+dnsConfig.Spec.BaseDomain)
	ic.Spec.IdleConnectionTerminationPolicy = operatorv1.IngressControllerConnectionTerminationPolicyDeferred
	ic.Spec.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope:               operatorv1.ExternalLoadBalancer,
		DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
	}
	err := kclient.Create(context.Background(), ic)
	if err != nil {
		t.Fatalf("failed to create IngressController: %v", err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if ic, err = getIngressController(t, kclient, icName, 1*time.Minute); err != nil {
		t.Fatalf("failed to get IngressController: %v", err)
	}

	elbHostname := getIngressControllerLBAddress(t, ic)
	t.Logf("Verifying IngressController on host %q", elbHostname)
	externalTestPodName := types.NamespacedName{Name: icName.Name + "-external-verify", Namespace: icName.Namespace}
	verifyExternalIngressController(t, externalTestPodName, "apps."+ic.Spec.Domain, elbHostname)

	podImage, err := getCanaryImageReference(t)
	if err != nil {
		t.Fatalf("failed to get canary image reference: %v", err)
	}

	ns := createNamespace(t, icName.Name)

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

	if err := kclient.Get(context.Background(), routeName, route); err != nil {
		t.Fatalf("failed to get route %s: %v", routeName, err)
	}

	routeHost := getRouteHost(route, ic.Name)
	if routeHost == "" {
		t.Fatalf("route %s has no host assigned by IngressController %s", routeName, ic.Name)
	}
	t.Logf("test host: %s", routeHost)

	httpClient := &http.Client{
		Timeout: time.Minute,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			MaxConnsPerHost:     100,
			IdleConnTimeout:     300 * time.Second,
		},
	}

	actions := []struct {
		description      string
		fetchResponse    func() (string, error)
		expectedResponse func() string
	}{
		{
			description: "Verify response is from web-service-1",
			fetchResponse: func() (string, error) {
				return idleConnectionFetchResponse(httpClient, elbHostname, routeHost)
			},
			expectedResponse: func() string {
				return webService1
			},
		},
		{
			description: "Switch route to web-service-2 and fetch response",
			fetchResponse: func() (string, error) {
				if err := idleConnectionSwitchRouteService(t, routeName, ic.Name, webService2); err != nil {
					return "", err
				}
				return idleConnectionFetchResponse(httpClient, elbHostname, routeHost)
			},
			expectedResponse: func() string {
				return webService1
			},
		},
		{
			description: "Verify response is from web-service-2",
			fetchResponse: func() (string, error) {
				return idleConnectionFetchResponse(httpClient, elbHostname, routeHost)
			},
			expectedResponse: func() string {
				return webService2
			},
		},
	}

	for step, action := range actions {
		t.Logf("[Deferred] step %d: %q", step+1, action.description)
		if got, err := action.fetchResponse(); err != nil {
			t.Fatalf("[Deferred] step %d: failed: %v", step+1, err)
		} else if want := action.expectedResponse(); got != want {
			t.Fatalf("[Deferred] step %d: unexpected response: got %q, want %q", step+1, got, want)
		}
	}
}

func getCanaryImageReference(t *testing.T) (string, error) {
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
	time.Sleep(10 * time.Second)

	return nil
}

func idleConnectionFetchResponse(httpClient *http.Client, elbhostname, hostname string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "http://"+elbhostname, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Host = hostname

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
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
