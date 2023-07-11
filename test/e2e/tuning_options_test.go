//go:build e2e
// +build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/kubernetes"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestHAProxyTimeouts(t *testing.T) {
	t.Parallel()
	// Use timeout values that are more likely to be unique across the ENTIRE
	// test suite
	const (
		clientTimeoutInput     = 47 * time.Second
		clientTimeoutOutput    = "47s"
		clientFinTimeoutInput  = 1500 * time.Millisecond
		clientFinTimeoutOutput = "1500ms"
		serverTimeoutInput     = 92 * time.Second
		serverTimeoutOutput    = "92s"
		serverFinTimeoutInput  = 7 * time.Second
		serverFinTimeoutOutput = "7s"
		tunnelTimeoutInput     = 93 * time.Minute
		tunnelTimeoutOutput    = "93m"
		tlsInspectDelayInput   = 720 * time.Hour
		tlsInspectDelayOutput  = "2147483647ms" // 720h is greater than the maximum timeout, so it should get clipped to max
	)
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "haproxy-timeout"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.TuningOptions = operatorv1.IngressControllerTuningOptions{
		ClientTimeout:    &metav1.Duration{Duration: clientTimeoutInput},
		ClientFinTimeout: &metav1.Duration{Duration: clientFinTimeoutInput},
		ServerTimeout:    &metav1.Duration{Duration: serverTimeoutInput},
		ServerFinTimeout: &metav1.Duration{Duration: serverFinTimeoutInput},
		TunnelTimeout:    &metav1.Duration{Duration: tunnelTimeoutInput},
		TLSInspectDelay:  &metav1.Duration{Duration: tlsInspectDelayInput},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		switch envVar.Name {
		case "ROUTER_DEFAULT_CLIENT_TIMEOUT":
			if envVar.Value != clientTimeoutOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, clientTimeoutOutput, envVar.Value)
			}
		case "ROUTER_CLIENT_FIN_TIMEOUT":
			if envVar.Value != clientFinTimeoutOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, clientFinTimeoutOutput, envVar.Value)
			}
		case "ROUTER_DEFAULT_SERVER_TIMEOUT":
			if envVar.Value != serverTimeoutOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, serverTimeoutOutput, envVar.Value)
			}
		case "ROUTER_DEFAULT_SERVER_FIN_TIMEOUT":
			if envVar.Value != serverFinTimeoutOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, serverFinTimeoutOutput, envVar.Value)
			}
		case "ROUTER_DEFAULT_TUNNEL_TIMEOUT":
			if envVar.Value != tunnelTimeoutOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, tunnelTimeoutOutput, envVar.Value)
			}
		case "ROUTER_INSPECT_DELAY":
			if envVar.Value != tlsInspectDelayOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, tlsInspectDelayOutput, envVar.Value)
			}
		}
	}

	var routerPod corev1.Pod
	podList := &corev1.PodList{}
	labels := map[string]string{
		controller.ControllerDeploymentLabel: icName.Name,
	}
	pollErr := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		if err := kclient.List(context.TODO(), podList, client.InNamespace(deployment.Namespace), client.MatchingLabels(labels)); err != nil {
			t.Logf("failed to list pods for ingress controllers %s: %v", ic.Name, err)
			return false, nil
		}

		if len(podList.Items) == 0 {
			t.Logf("failed to find any pods for ingress controller %s", ic.Name)
			return false, nil
		}

		routerPod = podList.Items[0]
		for _, cond := range routerPod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if pollErr != nil {
		t.Fatalf("Router pod %s failed to become ready: %v", routerPod.Name, pollErr)
	}

	var stdout, stderr bytes.Buffer

	timeouts := []struct {
		Name  string
		Value string
	}{
		{Name: "client", Value: clientTimeoutOutput},
		{Name: "client-fin", Value: clientFinTimeoutOutput},
		{Name: "server", Value: serverTimeoutOutput},
		{Name: "server-fin", Value: serverFinTimeoutOutput},
		{Name: "tunnel", Value: tunnelTimeoutOutput},
	}

	for _, timeout := range timeouts {
		cmd := []string{
			"grep",
			"-oP",
			"timeout\\s+" + timeout.Name + "\\s+\\K" + timeout.Value,
			"/var/lib/haproxy/conf/haproxy.config",
		}
		if err := podExec(t, routerPod, &stdout, &stderr, cmd); err != nil {
			t.Errorf("Error executing %s: %v", strings.Join(cmd, " "), err)
			t.Errorf("stderr: %s", stderr.Bytes())
			continue
		}
		value := strings.TrimSpace(stdout.String())
		if value != timeout.Value {
			t.Errorf("Expected value for \"timeout %s\" to be %q, got %q", timeout.Name, timeout.Value, value)
		}
		stdout.Reset()
		stderr.Reset()
	}

	cmd := []string{
		"grep",
		"-oP",
		"tcp-request\\s+inspect-delay\\s+\\K" + tlsInspectDelayOutput,
		"/var/lib/haproxy/conf/haproxy.config",
	}
	if err := podExec(t, routerPod, &stdout, &stderr, cmd); err != nil {
		t.Errorf("Error executing %s: %v", strings.Join(cmd, " "), err)
		t.Errorf("stderr: %s", stderr.Bytes())
	} else {
		values := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		// tcp-request inspect-delay is set in 2 places, but both should match
		if len(values) != 2 {
			t.Errorf("Expected 2 instances of \"tcp-request inspect-delay\", got %v", len(values))
		} else if strings.TrimSpace(values[0]) != tlsInspectDelayOutput ||
			strings.TrimSpace(values[1]) != tlsInspectDelayOutput {
			t.Errorf("Expected value for \"tcp-request inspect-delay\" to be %v, got %v", []string{"5s", "5s"}, values)
		}
	}
}

