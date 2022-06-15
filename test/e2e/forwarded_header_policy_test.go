//go:build e2e
// +build e2e

package e2e

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/client-go/kubernetes"
)

// testPodCount is a counter that is used to give each test pod a distinct name.
var testPodCount int

// testRouteHeaders connects to the specified route using the provided address
// and verifies that the response has the expected number of matches of the
// expected string.  Case is ignored when comparing the expected response and
// the actual response.
func testRouteHeaders(t *testing.T, image string, route *routev1.Route, address string, headers []string, expectedResponse string, expectedMatches int) {
	t.Helper()

	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	var extraCurlArgs []string
	for _, header := range headers {
		extraCurlArgs = append(extraCurlArgs, "-H", header)
	}
	extraCurlArgs = append(extraCurlArgs, "--resolve", route.Spec.Host+":80:"+address)
	testPodCount++
	name := fmt.Sprintf("%s%d", route.Name, testPodCount)
	clientPod := buildCurlPod(name, route.Namespace, image, route.Spec.Host, address, extraCurlArgs...)
	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientPod); err != nil {
			if !errors.IsNotFound(err) {
				t.Fatalf("failed to delete pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
			}
		}
	}()
	expectedResponse = strings.ToLower(expectedResponse)
	err = wait.PollImmediate(1*time.Second, 4*time.Minute, func() (bool, error) {
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
		var numMatches int
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(strings.ToLower(line), expectedResponse) {
				numMatches++
				t.Logf("found match %d of %d expected: %s", numMatches, expectedMatches, line)
			}
		}
		if numMatches > 0 && numMatches != expectedMatches {
			return false, fmt.Errorf("got %d matches for %q, expected %d", numMatches, expectedResponse, expectedMatches)
		}
		return numMatches == expectedMatches, nil
	})
	if err != nil {
		pod := &corev1.Pod{}
		podName := types.NamespacedName{
			Namespace: clientPod.Namespace,
			Name:      clientPod.Name,
		}
		if err := kclient.Get(context.TODO(), podName, pod); err != nil {
			t.Errorf("failed to get pod %s: %v", clientPod.Name, err)
		}

		logs, err := client.CoreV1().Pods(clientPod.Namespace).GetLogs(clientPod.Name, &corev1.PodLogOptions{
			Container: "curl",
			Follow:    false,
		}).DoRaw(context.TODO())
		if err != nil {
			t.Errorf("failed to get logs from pod %s: %v", clientPod.Name, err)
		}

		t.Fatalf("failed to observe the expected output: %v\nclient pod spec: %#v\nclient pod logs:\n%s", err, pod, logs)
	}
}

// TestForwardedHeaderPolicyAppend verifies that the ingress controller has the
// expected behavior if its policy is "Append".  If a client request doesn't
// specify any X-Forwarded-For header, the router should append one.  If the
// client specifies 2 X-Forwarded-For headers, then the router should append a
// 3rd.
func TestForwardedHeaderPolicyAppend(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "forwardedheader-append"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
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
	service := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.InternalIngressControllerServiceName(ic), service); err != nil {
		t.Fatalf("failed to get ingresscontroller service: %v", err)
	}

	// Create a pod and route that echoes back the request.
	echoPod := buildEchoPod("forwarded-header-policy-append-echo", deployment.Namespace)
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

	echoRoute := buildRoute(echoPod.Name, echoPod.Namespace, echoService.Name)
	if err := kclient.Create(context.TODO(), echoRoute); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoRoute); err != nil {
			t.Fatalf("failed to delete route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
		}
	}()

	// Use the OpenShift Router container image, which includes curl, to
	// create a client pod that sends a request to the echo route and checks
	// whether it gets the expected number of X-Forwarded-For headers.
	clientPodImage := deployment.Spec.Template.Spec.Containers[0].Image

	// The default policy is append.  If the client doesn't specify any
	// X-Forwarded-For header in the request, the router should append 1
	// X-Forwarded-For header.
	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, nil, "x-forwarded-for:", 1)
	// If the client specifies 2 X-Forwarded-For headers, then the router
	// should append a 3rd.
	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, []string{"x-forwarded-for:foo", "x-forwarded-for:bar"}, "x-forwarded-for:", 3)

	// Verify that we get the expected behavior if we set the policy to
	// "append" explicitly.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller: %v", err)
	}
	ic.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		ForwardedHeaderPolicy: operatorv1.AppendHTTPHeaderPolicy,
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, "ROUTER_SET_FORWARDED_HEADERS", "append"); err != nil {
		t.Fatalf("failed to observe ROUTER_SET_FORWARDED_HEADERS=append: %v", err)
	}
	if err := waitForDeploymentComplete(t, kclient, deployment, 3*time.Minute); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, nil, "x-forwarded-for:", 1)
	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, []string{"x-forwarded-for:foo", "x-forwarded-for:bar"}, "x-forwarded-for:", 3)
}

