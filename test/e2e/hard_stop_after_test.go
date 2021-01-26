// +build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	ingresscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"

	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

type hardStopAfterUpdateStrategy int

const (
	// Unconditionally set the duration value.
	hardStopAfterSetValue hardStopAfterUpdateStrategy = iota

	// Unconditionally delete the annotation.
	hardStopAfterDeleteAnnotation
)

const (
	// hardStopAfterRetryTimeout duration for retrying operations.
	hardStopAfterRetryTimeout = 5 * time.Minute
)

// NOTE: This test will mutate the default ingresscontroller and the
// "cluster" ingress configuration.
func TestRouteHardStopAfterEnableOnIngressConfig(t *testing.T) {
	ic, ingressConfig, routerDeployment, err := hardStopAfterGetTestObjects(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get test objects: %v", err)
	}

	if err := hardStopAfterTestIngressConfig(t, kclient, ingressConfig, routerDeployment, (200 * time.Minute).String()); err != nil {
		t.Fatalf("test assertions failed: %v", err)
	}

	if err := setHardStopAfterDurationForIngressConfig(t, kclient, hardStopAfterRetryTimeout, "", hardStopAfterDeleteAnnotation, ingressConfig); err != nil {
		t.Fatalf("failed to clear hard-stop-after on ingress config: %v", err)
	}
	if err := waitForHardStopAfterUnsetInAllComponents(kclient, hardStopAfterRetryTimeout, ic, ingressConfig, routerDeployment); err != nil {
		t.Fatalf("some component still has hard-stop-after set: %v", err)
	}
}

// NOTE: This test will mutate the default ingresscontroller and the
// "cluster" ingress configuration.
func TestRouteHardStopAfterEnableOnIngressController(t *testing.T) {
	ic, ingressConfig, routerDeployment, err := hardStopAfterTestSetup(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get test objects: %v", err)
	}

	if err := hardStopAfterTestIngressController(t, kclient, ic, routerDeployment, (300 * time.Minute).String()); err != nil {
		t.Fatalf("test assertions failed: %v", err)
	}

	if err := setHardStopAfterDurationForIngressController(t, kclient, hardStopAfterRetryTimeout, "", hardStopAfterDeleteAnnotation, ic); err != nil {
		t.Fatalf("failed to clear hard-stop-after on ingresscontroller: %v", err)
	}
	if err := waitForHardStopAfterUnsetInAllComponents(kclient, hardStopAfterRetryTimeout, ic, ingressConfig, routerDeployment); err != nil {
		t.Fatalf("some component still has hard-stop-after set: %v", err)
	}
}

// NOTE: This test will mutate the default ingresscontroller and the
// "cluster" ingress configuration.
func TestRouteHardStopAfterEnableOnIngressControllerHasPriorityOverIngressConfig(t *testing.T) {
	ic, ingressConfig, routerDeployment, err := hardStopAfterTestSetup(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get test objects: %v", err)
	}

	// First set hard-stop-after on the ingress config
	if err := hardStopAfterTestIngressConfig(t, kclient, ingressConfig, routerDeployment, (400 * time.Minute).String()); err != nil {
		t.Fatalf("test assertions failed: %v", err)
	}

	// Then set hard-stop-after on the controller which should take precedence.
	if err := hardStopAfterTestIngressController(t, kclient, ic, routerDeployment, (500 * time.Minute).String()); err != nil {
		t.Fatalf("test assertions failed: %v", err)
	}

	// Remove the controller setting.
	if err := setHardStopAfterDurationForIngressController(t, kclient, hardStopAfterRetryTimeout, "", hardStopAfterDeleteAnnotation, ic); err != nil {
		t.Fatalf("failed to clear hard-stop-after on ingresscontroller: %v", err)
	}

	// Deployment should revert back to config value
	if err := waitForRouterDeploymentHardStopAfterToBeSet(kclient, routerDeployTimeout, routerDeployment, (400 * time.Minute).String()); err != nil {
		t.Fatalf("expected router deployment to have hard-stop-after configured: %v", err)
	}

	if err := setHardStopAfterDurationForIngressConfig(t, kclient, hardStopAfterRetryTimeout, "", hardStopAfterDeleteAnnotation, ingressConfig); err != nil {
		t.Fatalf("failed to clear hard-stop-after on ingress config: %v", err)
	}
	if err := waitForHardStopAfterUnsetInAllComponents(kclient, hardStopAfterRetryTimeout, ic, ingressConfig, routerDeployment); err != nil {
		t.Fatalf("some component still has hard-stop-after set: %v", err)
	}
}

