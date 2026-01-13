//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
)

func TestSecureRedirectCorrectness(t *testing.T) {
	t.Parallel()

	// Ensure we can get the kube config, although kclient is used for most operations.
	_, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}

	name := "redirect-correctness"
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

	// Use an exec pod to run multiple curl commands.
	clientPodImage := deployment.Spec.Template.Spec.Containers[0].Image
	clientPod := buildExecPod(name+"-client", echoRoute.Namespace, clientPodImage)

	if err := kclient.Create(context.TODO(), clientPod); err != nil {
		t.Fatalf("failed to create client pod: %v", err)
	}
	defer func() {
		kclient.Delete(context.TODO(), clientPod)
	}()

	// Wait for client pod to be ready
	if err := wait.PollImmediate(1*time.Second, 2*time.Minute, func() (bool, error) {
		p := &corev1.Pod{}
		if err := kclient.Get(context.TODO(), types.NamespacedName{Name: clientPod.Name, Namespace: clientPod.Namespace}, p); err != nil {
			return false, nil
		}
		if p.Status.Phase == corev1.PodRunning {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Fatalf("client pod did not become ready: %v", err)
	}

	testCases := []struct {
		name        string
		path        string
		expectedLoc string
	}{
		{
			name:        "Root",
			path:        "/",
			expectedLoc: "https://" + echoRoute.Spec.Host + "/",
		},
		{
			name:        "PathOnly",
			path:        "/testpath",
			expectedLoc: "https://" + echoRoute.Spec.Host + "/testpath",
		},
		{
			name:        "PathAndQuery",
			path:        "/testpath?bar=baz",
			expectedLoc: "https://" + echoRoute.Spec.Host + "/testpath?bar=baz",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Service ClusterIP
			destIP := service.Spec.ClusterIP
			url := "http://" + destIP + tc.path

			// curl -v -H "Host: <host>" <url>
			// We need to pass the Host header to match the route
			// And use the Service IP to hit the router
			// -I (head) is enough for checking Location header and 302, but -v gives more debug info.
			cmd := []string{"curl", "-v", "-k", "-H", "Host: " + echoRoute.Spec.Host, url}

			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			// Retry a few times in case of transient issues
			err := wait.PollImmediate(1*time.Second, 30*time.Second, func() (bool, error) {
				stdout.Reset()
				stderr.Reset()
				if err := podExec(t, *clientPod, stdout, stderr, cmd); err != nil {
					t.Logf("exec failed: %v", err)
					return false, nil
				}

				output := stderr.String() + stdout.String()

				lines := strings.Split(output, "\n")
				found302 := false
				foundLocation := false

				for _, line := range lines {
					// Check for status code line from curl -v (starts with "< HTTP/")
					if strings.HasPrefix(strings.ToLower(line), "< http/") {
						if strings.Contains(line, "302 Found") {
							found302 = true
						} else {
							t.Logf("Unexpected status code line: %s", line)
						}
					}

					// Use HasPrefix to ensure it's a response header (starts with "< ")
					// and check specifically for "Location:"
					if strings.HasPrefix(strings.ToLower(line), "< location:") {
						t.Logf("Found location header: %s", line)
						// Extract value: "< Location: https://..." -> "https://..."
						parts := strings.SplitN(line, ":", 2)
						if len(parts) == 2 {
							got := strings.TrimSpace(parts[1])
							if got == tc.expectedLoc {
								foundLocation = true
							} else {
								t.Logf("Location header mismatch. Expected: %s, Got: %s", tc.expectedLoc, got)
							}
						}
					}
				}

				if !found302 {
					t.Logf("Did not find 302 Found in output:\n%s", output)
					return false, nil
				}

				if !foundLocation {
					t.Logf("Location header not matching expected %s. Output:\n%s", tc.expectedLoc, output)
					dumpRouterLogs(t, kclient, icName)
					return false, nil
				}

				return true, nil
			})

			if err != nil {
				t.Fatalf("Test case %s failed: %v", tc.name, err)
			}
		})
	}
}