// TestForwardedHeaderPolicyReplace verifies that the ingress controller has the
// expected behavior if its policy is "Replace".  A forwarded client request
// should always have exactly 1 X-Forwarded-For header.
func TestForwardedHeaderPolicyReplace(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "forwardedheader-replace"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		ForwardedHeaderPolicy: operatorv1.ReplaceHTTPHeaderPolicy,
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
	service := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.InternalIngressControllerServiceName(ic), service); err != nil {
		t.Fatalf("failed to get ingresscontroller service: %v", err)
	}

	echoPod := buildEchoPod("forwarded-header-policy-replace-echo", deployment.Namespace)
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

	echoRoute := buildRoute(echoPod.Name, echoPod.Namespace, echoService.Name)
	if err := kclient.Create(context.TODO(), echoRoute); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoRoute); err != nil {
			t.Fatalf("failed to delete route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
		}
	}()

	clientPodImage := deployment.Spec.Template.Spec.Containers[0].Image

	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, nil, "x-forwarded-for:", 1)
	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, []string{"x-forwarded-for:foo", "x-forwarded-for:bar"}, "x-forwarded-for:", 1)
}

// TestForwardedHeaderPolicyNever verifies that the ingress controller has the
// expected behavior if its policy is "Never".  A forwarded client request
// should always have exactly as many X-Forwarded-For headers as the client
// specified.
func TestForwardedHeaderPolicyNever(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "forwardedheader-never"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		ForwardedHeaderPolicy: operatorv1.NeverHTTPHeaderPolicy,
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
	service := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.InternalIngressControllerServiceName(ic), service); err != nil {
		t.Fatalf("failed to get ingresscontroller service: %v", err)
	}

	echoPod := buildEchoPod("forwarded-header-policy-never-echo", deployment.Namespace)
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

	echoRoute := buildRoute(echoPod.Name, echoPod.Namespace, echoService.Name)
	if err := kclient.Create(context.TODO(), echoRoute); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoRoute); err != nil {
			t.Fatalf("failed to delete route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
		}
	}()

	clientPodImage := deployment.Spec.Template.Spec.Containers[0].Image

	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, nil, "x-forwarded-for:", 0)
	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, []string{"x-forwarded-for:foo", "x-forwarded-for:bar"}, "x-forwarded-for:", 2)
}

// TestForwardedHeaderPolicyIfNone verifies that the ingress controller has the
// expected behavior if its policy is "IfNone".  A forwarded client request
// should always have at least 1 X-Forwarded-For header, and if the client
// specifies more than 1 X-Forwarded-For header, the forwarded request should
// include exactly as many X-Forwarded-For headers as the client specified.
func TestForwardedHeaderPolicyIfNone(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "forwardedheader-none"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		ForwardedHeaderPolicy: operatorv1.IfNoneHTTPHeaderPolicy,
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
	service := &corev1.Service{}
	if err := kclient.Get(context.TODO(), controller.InternalIngressControllerServiceName(ic), service); err != nil {
		t.Fatalf("failed to get ingresscontroller service: %v", err)
	}

	echoPod := buildEchoPod("forwarded-header-policy-if-none-echo", deployment.Namespace)
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

	echoRoute := buildRoute(echoPod.Name, echoPod.Namespace, echoService.Name)
	if err := kclient.Create(context.TODO(), echoRoute); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoRoute); err != nil {
			t.Fatalf("failed to delete route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
		}
	}()

	clientPodImage := deployment.Spec.Template.Spec.Containers[0].Image

	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, nil, "x-forwarded-for:", 1)
	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, []string{"x-forwarded-for:foo"}, "x-forwarded-for:", 1)
	testRouteHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, []string{"x-forwarded-for:foo", "x-forwarded-for:bar"}, "x-forwarded-for:", 2)
}
