//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	routev1client "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/library-go/test/library/metrics"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// TestRouteMetricsControllerOnlyRouteSelector creates an Ingress Controller with only RouteSelector and creates
// a Route with a label matching the RouteSelector. The Ingress Controller is modified to change the RouteSelector
// and then again the RouteSelector is changed back to the original value. The Route is then deleted and after that
// the Ingress Controller is deleted. For all the events (Create, Update, Delete), the metrics is verified with the
// expected metrics value.
func TestRouteMetricsControllerOnlyRouteSelector(t *testing.T) {
	t.Parallel()

	// Create a new prometheus client for fetching metrics.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %s", err)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatal(err)
	}
	routeClient, err := routev1client.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatal(err)
	}
	prometheusClient, err := metrics.NewPrometheusClient(context.TODO(), kubeClient, routeClient)
	if err != nil {
		t.Fatal(err)
	}

	// Create an Ingress Controller that can admit our Route.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "ic-rs-metrics-test"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.RouteSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "rs-foo",
		},
	}

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}

	// Delete the IC if any error occurs.
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Store the start time to get the time taken to update metrics after the creation of the IC.
	startTime := time.Now()

	// Wait for metrics to be added and set to 0.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Get the time taken for the metrics to be updated.
	t.Logf("time taken for the metrics to be updated after the creation of the IC: %fs", time.Since(startTime).Seconds())

	// Create a new namespace for the Route that we can immediately match with the IC's namespace selector.
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: names.SimpleNameGenerator.GenerateName("test-e2e-metrics-"),
		},
	}
	if err := kclient.Create(context.TODO(), ns); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}

	// Delete the Namespace if any error occurs.
	defer func() {
		if err := kclient.Delete(context.TODO(), ns); err != nil {
			t.Fatalf("failed to delete test namespace %v: %v", ns.Name, err)
		}
	}()

	// Create a Route to be immediately admitted by this Ingress Controller.
	// Use the new namespace to get a namespace outside the ingress-operator's cache.
	routeFooLabelName := types.NamespacedName{Namespace: ns.Name, Name: "route-rs-foo-label"}
	routeFooLabel := newRouteWithLabel(routeFooLabelName, "rs-foo")
	if err := kclient.Create(context.TODO(), routeFooLabel); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}

	// Delete the Route if any error occurs.
	defer func() {
		if err := kclient.Delete(context.TODO(), routeFooLabel); err != nil {
			if apierrors.IsNotFound(err) {
				return
			} else {
				t.Fatalf("failed to delete route %s: %v", routeFooLabelName, err)
			}
		}
		t.Logf("deleted route: %s", routeFooLabelName)
	}()

	// Wait for metrics to be updated to 1 as the Route will get admitted by the IC.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 1); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the RouteSelector of the Ingress Controller so that the Route gets un-admitted.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.RouteSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "rs-bar",
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for metrics to be updated to zero as the Route will get un-admitted by the IC.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the RouteSelector of the Ingress Controller so that the Route gets admitted again.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.RouteSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "rs-foo",
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for metrics to be updated to 1 as the Route will get admitted by the IC.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 1); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the label of the route so that it gets un-admitted from the Ingress Controller.
	if err := kclient.Get(context.TODO(), routeFooLabelName, routeFooLabel); err != nil {
		t.Fatalf("failed to get route resource: %v", err)
	}
	routeFooLabel.Labels = map[string]string{
		"type": "rs-bar",
	}
	if err := kclient.Update(context.TODO(), routeFooLabel); err != nil {
		t.Fatalf("failed to update route: %v", err)
	}

	// Wait for metrics to be updated to 0 as the Route will get un-admitted by the IC.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the label of the route so that it gets admitted to the Ingress Controller again.
	if err := kclient.Get(context.TODO(), routeFooLabelName, routeFooLabel); err != nil {
		t.Fatalf("failed to get route resource: %v", err)
	}
	routeFooLabel.Labels = map[string]string{
		"type": "rs-foo",
	}
	if err := kclient.Update(context.TODO(), routeFooLabel); err != nil {
		t.Fatalf("failed to update route: %v", err)
	}

	// Wait for metrics to be updated to 1 as the Route will get admitted by the IC again.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 1); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Delete the Route routeFooLabel.
	func() {
		if err := kclient.Delete(context.TODO(), routeFooLabel); err != nil {
			if apierrors.IsNotFound(err) {
				return
			} else {
				t.Fatalf("failed to delete route %s: %v", routeFooLabelName, err)
			}
		}
		t.Logf("deleted route: %s", routeFooLabelName)
	}()

	// Wait for metrics to be updated to zero as the admitted Route is deleted.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Delete the IC.
	assertIngressControllerDeleted(t, kclient, ic)

	// Wait for metrics corresponding to the IC to be deleted.
	if err := waitForRouteMetricsDelete(t, prometheusClient, ic.Name); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}
}

