//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/pointer"

	"github.com/openshift/library-go/test/library/metrics"
	prometheusv1client "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	routev1 "github.com/openshift/api/route/v1"
	routev1client "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
)

func waitForAllRoutesAdmitted(namespace string, timeout time.Duration, progress func(admittedRoutes, totalRoutes int, pendingRoutes []string)) (*routev1.RouteList, error) {
	isRouteAdmitted := func(route *routev1.Route) bool {
		for i := range route.Status.Ingress {
			if route.Status.Ingress[i].RouterCanonicalHostname != "" {
				return true
			}
		}
		return false
	}

	var routeList routev1.RouteList
	err := wait.PollImmediate(time.Second, timeout, func() (bool, error) {
		if err := kclient.List(context.TODO(), &routeList, client.InNamespace(namespace)); err != nil {
			return false, fmt.Errorf("failed to list routes in namespace %s: %v", namespace, err)
		}

		admittedRoutes := 0
		var pendingRoutes []string
		for i := range routeList.Items {
			if isRouteAdmitted(&routeList.Items[i]) {
				admittedRoutes++
			} else {
				pendingRoutes = append(pendingRoutes, fmt.Sprintf("%s/%s", routeList.Items[i].Namespace, routeList.Items[i].Name))
			}
		}

		totalRoutes := len(routeList.Items)
		if progress != nil {
			progress(admittedRoutes, totalRoutes, pendingRoutes)
		}

		if admittedRoutes == totalRoutes {
			return true, nil
		}

		return false, nil
	})

	if err != nil {
		return nil, fmt.Errorf("not all routes were admitted in namespace %s: %v", namespace, err)
	}

	return &routeList, nil
}

func waitForPodsReadyAndLive(t *testing.T, cl client.Client, namespace string, labelSelector map[string]string, timeout time.Duration) error {
	t.Helper()
	err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		pods := &corev1.PodList{}
		if err := cl.List(context.TODO(), pods, client.InNamespace(namespace), client.MatchingLabels(labelSelector)); err != nil {
			t.Logf("error listing pods in namespace %s with selector %v: %v", namespace, labelSelector, err)
			return false, nil
		}

		for _, pod := range pods.Items {
			isReady := false
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					isReady = true
					break
				}
			}
			if !isReady {
				t.Logf("pod %s/%s is not ready", pod.Namespace, pod.Name)
				return false, nil
			}

			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.State.Terminated != nil {
					t.Logf("pod %s/%s has a terminated container", pod.Namespace, pod.Name)
					return false, nil
				}
				if containerStatus.State.Waiting != nil && containerStatus.RestartCount > 0 {
					t.Logf("pod %s/%s is restarting (liveness probe failure)", pod.Namespace, pod.Name)
					return false, nil
				}
			}
		}

		// All pods are ready and live.
		return true, nil
	})

	if err != nil {
		return fmt.Errorf("failed to observe readiness and liveness for pods in namespace %s with selector %v", namespace, labelSelector)
	}

	return nil
}

func singleTransferEncodingResponseCheck(resp *http.Response, err error) error {
	if err != nil {
		return fmt.Errorf("unexpected error: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: got %d, expected %d", resp.StatusCode, http.StatusOK)
	}

	_, err = io.Copy(io.Discard, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %v", err)
	}

	return nil
}

func checkDuplicateTransferEncodingError(resp *http.Response, err error) error {
	expectedErrorMsg := `net/http: HTTP/1.x transport connection broken: too many transfer encodings: ["chunked" "chunked"]`

	if resp != nil {
		resp.Body.Close()
		return fmt.Errorf("expected response to be nil; got status: %s, code: %d", resp.Status, resp.StatusCode)
	}

	if err == nil {
		return fmt.Errorf("expected an error, got nil")
	}

	if !strings.Contains(err.Error(), expectedErrorMsg) {
		return fmt.Errorf("unmatched error: %v; expected message to contain %q", err, expectedErrorMsg)
	}

	return nil
}

