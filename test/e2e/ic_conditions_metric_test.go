//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	routev1client "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/library-go/test/library/metrics"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// waitForIngressControllerConditionsMetrics waits for the metrics for ingress_controller_conditions to be present.
func waitForIngressControllerConditionsMetrics(t *testing.T, prometheusClient prometheusv1.API) error {
	t.Logf("Waiting for ingress_controller_conditions to be present")
	if err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 2*time.Minute, false, func(context context.Context) (bool, error) {
		result, _, err := prometheusClient.Query(context, "ingress_controller_conditions", time.Now())
		if err != nil {
			t.Logf("Failed to fetch metrics: %v, retrying...", err)
			return false, nil
		}

		// Check if fetched metrics is of Vector type.
		vector, ok := result.(model.Vector)
		if !ok {
			t.Logf("Unexpected metric type, retrying...")
			return false, nil
		}

		// Check if length of returned metric Vector is zero.
		if len(vector) == 0 {
			t.Logf("Metric is empty, retrying...")
			return false, nil
		}

		return true, nil
	}); err != nil {
		return fmt.Errorf("Error waiting for ingress controller metrics: %w", err)
	}
	return nil
}

// restartOperatorPod will restart the operator pod, and if some error occurs it returns it
func restartOperatorPod(t *testing.T, kubeClient kubernetes.Interface) error {
	interval, timeout := 5*time.Second, 5*time.Minute
	var podsList *corev1.PodList

	// Find the operator pod
	t.Logf("Restarting Ingress operator pod...")
	if err := wait.PollUntilContextTimeout(context.Background(), interval, timeout, false, func(context context.Context) (bool, error) {
		innerPodsList, err := kubeClient.CoreV1().Pods("openshift-ingress-operator").List(context, metav1.ListOptions{})
		podsList = innerPodsList
		if err != nil {
			t.Logf("Failed to list pods: %v, retying...", err)
			return false, nil
		}
		return innerPodsList != nil && len(innerPodsList.Items) > 0, nil
	}); err != nil {
		return err
	}
	operatorPodName, operatorPodUID := extractIngressOperatorPodNameAndUID(podsList)

	if operatorPodName == "" || operatorPodUID == "" {
		return errors.New(("Unable to find ingress operator pod"))
	}

	// Delete the operator pod
	if err := wait.PollUntilContextTimeout(context.Background(), interval, timeout, false, func(context context.Context) (bool, error) {
		if err := kubeClient.CoreV1().Pods("openshift-ingress-operator").Delete(context, operatorPodName, metav1.DeleteOptions{}); err != nil {
			t.Logf("Failed to delete operator pod: %v, retying...", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return err
	}

	// Wait for new pod to be ready
	t.Logf("Polling for up to %v to verify that the operator restart is terminated...", timeout)
	if err := wait.PollUntilContextTimeout(context.Background(), interval, timeout, false, func(context context.Context) (bool, error) {
		podsList, err := kubeClient.CoreV1().Pods("openshift-ingress-operator").List(context, metav1.ListOptions{})
		if err != nil {
			t.Logf("Failed to list pods: %v, retying...", err)
			return false, nil
		}

		if podsList == nil || len(podsList.Items) == 0 {
			t.Logf("No pods found, retying...")
			return false, nil
		}
		//We are checking that the new pod is different from the previous one
		_, newOperatorPodUID := extractIngressOperatorPodNameAndUID(podsList)
		if newOperatorPodUID == "" {
			t.Logf("Failed to find ingress operator pod, retrying...")
			return false, nil
		}
		if newOperatorPodUID == operatorPodUID {
			t.Logf("Failed to find new ingress operator pod, retrying...")
			return false, nil
		}
		return true, nil
	}); err != nil {
		return err
	}
	return nil
}

// extractIngressOperatorPodNameAndUID helper function to extract from a pods list the name and UID of the ingress operator pod
func extractIngressOperatorPodNameAndUID(podsList *corev1.PodList) (string, types.UID) {
	operatorPodName := ""
	operatorPodUID := types.UID("")
	for _, pod := range podsList.Items {
		if strings.HasPrefix(pod.Name, "ingress-operator") {
			operatorPodName = pod.Name
			operatorPodUID = pod.UID
			break
		}
	}
	return operatorPodName, operatorPodUID
}

// TestIngressControllerConditionsMetricAfterRestart verifies that metric ingress_controller_conditions(router,status) is
// available after an operator pod restart too.
//
// This test:
//
// 1. Verifies that the metric is available in a normal situation when the operator pod is up&running (i.e. before restart)
//
// 2. Restarts the operator pod, waiting it will be available again
//
// 3. Repeats the step 1 expecting the same result again and so the presence of the metric
//
// NB:
//  1. this test requires an OpenShift version with the monitoring stack up&running
//  2. due to the fact that this test is restarting the operator pod it cannot be executed in parallel with other tests
func TestIngressControllerConditionsMetricAfterRestart(t *testing.T) {

	// Create a new prometheus client for fetching metrics and dependencies needed
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("Failed to get kube config: %s", err)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("Failed to create kube client: %v", err)
	}
	routeClient, err := routev1client.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("Failed to create route client: %v", err)
	}
	prometheusClient, err := metrics.NewPrometheusClient(context.Background(), kubeClient, routeClient)
	if err != nil {
		t.Fatalf("Failed to create prometheus client: %v", err)
	}

	// Check metric before restart
	t.Log("Verifying that in Prometheus metrics there are ingress_controller_conditions metrics before restart")
	// Wait for metrics to be added and set to 0.
	if err := waitForIngressControllerConditionsMetrics(t, prometheusClient); err != nil {
		t.Fatalf("Failed to fetch expected metrics: %v", err)
	}

	// Restart operator pod
	if err := restartOperatorPod(t, kubeClient); err != nil {
		t.Fatalf("Failed to restart operator pod: %v", err)
	}

	// Check metric after restart
	if err := waitForIngressControllerConditionsMetrics(t, prometheusClient); err != nil {
		t.Fatalf("Failed to fetch expected metrics: %v", err)
	}
}