// TestRouteMetricsControllerOnlyNamespaceSelector creates an Ingress Controller with only NamespaceSelector and creates
// a Route in a Namespace with a label matching the NamespaceSelector. The Ingress Controller is modified to change the
// NamespaceSelector and then again the NamespaceSelector is changed back to the original value. The Route is then
// deleted and after that the Ingress Controller is deleted. For all the events (Create, Update, Delete), the metrics is
// verified with the expected metrics value.
func TestRouteMetricsControllerOnlyNamespaceSelector(t *testing.T) {
	t.Parallel()

	// Create a new prometheus client for fetching metrics.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %s", err)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatal(err)
	}
	routeClient, err := routev1client.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatal(err)
	}
	prometheusClient, err := metrics.NewPrometheusClient(context.TODO(), kubeClient, routeClient)
	if err != nil {
		t.Fatal(err)
	}

	// Create an Ingress Controller that can admit our Route.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "ic-ns-metrics-test"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "ns-foo",
		},
	}

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}

	// Delete the IC if any error occurs.
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Store the start time to get the time taken to update metrics after the creation of the IC.
	startTime := time.Now()

	// Wait for metrics to be added and set to 0.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Get the time taken for the metrics to be updated.
	t.Logf("time taken for the metrics to be updated after the creation of the IC: %fs", time.Since(startTime).Seconds())

	// Create a new namespace for the Route that we can immediately match with the IC's namespace selector.
	nsFoo := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: names.SimpleNameGenerator.GenerateName("test-e2e-metrics-"),
			Labels: map[string]string{
				"type": "ns-foo",
			},
		},
	}
	if err := kclient.Create(context.TODO(), nsFoo); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	defer func() {
		if err := kclient.Delete(context.TODO(), nsFoo); err != nil {
			t.Fatalf("failed to delete test namespace %v: %v", nsFoo.Name, err)
		}
	}()

	// Create a Route to be immediately admitted by this Ingress Controller.
	routeFooLabelName := types.NamespacedName{Namespace: nsFoo.Name, Name: "route-ns-foo-label"}
	routeFooLabel := newRouteWithLabel(routeFooLabelName, "")
	if err := kclient.Create(context.TODO(), routeFooLabel); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}

	// Delete the Route if any error occurs.
	defer func() {
		if err := kclient.Delete(context.TODO(), routeFooLabel); err != nil {
			if apierrors.IsNotFound(err) {
				return
			} else {
				t.Fatalf("failed to delete route %s: %v", routeFooLabelName, err)
			}
		}
		t.Logf("deleted route: %s", routeFooLabelName)
	}()

	// Wait for metrics to be updated to 1 as the Route will get admitted by the IC.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 1); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the NamespaceSelector of the Ingress Controller so that the Route gets un-admitted.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "ns-bar",
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for metrics to be updated to zero as the Route will get un-admitted by the IC.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the NamespaceSelector of the Ingress Controller so that the Route gets admitted again.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "ns-foo",
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for metrics to be updated to 1 as the Route will get admitted by the IC.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 1); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Delete the Route routeFooLabel.
	func() {
		if err := kclient.Delete(context.TODO(), routeFooLabel); err != nil {
			if apierrors.IsNotFound(err) {
				return
			} else {
				t.Fatalf("failed to delete route %s: %v", routeFooLabelName, err)
			}
		}
		t.Logf("deleted route: %s", routeFooLabelName)
	}()

	// Wait for metrics to be updated to zero as the admitted Route is deleted.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Delete the IC.
	assertIngressControllerDeleted(t, kclient, ic)

	// Wait for metrics corresponding to the IC to be deleted.
	if err := waitForRouteMetricsDelete(t, prometheusClient, ic.Name); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}
}

