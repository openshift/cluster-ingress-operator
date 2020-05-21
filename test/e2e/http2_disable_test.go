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

// NOTE: This test will mutate the default ingresscontroller and the
// "cluster" ingress configuration.
func TestRouteHTTP2EnableAndDisable(t *testing.T) {
	routerDeployTimeout := 1 * time.Minute

	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
	ic, ingressConfig, routerDeployment, err := http2RefreshTestObjects(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get test objects: %v", err)
	}
	if err := checkHTTP2IsEnabled(ic, ingressConfig, routerDeployment); err != nil {
		t.Fatalf("test assertions failed: %v", err)
	}

	// Disable http/2 on the default ingresscontroller
	if err := setHTTP2DisabledForIngressController(t, kclient, 1*time.Minute, true, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
	if err := waitForRouterDeploymentHTTP2Disabled(kclient, routerDeployTimeout, ic, true); err != nil {
		t.Fatalf("expected router deployment to have http/2 disabled: %v", err)
	}

	// Revert the previous change to the default ingresscontroller
	if err := setHTTP2DisabledForIngressController(t, kclient, 1*time.Minute, false, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
	if err := waitForRouterDeploymentHTTP2Disabled(kclient, routerDeployTimeout, ic, false); err != nil {
		t.Fatalf("expected router deployment to have http/2 enabled: %v", err)
	}

	//
	// Now assert that changing the "cluster" ingress
	// configuration will also disable/enable http/2 for all
	// ingress controllers.
	ic, ingressConfig, routerDeployment, err = http2RefreshTestObjects(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get test objects: %v", err)
	}
	if err := checkHTTP2IsEnabled(ic, ingressConfig, routerDeployment); err != nil {
		t.Fatalf("test assertions failed: %v", err)
	}

	// Disable http/2 on the ingress config "cluster"
	if err := setHTTP2DisabledForIngressConfig(t, kclient, 1*time.Minute, true, ingressConfig); err != nil {
		t.Fatalf("failed to update ingress config: %v", err)
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
	if err := waitForRouterDeploymentHTTP2Disabled(kclient, routerDeployTimeout, ic, true); err != nil {
		t.Fatalf("expected router deployment to have http/2 disabled: %v", err)
	}

	// Revert the previous change to the ingress config
	ic, ingressConfig, routerDeployment, err = http2RefreshTestObjects(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get test objects: %v", err)
	}
	if err := setHTTP2DisabledForIngressConfig(t, kclient, 1*time.Minute, false, ingressConfig); err != nil {
		t.Fatalf("failed to update ingress config: %v", err)
	}
	if err := waitForIngressControllerCondition(kclient, 5*time.Minute, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
	if err := waitForRouterDeploymentHTTP2Disabled(kclient, routerDeployTimeout, ic, false); err != nil {
		t.Fatalf("expected router deployment to have http/2 enabled: %v", err)
	}

	// Assert all our mutations are back to their initial state
	ic, ingressConfig, routerDeployment, err = http2RefreshTestObjects(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get test objects: %v", err)
	}
	if err := checkHTTP2IsEnabled(ic, ingressConfig, routerDeployment); err != nil {
		t.Fatalf("test assertions failed: %v", err)
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

func http2RefreshTestObjects(t *testing.T, client client.Client, timeout time.Duration) (*operatorv1.IngressController, *configv1.Ingress, *appsv1.Deployment, error) {
	ic := operatorv1.IngressController{}
	if err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), defaultName, &ic); err != nil {
			t.Logf("Get %q failed: %v, retrying...", defaultName, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get %q: %v", defaultName, err)
	}

	deployment := appsv1.Deployment{}
	if err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), controller.RouterDeploymentName(&ic), &deployment); err != nil {
			t.Logf("Get %q failed: %v, retrying...", controller.RouterDeploymentName(&ic), err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get default ingresscontroller router deployment: %v", err)
	}

	ingressConfig := configv1.Ingress{}
	if err := wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		name := types.NamespacedName{Name: "cluster"}
		if err := client.Get(context.TODO(), name, &ingressConfig); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get ingress configuration: %v", err)
	}

	return &ic, &ingressConfig, &deployment, nil
}

func checkHTTP2IsEnabled(ic *operatorv1.IngressController, ingressConfig *configv1.Ingress, d *appsv1.Deployment) error {
	icName := fmt.Sprintf("%s/%s", ic.Namespace, ic.Name)
	deploymentName := fmt.Sprintf("%s/%s", d.Namespace, d.Name)
	ingressConfigName := fmt.Sprintf("%s/%s", ingressConfig.Namespace, ingressConfig.Name)

	if ingress.HTTP2IsDisabledByAnnotation(ic.Annotations) {
		return fmt.Errorf("expected ingress controller %q to have HTTP/2 enabled by default", icName)
	}
	if ingress.HTTP2IsDisabledByAnnotation(ingressConfig.Annotations) {
		return fmt.Errorf("expected ingress config %q to have HTTP/2 enabled by default", ingressConfigName)
	}
	if len(d.Spec.Template.Spec.Containers) < 1 {
		return fmt.Errorf("expected deployment %q to have at least one container", deploymentName)
	}
	if http2IsDisabledInEnv(d.Spec.Template.Spec.Containers[0].Env) {
		return fmt.Errorf("expected deployment %q to have http/2 enabled by default", deploymentName)
	}
	return nil
}

func waitForRouterDeploymentHTTP2Disabled(client client.Client, timeout time.Duration, c *operatorv1.IngressController, expectedDisabledStatus bool) error {
	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		deployment := &appsv1.Deployment{}
		if err := client.Get(context.TODO(), controller.RouterDeploymentName(c), deployment); err != nil {
			return false, nil
		}
		return http2IsDisabledInEnv(deployment.Spec.Template.Spec.Containers[0].Env) == expectedDisabledStatus, nil
	})
}

func setHTTP2DisabledForIngressConfig(t *testing.T, client client.Client, timeout time.Duration, disableHTTP2 bool, c *configv1.Ingress) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		if disableHTTP2 {
			c.Annotations[ingress.RouterDisableHTTP2Annotation] = "true"
		} else {
			delete(c.Annotations, ingress.RouterDisableHTTP2Annotation)
		}
		if err := client.Update(context.TODO(), c); err != nil {
			t.Logf("Update %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}

func setHTTP2DisabledForIngressController(t *testing.T, client client.Client, timeout time.Duration, disableHTTP2 bool, c *operatorv1.IngressController) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		if disableHTTP2 {
			c.Annotations[ingress.RouterDisableHTTP2Annotation] = "true"
		} else {
			delete(c.Annotations, ingress.RouterDisableHTTP2Annotation)
		}
		if err := client.Update(context.TODO(), c); err != nil {
			t.Logf("Update %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}
