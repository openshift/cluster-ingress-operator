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
		t.Fatalf("Failed to get kube config: %v", err)
	}

	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("Failed to create kube client: %v", err)
	}

	t.Log("Checking that the default ingresscontroller is ready...")
	def := &operatorv1.IngressController{}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("Failed to observe expected conditions: %v", err)
	}

	if err := kclient.Get(context.TODO(), defaultName, def); err != nil {
		t.Fatalf("Failed to get the default ingresscontroller: %v", err)
	}

	t.Log("Getting the default ingresscontroller deployment...")
	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(def), deployment); err != nil {
		t.Fatalf("Failed to get the router deployment: %v", err)
	}

	t.Log("Getting the canary route...")
	canaryRoute := &routev1.Route{}
	name := controller.CanaryRouteName()
	err = wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), name, canaryRoute); err != nil {
			t.Logf("Failed to get route %s: %v", name, err)
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		t.Fatalf("Failed to observe canary route: %v", err)
	}

	canaryRouteHost := getRouteHost(canaryRoute, defaultName.Name)
	if canaryRouteHost == "" {
		t.Fatalf("Failed to find host name for the %q router in route %s: %+v", defaultName.Name, name, canaryRoute)
	}

	image := deployment.Spec.Template.Spec.Containers[0].Image
	clientPod := buildCanaryCurlPod("canary-route-check", canaryRoute.Namespace, image, canaryRouteHost)
	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("Failed to create pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
	}
	t.Cleanup(func() {
		if err := kclient.Delete(context.TODO(), clientPod); err != nil {
			if errors.IsNotFound(err) {
				return
			}
			t.Errorf("Failed to delete pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
		}
	})

	t.Log("Curl the canary route and verify that it sends the expected response...")
	var lines []string
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
				t.Errorf("Failed to close reader for logs from pod %s/%s: %v", clientPod.Namespace, clientPod.Name, err)
			}
		}()

		foundBody := false
		foundRequestPortHeader := false
		lines = []string{}
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, canarycontroller.CanaryHealthcheckResponse) {
				foundBody = true
			}
			if strings.Contains(strings.ToLower(line), "x-request-port:") {
				foundRequestPortHeader = true
			}
			lines = append(lines, line)
		}

		if foundBody && foundRequestPortHeader {
			t.Log("Found the expected response body and header")

			return true, nil
		}

		return false, nil
	})
	if err != nil {
		t.Logf("Got pods logs:\n%s", strings.Join(lines, "\n"))

		t.Fatalf("Failed to observe the expected canary response body: %v", err)
	}
}

