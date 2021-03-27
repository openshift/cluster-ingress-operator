// +build e2e

package e2e

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/client-go/kubernetes"
)

func TestHeaderNameCaseAdjustment(t *testing.T) {
	testHeaderNames := []operatorv1.IngressControllerHTTPHeaderNameCaseAdjustment{
		"X-Forwarded-For",
		"Cache-Control",
	}
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "header-name-case-adjustment"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		HeaderNameCaseAdjustments: testHeaderNames,
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

	echoPod := buildEchoPod("header-name-case-adjustment-echo", deployment.Namespace)
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
	echoRoute.Annotations = map[string]string{
		"haproxy.router.openshift.io/h1-adjust-case": "true",
	}
	if err := kclient.Create(context.TODO(), echoRoute); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), echoRoute); err != nil {
			t.Fatalf("failed to delete route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
		}
	}()

	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	name := "header-name-case-adjustment-test"
	image := deployment.Spec.Template.Spec.Containers[0].Image
	clientPod := buildCurlPod(name, echoRoute.Namespace, image, echoRoute.Spec.Host, service.Spec.ClusterIP, "-v")
	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientPod); err != nil {
			t.Fatalf("failed to delete pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
		}
	}()

	err = wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
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
		foundHeaderNames := map[string]bool{}
		for scanner.Scan() {
			line := scanner.Text()
			if idx := strings.Index(line, ":"); idx != -1 {
				headerName := line[:idx]
				// The echo server echoes back the request
				// headers without any prefix.  Curl prints the
				// response headers prefixed with "< ".
				if strings.HasPrefix(headerName, "< ") {
					headerName = headerName[2:]
				}
				foundHeaderNames[headerName] = true
			}
		}
		for _, expectedHeaderName := range testHeaderNames {
			if !foundHeaderNames[string(expectedHeaderName)] {
				return false, nil
			}
		}
		return true, nil
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
