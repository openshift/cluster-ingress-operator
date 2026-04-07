package test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// shellImage returns the container image used for test pods that need
// a shell with standard network tools (bash, curl, ncat, getent).
// Override with the SHELL_IMAGE environment variable.
func shellImage() string {
	if img := os.Getenv("SHELL_IMAGE"); img != "" {
		return img
	}
	return "image-registry.openshift-image-registry.svc:5000/openshift/tools:latest"
}

// createExecPod creates a pod with a shell and waits for it to be running.
// Uses AdminKubeClient. Returns the pod name.
func createExecPod(ctx context.Context, oc *CLI, name string) string {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "shell",
					Image:   shellImage(),
					Command: []string{"sh", "-c", "trap exit TERM; while true; do sleep 5; done"},
				},
			},
		},
	}
	adminKC := oc.AdminKubeClient()
	created, err := adminKC.CoreV1().Pods(oc.Namespace()).Create(ctx, pod, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create exec pod")

	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		p, err := adminKC.CoreV1().Pods(oc.Namespace()).Get(ctx, created.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return p.Status.Phase == corev1.PodRunning, nil
	})
	o.Expect(err).NotTo(o.HaveOccurred(), "exec pod not running")
	return created.Name
}

// waitForDNSResolution waits until DNS resolves the given host from inside
// the exec pod. Uses the CLI's Exec method for oc exec.
func waitForDNSResolution(oc *CLI, execPodName, host string, timeout time.Duration) error {
	getentCmd := fmt.Sprintf("getent hosts %s", host)
	var lastOutput string
	ctx := context.Background()
	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		output, err := oc.Exec(oc.Namespace(), execPodName, getentCmd)
		lastOutput = output
		if err != nil {
			return false, nil
		}
		fmt.Fprintf(g.GinkgoWriter, "DNS resolution for %s:\n%s\n", host, strings.TrimSpace(output))
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("DNS resolution for %s timed out, last output: %s", host, lastOutput)
	}
	return nil
}

// waitForRouteResponse waits until the route responds with HTTP 200 over
// the given IP version. Requires 3 consecutive successes.
func waitForRouteResponse(oc *CLI, execPodName, host, ipFlag string, timeout time.Duration) error {
	curlCmd := fmt.Sprintf("curl %s -k -v -m 10 --connect-timeout 5 -o /dev/null https://%s 2>&1", ipFlag, host)
	var lastOutput string
	consecutiveSuccesses := 0
	requiredSuccesses := 3
	ctx := context.Background()
	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		output, err := oc.Exec(oc.Namespace(), execPodName, curlCmd)
		lastOutput = output
		if err != nil {
			consecutiveSuccesses = 0
			return false, nil
		}
		if strings.Contains(output, "< HTTP/1.1 200") || strings.Contains(output, "< HTTP/2 200") {
			consecutiveSuccesses++
			fmt.Fprintf(g.GinkgoWriter, "curl %s %s: success (%d/%d)\n", ipFlag, host, consecutiveSuccesses, requiredSuccesses)
			if consecutiveSuccesses >= requiredSuccesses {
				fmt.Fprintf(g.GinkgoWriter, "curl %s %s:\n%s\n", ipFlag, host, output)
				return true, nil
			}
			return false, nil
		}
		consecutiveSuccesses = 0
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("curl %s to %s timed out, last output:\n%s", ipFlag, host, lastOutput)
	}
	return nil
}