// TestCanaryRouteClearsSpecHost verifies that the operator clears the canary
// route's spec.host field (which requires deleting and recreating the route) if
// spec.host gets set.
//
// This is a serial test because it modifies the canary route.
func TestCanaryRouteClearsSpecHost(t *testing.T) {
	t.Log("Waiting for the default IngressController to be available...")
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatal(err)
	}

	var canaryRoute routev1.Route
	canaryRouteName := controller.CanaryRouteName()
	t.Log("Getting the canary route...")
	if err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), canaryRouteName, &canaryRoute); err != nil {
			t.Log(err)
			return false, nil
		}

		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	if v := canaryRoute.Spec.Host; len(v) != 0 {
		t.Fatalf("Expected canary route to have empty spec.host, found %q.", v)
	}

	const bogusHost = "foo.bar"
	canaryRoute.Spec.Host = bogusHost
	t.Log("Setting a bogus spec.host...")
	if err := kclient.Update(context.TODO(), &canaryRoute); err != nil {
		t.Fatal(err)
	}

	t.Log("Waiting for the operator to clear spec.host...")
	if err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), canaryRouteName, &canaryRoute); err != nil {
			t.Log(err)
			return false, nil
		}

		switch v := canaryRoute.Spec.Host; v {
		case bogusHost:
			t.Log("The operator has not yet cleared spec.host.")
			return false, nil
		case "":
			t.Log("The operator has cleared spec.host.")
			return true, nil
		default:
			return true, fmt.Errorf("found unexpected spec.host: %q", v)
		}
	}); err != nil {
		t.Fatal(err)
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

// TestCanaryWithMTLS enables mTLS on the default ingress controller, then verifies that the CanaryChecksSucceeding
// condition remains true.
func TestCanaryWithMTLS(t *testing.T) {
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

	t.Cleanup(func() { assertDeleted(t, kclient, clientCAConfigmap) })

	defaultIC := &operatorv1.IngressController{}
	if err := kclient.Get(context.TODO(), defaultName, defaultIC); err != nil {
		t.Fatalf("failed to get default ingresscontroller: %v", err)
	}

	// Save the current clientTLS config so it can be restored at the end of the test.
	originalClientTLS := defaultIC.Spec.ClientTLS.DeepCopy()

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

	t.Cleanup(func() {
		// Reset client TLS.
		if err := updateIngressControllerWithRetryOnConflict(t, defaultName, timeout, func(defaultIC *operatorv1.IngressController) {
			defaultIC.Spec.ClientTLS = *originalClientTLS
		}); err != nil {
			t.Fatalf("Failed to restore default ingress configuration: %v", err)
		}
		t.Log("Restored clientTLS config for default ingresscontroller.")
	})

	// Watch for the RouterClientAuthCA env variable to be non-empty, indicating that the router update has begun to
	// roll out.
	routerDeployment := &appsv1.Deployment{}
	routerDeploymentName := controller.RouterDeploymentName(defaultIC)

	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := kclient.Get(context.TODO(), routerDeploymentName, routerDeployment); err != nil {
			t.Logf("error getting deployment %s/%s: %v", routerDeploymentName.Namespace, routerDeploymentName.Name, err)
			return false, nil
		}
		for _, container := range routerDeployment.Spec.Template.Spec.Containers {
			if container.Name == "router" {
				for _, v := range container.Env {
					if v.Name == ingresscontroller.RouterClientAuthCA {
						return v.Value != "", nil
					}
				}
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Failed while waiting for router update to start: %v", err)
	}

	// Wait for the router update to complete.
	if err := waitForDeploymentComplete(t, kclient, routerDeployment, 3*time.Minute); err != nil {
		t.Fatalf("Timed out waiting for ingress update to complete: %v", err)
	}

	// checkCanarySuccessCondition examines the
	// IngressController's status conditions to check for the
	// IngressControllerCanaryCheckSuccessConditionType condition.
	//
	// Returns:
	//
	// - true, nil:    if the canary is succeeding.
	//
	// - false, nil:   if the canary is failing.
	//
	// - false, error: if the canary's status is unknown, or no status
	//                 condition is found.
	checkCanarySuccessCondition := func(ic *operatorv1.IngressController) (bool, error) {
		for _, condition := range ic.Status.Conditions {
			if condition.Type == ingresscontroller.IngressControllerCanaryCheckSuccessConditionType {
				switch condition.Status {
				case operatorv1.ConditionUnknown:
					return false, fmt.Errorf("Canary status is Unknown: %s", condition.Reason)
				default:
					return condition.Status == operatorv1.ConditionTrue, nil
				}
			}
		}
		return false, fmt.Errorf("Canary success condition not found.")
	}

	// Verify that canary does not fail using PollImmediate.
	// PollImmediate will continue polling until either the canary fails
	// (indicating the test failed), or the timeout is reached (indicating
	// success).
	err = wait.PollImmediate(20*time.Second, 6*time.Minute, func() (bool, error) {
		t.Log("Polling for canary success condition...")
		ic := &operatorv1.IngressController{}
		if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
			t.Logf("Failed to get IngressController: %v", err)
			return false, err
		}
		canarySuccess, err := checkCanarySuccessCondition(ic)
		if err != nil {
			t.Log("Canary success condition not found. Continuing poll.")
		} else if canarySuccess {
			t.Log("Canary check passed. Continuing poll.")
		} else {
			t.Fatalf("Canary check failed. Stopping poll.")
		}
		return false, err
	})

	if err != nil && err != wait.ErrWaitTimeout {
		t.Fatalf("Polling terminated with unexpected error: %v", err)
	} else if err == wait.ErrWaitTimeout {
		t.Log("Polling terminated due to timeout. Assuming canary succeeded for the entire polling interval.")
	} else {
		t.Fatalf("Expected poll to time out.")
	}
}
