//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultHealthCheckInterval = 5 * time.Second
	deploymentTimeout          = 1 * time.Minute
)

func TestHealthCheckIntervalIngressController(t *testing.T) {
	name := types.NamespacedName{Namespace: operatorNamespace, Name: "healthcheckinterval"}
	domain := name.Name + "." + dnsConfig.Spec.BaseDomain
	ic := newPrivateController(name, domain)

	if err := kclient.Create(context.TODO(), ic); err != nil {
		t.Fatalf("failed to create ingresscontroller %s: %v", name, err)
	}
	defer assertIngressControllerDeleted(t, kclient, ic)

	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := kclient.Get(context.TODO(), name, ic); err != nil {
		t.Fatalf("failed to get ingresscontroller %s: %v", name, err)
	}

	routerDeployment, err := getDeployment(t, kclient, controller.RouterDeploymentName(ic), 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get router deployment: %v", err)
	}

	// set spec.tuningOptions.healthCheckInterval to a nondefault value, double the default
	if err := setHealthCheckInterval(t, kclient, 10*time.Second, &metav1.Duration{2 * defaultHealthCheckInterval}, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	if err := waitForIngressControllerCondition(t, kclient, 5*deploymentTimeout, name, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions on %s: %v", name, err)
	}

	// verify the update to double the default
	if err := waitForDeploymentEnvVar(t, kclient, routerDeployment, deploymentTimeout, ingresscontroller.RouterBackendCheckInterval, (2 * defaultHealthCheckInterval).String()); err != nil {
		t.Fatalf("expected router deployment to set %s to %v: %v", ingresscontroller.RouterBackendCheckInterval, 2*defaultHealthCheckInterval, err)
	}

	// set spec.healthCheckInterval to an unacceptable value, 0s
	if err := setHealthCheckInterval(t, kclient, 1*time.Minute, &metav1.Duration{0 * time.Second}, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	if err := waitForIngressControllerCondition(t, kclient, 5*deploymentTimeout, name, availableConditionsForPrivateIngressController...); err != nil {
		t.Fatalf("failed to observe expected conditions on %s: %v", name, err)
	}

	// when set to an unacceptable value, the env variable should not exist
	if err := waitForDeploymentEnvVar(t, kclient, routerDeployment, deploymentTimeout, ingresscontroller.RouterBackendCheckInterval, ""); err != nil {
		t.Fatalf("expected router deployment to remove %s: %v", ingresscontroller.RouterBackendCheckInterval, err)
	}
}

func setHealthCheckInterval(t *testing.T, client client.Client, timeout time.Duration, interval *metav1.Duration, c *operatorv1.IngressController) error {
	t.Helper()
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		c.Spec.TuningOptions.HealthCheckInterval = interval
		if err := client.Update(context.TODO(), c); err != nil {
			t.Logf("Update %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}