func createOCPBUGS48050Service(namespace, name string) (*corev1.Service, error) {
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				ingress.ServingCertSecretAnnotation:                  "serving-cert-" + namespace,
				"service.beta.openshift.io/serving-cert-secret-name": "serving-cert-" + namespace,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 8080, TargetPort: intstr.FromInt(8080)},
				{Name: "https", Port: 8443, TargetPort: intstr.FromInt(8443)},
			},
		},
	}

	if err := kclient.Create(context.TODO(), &service); err != nil {
		return nil, err
	}

	return &service, nil
}

func createOCPBUGS48050Deployment(namespace, name, image string) (*appsv1.Deployment, error) {
	deployment := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name, "test": "ocpbugs48050"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name, "test": "ocpbugs48050"}, // Add "test" label here
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            name,
							Image:           image,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/usr/bin/ingress-operator"},
							Args:            []string{"serve-ocpbugs40850-test-server"},
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: 8080},
								{Name: "https", ContainerPort: 8443},
							},
							Env: []corev1.EnvVar{
								{Name: "HTTP_PORT", Value: "8080"},
								{Name: "HTTPS_PORT", Value: "8443"},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 15,
								PeriodSeconds:       10,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 15,
								PeriodSeconds:       10,
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "cert",
									MountPath: "/etc/serving-cert",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "cert",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "serving-cert-" + namespace,
								},
							},
						},
					},
				},
			},
		},
	}

	if err := kclient.Create(context.TODO(), &deployment); err != nil {
		return nil, err
	}

	return &deployment, nil
}

func createOCPBUGS48050Route(namespace, routeName, serviceName, targetPort string, terminationType routev1.TLSTerminationType) (*routev1.Route, error) {
	route := routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      routeName,
			Namespace: namespace,
		},
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: serviceName,
			},
			Port: &routev1.RoutePort{
				TargetPort: intstr.FromString(targetPort),
			},
			TLS: &routev1.TLSConfig{
				Termination:                   terminationType,
				InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
			},
			WildcardPolicy: routev1.WildcardPolicyNone,
		},
	}

	if err := kclient.Create(context.TODO(), &route); err != nil {
		return nil, fmt.Errorf("failed to create route %s/%s: %v", route.Namespace, route.Name, err)
	}

	return &route, nil
}

func setupOCPBUGS48050(t *testing.T, baseName string, routeCount int) (*corev1.Namespace, error) {
	targetPortNames := map[routev1.TLSTerminationType]string{
		routev1.TLSTerminationEdge:        "http",
		routev1.TLSTerminationReencrypt:   "https",
		routev1.TLSTerminationPassthrough: "https",
	}

	ns := createNamespace(t, baseName+"-"+rand.String(5))

	service, err := createOCPBUGS48050Service(ns.Name, baseName)
	if err != nil {
		return nil, fmt.Errorf("failed to create service %s/%s: %v", ns.Name, baseName, err)
	}

	image, err := getCanaryImageFromIngressOperatorDeployment()
	if err != nil {
		return nil, fmt.Errorf("failed to get canary image: %v", err)
	}

	deployment, err := createOCPBUGS48050Deployment(ns.Name, baseName, image)
	if err != nil {
		return nil, fmt.Errorf("failed to create deployment %s/%s: %v", ns.Name, baseName, err)
	}

	if err := waitForDeploymentComplete(t, kclient, deployment, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("deployment %s/%s not ready: %v", deployment.Namespace, deployment.Name, err)
	}

	makeRouteName := func(terminationType routev1.TLSTerminationType, i int) string {
		return fmt.Sprintf("%s-route-%02d", string(terminationType), i)
	}

	for i := 1; i <= routeCount; i++ {
		for terminationType, targetPort := range targetPortNames {
			routeName := makeRouteName(terminationType, i)
			route, err := createOCPBUGS48050Route(ns.Name, routeName, service.Name, targetPort, terminationType)
			if err != nil {
				t.Fatalf("failed to create route %s/%s: %v", ns.Name, routeName, err)
			}
			t.Logf("Created route %s/%s", route.Namespace, route.Name)
		}
	}

	if err := waitForPodsReadyAndLive(t, kclient, ns.Name, deployment.Spec.Template.Labels, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("pods failed readiness or liveness checks: %v", err)
	}

	return ns, nil
}

