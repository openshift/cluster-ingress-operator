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

const routerDeployTimeout time.Duration = 1 * time.Minute

// NOTE: This test will mutate the default ingresscontroller and the
// "cluster" ingress configuration.
func TestRouteHTTP2EnableAndDisableIngressController(t *testing.T) {
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ic, err := http2GetIngressController(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	// By default the router should not have http/2 enabled
	if err := waitForRouterDeploymentHTTP2Enabled(kclient, routerDeployTimeout, ic, false); err != nil {
		t.Fatalf("expected router deployment to have http/2 disabled: %v", err)
	}

	if err := setHTTP2EnabledForIngressController(t, kclient, 1*time.Minute, true, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := waitForRouterDeploymentHTTP2Enabled(kclient, routerDeployTimeout, ic, true); err != nil {
		t.Fatalf("expected router deployment to have http/2 enabled: %v", err)
	}

	if err := setHTTP2EnabledForIngressController(t, kclient, 1*time.Minute, false, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}

	if err := waitForRouterDeploymentHTTP2Enabled(kclient, routerDeployTimeout, ic, false); err != nil {
		t.Fatalf("expected router deployment to have http/2 disabled: %v", err)
	}
}

func TestRouteHTTP2EnableAndDisableIngressConfig(t *testing.T) {
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	ic, err := http2GetIngressController(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress controller: %v", err)
	}

	if err := setHTTP2EnabledForIngressController(t, kclient, 1*time.Minute, false, ic); err != nil {
		t.Fatalf("failed to update ingress controller: %v", err)
	}

	ingressConfig, err := http2GetIngressConfig(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get ingress config: %v", err)
	}

	// By default the router should not have http/2 enabled
	if err := waitForRouterDeploymentHTTP2Enabled(kclient, routerDeployTimeout, ic, false); err != nil {
		t.Fatalf("expected router deployment to have http/2 disabled: %v", err)
	}

	if err := setHTTP2EnabledForIngressConfig(t, kclient, 1*time.Minute, true, ingressConfig); err != nil {
		t.Fatalf("failed to update ingress config: %v", err)
	}

	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}

	if err := waitForRouterDeploymentHTTP2Enabled(kclient, routerDeployTimeout, ic, true); err != nil {
		t.Fatalf("expected router deployment to have http/2 enabled: %v", err)
	}

	// Now set the ingress controller to also have the annotation
	// but with an explicit value of "false" which will take
	// precedence over the ingress config which is currently
	// "true". The net result should be that the router deployment
	// transitions from its current state of http/2 enabled
	// (which we just asserted) back to http/2 disabled.
	//
	// This update is different to
	// setHTTP2EnabledForIngressController() as we preserve the
	// annotation if the value is "false".
	if err := wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		if err := kclient.Get(context.TODO(), defaultName, ic); err != nil {
			t.Logf("Get failed: %v, retrying...", err)
			return false, nil
		}
		if ic.Annotations == nil {
			ic.Annotations = map[string]string{}
		}
		ic.Annotations[ingress.RouterDefaultEnableHTTP2Annotation] = "false"
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

	if err := waitForRouterDeploymentHTTP2Enabled(kclient, routerDeployTimeout, ic, false); err != nil {
		t.Fatalf("expected router deployment to have http/2 disabled: %v", err)
	}

	// cleanup

	if err := setHTTP2EnabledForIngressController(t, kclient, 1*time.Minute, false, ic); err != nil {
		t.Fatalf("failed to update ingress controller: %v", err)
	}

	if err := setHTTP2EnabledForIngressConfig(t, kclient, 1*time.Minute, false, ingressConfig); err != nil {
		t.Fatalf("failed to update ingress config: %v", err)
	}
}

func http2IsDisabledInEnv(env []corev1.EnvVar) bool {
	for _, v := range env {
		if v.Name == ingresscontroller.RouterDisableHTTP2EnvName {
			if val, err := strconv.ParseBool(v.Value); err == nil {
				return val
			}
		}
	}
	return false
}

func http2GetIngressController(t *testing.T, client client.Client, timeout time.Duration) (*operatorv1.IngressController, error) {
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

func http2GetIngressConfig(t *testing.T, client client.Client, timeout time.Duration) (*configv1.Ingress, error) {
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

func waitForRouterDeploymentHTTP2Enabled(client client.Client, timeout time.Duration, c *operatorv1.IngressController, expectedDisabledStatus bool) error {
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		deployment := &appsv1.Deployment{}
		if err := client.Get(context.TODO(), controller.RouterDeploymentName(c), deployment); err != nil {
			return false, nil
		}
		return http2IsDisabledInEnv(deployment.Spec.Template.Spec.Containers[0].Env) == expectedDisabledStatus, nil
	})
}

func setHTTP2EnabledForIngressConfig(t *testing.T, client client.Client, timeout time.Duration, enableHTTP2 bool, c *configv1.Ingress) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		if enableHTTP2 {
			c.Annotations[ingress.RouterDefaultEnableHTTP2Annotation] = "true"
		} else {
			delete(c.Annotations, ingress.RouterDefaultEnableHTTP2Annotation)
		}
		if err := client.Update(context.TODO(), c); err != nil {
			t.Logf("Update %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}

func setHTTP2EnabledForIngressController(t *testing.T, client client.Client, timeout time.Duration, enableHTTP2 bool, c *operatorv1.IngressController) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		if enableHTTP2 {
			c.Annotations[ingress.RouterDefaultEnableHTTP2Annotation] = "true"
		} else {
			delete(c.Annotations, ingress.RouterDefaultEnableHTTP2Annotation)
		}
		if err := client.Update(context.TODO(), c); err != nil {
			t.Logf("Update %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}