func TestRouteHardStopAfterTestInvalidDuration(t *testing.T) {
	ic, ingressConfig, routerDeployment, err := hardStopAfterTestSetup(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get test objects: %v", err)
	}

	if err := hardStopAfterTestIngressController(t, kclient, ic, routerDeployment, (600 * time.Minute).String()); err != nil {
		t.Fatalf("test assertions failed: %v", err)
	}

	if err := setHardStopAfterDurationForIngressController(t, kclient, hardStopAfterRetryTimeout, "mañana", hardStopAfterSetValue, ic); err != nil {
		t.Fatalf("failed to clear hard-stop-after on ingresscontroller: %v", err)
	}

	// This bad value will assert that the annotation is removed
	// and we would expect no component to have any registration
	// for hard-stop-after.
	if err := waitForHardStopAfterUnsetInAllComponents(kclient, hardStopAfterRetryTimeout, ic, ingressConfig, routerDeployment); err != nil {
		t.Fatalf("some component still has hard-stop-after set: %v", err)
	}
}

func TestRouteHardStopAfterTestZeroLengthDuration(t *testing.T) {
	ic, ingressConfig, routerDeployment, err := hardStopAfterTestSetup(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get test objects: %v", err)
	}

	if err := hardStopAfterTestIngressController(t, kclient, ic, routerDeployment, (700 * time.Minute).String()); err != nil {
		t.Fatalf("test assertions failed: %v", err)
	}

	if err := setHardStopAfterDurationForIngressController(t, kclient, hardStopAfterRetryTimeout, "", hardStopAfterSetValue, ic); err != nil {
		t.Fatalf("failed to clear hard-stop-after on ingresscontroller: %v", err)
	}

	// This bad value will assert that the annotation is removed
	// and we would expect no component to have any registration
	// for hard-stop-after.
	if err := waitForHardStopAfterUnsetInAllComponents(kclient, hardStopAfterRetryTimeout, ic, ingressConfig, routerDeployment); err != nil {
		t.Fatalf("some component still has hard-stop-after set: %v", err)
	}
}

func TestRouteHardStopAfterTestOneDayDuration(t *testing.T) {
	ic, ingressConfig, routerDeployment, err := hardStopAfterTestSetup(t, kclient, 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to get test objects: %v", err)
	}

	if err := hardStopAfterTestIngressController(t, kclient, ic, routerDeployment, "1d"); err != nil {
		t.Fatalf("test assertions failed: %v", err)
	}

	if err := setHardStopAfterDurationForIngressController(t, kclient, hardStopAfterRetryTimeout, "", hardStopAfterDeleteAnnotation, ic); err != nil {
		t.Fatalf("failed to clear hard-stop-after on ingresscontroller: %v", err)
	}

	if err := waitForHardStopAfterUnsetInAllComponents(kclient, hardStopAfterRetryTimeout, ic, ingressConfig, routerDeployment); err != nil {
		t.Fatalf("some component still has hard-stop-after set: %v", err)
	}
}

