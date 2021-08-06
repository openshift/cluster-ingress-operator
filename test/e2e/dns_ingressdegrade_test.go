// +build e2e

package e2e

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorclient "github.com/openshift/cluster-ingress-operator/pkg/operator/client"

	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"k8s.io/apimachinery/pkg/types"
)

// TestIngressStatus - degrade/restore status via DNS config
// This test will check the ingress status
//
// Steps :-
// 1. initial check - should be false
// 2. update the DNS private zone tags to unknown value
// 3. check the status should have degraded to true
// 4. re-instate the original condition
// 5. check the status should have degraded to false
func TestIngressStatus(t *testing.T) {
	kubeConfig, err := config.GetConfig()
	if err != nil {
		t.Fatalf("failed to get kube config: %s\n", err)
	}

	kubeClient, err := operatorclient.NewClient(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	if err := kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, &dnsConfig); err != nil {
		t.Fatalf("failed to get DNS config: %v", err)
	}

	// step 1
	expected := []configv1.ClusterOperatorStatusCondition{
		{Type: configv1.OperatorAvailable, Status: configv1.ConditionTrue},
		{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse},
	}
	if err := waitForClusterOperatorConditions(t, kclient, expected...); err != nil {
		t.Fatalf("did not get expected available condition: %v", err)
	}

	// step 2
	updCfg := dnsConfig.DeepCopy()
	updateDNSConfig(true, updCfg)
	if err := kclient.Update(context.TODO(), updCfg); err != nil {
		t.Fatalf("failed to update DNS config: %v", err)
	}

	// step 3
	expected = []configv1.ClusterOperatorStatusCondition{
		{Type: configv1.OperatorDegraded, Status: configv1.ConditionTrue},
	}
	if err := waitForClusterOperatorConditions(t, kclient, expected...); err != nil {
		t.Fatalf("did not get expected available condition: %v", err)
	}

	// step 4
	updateDNSConfig(false, updCfg)
	if err := kclient.Update(context.TODO(), updCfg); err != nil {
		t.Fatalf("failed to update DNS config: %v", err)
	}

	// step 5
	expected = []configv1.ClusterOperatorStatusCondition{
		{Type: configv1.OperatorAvailable, Status: configv1.ConditionTrue},
		{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse},
	}
	if err := waitForClusterOperatorConditions(t, kclient, expected...); err != nil {
		t.Fatalf("did not get expected available condition: %v", err)
	}
}

// updateDNSConfig - utility to set/unset PrivateZone Tags/ID
func updateDNSConfig(set bool, dnsConfig *configv1.DNS) {
	if dnsConfig.Spec.PrivateZone.ID != "" {
		if set {
			dnsConfig.Spec.PrivateZone.ID = "error-" + dnsConfig.Spec.PrivateZone.ID
		} else {
			// remove 'error-' from prefix
			dnsConfig.Spec.PrivateZone.ID = dnsConfig.Spec.PrivateZone.ID[6:]
		}
	}
	if dnsConfig.Spec.PrivateZone.Tags["Name"] != "" {
		if set {
			dnsConfig.Spec.PrivateZone.Tags["Name"] = "error-" + dnsConfig.Spec.PrivateZone.Tags["Name"]
		} else {
			// remove 'error-' from prefix
			dnsConfig.Spec.PrivateZone.Tags["Name"] = dnsConfig.Spec.PrivateZone.Tags["Name"][6:]
		}
	}
}