func getCanaryImageFromIngressOperatorDeployment() (string, error) {
	ingressOperator := types.NamespacedName{Namespace: operatorNamespace, Name: "ingress-operator"}

	deployment := appsv1.Deployment{}
	if err := kclient.Get(context.TODO(), ingressOperator, &deployment); err != nil {
		return "", fmt.Errorf("failed to get deployment %s/%s: %v", ingressOperator.Namespace, ingressOperator.Name, err)
	}

	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "CANARY_IMAGE" {
				return env.Value, nil
			}
		}
	}

	return "", fmt.Errorf("CANARY_IMAGE environment variable not found in deployment %s/%s", ingressOperator.Namespace, ingressOperator.Name)
}

func newPrometheusClient() (prometheusv1client.API, error) {
	kubeConfig, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes configuration: %v", err)
	}

	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	routeClient, err := routev1client.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create route client: %v", err)
	}

	prometheusClient, err := metrics.NewPrometheusClient(context.TODO(), kubeClient, routeClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create Prometheus client: %v", err)
	}

	return prometheusClient, nil
}

func executePrometheusQuery(client prometheusv1client.API, query string, timeout time.Duration) (model.Value, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, warnings, err := client.Query(ctx, query, time.Now())
	if err != nil {
		return nil, fmt.Errorf("error querying Prometheus: %v", err)
	}

	if len(warnings) > 0 {
		return nil, fmt.Errorf("warnings querying Prometheus: %v", warnings)
	}

	return result, nil
}

func queryRouteToDuplicateTETotals(promClient prometheusv1client.API, query string, timeout time.Duration) (map[string]float64, error) {
	result, err := executePrometheusQuery(promClient, query, timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to query Prometheus for duplicate Transfer-Encoding headers: %w", err)
	}

	vector, ok := result.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("expected result type model.Vector, got %T", result)
	}

	routeCounts := make(map[string]float64)
	for _, sample := range vector {
		routeName := string(sample.Metric["route"])
		value := float64(sample.Value)
		routeCounts[routeName] = value
	}

	return routeCounts, nil
}

