//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestHAProxyTimeouts(t *testing.T) {
	t.Parallel()
	// Use timeout values that are more likely to be unique across the ENTIRE
	// test suite
	const (
		clientTimeoutInput     = 47 * time.Second
		clientTimeoutOutput    = "47s"
		clientFinTimeoutInput  = 1500 * time.Millisecond
		clientFinTimeoutOutput = "1500ms"
		serverTimeoutInput     = 92 * time.Second
		serverTimeoutOutput    = "92s"
		serverFinTimeoutInput  = 7 * time.Second
		serverFinTimeoutOutput = "7s"
		tunnelTimeoutInput     = 93 * time.Minute
		tunnelTimeoutOutput    = "93m"
		tlsInspectDelayInput   = 720 * time.Hour
		tlsInspectDelayOutput  = "2147483647ms" // 720h is greater than the maximum timeout, so it should get clipped to max
	)
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "haproxy-timeout"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.TuningOptions = operatorv1.IngressControllerTuningOptions{
		ClientTimeout:    &metav1.Duration{Duration: clientTimeoutInput},
		ClientFinTimeout: &metav1.Duration{Duration: clientFinTimeoutInput},
		ServerTimeout:    &metav1.Duration{Duration: serverTimeoutInput},
		ServerFinTimeout: &metav1.Duration{Duration: serverFinTimeoutInput},
		TunnelTimeout:    &metav1.Duration{Duration: tunnelTimeoutInput},
		TLSInspectDelay:  &metav1.Duration{Duration: tlsInspectDelayInput},
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
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		switch envVar.Name {
		case "ROUTER_DEFAULT_CLIENT_TIMEOUT":
			if envVar.Value != clientTimeoutOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, clientTimeoutOutput, envVar.Value)
			}
		case "ROUTER_SLOWLORIS_TIMEOUT":
			if envVar.Value != httpRequestTimeout {
				t.Errorf("expected %s = %q, got %q", envVar.Name, clientTimeoutOutput, envVar.Value)
			}
		case "ROUTER_CLIENT_FIN_TIMEOUT":
			if envVar.Value != clientFinTimeoutOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, clientFinTimeoutOutput, envVar.Value)
			}
		case "ROUTER_DEFAULT_SERVER_TIMEOUT":
			if envVar.Value != serverTimeoutOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, serverTimeoutOutput, envVar.Value)
			}
		case "ROUTER_DEFAULT_SERVER_FIN_TIMEOUT":
			if envVar.Value != serverFinTimeoutOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, serverFinTimeoutOutput, envVar.Value)
			}
		case "ROUTER_DEFAULT_TUNNEL_TIMEOUT":
			if envVar.Value != tunnelTimeoutOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, tunnelTimeoutOutput, envVar.Value)
			}
		case "ROUTER_INSPECT_DELAY":
			if envVar.Value != tlsInspectDelayOutput {
				t.Errorf("expected %s = %q, got %q", envVar.Name, tlsInspectDelayOutput, envVar.Value)
			}
		}
	}

	var routerPod corev1.Pod
	podList := &corev1.PodList{}
	labels := map[string]string{
		controller.ControllerDeploymentLabel: icName.Name,
	}
	pollErr := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		if err := kclient.List(context.TODO(), podList, client.InNamespace(deployment.Namespace), client.MatchingLabels(labels)); err != nil {
			t.Logf("failed to list pods for ingress controllers %s: %v", ic.Name, err)
			return false, nil
		}

		if len(podList.Items) == 0 {
			t.Logf("failed to find any pods for ingress controller %s", ic.Name)
			return false, nil
		}

		routerPod = podList.Items[0]
		for _, cond := range routerPod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if pollErr != nil {
		t.Fatalf("Router pod %s failed to become ready: %v", routerPod.Name, pollErr)
	}

	var stdout, stderr bytes.Buffer

	timeouts := []struct {
		Name  string
		Value string
	}{
		{Name: "client", Value: clientTimeoutOutput},
		{Name: "client-fin", Value: clientFinTimeoutOutput},
		{Name: "server", Value: serverTimeoutOutput},
		{Name: "server-fin", Value: serverFinTimeoutOutput},
		{Name: "tunnel", Value: tunnelTimeoutOutput},
	}

	for _, timeout := range timeouts {
		cmd := []string{
			"grep",
			"-oP",
			"timeout\\s+" + timeout.Name + "\\s+\\K" + timeout.Value,
			"/var/lib/haproxy/conf/haproxy.config",
		}
		if err := podExec(t, routerPod, &stdout, &stderr, cmd); err != nil {
			t.Errorf("Error executing %s: %v", strings.Join(cmd, " "), err)
			t.Errorf("stderr: %s", stderr.Bytes())
			continue
		}
		value := strings.TrimSpace(stdout.String())
		if value != timeout.Value {
			t.Errorf("Expected value for \"timeout %s\" to be %q, got %q", timeout.Name, timeout.Value, value)
		}
		stdout.Reset()
		stderr.Reset()
	}

	cmd := []string{
		"grep",
		"-oP",
		"tcp-request\\s+inspect-delay\\s+\\K" + tlsInspectDelayOutput,
		"/var/lib/haproxy/conf/haproxy.config",
	}
	if err := podExec(t, routerPod, &stdout, &stderr, cmd); err != nil {
		t.Errorf("Error executing %s: %v", strings.Join(cmd, " "), err)
		t.Errorf("stderr: %s", stderr.Bytes())
	} else {
		values := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		// tcp-request inspect-delay is set in 2 places, but both should match
		if len(values) != 2 {
			t.Errorf("Expected 2 instances of \"tcp-request inspect-delay\", got %v", len(values))
		} else if strings.TrimSpace(values[0]) != tlsInspectDelayOutput ||
			strings.TrimSpace(values[1]) != tlsInspectDelayOutput {
			t.Errorf("Expected value for \"tcp-request inspect-delay\" to be %v, got %v", []string{"5s", "5s"}, values)
		}
	}
}

