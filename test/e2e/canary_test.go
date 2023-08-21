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

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	canarycontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/canary"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/pointer"
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

	canaryRouteHost := getRouteHost(t, canaryRoute, defaultName.Name)

	image := deployment.Spec.Template.Spec.Containers[0].Image
	clientPod := buildCanaryCurlPod("canary-route-check", canaryRoute.Namespace, image, canaryRouteHost)
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
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: pointer.Bool(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
						RunAsNonRoot: pointer.Bool(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

func TestCanaryWithMTLS(t *testing.T) {
	defaultIC := &operatorv1.IngressController{}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	caCert := MustCreateTLSKeyCert("root-ca", time.Now(), time.Now().Add(24*time.Hour), true, nil, nil)

	clientCAConfigmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "canary-with-mtls-client-ca",
			Namespace: "openshift-config",
		},
		Data: map[string]string{
			"ca-bundle.pem": caCert.CertPem,
		},
	}

	if err := kclient.Create(context.TODO(), clientCAConfigmap); err != nil {
		t.Fatalf("Failed to create configmap %s: %v", clientCAConfigmap.Name, err)
	}

	defer assertDeleted(t, kclient, clientCAConfigmap)

	if err := kclient.Get(context.TODO(), defaultName, defaultIC); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}

	// Save the current clientTLS config so it can be restored at the end of the test.
	originalClientTLS := defaultIC.Spec.ClientTLS

	// Set client TLS (mTLS) to required, and use clientCAConfigmap as the CA bundle.
	defaultIC.Spec.ClientTLS = operatorv1.ClientTLS{
		ClientCertificatePolicy: operatorv1.ClientCertificatePolicyRequired,
		ClientCA: configv1.ConfigMapNameReference{
			Name: clientCAConfigmap.Name,
		},
	}
	if err := kclient.Update(context.TODO(), defaultIC); err != nil {
		t.Fatalf("Failed to enable mTLS on default ingress controller: %v", err)
	}

	defer func() {
		// Reset client TLS.
		if err := kclient.Get(context.TODO(), defaultName, defaultIC); err != nil {
			panic(fmt.Errorf("failed to get default ingresscontroller: %v", err))
		}
		defaultIC.Spec.ClientTLS = originalClientTLS
		if err := kclient.Update(context.TODO(), defaultIC); err != nil {
			panic(fmt.Errorf("Failed to restore default ingress configuration: %w", err))
		}
	}()

	// Wait for IC update to completely roll out.
	routerDeployment := &appsv1.Deployment{}
	routerDeploymentName := controller.RouterDeploymentName(defaultIC)
	if err := kclient.Get(context.TODO(), routerDeploymentName, routerDeployment); err != nil {
		t.Fatalf("Failed to get router deployment for ingresscontroller %s: %v", defaultIC.Name, err)
	}

	if err := waitForDeploymentComplete(t, kclient, routerDeployment, 3*time.Minute); err != nil {
		t.Fatalf("Timed out waiting for ingress update to complete: %v", err)
	}

	// Verify that canary does not fail.
	err := wait.PollImmediate(10*time.Second, 6*time.Minute, func() (bool, error) {
		ic := &operatorv1.IngressController{}
		if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
			return false, err
		}
		foundCondition := false
		for _, condition := range ic.Status.Conditions {
			if condition.Type == ingresscontroller.IngressControllerCanaryCheckSuccessConditionType {
				foundCondition = true
				if condition.Status == operatorv1.ConditionFalse {
					t.Errorf("Canary failed: %s", condition.Reason)
					return true, nil
				}
				break
			}
		}
		if !foundCondition {
			t.Errorf("Canary status condition not found")
			return false, nil
		}
		return false, nil
	})

	// Timeout means the canary succeeded for the entire polling interval, so only consider non-timeout errors a
	// failure.
	if err != nil && err != wait.ErrWaitTimeout {
		t.Fatalf("Got unexpected error %v", err)
	}
}