// TestRouteMetricsControllerRouteAndNamespaceSelector creates an Ingress Controller with both RouteSelector and
// NamespaceSelector and creates a Route with a label matching the RouteSelector and in a Namespace with a label
// matching the NamespaceSelector. The Ingress Controller is modified to change the RouteSelector and then again
// the RouteSelector is changed back to the original value. The Ingress Controller is then modified to change the
// NamespaceSelector and then again the NamespaceSelector is changed back to the original value. The Route is then
// deleted and after that the Ingress Controller is deleted. For all the events (Create, Update, Delete), the
// metrics is verified with the expected metrics value.
func TestRouteMetricsControllerRouteAndNamespaceSelector(t *testing.T) {
	t.Parallel()

	// Create a new prometheus client for fetching metrics.
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %s", err)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatal(err)
	}
	routeClient, err := routev1client.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatal(err)
	}
	prometheusClient, err := metrics.NewPrometheusClient(context.TODO(), kubeClient, routeClient)
	if err != nil {
		t.Fatal(err)
	}

	// Create an Ingress Controller that can admit our Route.
	icName := types.NamespacedName{Namespace: operatorNamespace, Name: "ic-rs-ns-metrics-test"}
	domain := icName.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(icName, domain)
	ic.Spec.RouteSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "rs-ns-foo",
		},
	}
	ic.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "rs-ns-foo",
		},
	}

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller: %v", err)
	}

	// Delete the IC if any error occurs.
	defer assertIngressControllerDeleted(t, kclient, ic)

	// Store the start time to get the time taken to update metrics after the creation of the IC.
	startTime := time.Now()

	// Wait for metrics to be added and set to 0.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Get the time taken for the metrics to be updated.
	t.Logf("time taken for the metrics to be updated after the creation of the IC: %fs", time.Since(startTime).Seconds())

	// Create a new namespace for the Route that we can immediately match with the IC's namespace selector.
	nsFoo := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: names.SimpleNameGenerator.GenerateName("test-e2e-metrics-"),
			Labels: map[string]string{
				"type": "rs-ns-foo",
			},
		},
	}
	if err := kclient.Create(context.TODO(), nsFoo); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}

	// Delete the Namespace if any error occurs.
	defer func() {
		if err := kclient.Delete(context.TODO(), nsFoo); err != nil {
			t.Fatalf("failed to delete test namespace %v: %v", nsFoo.Name, err)
		}
	}()

	// Create a Route to be immediately admitted by this Ingress Controller.
	routeFooLabelName := types.NamespacedName{Namespace: nsFoo.Name, Name: "route-rs-ns-foo-label"}
	routeFooLabel := newRouteWithLabel(routeFooLabelName, "rs-ns-foo")
	if err := kclient.Create(context.TODO(), routeFooLabel); err != nil {
		t.Fatalf("failed to create route: %v", err)
	}

	// Delete the Route if any error occurs.
	defer func() {
		if err := kclient.Delete(context.TODO(), routeFooLabel); err != nil {
			if apierrors.IsNotFound(err) {
				return
			} else {
				t.Fatalf("failed to delete route %s: %v", routeFooLabelName, err)
			}
		}
		t.Logf("deleted route: %s", routeFooLabelName)
	}()

	// Wait for metrics to be updated to 1 as the Route will get admitted by the IC.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 1); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the NamespaceSelector of the Ingress Controller so that the Route gets un-admitted.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "rs-ns-bar",
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for metrics to be updated to zero as the Route will get un-admitted by the IC.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the NamespaceSelector of the Ingress Controller so that the Route gets admitted again.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "rs-ns-foo",
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for metrics to be updated to 1 as the Route will get admitted by the IC again.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 1); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the RouteSelector of the Ingress Controller so that the Route gets un-admitted again.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.RouteSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "rs-ns-bar",
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for metrics to be updated to zero as the Route will get un-admitted by the IC again.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the RouteSelector of the Ingress Controller so that the Route gets admitted again.
	if err := kclient.Get(context.TODO(), icName, ic); err != nil {
		t.Fatalf("failed to get ingress resource: %v", err)
	}
	ic.Spec.RouteSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"type": "rs-ns-foo",
		},
	}
	if err := kclient.Update(context.TODO(), ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// Wait for metrics to be updated to 1 as the Route will get admitted by the IC again.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 1); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the label of the route so that it gets un-admitted from the Ingress Controller.
	if err := kclient.Get(context.TODO(), routeFooLabelName, routeFooLabel); err != nil {
		t.Fatalf("failed to get route resource: %v", err)
	}
	routeFooLabel.Labels = map[string]string{
		"type": "rs-ns-bar",
	}
	if err := kclient.Update(context.TODO(), routeFooLabel); err != nil {
		t.Fatalf("failed to update route: %v", err)
	}

	// Wait for metrics to be updated to 0 as the Route will get un-admitted by the IC.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Update the label of the route so that it gets admitted to the Ingress Controller again.
	if err := kclient.Get(context.TODO(), routeFooLabelName, routeFooLabel); err != nil {
		t.Fatalf("failed to get route resource: %v", err)
	}
	routeFooLabel.Labels = map[string]string{
		"type": "rs-ns-foo",
	}
	if err := kclient.Update(context.TODO(), routeFooLabel); err != nil {
		t.Fatalf("failed to update route: %v", err)
	}

	// Wait for metrics to be updated to 1 as the Route will get admitted by the IC again.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 1); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Delete the Route routeFooLabel.
	func() {
		if err := kclient.Delete(context.TODO(), routeFooLabel); err != nil {
			if apierrors.IsNotFound(err) {
				return
			} else {
				t.Fatalf("failed to delete route %s: %v", routeFooLabelName, err)
			}
		}
		t.Logf("deleted route: %s", routeFooLabelName)
	}()

	// Wait for metrics to be updated to zero as the admitted Route is deleted.
	if err := waitForRouteMetricsAddorUpdate(t, prometheusClient, ic.Name, 0); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}

	// Delete the IC.
	assertIngressControllerDeleted(t, kclient, ic)

	// Wait for metrics corresponding to the IC to be deleted.
	if err := waitForRouteMetricsDelete(t, prometheusClient, ic.Name); err != nil {
		t.Fatalf("failed to fetch expected metrics: %v", err)
	}
}

