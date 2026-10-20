//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/names"
)

// TestHostedClusterCustomRouterReadiness ensures a new custom router on an
// external-control-plane cluster does not report readiness while unschedulable.
// The unmatched selector makes the #1607 readiness regression deterministic.
func TestHostedClusterCustomRouterReadiness(t *testing.T) {
	t.Parallel()
	if infraConfig.Status.ControlPlaneTopology != configv1.ExternalTopologyMode {
		t.Skip("requires a hosted cluster with external control-plane topology")
	}
	ctx := context.Background()
	nodes := &corev1.NodeList{}
	if err := kclient.List(ctx, nodes); err != nil {
		t.Fatalf("failed to list nodes: %v", err)
	}
	if len(nodes.Items) == 0 {
		t.Skip("requires guest worker nodes to test custom-router readiness")
	}
	name := types.NamespacedName{Namespace: operatorNamespace, Name: names.SimpleNameGenerator.GenerateName("readiness-")}
	ic := newPrivateController(name, name.Name+".router.test")
	ic.Spec.NodePlacement = &operatorv1.NodePlacement{NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"ingress-e2e-readiness": name.Name}}}
	dynamic, err := isFeatureGateEnabled(features.FeatureGateIngressControllerDynamicConfigurationManager)
	if err != nil {
		t.Fatalf("failed to check dynamic configuration feature gate: %v", err)
	}
	if dynamic {
		ic.Spec.TuningOptions.ConfigurationManagement = operatorv1.IngressControllerConfigurationManagementDynamic
	}
	if err := createWithRetryOnError(t, ctx, ic, DefaultRetryTimeout); err != nil {
		t.Fatalf("failed to create custom ingresscontroller: %v", err)
	}
	t.Cleanup(func() { assertIngressControllerDeleted(t, kclient, ic) })
	conditions := []operatorv1.OperatorCondition{
		{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionFalse},
		{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionTrue},
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, conditions...); err != nil {
		t.Fatalf("unschedulable router did not report unavailable and progressing: %v", err)
	}
	// Observe several reconciliations rather than accepting a transient false
	// condition before an incorrect startup exception is applied.
	observeUntil := time.Now().Add(20 * time.Second)
	err = wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		actual := &operatorv1.IngressController{}
		if err := kclient.Get(ctx, name, actual); err != nil {
			return false, err
		}
		for _, expected := range conditions {
			found := false
			for _, condition := range actual.Status.Conditions {
				if condition.Type == expected.Type {
					found = true
					if condition.Status != expected.Status {
						return false, fmt.Errorf("unschedulable router reports %s=%s: %s", condition.Type, condition.Status, condition.Message)
					}
				}
			}
			if !found {
				return false, fmt.Errorf("unschedulable router no longer reports %s", expected.Type)
			}
		}
		deployment := &appsv1.Deployment{}
		if err := kclient.Get(ctx, controller.RouterDeploymentName(ic), deployment); err != nil {
			return false, err
		}
		if deployment.Status.AvailableReplicas != 0 {
			return false, fmt.Errorf("unmatched selector unexpectedly has %d available replicas", deployment.Status.AvailableReplicas)
		}
		return time.Now().After(observeUntil), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := updateIngressControllerWithRetryOnConflict(t, name, DefaultRetryTimeout, func(ic *operatorv1.IngressController) {
		ic.Spec.NodePlacement = nil
	}); err != nil {
		t.Fatalf("failed to allow router scheduling: %v", err)
	}
	if err := waitForIngressControllerCondition(t, kclient, 5*time.Minute, name,
		operatorv1.OperatorCondition{Type: operatorv1.IngressControllerAvailableConditionType, Status: operatorv1.ConditionTrue},
		operatorv1.OperatorCondition{Type: operatorv1.OperatorStatusTypeProgressing, Status: operatorv1.ConditionFalse}); err != nil {
		t.Fatalf("router did not become ready after allowing scheduling: %v", err)
	}
	if err := waitForDeploymentCompleteWithOldPodTermination(t, kclient, controller.RouterDeploymentName(ic), 5*time.Minute); err != nil {
		t.Fatalf("router readiness preceded a completed deployment: %v", err)
	}
}
