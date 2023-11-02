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

var podCount int

const (
	headerName  = "x-frame-options"
	headerValue = "DENY"
)

func testHeaders(t *testing.T, image string, route *routev1.Route, address string, expectedResponse string) {
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

	extraCurlArgs = append(extraCurlArgs, "-v", "--resolve", route.Spec.Host+":80:"+address)
	podCount++
	name := fmt.Sprintf("%s%d", route.Name, podCount)
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
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, expectedResponse) {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Errorf("Failed to observe the expected output, no match for %s: %v", expectedResponse, err)
		pod := &corev1.Pod{}
		podName := types.NamespacedName{
			Namespace: clientPod.Namespace,
			Name:      clientPod.Name,
		}
		if err = kclient.Get(context.TODO(), podName, pod); err != nil {
			t.Errorf("failed to get pod %s: %v", clientPod.Name, err)
		}

		logs, err := client.CoreV1().Pods(clientPod.Namespace).GetLogs(clientPod.Name, &corev1.PodLogOptions{
			Container: "curl",
			Follow:    false,
		}).DoRaw(context.TODO())
		if err != nil {
			t.Errorf("failed to get logs from pod %s: %v", clientPod.Name, err)
		}
		t.Errorf("client pod spec: %#v\nclient pod logs:\n%s", pod, logs)
	}
}

func TestSetIngressControllerResponseHeaders(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "set-response-headers-ingresscontroller"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		Actions: operatorv1.IngressControllerHTTPHeaderActions{
			Response: []operatorv1.IngressControllerHTTPHeader{
				{
					Name: headerName,
					Action: operatorv1.IngressControllerHTTPHeaderActionUnion{
						Type: operatorv1.Set,
						Set: &operatorv1.IngressControllerSetHTTPHeader{
							Value: headerValue,
						},
					},
				},
			},
		},
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

	echoPod := buildEchoPod("set-response-header-via-ic-echo", deployment.Namespace)
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
	cond := routev1.RouteIngressCondition{
		Type:   routev1.RouteAdmitted,
		Status: corev1.ConditionTrue,
	}
	echoRouteName := types.NamespacedName{
		Namespace: echoRoute.Namespace,
		Name:      echoRoute.Name,
	}
	waitForRouteIngressConditions(t, kclient, echoRouteName, ic.Name, cond)

	clientPodImage := deployment.Spec.Template.Spec.Containers[0].Image

	testHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, fmt.Sprintf("%v: %v", headerName, headerValue))
}

func TestSetRouteResponseHeaders(t *testing.T) {
	t.Parallel()
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "set-response-headers-route"}
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

	echoPod := buildEchoPod("set-response-header-via-route-echo", deployment.Namespace)
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

	echoRoute := buildRouteWithHTTPResponseHeaders(echoPod.Name, echoPod.Namespace, echoService.Name, headerName, headerValue)
	if err := kclient.Create(context.TODO(), echoRoute); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoRoute); err != nil {
			t.Fatalf("failed to delete route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
		}
	}()
	cond := routev1.RouteIngressCondition{
		Type:   routev1.RouteAdmitted,
		Status: corev1.ConditionTrue,
	}
	echoRouteName := types.NamespacedName{
		Namespace: echoRoute.Namespace,
		Name:      echoRoute.Name,
	}
	waitForRouteIngressConditions(t, kclient, echoRouteName, ic.Name, cond)

	clientPodImage := deployment.Spec.Template.Spec.Containers[0].Image

	testHeaders(t, clientPodImage, echoRoute, service.Spec.ClusterIP, fmt.Sprintf("%v: %v", headerName, headerValue))
}