func TestHAProxyTimeoutsRejection(t *testing.T) {
	t.Parallel()
	const (
		clientTimeoutInput      = -45 * time.Second
		clientFinTimeoutInput   = 0 * time.Millisecond
		serverTimeoutInput      = 0 * time.Second
		serverFinTimeoutInput   = -5 * time.Second
		tunnelTimeoutInput      = -90 * time.Minute
		tlsInspectDelayInput    = -720 * time.Hour
		clientTimeoutDefault    = -45 * time.Second
		clientFinTimeoutDefault = 0 * time.Millisecond
		serverTimeoutDefault    = 0 * time.Second
		serverFinTimeoutDefault = -5 * time.Second
		tunnelTimeoutDefault    = -90 * time.Minute
		tlsInspectDelayDefault  = -720 * time.Hour
	)
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "haproxy-timeout-rejection"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.TuningOptions = operatorv1.IngressControllerTuningOptions{
		ClientTimeout:    &metav1.Duration{Duration: clientTimeoutInput},
		ClientFinTimeout: &metav1.Duration{Duration: clientFinTimeoutInput},
		ServerTimeout:    &metav1.Duration{Duration: serverTimeoutInput},
		ServerFinTimeout: &metav1.Duration{Duration: serverFinTimeoutInput},
		TunnelTimeout:    &metav1.Duration{Duration: tunnelTimeoutInput},
		TLSInspectDelay:  &metav1.Duration{Duration: tlsInspectDelayInput},
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
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		switch envVar.Name {
		case "ROUTER_DEFAULT_CLIENT_TIMEOUT",
			"ROUTER_SLOWLORIS_TIMEOUT",
			"ROUTER_CLIENT_FIN_TIMEOUT",
			"ROUTER_DEFAULT_SERVER_TIMEOUT",
			"ROUTER_DEFAULT_SERVER_FIN_TIMEOUT",
			"ROUTER_DEFAULT_TUNNEL_TIMEOUT",
			"ROUTER_INSPECT_DELAY":
			t.Errorf("expected no value for %s, got %q", envVar.Name, envVar.Value)
		}
	}

	var routerPod corev1.Pod
	podList := &corev1.PodList{}
	labels := map[string]string{
		controller.ControllerDeploymentLabel: icName.Name,
	}
	pollErr := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		if err := kclient.List(context.TODO(), podList, client.InNamespace(deployment.Namespace), client.MatchingLabels(labels)); err != nil {
			t.Logf("failed to list pods for ingress controllers %s: %v", ic.Name, err)
			return false, nil
		}

		if len(podList.Items) == 0 {
			t.Logf("failed to find any pods for ingress controller %s", ic.Name)
			return false, nil
		}

		routerPod = podList.Items[0]
		for _, cond := range routerPod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if pollErr != nil {
		t.Fatalf("Router pod %s failed to become ready: %v", routerPod.Name, pollErr)
	}

	var stdout, stderr bytes.Buffer

	timeouts := []struct {
		Name  string
		Value string
	}{
		{Name: "client", Value: "30s"},
		{Name: "client-fin", Value: "1s"},
		{Name: "server", Value: "30s"},
		{Name: "server-fin", Value: "1s"},
		{Name: "tunnel", Value: "1h"},
	}

	for _, timeout := range timeouts {
		cmd := []string{
			"grep",
			"-oP",
			"timeout\\s+" + timeout.Name + "\\s+\\K" + timeout.Value,
			"/var/lib/haproxy/conf/haproxy.config",
		}
		if err := podExec(t, routerPod, &stdout, &stderr, cmd); err != nil {
			t.Errorf("Error executing %s: %v", strings.Join(cmd, " "), err)
			t.Errorf("stderr: %s", stderr.Bytes())
			continue
		}
		value := strings.TrimSpace(stdout.String())
		if value != timeout.Value {
			t.Errorf("Expected value for \"timeout %s\" to be %q, got %q", timeout.Name, timeout.Value, value)
		}
		stdout.Reset()
		stderr.Reset()
	}

	cmd := []string{
		"grep",
		"-oP",
		"tcp-request\\s+inspect-delay\\s+\\K5s",
		"/var/lib/haproxy/conf/haproxy.config",
	}
	if err := podExec(t, routerPod, &stdout, &stderr, cmd); err != nil {
		t.Errorf("Error executing %s: %v", strings.Join(cmd, " "), err)
		t.Errorf("stderr: %s", stderr.Bytes())
	} else {
		values := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		// tcp-request inspect-delay is set in 2 places, but both should match
		if len(values) != 2 {
			t.Errorf("Expected 2 instances of \"tcp-request inspect-delay\", got %v", len(values))
		} else {
			inspectDelayDefault := "5s"
			if strings.TrimSpace(values[0]) != inspectDelayDefault ||
				strings.TrimSpace(values[1]) != inspectDelayDefault {
				t.Errorf("Expected value for \"tcp-request inspect-delay\" to be %v, got %v", []string{inspectDelayDefault, inspectDelayDefault}, values)
			}
		}
	}
}
