package gatewayapi

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test_desiredGatewayAPINetworkPolicy verifies ports and labels on the desired policy.
func Test_desiredGatewayAPINetworkPolicy(t *testing.T) {
	np := desiredGatewayAPINetworkPolicy()

	// Pod selector must target the Istio gateway pods.
	if np.Spec.PodSelector.MatchLabels == nil || np.Spec.PodSelector.MatchLabels["istio.io/rev"] != "openshift-gateway" {
		t.Fatalf("pod selector should match istio.io/rev=openshift-gateway, got: %#v", np.Spec.PodSelector.MatchLabels)
	}

	// Ingress should allow from 0.0.0.0/0
	hasIngressIPBlock := false
	for _, r := range np.Spec.Ingress {
		for _, f := range r.From {
			if f.IPBlock != nil && f.IPBlock.CIDR == "0.0.0.0/0" {
				hasIngressIPBlock = true
			}
		}
	}
	if !hasIngressIPBlock {
		t.Fatalf("expected ingress from 0.0.0.0/0")
	}

	// Egress should allow to 0.0.0.0/0
	hasEgressIPBlock := false
	for _, r := range np.Spec.Egress {
		for _, to := range r.To {
			if to.IPBlock != nil && to.IPBlock.CIDR == "0.0.0.0/0" {
				hasEgressIPBlock = true
			}
		}
	}
	if !hasEgressIPBlock {
		t.Fatalf("expected egress to 0.0.0.0/0")
	}

	// PolicyTypes should include Ingress and Egress
	hasIngress, hasEgress := false, false
	for _, pt := range np.Spec.PolicyTypes {
		if pt == networkingv1.PolicyTypeIngress {
			hasIngress = true
		}
		if pt == networkingv1.PolicyTypeEgress {
			hasEgress = true
		}
	}
	if !(hasIngress && hasEgress) {
		t.Fatalf("expected policyTypes to include Ingress and Egress, got: %#v", np.Spec.PolicyTypes)
	}
}

func Test_gatewayAPINetworkPolicyChanged(t *testing.T) {
	// Minimal synthetic current/expected with different rules
	current := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "openshift-ingress",
			Name:      "gateway-api-allow",
			Labels:    map[string]string{"a": "1"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"foo": "bar"}},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{}},
			Egress:      []networkingv1.NetworkPolicyEgressRule{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	expected := current.DeepCopy()
	expected.Spec.Ingress = append(expected.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{})
	expected.Labels = map[string]string{"a": "2"}

	if changed, updated := gatewayAPINetworkPolicyChanged(current, expected); !changed {
		t.Fatalf("expected change detected")
	} else {
		if len(updated.Spec.Ingress) != len(expected.Spec.Ingress) {
			t.Fatalf("expected spec to be updated")
		}
		if updated.Labels["a"] != "2" {
			t.Fatalf("expected labels to be merged/updated")
		}
		if changedAgain, _ := gatewayAPINetworkPolicyChanged(updated, expected); changedAgain {
			t.Fatalf("function should be idempotent once updated")
		}
	}
}
