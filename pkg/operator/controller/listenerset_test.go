package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestListenerSetDNSHostnames(t *testing.T) {
	base := &gatewayapiv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "listeners", Generation: 2},
		Spec: gatewayapiv1.ListenerSetSpec{
			ParentRef: gatewayapiv1.ParentGatewayReference{Name: "gateway", Namespace: ptr.To(gatewayapiv1.Namespace(DefaultOperandNamespace))},
			Listeners: []gatewayapiv1.ListenerEntry{{Name: "http", Hostname: ptr.To(gatewayapiv1.Hostname("*.EXAMPLE.com."))}},
		},
		Status: gatewayapiv1.ListenerSetStatus{
			Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 2}},
			Listeners:  []gatewayapiv1.ListenerEntryStatus{{Name: "http", Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 2}, {Type: "Conflicted", Status: metav1.ConditionFalse, ObservedGeneration: 2}}}},
		},
	}
	for _, testCase := range []struct {
		name     string
		mutate   func(*gatewayapiv1.ListenerSet)
		eligible bool
	}{
		{"accepted", func(*gatewayapiv1.ListenerSet) {}, true},
		{"other parent group", func(ls *gatewayapiv1.ListenerSet) {
			ls.Spec.ParentRef.Group = ptr.To(gatewayapiv1.Group("other.example.com"))
		}, false},
		{"other parent kind", func(ls *gatewayapiv1.ListenerSet) { ls.Spec.ParentRef.Kind = ptr.To(gatewayapiv1.Kind("Service")) }, false},
		{"rejected upgrade", func(ls *gatewayapiv1.ListenerSet) {
			ls.Status.Conditions[0].Status = metav1.ConditionFalse
			ls.Status.Conditions[0].Reason = "UnsupportedByController"
		}, false},
		{"stale acceptance", func(ls *gatewayapiv1.ListenerSet) { ls.Status.Conditions[0].ObservedGeneration-- }, false},
		{"stale listener", func(ls *gatewayapiv1.ListenerSet) { ls.Status.Listeners[0].Conditions[0].ObservedGeneration-- }, false},
		{"DNSReady is not acceptance", func(ls *gatewayapiv1.ListenerSet) {
			ls.Status.Listeners[0].Conditions = []metav1.Condition{{Type: "DNSReady", Status: metav1.ConditionTrue, ObservedGeneration: 2}}
		}, false},
		{"conflicted", func(ls *gatewayapiv1.ListenerSet) { ls.Status.Listeners[0].Conditions[1].Status = metav1.ConditionTrue }, false},
		{"not accepted", func(ls *gatewayapiv1.ListenerSet) {
			ls.Status.Listeners[0].Conditions[0].Status = metav1.ConditionFalse
		}, false},
		{"missing status", func(ls *gatewayapiv1.ListenerSet) { ls.Status.Listeners = nil }, false},
		{"wrong listener", func(ls *gatewayapiv1.ListenerSet) { ls.Status.Listeners[0].Name = "other" }, false},
		{"no hostname", func(ls *gatewayapiv1.ListenerSet) { ls.Spec.Listeners[0].Hostname = nil }, false},
		{"deleting", func(ls *gatewayapiv1.ListenerSet) { ls.DeletionTimestamp = ptr.To(metav1.Now()) }, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			listenerSet := base.DeepCopy()
			testCase.mutate(listenerSet)
			hostnames := ListenerSetDNSHostnames(listenerSet)
			if listenerSet.Spec.ParentRef.Group != nil || listenerSet.Spec.ParentRef.Kind != nil {
				require.Empty(t, IndexListenerSetParent(listenerSet))
			}
			if testCase.eligible {
				require.Equal(t, gatewayapiv1.Hostname("*.example.com"), hostnames["http"])
			} else {
				require.Empty(t, hostnames)
			}
		})
	}
	require.Equal(t, []string{DefaultOperandNamespace + "/gateway"}, IndexListenerSetParent(base))
	base.Spec.ParentRef.Namespace = nil
	require.Equal(t, "tenant/gateway", ListenerSetParent(base).String())
	require.NotEqual(t, NormalizeDNSHostname("*.example.com"), NormalizeDNSHostname("example.com"))
}