// waitForRouteMetricsAddorUpdate waits for the metrics for the corresponding shard to be added or updated to the expected value.
func waitForRouteMetricsAddorUpdate(t *testing.T, prometheusClient prometheusv1.API, shardName string, value int) error {
	return wait.PollImmediate(1*time.Second, 2*time.Minute, func() (bool, error) {
		result, _, err := prometheusClient.Query(context.TODO(), fmt.Sprintf(`route_metrics_controller_routes_per_shard{name="%s"}`, shardName), time.Now())
		if err != nil {
			t.Logf("failed to fetch metrics: %v", err)
			return false, nil
		}

		// Check if fetched metrics is of Vector type.
		vec, ok := result.(model.Vector)
		if !ok {
			return ok, nil
		}

		// Check if length of returned metric Vector is zero.
		if len(vec) == 0 {
			return false, nil
		}

		// Check if metrics is updated.
		if !vec[0].Value.Equal(model.SampleValue(float64(value))) {
			return false, nil
		}

		t.Logf("metrics matched expected value: %d", value)

		return true, nil
	})
}

// waitForRouteMetricsDelete waits for the metrics for the corresponding shard to be deleted.
func waitForRouteMetricsDelete(t *testing.T, prometheusClient prometheusv1.API, shardName string) error {
	return wait.PollImmediate(1*time.Second, 2*time.Minute, func() (bool, error) {
		result, _, err := prometheusClient.Query(context.TODO(), fmt.Sprintf(`route_metrics_controller_routes_per_shard{name="%s"}`, shardName), time.Now())
		if err != nil {
			t.Logf("failed to fetch metrics: %v", err)
			return false, nil
		}

		// Check if fetched metrics is of Vector type.
		vec, ok := result.(model.Vector)
		if !ok {
			return ok, nil
		}

		// Check if metrics is deleted.
		if len(vec) != 0 {
			return false, nil
		}

		t.Log("metrics deleted")

		return true, nil
	})
}
