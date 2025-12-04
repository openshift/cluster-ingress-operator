//go:build e2e
// +build e2e

package e2e

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
)

func TestSecureRedirectStripsPort(t *testing.T) {
	t.Parallel()

	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}

	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	name := "redirect-port-stripping"
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: name}
	domain := name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
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

	echoPod := buildEchoPod(name+"-echo", deployment.Namespace)
	if err := kclient.Create(context.TODO(), echoPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", echoPod.Namespace, echoPod.Name, err)
	}
	defer func() {
		kclient.Delete(context.TODO(), echoPod)
	}()

	echoService := buildEchoService(echoPod.Name, echoPod.Namespace, echoPod.ObjectMeta.Labels)
	if err := kclient.Create(context.TODO(), echoService); err != nil {
		t.Fatalf("failed to create service %s/%s: %v", echoService.Namespace, echoService.Name, err)
	}
	defer func() {
		kclient.Delete(context.TODO(), echoService)
	}()

	echoRoute := buildRoute(echoPod.Name, echoPod.Namespace, echoService.Name)
	echoRoute.Spec.TLS = &routev1.TLSConfig{
		Termination:                   routev1.TLSTerminationEdge,
		InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
	}
	if err := kclient.Create(context.TODO(), echoRoute); err != nil {
		t.Fatalf("failed to create route %s/%s: %v", echoRoute.Namespace, echoRoute.Name, err)
	}
	defer func() {
		kclient.Delete(context.TODO(), echoRoute)
	}()

	// Host header with port 80
	hostWithPort := echoRoute.Spec.Host + ":80"

	extraCurlArgs := []string{" -i", "-H", "Host: " + hostWithPort}

	clientPodImage := deployment.Spec.Template.Spec.Containers[0].Image
	clientPod := buildCurlPod(name+"-client", echoRoute.Namespace, clientPodImage, echoRoute.Spec.Host, service.Spec.ClusterIP, extraCurlArgs...)

	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("failed to create client pod: %v", err)
	}
	defer func() {
		kclient.Delete(context.TODO(), clientPod)
	}()

	err = wait.PollImmediate(1*time.Second, 2*time.Minute, func() (bool, error) {
		readCloser, err := kubeClient.CoreV1().Pods(clientPod.Namespace).GetLogs(clientPod.Name, &corev1.PodLogOptions{
			Container: "curl",
		}).Stream(context.TODO())
		if err != nil {
			return false, nil
		}
		defer readCloser.Close()

		scanner := bufio.NewScanner(readCloser)
		found302 := false
		foundLocation := false
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "HTTP/1.1 302 Found") {
				found302 = true
			}
			// Looking for "Location: https://<host><path>"
			// We want to ensure NO ":80" is present.
			if strings.HasPrefix(strings.ToLower(line), "< location:") {
				t.Logf("Found location header: %s", line)
				if strings.Contains(line, "https://"+echoRoute.Spec.Host+"/") && !strings.Contains(line, ":80") {
					foundLocation = true
				}
			}
		}
		if found302 && foundLocation {
			return true, nil
		}
		return false, nil
	})

	if err != nil {
		// Fetch logs for debugging
		logs, _ := kubeClient.CoreV1().Pods(clientPod.Namespace).GetLogs(clientPod.Name, &corev1.PodLogOptions{Container: "curl"}).DoRaw(context.TODO())
		t.Fatalf("did not see expected Location header. Logs:\n%s", string(logs))
	}
}
