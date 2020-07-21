// +build e2e

package e2e

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const endpointSlicesRouterDeployTimeout time.Duration = 1 * time.Minute

// NOTE: This test will mutate the default ingresscontroller and the
// "cluster" ingress configuration.
func TestRouteEndpointSlicesEnableAndDisableIngressController(t *testing.T) {
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ic, err := endpointSlicesGetIngressController(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	// By default the router should not have endpointslices disabled
	if err := waitForRouterDeploymentEndpointSlicesDisabled(kclient, endpointSlicesRouterDeployTimeout, ic, false); err != nil {
		t.Fatalf("expected router deployment to have endpointslices enabled: %v", err)
	}

	if err := setEndpointSlicesDisabledForIngressController(t, kclient, 1*time.Minute, true, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := waitForRouterDeploymentEndpointSlicesDisabled(kclient, endpointSlicesRouterDeployTimeout, ic, true); err != nil {
		t.Fatalf("expected router deployment to have endpointslices disabled: %v", err)
	}

	if err := setEndpointSlicesDisabledForIngressController(t, kclient, 1*time.Minute, false, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	if err := waitForRouterDeploymentEndpointSlicesDisabled(kclient, endpointSlicesRouterDeployTimeout, ic, false); err != nil {
		t.Fatalf("expected router deployment to have endpointslices enabled: %v", err)
	}
}

func TestRouteEndpointSlicesEnableAndDisableIngressConfig(t *testing.T) {
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ic, err := endpointSlicesGetIngressController(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	if err := setEndpointSlicesDisabledForIngressController(t, kclient, 1*time.Minute, false, ic); err != nil {
		t.Fatalf("failed to update ingress config: %v", err)
	}

	ingressConfig, err := endpointSlicesGetIngressConfig(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress config: %v", err)
	}

	// By default the router should not have endpointslices disabled
	if err := waitForRouterDeploymentEndpointSlicesDisabled(kclient, endpointSlicesRouterDeployTimeout, ic, false); err != nil {
		t.Fatalf("expected router deployment to have endpointslices enabled: %v", err)
	}

	if err := setEndpointSlicesDisabledForIngressConfig(t, kclient, 1*time.Minute, true, ingressConfig); err != nil {
		t.Fatalf("failed to update ingress config: %v", err)
	}

	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := waitForRouterDeploymentEndpointSlicesDisabled(kclient, endpointSlicesRouterDeployTimeout, ic, true); err != nil {
		t.Fatalf("expected router deployment to have endpointslices disabled: %v", err)
	}

	// Now set the ingress controller to also have the annotation
	// but with an explicit value of "false" which will take
	// precedence over the ingress config which is currently
	// "true". The net result should be that the router deployment
	// transitions from its current state of endpointslices
	// disabled (which we just asserted) back to endpointslices
	// enabled.
	//
	// This update is different to
	// setEndpointSlicesDisabledForIngressController() as we preserve the
	// annotation if the value is "false".
	if err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
			t.Logf("Get failed: %v, retrying...", err)
			return false, nil
		}
		if ic.Annotations == nil {
			ic.Annotations = map[string]string{}
		}
		ic.Annotations[ingress.RouterDisableEndpointSlicesAnnotation] = "false"
		if err := kclient.Update(context.TODO(), ic); err != nil {
			t.Logf("failed to update ingress controller: %v", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Fatalf("failed to update ingress controller: %v", err)
	}

	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := waitForRouterDeploymentEndpointSlicesDisabled(kclient, endpointSlicesRouterDeployTimeout, ic, false); err != nil {
		t.Fatalf("expected router deployment to have endpointslices disabled: %v", err)
	}

	// cleanup

	if err := setEndpointSlicesDisabledForIngressController(t, kclient, 1*time.Minute, false, ic); err != nil {
		t.Fatalf("failed to update ingress controller: %v", err)
	}

	if err := setEndpointSlicesDisabledForIngressConfig(t, kclient, 1*time.Minute, false, ingressConfig); err != nil {
		t.Fatalf("failed to update ingress config: %v", err)
	}
}

func endpointSlicesIsDisabledInEnv(env []corev1.EnvVar) bool {
	for _, v := range env {
		if v.Name == ingresscontroller.RouterWatchEndpointsEnvName {
			if val, err := strconv.ParseBool(v.Value); err == nil {
				return val
			}
		}
	}
	return false
}

func endpointSlicesGetIngressController(t *testing.T, client client.Client, timeout time.Duration) (*operatorv1.IngressController, error) {
	ic := operatorv1.IngressController{}
	if err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), defaultName, &ic); err != nil {
			t.Logf("Get %q failed: %v, retrying...", defaultName, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to get %q: %v", defaultName, err)
	}
	return &ic, nil
}

func endpointSlicesGetIngressConfig(t *testing.T, client client.Client, timeout time.Duration) (*configv1.Ingress, error) {
	ingressConfig := configv1.Ingress{}
	if err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		name := types.NamespacedName{Name: "cluster"}
		if err := client.Get(context.TODO(), name, &ingressConfig); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to get ingress configuration: %v", err)
	}

	return &ingressConfig, nil
}

func waitForRouterDeploymentEndpointSlicesDisabled(client client.Client, timeout time.Duration, c *operatorv1.IngressController, expectedDisabledStatus bool) error {
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		deployment := &appsv1.Deployment{}
		if err := client.Get(context.TODO(), controller.RouterDeploymentName(c), deployment); err != nil {
			return false, nil
		}
		return endpointSlicesIsDisabledInEnv(deployment.Spec.Template.Spec.Containers[0].Env) == expectedDisabledStatus, nil
	})
}

func setEndpointSlicesDisabledForIngressConfig(t *testing.T, client client.Client, timeout time.Duration, disableEndpointSlices bool, c *configv1.Ingress) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		if disableEndpointSlices {
			c.Annotations[ingress.RouterDisableEndpointSlicesAnnotation] = "true"
		} else {
			delete(c.Annotations, ingress.RouterDisableEndpointSlicesAnnotation)
		}
		if err := client.Update(context.TODO(), c); err != nil {
			t.Logf("Update %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}

func setEndpointSlicesDisabledForIngressController(t *testing.T, client client.Client, timeout time.Duration, disableEndpointSlices bool, c *operatorv1.IngressController) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		if disableEndpointSlices {
			c.Annotations[ingress.RouterDisableEndpointSlicesAnnotation] = "true"
		} else {
			delete(c.Annotations, ingress.RouterDisableEndpointSlicesAnnotation)
		}
		if err := client.Update(context.TODO(), c); err != nil {
			t.Logf("Update %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}