func hardStopAfterTestSetup(t *testing.T, client client.Client, timeout time.Duration) (*operatorv1.IngressController, *configv1.Ingress, *appsv1.Deployment, error) {
	if err := waitForIngressControllerCondition(client, timeout, defaultName, defaultAvailableConditions...); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to observe expected conditions: %v", err)
	}

	ic, ingressConfig, routerDeployment, err := hardStopAfterGetTestObjects(t, client, 1*time.Minute)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get test objects: %v", err)
	}

	if err := waitForHardStopAfterUnsetInAllComponents(client, hardStopAfterRetryTimeout, ic, ingressConfig, routerDeployment); err != nil {
		return nil, nil, nil, fmt.Errorf("test preconditions not met: %v", err)
	}

	return ic, ingressConfig, routerDeployment, nil
}

func hardStopAfterTestIngressConfig(t *testing.T, client client.Client, ingressConfig *configv1.Ingress, routerDeployment *appsv1.Deployment, duration string) error {
	if err := setHardStopAfterDurationForIngressConfig(t, client, hardStopAfterRetryTimeout, duration, hardStopAfterSetValue, ingressConfig); err != nil {
		return fmt.Errorf("failed to update ingress config: %v", err)
	}
	if err := waitForIngressControllerCondition(client, hardStopAfterRetryTimeout, defaultName, defaultAvailableConditions...); err != nil {
		return fmt.Errorf("failed to observe expected conditions: %v", err)
	}
	if err := waitForIngressConfigHardStopAfterToBeSet(client, hardStopAfterRetryTimeout, ingressConfig, duration); err != nil {
		return fmt.Errorf("hard-stop-after not set on ingress config: %v", err)
	}
	if err := waitForRouterDeploymentHardStopAfterToBeSet(client, routerDeployTimeout, routerDeployment, duration); err != nil {
		return fmt.Errorf("expected router deployment to have hard-stop-after configured: %v", err)
	}
	return nil
}

func hardStopAfterTestIngressController(t *testing.T, client client.Client, ic *operatorv1.IngressController, routerDeployment *appsv1.Deployment, duration string) error {
	if err := setHardStopAfterDurationForIngressController(t, client, hardStopAfterRetryTimeout, duration, hardStopAfterSetValue, ic); err != nil {
		t.Fatalf("failed to update ingresscontroller: %v", err)
	}
	if err := waitForIngressControllerCondition(client, hardStopAfterRetryTimeout, defaultName, defaultAvailableConditions...); err != nil {
		t.Fatalf("failed to observe expected conditions: %v", err)
	}
	if err := waitForIngressControllerHardStopAfterToBeSet(client, hardStopAfterRetryTimeout, ic, duration); err != nil {
		t.Fatalf("hard-stop-after not set on ingress controller: %v", err)
	}
	if err := waitForRouterDeploymentHardStopAfterToBeSet(client, routerDeployTimeout, routerDeployment, duration); err != nil {
		t.Fatalf("expected router deployment to have hard-stop-after configured: %v", err)
	}
	return nil
}

func waitForHardStopAfterUnsetInAllComponents(client client.Client, timeout time.Duration, ic *operatorv1.IngressController, ingressConfig *configv1.Ingress, routerDeployment *appsv1.Deployment) error {
	if err := waitForIngressConfigHardStopAfterToBeUnset(client, timeout, ingressConfig); err != nil {
		return err
	}
	if err := waitForIngressControllerHardStopAfterToBeUnset(client, timeout, ic); err != nil {
		return err
	}
	return waitForRouterDeploymentHardStopAfterToBeUnset(client, timeout, routerDeployment)
}

func waitForIngressConfigHardStopAfterToBeSet(client client.Client, timeout time.Duration, c *configv1.Ingress, expectedDuration string) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			return false, nil
		}
		if exists, val := ingress.HardStopAfterIsEnabled(&operatorv1.IngressController{}, c); exists {
			if val == expectedDuration {
				return true, nil
			}
		}
		return false, nil
	})
}