func TestHAProxyTimeoutsRejection(t *testing.T) {
	t.Parallel()
	const (
		clientTimeoutInput      = -45 * time.Second
		clientFinTimeoutInput   = 0 * time.Millisecond
		serverTimeoutInput      = 0 * time.Second
		serverFinTimeoutInput   = -5 * time.Second
		tunnelTimeoutInput      = -90 * time.Minute
		tlsInspectDelayInput    = -720 * time.Hour
		clientTimeoutDefault    = -45 * time.Second
		clientFinTimeoutDefault = 0 * time.Millisecond
		serverTimeoutDefault    = 0 * time.Second
		serverFinTimeoutDefault = -5 * time.Second
		tunnelTimeoutDefault    = -90 * time.Minute
		tlsInspectDelayDefault  = -720 * time.Hour
	)
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "haproxy-timeout-rejection"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.TuningOptions = operatorv1.IngressControllerTuningOptions{
		ClientTimeout:    &metav1.Duration{Duration: clientTimeoutInput},
		ClientFinTimeout: &metav1.Duration{Duration: clientFinTimeoutInput},
		ServerTimeout:    &metav1.Duration{Duration: serverTimeoutInput},
		ServerFinTimeout: &metav1.Duration{Duration: serverFinTimeoutInput},
		TunnelTimeout:    &metav1.Duration{Duration: tunnelTimeoutInput},
		TLSInspectDelay:  &metav1.Duration{Duration: tlsInspectDelayInput},
	}
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		{Type: operatorv1.LoadBalancerManagedIngressConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.DNSManagedIngressConditionType, Status: operatorv1.ConditionFalse},
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		switch envVar.Name {
		case "ROUTER_DEFAULT_CLIENT_TIMEOUT",
			"ROUTER_CLIENT_FIN_TIMEOUT",
			"ROUTER_DEFAULT_SERVER_TIMEOUT",
			"ROUTER_DEFAULT_SERVER_FIN_TIMEOUT",
			"ROUTER_DEFAULT_TUNNEL_TIMEOUT",
			"ROUTER_INSPECT_DELAY":
			t.Errorf("expected no value for %s, got %q", envVar.Name, envVar.Value)
		}
	}

	var routerPod corev1.Pod
	podList := &corev1.PodList{}
	labels := map[string]string{
		controller.ControllerDeploymentLabel: icName.Name,
	}
	pollErr := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		if err := kclient.List(context.TODO(), podList, client.InNamespace(deployment.Namespace), client.MatchingLabels(labels)); err != nil {
			t.Logf("failed to list pods for ingress controllers %s: %v", ic.Name, err)
			return false, nil
		}

		if len(podList.Items) == 0 {
			t.Logf("failed to find any pods for ingress controller %s", ic.Name)
			return false, nil
		}

		routerPod = podList.Items[0]
		for _, cond := range routerPod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if pollErr != nil {
		t.Fatalf("Router pod %s failed to become ready: %v", routerPod.Name, pollErr)
	}

	var stdout, stderr bytes.Buffer

	timeouts := []struct {
		Name  string
		Value string
	}{
		{Name: "client", Value: "30s"},
		{Name: "client-fin", Value: "1s"},
		{Name: "server", Value: "30s"},
		{Name: "server-fin", Value: "1s"},
		{Name: "tunnel", Value: "1h"},
	}

	for _, timeout := range timeouts {
		cmd := []string{
			"grep",
			"-oP",
			"timeout\\s+" + timeout.Name + "\\s+\\K" + timeout.Value,
			"/var/lib/haproxy/conf/haproxy.config",
		}
		if err := podExec(t, routerPod, &stdout, &stderr, cmd); err != nil {
			t.Errorf("Error executing %s: %v", strings.Join(cmd, " "), err)
			t.Errorf("stderr: %s", stderr.Bytes())
			continue
		}
		value := strings.TrimSpace(stdout.String())
		if value != timeout.Value {
			t.Errorf("Expected value for \"timeout %s\" to be %q, got %q", timeout.Name, timeout.Value, value)
		}
		stdout.Reset()
		stderr.Reset()
	}

	cmd := []string{
		"grep",
		"-oP",
		"tcp-request\\s+inspect-delay\\s+\\K5s",
		"/var/lib/haproxy/conf/haproxy.config",
	}
	if err := podExec(t, routerPod, &stdout, &stderr, cmd); err != nil {
		t.Errorf("Error executing %s: %v", strings.Join(cmd, " "), err)
		t.Errorf("stderr: %s", stderr.Bytes())
	} else {
		values := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		// tcp-request inspect-delay is set in 2 places, but both should match
		if len(values) != 2 {
			t.Errorf("Expected 2 instances of \"tcp-request inspect-delay\", got %v", len(values))
		} else {
			inspectDelayDefault := "5s"
			if strings.TrimSpace(values[0]) != inspectDelayDefault ||
				strings.TrimSpace(values[1]) != inspectDelayDefault {
				t.Errorf("Expected value for \"tcp-request inspect-delay\" to be %v, got %v", []string{inspectDelayDefault, inspectDelayDefault}, values)
			}
		}
	}
}

