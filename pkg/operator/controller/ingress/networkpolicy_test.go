package ingress

import (
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test_desiredRouterNetworkPolicy verifies the desired NetworkPolicy for a router.
func Test_desiredRouterNetworkPolicy(t *testing.T) {
	ic := &operatorv1.IngressController{ObjectMeta: metav1.ObjectMeta{Name: "default"}}

	np := desiredRouterNetworkPolicy(ic)

	if np.Name != "router-default" || np.Namespace != "openshift-ingress" {
		t.Fatalf("unexpected name/namespace: %s/%s", np.Namespace, np.Name)
	}
	if np.Labels["ingresscontroller.operator.openshift.io/owning-ingresscontroller"] != "default" {
		t.Fatalf("missing or wrong owner label: %#v", np.Labels)
	}
	// Ensure pod selector is set via helper.
	if np.Spec.PodSelector.MatchLabels == nil {
		t.Fatalf("pod selector should be set")
	}
	// Ensure ingress rules include ports 80, 443, 1936.
	want := map[int32]struct{}{80: {}, 443: {}, 1936: {}}
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

// Test_routerNetworkPolicyChanged verifies change detection and update behavior.
func Test_routerNetworkPolicyChanged(t *testing.T) {
	original := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "openshift-ingress",
			Name:      "router-default",
			Labels: map[string]string{
				"ingresscontroller.operator.openshift.io/owning-ingresscontroller": "default",
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"ingresscontroller.operator.openshift.io/deployment-ingresscontroller": "default"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{}},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{}},
		},
	}

	// No change.
	if changed, _ := routerNetworkPolicyChanged(original, original.DeepCopy()); changed {
		t.Fatalf("expected no change detected")
	}

	// Change in spec should be detected.
	mutated := original.DeepCopy()
	mutated.Spec.Ingress = append(mutated.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{})
	if changed, _ := routerNetworkPolicyChanged(original, mutated); !changed {
		t.Fatalf("expected change detected for spec diff")
	}

	// Change in owner label should be detected but preserve other labels.
	mutated = original.DeepCopy()
	mutated.Labels["ingresscontroller.operator.openshift.io/owning-ingresscontroller"] = "other"
	if changed, updated := routerNetworkPolicyChanged(original, mutated); !changed {
		t.Fatalf("expected change detected for label diff")
	} else if updated.Labels["ingresscontroller.operator.openshift.io/owning-ingresscontroller"] != "other" {
		t.Fatalf("expected owner label updated")
	}
}