// TestOCPBUGS48050 validates the new metric [1],
// duplicate_te_header_total, which detects duplicate
// Transfer-Encoding headers per backend response. This test addresses
// issues encountered when upgrading from HAProxy 2.2 in OpenShift
// 4.13 to HAProxy 2.6 in OpenShift 4.14, where responses are rejected
// due to RFC 7230 compliance.
//
// RFC 7230 defines the standards for HTTP/1.1, and specifically, it
// addresses issues related to the use of multiple Transfer-Encoding
// headers. According to Section 3.3.3 of RFC 7230:
//
// A sender MUST NOT send more than one Transfer-Encoding header field
// in the same message.
//
// [1] https://github.com/openshift/router/pull/626.
func TestOCPBUGS48050(t *testing.T) {
	baseName := "ocpbugs40850-e2e"

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ns, err := setupOCPBUGS48050(t, baseName, 10)
	if err != nil {
		t.Fatalf("failed to setup test resources: %v", err)
	}

	routeList, err := waitForAllRoutesAdmitted(ns.Name, 2*time.Minute, func(admittedRoutes, totalRoutes int, pendingRoutes []string) {
		if len(pendingRoutes) > 0 {
			t.Logf("%d/%d routes admitted. Waiting for: %s", admittedRoutes, totalRoutes, strings.Join(pendingRoutes, ", "))
		} else {
			t.Logf("All %d routes in namespace %s have been admitted", totalRoutes, ns.Name)
		}
	})
	if err != nil {
		t.Fatalf("Error waiting for routes to be admitted: %v", err)
	}

	if len(routeList.Items) == 0 {
		t.Fatalf("Expected len(routeList.Items) > 0")
	}

	httpClient := http.Client{
		Timeout: time.Minute,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	httpGet := func(url string, check func(*http.Response, error) error) error {
		t.Logf("GET %s", url)
		resp, err := httpClient.Get(url)
		if resp != nil {
			defer resp.Body.Close()
		}
		return check(resp, err)
	}

	routeIndex := func(name string) int {
		parts := strings.Split(name, "-")
		if len(parts) < 2 {
			t.Fatalf("Unexpected route name format: %q. Expected at least two parts separated by '-'", name)
		}

		index, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil {
			t.Fatalf("Failed to convert route index to integer for route %q: %v", name, err)
		}

		return index
	}

	var duplicateTETotalCount = map[string]int{}

	for _, route := range routeList.Items {
		host := route.Spec.Host

		// Retry only the /healthz endpoint because sometimes
		// we get a 503 status even though the routes have
		// been admitted.
		if err := wait.PollImmediate(time.Second, time.Minute, func() (bool, error) {
			healthzURL := fmt.Sprintf("http://%s/healthz", host)
			if err := httpGet(healthzURL, func(resp *http.Response, err error) error {
				if err != nil {
					t.Logf("Health check error for route %s: %v. Retrying...", host, err)
					return err
				}
				if resp.StatusCode != http.StatusOK {
					t.Logf("Unexpected status code for %s: got %d, expected: %d. Retrying...", healthzURL, resp.StatusCode, http.StatusOK)
					return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
				}
				return nil
			}); err != nil {
				return false, nil
			}
			return true, nil
		}); err != nil {
			t.Fatalf("/healthz check failed for route %s: %v", host, err)
		}

		duplicateTETotalCount[route.Name] = 0

		// We don't retry hits to the /single-te or
		// /duplicate-te endpoints because we can't determine
		// which side of the proxy the GET request fails on.
		// If the HAProxy <-> server communication succeeds, a
		// hit will be recorded to the metric, which makes
		// counting hits unreliable if we retry on client <->
		// HAProxy failures.
		for _, tc := range []struct {
			path                  string
			responseChecker       func(*http.Response, error) error
			metricShouldIncrement bool
		}{
			{"/duplicate-te", checkDuplicateTransferEncodingError, true},
			{"/single-te", singleTransferEncodingResponseCheck, false},
		} {
			url := fmt.Sprintf("http://%s%s", host, tc.path)

			for j := 1; j <= routeIndex(route.Name); j++ {
				if err := httpGet(url, tc.responseChecker); err != nil {
					t.Fatalf("GET request to %s failed: %v", url, err)
				}

				if route.Spec.TLS.Termination != routev1.TLSTerminationPassthrough && tc.metricShouldIncrement {
					duplicateTETotalCount[route.Name]++
				}
			}
		}
	}

	promClient, err := newPrometheusClient()
	if err != nil {
		t.Fatalf("failed to create Prometheus client: %v", err)
	}

	pollingInterval := 5 * time.Second
	query := fmt.Sprintf(`sum by (route) (haproxy_backend_duplicate_te_header_total{exported_namespace="%s"})`, ns.Name)

	if err := wait.PollImmediate(pollingInterval, 10*time.Minute, func() (bool, error) {
		t.Logf("Querying Prometheus with: %s", query)
		routeToDuplicateTETotals, err := queryRouteToDuplicateTETotals(promClient, query, time.Minute)
		if err != nil {
			t.Logf("Prometheus query failed: %v. Retrying...", err)
			return false, nil
		}

		if len(routeToDuplicateTETotals) == 0 {
			t.Logf("Prometheus query has zero results. Retrying...")
			return false, nil
		}

		t.Logf("%-30s %-15s %-15s %-15s %-15s", "Route", "Termination", "Actual Count", "Expected Count", "Match?")

		mismatches := 0

		for _, route := range routeList.Items {
			expectedCount := float64(duplicateTETotalCount[route.Name])
			actualCount, exists := routeToDuplicateTETotals[route.Name]

			if exists {
				success := actualCount == expectedCount
				t.Logf("%-30s %-15s %-15.0f %-15.0f %-15v", route.Name, route.Spec.TLS.Termination, actualCount, expectedCount, success)
				if !success {
					mismatches++
				}
			} else {
				t.Logf("%-30s %-15s %-15s %-15.0f %-15v", route.Name, route.Spec.TLS.Termination, "N/A", expectedCount, false)
				mismatches++
			}
		}

		if mismatches > 0 {
			t.Logf("Scraping again in %s", pollingInterval.String())
			return false, nil
		}

		return true, nil // success
	}); err != nil {
		t.Fatalf("Failed to validate metrics: %v", err)
	}
}
