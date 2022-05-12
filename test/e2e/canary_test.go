//go:build e2e
// +build e2e

package e2e

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	canarycontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/canary"

	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// TestCanaryRoute tests the ingress canary route
// and checks that the serve-healthcheck echo server
// works as expected.
func TestCanaryRoute(t *testing.T) {
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}

	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	// check that the default ingress controller is ready
	def := &operatorv1.IngressController{}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := kclient.Get(context.TODO(), defaultName, def); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}

	// Get default ingress controller deployment
	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(def), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}

	// Get canary route
	canaryRoute := &routev1.Route{}
	name := controller.CanaryRouteName()
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, canaryRoute); err != nil {
			t.Logf("failed to get canary route %s: %v", name, err)
			return false, nil
		}

		return true, nil
	})

	if err != nil {
		t.Fatalf("failed to observe canary route: %v", err)
	}

	image := deployment.Spec.Template.Spec.Containers[0].Image
	clientPod := buildCanaryCurlPod("canary-route-check", canaryRoute.Namespace, image, canaryRoute.Spec.Host)
	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("failed to create pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), clientPod); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Errorf("failed to delete pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
		}
	}()

	// Test canary route and verify that the hello-openshift echo pod is running properly.
	err = wait.PollImmediate(1*time.Second, 5*time.Minute, func() (bool, error) {
		readCloser, err := client.CoreV1().Pods(clientPod.Namespace).GetLogs(clientPod.Name, &corev1.PodLogOptions{
			Container: "curl",
			Follow:    false,
		}).Stream(context.TODO())
		if err != nil {
			return false, nil
		}
		scanner := bufio.NewScanner(readCloser)
		defer func() {
			if err := readCloser.Close(); err != nil {
				t.Errorf("failed to close reader for pod %s: %v", clientPod.Name, err)
			}
		}()
		foundBody := false
		foundRequestPortHeader := false
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, canarycontroller.CanaryHealthcheckResponse) {
				foundBody = true
			}
			if strings.Contains(strings.ToLower(line), "x-request-port:") {
				foundRequestPortHeader = true
			}
			if foundBody && foundRequestPortHeader {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("failed to observe the expected canary response body: %v", err)
	}
}

// buildCanaryCurlPod returns a pod definition for a pod with the given name and image
// and in the given namespace that curls the specified route via the route's hostname.
func buildCanaryCurlPod(name, namespace, image, host string) *corev1.Pod {
	curlArgs := []string{
		"-s", "-v", "-k", "-L",
		"--retry", "300", "--retry-delay", "1", "--max-time", "2",
	}
	curlArgs = append(curlArgs, "http://"+host)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "curl",
					Image:   image,
					Command: []string{"/bin/curl"},
					Args:    curlArgs,
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}
