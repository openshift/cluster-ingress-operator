//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1client "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	"github.com/openshift/library-go/test/library/metrics"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// TestMaxConnectionsMetric verifies that the haproxy_max_connections
// Prometheus metric correctly reflects the configured maxConnections
// tuning option on an IngressController.
//
// This test:
//
//  1. Creates a private IngressController with default settings and
//     verifies the haproxy_max_connections metric reports 50000.
//
//  2. Updates maxConnections to a custom value and verifies the metric
//     reflects the new value after the router deployment rolls out.
func TestMaxConnectionsMetric(t *testing.T) {
	t.Parallel()

	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %v", err)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}
	routeClient, err := routev1client.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create route client: %v", err)
	}
	prometheusClient, err := metrics.NewPrometheusClient(context.TODO(), kubeClient, routeClient)
	if err != nil {
		t.Fatalf("failed to create prometheus client: %v", err)
	}

	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "maxconn-metric"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", icName, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)
	t.Logf("waiting for ingresscontroller %v", icName)
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ic, err = getIngressController(t, kclient, icName, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	deployment := &appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), controller.RouterDeploymentName(ic), deployment); err != nil {
		t.Fatalf("failed to get ingresscontroller deployment: %v", err)
	}

	// Verify the default haproxy_max_connections metric value (50000)
	// before any tuning is applied.
	if err := waitForMaxConnectionsMetric(t, prometheusClient, icName.Name, 50000); err != nil {
		t.Fatalf("expected default haproxy_max_connections metric to be 50000: %v", err)
	}

	// Update maxConnections to a custom value.
	const customMaxConn int32 = 10000
	if err := updateIngressControllerWithRetryOnConflict(t, icName, 1*time.Minute, func(ic *operatorv1.IngressController) {
		ic.Spec.TuningOptions.MaxConnections = customMaxConn
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller maxConnections to %d: %v", customMaxConn, err)
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, icName, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
	if err := waitForDeploymentEnvVar(t, kclient, deployment, 1*time.Minute, ingress.RouterMaxConnectionsEnvName, fmt.Sprintf("%d", customMaxConn)); err != nil {
		t.Fatalf("router deployment not updated with %s=%d: %v", ingress.RouterMaxConnectionsEnvName, customMaxConn, err)
	}
	if err := waitForDeploymentComplete(t, kclient, deployment, 3*time.Minute); err != nil {
		t.Fatalf("failed waiting for deployment rollout to complete: %v", err)
	}

	// Verify the haproxy_max_connections metric reflects the custom value.
	if err := waitForMaxConnectionsMetric(t, prometheusClient, icName.Name, int(customMaxConn)); err != nil {
		t.Fatalf("expected haproxy_max_connections metric to be %d after tuning: %v", customMaxConn, err)
	}
}

// waitForMaxConnectionsMetric waits for the haproxy_max_connections metric
// to reach the expected value for the given IngressController's router service.
func waitForMaxConnectionsMetric(t *testing.T, prometheusClient prometheusv1.API, icName string, expectedValue int) error {
	t.Helper()
	serviceName := "router-internal-" + icName
	query := fmt.Sprintf(`haproxy_max_connections{namespace="openshift-ingress",service="%s"}`, serviceName)
	t.Logf("waiting for haproxy_max_connections{service=%q} to become %d", serviceName, expectedValue)
	return wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 5*time.Minute, false, func(ctx context.Context) (bool, error) {
		result, _, err := prometheusClient.Query(ctx, query, time.Now())
		if err != nil {
			t.Logf("failed to query haproxy_max_connections: %v, retrying...", err)
			return false, nil
		}
		vec, ok := result.(model.Vector)
		if !ok {
			t.Logf("unexpected metric result type %T, retrying...", result)
			return false, nil
		}
		if len(vec) == 0 {
			t.Logf("haproxy_max_connections metric not found yet, retrying...")
			return false, nil
		}
		for _, sample := range vec {
			if !sample.Value.Equal(model.SampleValue(float64(expectedValue))) {
				t.Logf("haproxy_max_connections for pod %q is %v, expected %d, retrying...", sample.Metric["pod"], sample.Value, expectedValue)
				return false, nil
			}
		}
		return true, nil
	})
}
