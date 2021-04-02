// +build e2e

package e2e

import (
	"bufio"
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/client-go/kubernetes"
)

func TestHTTPHeaderBufferSize(t *testing.T) {
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "header-buffer-size"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	// Create an ingress controller with header buffer values that are 2x
	// the defaults.
	ic.Spec.TuningOptions = operatorv1.IngressControllerTuningOptions{
		HeaderBufferBytes:           65536,
		HeaderBufferMaxRewriteBytes: 16384,
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

	echoPod := buildEchoPod("header-buffer-size-echo", deployment.Namespace)
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

	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	cl, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	// Generate an arbitrary request header with random phony data.
	// Use a random string with length reasonably close to the
	// buffer limits to save space for other headers.
	length := int(ic.Spec.TuningOptions.HeaderBufferBytes-ic.Spec.TuningOptions.HeaderBufferMaxRewriteBytes) - 1024
	random := randomString(length)

	testHeaderName := "test-random-data-header"
	headerString := fmt.Sprintf("%s: %s", testHeaderName, random)
	extraCurlArgs := []string{
		"-v",
		"-H",
		headerString,
	}

	name := "header-buffer-size-test-large-buffers"
	image := deployment.Spec.Template.Spec.Containers[0].Image
	clientPodValidRequest := buildCurlPod(name, echoRoute.Namespace, image, echoRoute.Spec.Host, service.Spec.ClusterIP, extraCurlArgs...)
	if err := kclient.Create(context.TODO(), clientPodValidRequest); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", clientPodValidRequest.Namespace, clientPodValidRequest.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientPodValidRequest); err != nil {
			t.Fatalf("failed to delete pod %s/%s: %v", clientPodValidRequest.Namespace, clientPodValidRequest.Name, err)
		}
	}()

	pollErr := wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		readCloser, err := cl.CoreV1().Pods(clientPodValidRequest.Namespace).GetLogs(clientPodValidRequest.Name, &corev1.PodLogOptions{
			Container: "curl",
			Follow:    false,
		}).Stream(context.TODO())
		if err != nil {
			t.Logf("failed to read output from pod %s: %v", clientPodValidRequest.Name, err)
			return false, nil
		}
		scanner := bufio.NewScanner(readCloser)
		defer func() {
			if err := readCloser.Close(); err != nil {
				t.Errorf("failed to close reader for pod %s: %v", clientPodValidRequest.Name, err)
			}
		}()
		for scanner.Scan() {
			line := scanner.Text()
			if idx := strings.Index(line, ":"); idx != -1 {
				// Ensure that we see the test header name in
				// the echo response. We don't need to verify the
				// contents of the header since HAProxy would return
				// 400 if the header was too large.
				headerName := line[:idx]
				if headerName == testHeaderName {
					return true, nil
				}
			}
		}
		return false, nil
	})
	if pollErr != nil {
		pod := &corev1.Pod{}
		podName := types.NamespacedName{
			Namespace: clientPodValidRequest.Namespace,
			Name:      clientPodValidRequest.Name,
		}
		if err := kclient.Get(context.TODO(), podName, pod); err != nil {
			t.Errorf("failed to get pod %s: %v", clientPodValidRequest.Name, err)
		}

		logs, err := cl.CoreV1().Pods(clientPodValidRequest.Namespace).GetLogs(clientPodValidRequest.Name, &corev1.PodLogOptions{
			Container: "curl",
			Follow:    false,
		}).DoRaw(context.TODO())
		if err != nil {
			t.Errorf("failed to get logs from pod %s: %v", clientPodValidRequest.Name, err)
		}

		output := fmt.Sprintf("failed to observe the expected output: %v\nclient pod spec: %#v\nclient pod logs:\n%s", pollErr, pod, logs)
		output = strings.Replace(output, random, fmt.Sprintf("<%d bytes of random data snipped>", length), 0)
		t.Fatal(output)
	}

	if err := kclient.Get(context.TODO(), types.NamespacedName{Name: ic.Name, Namespace: ic.Namespace}, ic); err != nil {
		t.Errorf("failed to get ingresscontroller %s: %v", ic.Name, err)
	}

	// Get the name of the current router pod for the test ingress controller
	podList := &corev1.PodList{}
	labels := map[string]string{
		controller.ControllerDeploymentLabel: "header-buffer-size",
	}
	if err := kclient.List(context.TODO(), podList, client.InNamespace(deployment.Namespace), client.MatchingLabels(labels)); err != nil {
		t.Errorf("failed to list pods for ingress controllers %s: %v", ic.Name, err)
	}

	if len(podList.Items) != 1 {
		t.Errorf("expected ingress controller %s to have exactly 1 router pod, but it has %d", ic.Name, len(podList.Items))
	}

	oldRouterPodName := podList.Items[0].Name

	// Mutate the ic to use header buffer values that are 1/2 the default.
	ic.Spec.TuningOptions = operatorv1.IngressControllerTuningOptions{
		HeaderBufferBytes:           16384,
		HeaderBufferMaxRewriteBytes: 4096,
	}

	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller %s: %v", icName, err)
	}

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, conditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	// Wait for the new router pod for the updated ingresscontroller to become ready.
	pollErr = wait.PollImmediate(2*time.Second, 3*time.Minute, func() (bool, error) {
		podList := &corev1.PodList{}
		if err := kclient.List(context.TODO(), podList, client.InNamespace(deployment.Namespace), client.MatchingLabels(labels)); err != nil {
			t.Errorf("failed to list pods for ingress controllers %s: %v", ic.Name, err)
		}

		for _, pod := range podList.Items {
			if pod.Name == oldRouterPodName {
				continue
			}

			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					return true, nil
				}
			}

		}
		t.Logf("waiting for new router pod for %s to become ready", ic.Name)
		return false, nil
	})

	if pollErr != nil {
		t.Errorf("timed out waiting for new router pod for %s to become ready: %v", ic.Name, pollErr)
	}

	name = name + "-fail-case"
	clientPodInvalidRequest := buildCurlPod(name, echoRoute.Namespace, image, echoRoute.Spec.Host, service.Spec.ClusterIP, extraCurlArgs...)

	if err := kclient.Create(context.TODO(), clientPodInvalidRequest); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", clientPodInvalidRequest.Namespace, clientPodInvalidRequest.Name, err)
	}

	defer func() {
		if err := kclient.Delete(context.TODO(), clientPodInvalidRequest); err != nil {
			t.Fatalf("failed to delete pod %s/%s: %v", clientPodInvalidRequest.Namespace, clientPodInvalidRequest.Name, err)
		}
	}()

	// Check curl pod logs for a 400 response since the sent headers
	// are too large for HAProxy.
	pollErr = wait.PollImmediate(1*time.Second, 3*time.Minute, func() (bool, error) {
		readCloser, err := cl.CoreV1().Pods(clientPodInvalidRequest.Namespace).GetLogs(clientPodInvalidRequest.Name, &corev1.PodLogOptions{
			Container: "curl",
			Follow:    false,
		}).Stream(context.TODO())
		if err != nil {
			t.Logf("failed to read output from pod %s: %v", clientPodInvalidRequest.Name, err)
			return false, nil
		}
		scanner := bufio.NewScanner(readCloser)
		defer func() {
			if err := readCloser.Close(); err != nil {
				t.Errorf("failed to close reader for pod %s: %v", clientPodInvalidRequest.Name, err)
			}
		}()
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "400 Bad request") {
				return true, nil
			}
		}
		return false, nil
	})
	if pollErr != nil {
		pod := &corev1.Pod{}
		podName := types.NamespacedName{
			Namespace: clientPodInvalidRequest.Namespace,
			Name:      clientPodInvalidRequest.Name,
		}
		if err := kclient.Get(context.TODO(), podName, pod); err != nil {
			t.Errorf("failed to get pod %s: %v", clientPodInvalidRequest.Name, err)
		}

		logs, err := cl.CoreV1().Pods(clientPodInvalidRequest.Namespace).GetLogs(clientPodInvalidRequest.Name, &corev1.PodLogOptions{
			Container: "curl",
			Follow:    false,
		}).DoRaw(context.TODO())
		if err != nil {
			t.Errorf("failed to get logs from pod %s: %v", clientPodInvalidRequest.Name, err)
		}

		output := fmt.Sprintf("failed to observe the expected output: %v\nclient pod spec: %#v\nclient pod logs:\n%s", pollErr, pod, logs)
		output = strings.Replace(output, random, fmt.Sprintf("<%d bytes of random data snipped>", length), 0)
		t.Fatal(output)
	}
}

// randomString quickly returns a string of
// pseudo-random characters.
func randomString(length int) (str string) {
	b := make([]byte, (length+1)/2)
	rand.Read(b)
	// %x will output 2 chars per byte.
	str = fmt.Sprintf("%x", b)
	return
}