func waitForIngressConfigHardStopAfterToBeUnset(client client.Client, timeout time.Duration, c *configv1.Ingress) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			return false, nil
		}
		exists, _ := ingress.HardStopAfterIsEnabled(&operatorv1.IngressController{}, c)
		return !exists, nil
	})
}

func waitForIngressControllerHardStopAfterToBeSet(client client.Client, timeout time.Duration, c *operatorv1.IngressController, expectedDuration string) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			return false, nil
		}
		if exists, val := ingress.HardStopAfterIsEnabled(c, &configv1.Ingress{}); exists {
			if val == expectedDuration {
				return true, nil
			}
		}
		return false, nil
	})
}

func waitForIngressControllerHardStopAfterToBeUnset(client client.Client, timeout time.Duration, c *operatorv1.IngressController) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			return false, nil
		}
		exists, _ := ingress.HardStopAfterIsEnabled(c, &configv1.Ingress{})
		return !exists, nil
	})
}

func waitForRouterDeploymentHardStopAfterToBeSet(client client.Client, timeout time.Duration, routerDeployment *appsv1.Deployment, expectedDuration string) error {
	name := types.NamespacedName{Namespace: routerDeployment.Namespace, Name: routerDeployment.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, routerDeployment); err != nil {
			return false, nil
		}
		for _, c := range routerDeployment.Spec.Template.Spec.Containers {
			for _, v := range c.Env {
				if v.Name == ingresscontroller.RouterHardStopAfterEnvName && v.Value == expectedDuration {
					return true, nil
				}
			}
		}
		return false, nil
	})
}

func waitForRouterDeploymentHardStopAfterToBeUnset(client client.Client, timeout time.Duration, routerDeployment *appsv1.Deployment) error {
	name := types.NamespacedName{Namespace: routerDeployment.Namespace, Name: routerDeployment.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, routerDeployment); err != nil {
			return false, nil
		}
		for _, c := range routerDeployment.Spec.Template.Spec.Containers {
			for _, v := range c.Env {
				if v.Name == ingresscontroller.RouterHardStopAfterEnvName {
					return false, nil
				}
			}
		}
		return true, nil
	})
}

func setHardStopAfterDurationForIngressConfig(t *testing.T, client client.Client, timeout time.Duration, hardStopAfterDuration string, updateStrategy hardStopAfterUpdateStrategy, c *configv1.Ingress) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		switch updateStrategy {
		case hardStopAfterSetValue:
			c.Annotations[ingress.RouterHardStopAfterAnnotation] = hardStopAfterDuration
		case hardStopAfterDeleteAnnotation:
			delete(c.Annotations, ingress.RouterHardStopAfterAnnotation)
		}
		if err := client.Update(context.TODO(), c); err != nil {
			t.Logf("Update %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}

func setHardStopAfterDurationForIngressController(t *testing.T, client client.Client, timeout time.Duration, hardStopAfterDuration string, updateStrategy hardStopAfterUpdateStrategy, c *operatorv1.IngressController) error {
	name := types.NamespacedName{Namespace: c.Namespace, Name: c.Name}

	return wait.PollImmediate(1*time.Second, timeout, func() (bool, error) {
		if err := client.Get(context.TODO(), name, c); err != nil {
			t.Logf("Get %q failed: %v, retrying...", name, err)
			return false, nil
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		switch updateStrategy {
		case hardStopAfterSetValue:
			c.Annotations[ingress.RouterHardStopAfterAnnotation] = hardStopAfterDuration
		case hardStopAfterDeleteAnnotation:
			delete(c.Annotations, ingress.RouterHardStopAfterAnnotation)
		}
		if err := client.Update(context.TODO(), c); err != nil {
			t.Logf("Update %q failed: %v, retrying...", name, err)
			return false, nil
		}
		return true, nil
	})
}

func hardStopAfterGetTestObjects(t *testing.T, client client.Client, timeout time.Duration) (*operatorv1.IngressController, *configv1.Ingress, *appsv1.Deployment, error) {
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