// TestCookieLen verifies that when logging is enabled and a cookie capture is specified, any captured cookies are
// truncated at the expected max length.
func TestCookieLen(t *testing.T) {
	testCases := []struct {
		name      string
		maxLength int
	}{
		// 63 bytes is maximum cookie length allowed when tune.http.cookielen is unset in the HAProxy config.
		{"63 bytes", 63},
		// 64 bytes is the smallest number requires tune.http.cookielen to be set.
		{"64 bytes", 64},
		// 128 bytes is an arbitrary number between the 64 and 1024 bytes. It requires tune.http.cookielen to be set,
		// but is between the boundaries.
		{"128 bytes", 128},
		// The upper bound. 1024 bytes is the maximum allowed by the OpenShift API.
		{"1024 bytes", 1024},
	}
	testNamespace := "openshift-ingress"
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			maxLength := testCase.maxLength
			t.Parallel()
			icName := types.NamespacedName{
				Namespace: operatorNamespace,
				Name:      names.SimpleNameGenerator.GenerateName("test-cookielen-"),
			}
			domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
			ic := newPrivateController(icName, domain)

			cookieName := "X-foo-bar"
			ic.Spec.Logging = &operatorv1.IngressControllerLogging{
				Access: &operatorv1.AccessLogging{
					Destination: operatorv1.LoggingDestination{
						Type: "Container",
						Container: &operatorv1.ContainerLoggingDestinationParameters{
							MaxLength: 2048,
						},
					},
					HTTPCaptureCookies: []operatorv1.IngressControllerCaptureHTTPCookie{{
						IngressControllerCaptureHTTPCookieUnion: operatorv1.IngressControllerCaptureHTTPCookieUnion{
							MatchType: operatorv1.CookieMatchTypeExact,
							Name:      cookieName,
						},
						MaxLength: maxLength,
					}},
					HttpLogFormat: "cookie: %CC",
				},
			}

			if err := kclient.Create(context.TODO(), ic); err != nil {
				t.Fatalf("Failed to create ingresscontroller %s: %v", icName, err)
			}
			defer assertIngressControllerDeleted(t, kclient, ic)

			if err := waitForIngressControllerCondition(t, kclient, 2*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
				t.Fatalf("Timed out waiting for ingresscontroller %s to become available: %v", icName.Name, err)
			}

			echoPod := buildEchoPod(names.SimpleNameGenerator.GenerateName("echo-pod-"), testNamespace)
			if err := kclient.Create(context.TODO(), echoPod); err != nil {
				t.Fatalf("failed to create pod %s/%s: %v", echoPod.Namespace, echoPod.Name, err)
			}
			defer assertDeleted(t, kclient, echoPod)

			echoService := buildEchoService(echoPod.Name, echoPod.Namespace, echoPod.ObjectMeta.Labels)
			if err := kclient.Create(context.TODO(), echoService); err != nil {
				t.Fatalf("failed to create service %s/%s: %v", echoService.Namespace, echoService.Name, err)
			}
			defer assertDeleted(t, kclient, echoService)

			echoRoute := buildRoute(echoPod.Name, echoPod.Namespace, echoService.Name)
			if err := kclient.Create(context.TODO(), echoRoute); err != nil {
				t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
			}
			defer assertDeleted(t, kclient, echoRoute)

			// Wait for echo pod to be ready
			err := wait.PollImmediate(2*time.Second, 1*time.Minute, func() (bool, error) {
				if err := kclient.Get(context.TODO(), types.NamespacedName{Name: echoPod.Name, Namespace: echoPod.Namespace}, echoPod); err != nil {
					t.Logf("failed to get client pod %q: %v", echoPod.Name, err)
					return false, nil
				}
				for _, cond := range echoPod.Status.Conditions {
					if cond.Type == corev1.PodReady {
						return cond.Status == corev1.ConditionTrue, nil
					}
				}
				return false, nil
			})

			if err != nil {
				t.Fatalf("timed out waiting for pod %s to become ready: %v", echoPod.Name, err)
			}

			// Send a request through the router that includes the captured cookie, and verify that it's truncated as expected.

			// Use the router image for the client pod since it includes curl.
			routerDeployment := &appsv1.Deployment{}
			routerDeploymentName := controller.RouterDeploymentName(ic)
			if err := kclient.Get(context.TODO(), routerDeploymentName, routerDeployment); err != nil {
				t.Fatalf("Failed to get router deployment for ingresscontroller %s: %v", icName.Name, err)
			}

			curlPodImage := ""
			for _, container := range routerDeployment.Spec.Template.Spec.Containers {
				if container.Name == "router" {
					curlPodImage = container.Image
					break
				}
			}
			if curlPodImage == "" {
				t.Fatalf("Failed to find container \"router\" in deployment %s", routerDeployment.Name)
			}
			curlPodName := types.NamespacedName{
				Name:      names.SimpleNameGenerator.GenerateName("cookielen-curl-"),
				Namespace: testNamespace,
			}

			icInternalService := &corev1.Service{}
			icInternalServiceName := controller.InternalIngressControllerServiceName(ic)
			if err := kclient.Get(context.TODO(), icInternalServiceName, icInternalService); err != nil {
				t.Fatalf("failed to get service %q: %v", icInternalServiceName, err)
			}

			// Create a cookie that's one character over the max length of a captured cookie. Pad the cookie with a bunch of
			// 'a's, and include an x at the end that should get truncated. The name of the cookie and the '=' are included in
			// the max length, so account for that in the size of the padding.
			padding := strings.Repeat("a", maxLength-len(cookieName)-1)
			cookieString := fmt.Sprintf("%s=%sx", cookieName, padding)

			curlArgs := []string{
				"-b", cookieString,
				// --resolve used this way makes curl send the request to our ingress controller's internal service address as
				// if it was directed to it by a load balancer. This ensures the request is handled by our ingress controller
				// without having to wait extra time for a load balancer to be provisioned.
				"--resolve", fmt.Sprintf("%s:80:%s", echoRoute.Spec.Host, icInternalService.Spec.ClusterIP),
			}

			curlPod := buildCurlPod(curlPodName.Name, curlPodName.Namespace, curlPodImage, echoRoute.Spec.Host, "", curlArgs...)
			if err := kclient.Create(context.TODO(), curlPod); err != nil {
				t.Fatalf("failed to create pod %q: %v", curlPodName.Name, err)
			}
			defer assertDeleted(t, kclient, curlPod)

			// Wait for the curl pod to complete its request
			err = wait.PollImmediate(2*time.Second, 3*time.Minute, func() (bool, error) {
				if err := kclient.Get(context.TODO(), curlPodName, curlPod); err != nil {
					t.Logf("failed to get client pod %q: %v", curlPodName, err)
					return false, nil
				}
				return curlPod.Status.Phase == corev1.PodSucceeded, nil
			})

			if err != nil {
				t.Fatalf("timed out waiting for pod %q to transition to \"Succeeded\". current phase is %q. error: %v", curlPodName, curlPod.Status.Phase, err)
			}

			// Find the router pod for our ingress controller so we can check its logs. This ingress controller specifies
			// replicas=1, so there will only be one.
			routerPodList, err := getPods(t, kclient, routerDeployment)
			if err != nil {
				t.Fatalf("failed to get pods in deployment %s: %v", routerDeploymentName, err)
			} else if len(routerPodList.Items) != 1 {
				t.Fatalf("expected to find 1 pod in deployment %s, found %d", routerDeploymentName, len(routerPodList.Items))
			}
			routerPod := routerPodList.Items[0]

			// Get the logs from the router pod.
			kubeConfig, err := config.GetConfig()
			if err != nil {
				t.Fatalf("failed to get kube config: %v", err)
			}

			client, err := kubernetes.NewForConfig(kubeConfig)
			if err != nil {
				t.Fatalf("failed to create kube client: %v", err)
			}

			logsContainerName := "logs"
			foundExpectedCookie := false
			expectedTruncatedCookieString := fmt.Sprintf("%s=%s", cookieName, padding) // Same as cookieString, minus the 'x' at the end.
			err = wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
				readCloser, err := client.CoreV1().Pods(routerPod.Namespace).GetLogs(routerPod.Name, &corev1.PodLogOptions{
					Container: logsContainerName,
					Follow:    false,
				}).Stream(context.TODO())
				if err != nil {
					t.Logf("Failed to read logs from pod %s container %s: %v", routerPod.Name, logsContainerName, err)
					return false, nil
				}

				defer func() {
					if err := readCloser.Close(); err != nil {
						t.Logf("Failed to close reader for pod %s container %s: %v", routerPod.Name, logsContainerName, err)
					}
				}()

				// Search the logs container for the specified cookie, making sure that it exactly matches the expected truncated value.
				scanner := bufio.NewScanner(readCloser)
				for scanner.Scan() {
					line := strings.TrimSpace(scanner.Text())
					if strings.Contains(line, cookieString) {
						t.Fatalf("found untruncated cookie string: %v", cookieString)
					}
					if strings.Contains(line, expectedTruncatedCookieString) {
						foundExpectedCookie = true
					}
				}
				if err := scanner.Err(); err != nil {
					t.Logf("Error while reading container logs: %v", err)
					return false, nil
				}
				return foundExpectedCookie, nil
			})
			if !foundExpectedCookie {
				t.Fatalf("did not find expected truncated cookie string: %v", expectedTruncatedCookieString)
			}
		})
	}
}
