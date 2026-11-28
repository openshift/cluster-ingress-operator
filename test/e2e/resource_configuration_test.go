//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openshift/api/features"
	operatorv1 "github.com/openshift/api/operator/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
)

// TestIngressControllerResourceConfiguration verifies that spec.resources
// requests and limits reach the router and haproxy containers, and that
// changing or removing an override rolls the router deployment out and
// restores the platform default.
func TestIngressControllerResourceConfiguration(t *testing.T) {
	enabled, err := isFeatureGateEnabled(features.FeatureGateIngressControllerResourceConfiguration)
	require.NoError(t, err)
	if !enabled {
		t.Skip("Skipping TestIngressControllerResourceConfiguration as FeatureGateIngressControllerResourceConfiguration is disabled")
	}
	t.Parallel()

	name := types.NamespacedName{Namespace: operatorNamespace, Name: "resources"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newLoadBalancerController(name, domain)

	cpuRequest := resource.MustParse("500m")
	haproxyMemory := resource.MustParse("600Mi")
	ic.Spec.Resources = operatorv1.IngressControllerResources{
		Router: []operatorv1.IngressControllerContainerResource{
			{Name: "cpu", Request: &cpuRequest},
		},
		HAProxy: []operatorv1.IngressControllerContainerResource{
			{Name: "memory", Limit: &haproxyMemory},
		},
	}

	if err := createWithRetryOnError(t, context.Background(), ic, DefaultRetryTimeout); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", name, err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForIngressControllerWithLoadBalancer...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	deployment, err := getDeployment(t, kclient, controller.RouterDeploymentName(ic), 5*time.Minute)
	require.NoError(t, err)
	require.NoError(t, waitForDeploymentComplete(t, kclient, deployment, 5*time.Minute))

	routerResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    cpuRequest,
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
	}
	haproxyResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: haproxyMemory,
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: haproxyMemory,
		},
	}
	assertRouterResources(t, deployment, routerResources, haproxyResources)

	// Change the router override and remove the haproxy one. The router pods
	// must roll out with the new request, and the haproxy container must get
	// the platform default back.
	newCPURequest := resource.MustParse("750m")
	if err := updateIngressControllerWithRetryOnConflict(t, name, DefaultRetryTimeout, func(ic *operatorv1.IngressController) {
		ic.Spec.Resources = operatorv1.IngressControllerResources{
			Router: []operatorv1.IngressControllerContainerResource{
				{Name: "cpu", Request: &newCPURequest},
			},
		}
	}); err != nil {
		t.Fatalf("failed to update ingresscontroller %s: %v", name, err)
	}
	require.NoError(t, waitForDeploymentFunc(t, deployment, 5*time.Minute, func(deployment *appsv1.Deployment) bool {
		router, ok := findContainer(deployment.Spec.Template.Spec.Containers, "router")
		if !ok {
			return false
		}
		request, ok := router.Resources.Requests[corev1.ResourceCPU]
		return ok && request.String() == newCPURequest.String()
	}))

	deployment, err = getDeployment(t, kclient, controller.RouterDeploymentName(ic), 5*time.Minute)
	require.NoError(t, err)
	require.NoError(t, waitForDeploymentComplete(t, kclient, deployment, 5*time.Minute))

	updatedRouterResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    newCPURequest,
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
	}
	updatedHAProxyResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}
	assertRouterResources(t, deployment, updatedRouterResources, updatedHAProxyResources)
}

// assertRouterResources checks the router and haproxy container resources in
// the deployment's pod template and in the current revision's router pods.
func assertRouterResources(t *testing.T, deployment *appsv1.Deployment, expectedRouter, expectedHAProxy corev1.ResourceRequirements) {
	t.Helper()

	router, ok := findContainer(deployment.Spec.Template.Spec.Containers, "router")
	require.True(t, ok, "router container not found in deployment %s/%s", deployment.Namespace, deployment.Name)
	assertResourceRequirements(t, "router container", expectedRouter, router.Resources)
	haproxy, ok := findContainer(deployment.Spec.Template.Spec.InitContainers, "haproxy")
	require.True(t, ok, "haproxy container not found in deployment %s/%s", deployment.Namespace, deployment.Name)
	assertResourceRequirements(t, "haproxy container", expectedHAProxy, haproxy.Resources)

	pods, err := getPods(t, kclient, deployment)
	require.NoError(t, err)
	require.NotEmpty(t, pods.Items, "no router pods found for deployment %s/%s", deployment.Namespace, deployment.Name)
	hash := deployment.Spec.Template.Labels[controller.ControllerDeploymentHashLabel]
	for _, pod := range pods.Items {
		if hash != "" && pod.Labels[controller.ControllerDeploymentHashLabel] != hash {
			continue
		}
		router, ok := findContainer(pod.Spec.Containers, "router")
		require.True(t, ok, "router container not found in pod %s", pod.Name)
		assertResourceRequirements(t, "router container in pod "+pod.Name, expectedRouter, router.Resources)
		haproxy, ok := findContainer(pod.Spec.InitContainers, "haproxy")
		require.True(t, ok, "haproxy container not found in pod %s", pod.Name)
		assertResourceRequirements(t, "haproxy container in pod "+pod.Name, expectedHAProxy, haproxy.Resources)
	}
}

func findContainer(containers []corev1.Container, name string) (corev1.Container, bool) {
	for _, container := range containers {
		if container.Name == name {
			return container, true
		}
	}
	return corev1.Container{}, false
}

func assertResourceRequirements(t *testing.T, what string, expected, actual corev1.ResourceRequirements) {
	t.Helper()

	require.Len(t, actual.Requests, len(expected.Requests), "%s requests", what)
	for name, expectedQuantity := range expected.Requests {
		actualQuantity, ok := actual.Requests[name]
		require.Truef(t, ok, "%s is missing the %s request", what, name)
		assert.Equalf(t, expectedQuantity.String(), actualQuantity.String(), "%s %s request", what, name)
	}
	require.Len(t, actual.Limits, len(expected.Limits), "%s limits", what)
	for name, expectedQuantity := range expected.Limits {
		actualQuantity, ok := actual.Limits[name]
		require.Truef(t, ok, "%s is missing the %s limit", what, name)
		assert.Equalf(t, expectedQuantity.String(), actualQuantity.String(), "%s %s limit", what, name)
	}
}
