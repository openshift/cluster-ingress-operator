//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"strconv"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const nbthreadRouterDeployTimeout time.Duration = 1 * time.Minute

// NOTE: This test will mutate the default ingresscontroller
func TestRouteNbthreadIngressController(t *testing.T) {
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ic, err := getIngressController(t, kclient, defaultName, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	routerDeployment, err := getDeployment(t, kclient, controller.RouterDeploymentName(ic), 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get router deployment: %v", err)
	}

	defaultThreadsStr := strconv.Itoa(ingresscontroller.RouterHAProxyThreadsDefaultValue)

	// by default, the router deployment should have ROUTER_THREADS set to 4
	if err := waitForDeploymentEnvVar(t, kclient, routerDeployment, nbthreadRouterDeployTimeout, "ROUTER_THREADS", defaultThreadsStr); err != nil {
		t.Fatalf("expected router deployment to set ROUTER_THREADS to %v: %v", ingresscontroller.RouterHAProxyThreadsDefaultValue, err)
	}

	// set spec.tuningOptions.threadCount to a nonstandard value, double the default
	if err := nbthreadSetThreadCount(t, kclient, 1*time.Minute, ingresscontroller.RouterHAProxyThreadsDefaultValue*2, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := waitForDeploymentEnvVar(t, kclient, routerDeployment, nbthreadRouterDeployTimeout, "ROUTER_THREADS", strconv.Itoa(ingresscontroller.RouterHAProxyThreadsDefaultValue*2)); err != nil {
		t.Fatalf("expected router deployment to set ROUTER_THREADS to %v: %v", ingresscontroller.RouterHAProxyThreadsDefaultValue*2, err)
	}

	// set spec.tuningOptions.threadCount to 0, as if it was unset
	if err := nbthreadSetThreadCount(t, kclient, 1*time.Minute, 0, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	// when set back to default, the router deployment should have ROUTER_THREADS set to 4
	if err := waitForDeploymentEnvVar(t, kclient, routerDeployment, nbthreadRouterDeployTimeout, "ROUTER_THREADS", defaultThreadsStr); err != nil {
		t.Fatalf("expected router deployment to set ROUTER_THREADS to %v: %v", ingresscontroller.RouterHAProxyThreadsDefaultValue, err)
	}
}

func nbthreadSetThreadCount(t *testing.T, client client.Client, timeout time.Duration, nbthreads int, c *operatorv1.IngressController) error {
	t.Helper()
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		c.Spec.TuningOptions.ThreadCount = int32(nbthreads)
		if err := client.Update(context.TODO(), c); err != nil {
			t.Logf("Update %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}
