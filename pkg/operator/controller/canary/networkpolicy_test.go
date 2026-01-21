package canary

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test_desiredCanaryNetworkPolicy verifies the desired NetworkPolicy for canary pods.
func Test_desiredCanaryNetworkPolicy(t *testing.T) {
	np := desiredCanaryNetworkPolicy()

	if np.Name != "ingress-canary" || np.Namespace != "openshift-ingress-canary" {
		t.Fatalf("unexpected name/namespace: %s/%s", np.Namespace, np.Name)
	}
	if np.Labels["ingress.openshift.io/canary"] != canaryControllerName {
		t.Fatalf("missing or wrong owner label: %#v", np.Labels)
	}
	// Ensure pod selector is set via helper.
	if np.Spec.PodSelector.MatchLabels == nil {
		t.Fatalf("pod selector should be set")
	}
	// Ensure ingress rules include ports 8888 and 8443.
	want := map[int32]struct{}{8888: {}, 8443: {}}
	got := map[int32]struct{}{}
	for _, r := range np.Spec.Ingress {
		for _, p := range r.Ports {
			if p.Protocol != nil && *p.Protocol == corev1.ProtocolTCP && p.Port != nil && p.Port.IntVal != 0 {
				got[p.Port.IntVal] = struct{}{}
			}
		}
	}
	for port := range want {
		if _, ok := got[port]; !ok {
			t.Fatalf("expected ingress port %d to be present", port)
		}
	}
}

// Test_canaryNetworkPolicyChanged verifies change detection and update behavior.
func Test_canaryNetworkPolicyChanged(t *testing.T) {
	original := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "openshift-ingress-canary",
			Name:      "ingress-canary",
			Labels: map[string]string{
				"ingress.openshift.io/canary": canaryControllerName,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"ingresscanary.operator.openshift.io/daemonset-ingresscanary": canaryControllerName}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{}},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{}},
		},
	}

	// No change.
	if changed, _ := canaryNetworkPolicyChanged(original, original.DeepCopy()); changed {
		t.Fatalf("expected no change detected")
	}

	// Change in spec should be detected.
	mutated := original.DeepCopy()
	mutated.Spec.Ingress = append(mutated.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{})
	if changed, _ := canaryNetworkPolicyChanged(original, mutated); !changed {
		t.Fatalf("expected change detected for spec diff")
	}

	// Change in owner label should be detected but preserve other labels.
	mutated = original.DeepCopy()
	mutated.Labels["ingress.openshift.io/canary"] = "other"
	if changed, updated := canaryNetworkPolicyChanged(original, mutated); !changed {
		t.Fatalf("expected change detected for label diff")
	} else if updated.Labels["ingress.openshift.io/canary"] != "other" {
		t.Fatalf("expected owner label updated")
	}
}
