package controller

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const ListenerSetParentGatewayIndex = "spec.parentRef.gateway"

func ListenerSetParent(ls *gatewayapiv1.ListenerSet) types.NamespacedName {
	namespace := ls.Namespace
	if ls.Spec.ParentRef.Namespace != nil {
		namespace = string(*ls.Spec.ParentRef.Namespace)
	}
	return types.NamespacedName{Namespace: namespace, Name: string(ls.Spec.ParentRef.Name)}
}

func IndexListenerSetParent(object client.Object) []string {
	listenerSet := object.(*gatewayapiv1.ListenerSet)
	if !listenerSetTargetsGateway(listenerSet) {
		return nil
	}
	return []string{ListenerSetParent(listenerSet).String()}
}

func listenerSetTargetsGateway(ls *gatewayapiv1.ListenerSet) bool {
	parent := ls.Spec.ParentRef
	return (parent.Group == nil || *parent.Group == gatewayapiv1.Group(gatewayapiv1.GroupName)) && (parent.Kind == nil || *parent.Kind == "Gateway")
}

// NormalizeDNSHostname keeps wildcard and apex names distinct.
func NormalizeDNSHostname(hostname string) string {
	return strings.ToLower(strings.TrimSuffix(hostname, "."))
}

// ListenerSetDNSHostnames trusts only current Istio status, never CIO's DNSReady.
// In particular, a spec edit must not expose DNS using acceptance of the old spec.
func ListenerSetDNSHostnames(ls *gatewayapiv1.ListenerSet) map[gatewayapiv1.SectionName]gatewayapiv1.Hostname {
	hostnames := map[gatewayapiv1.SectionName]gatewayapiv1.Hostname{}
	accepted := meta.FindStatusCondition(ls.Status.Conditions, string(gatewayapiv1.ListenerSetConditionAccepted))
	if !listenerSetTargetsGateway(ls) || !ls.DeletionTimestamp.IsZero() || accepted == nil || accepted.Status != metav1.ConditionTrue || accepted.ObservedGeneration != ls.Generation {
		return hostnames
	}
	for _, listener := range ls.Spec.Listeners {
		if listener.Hostname == nil || *listener.Hostname == "" {
			continue
		}
		for _, listenerStatus := range ls.Status.Listeners {
			if listenerStatus.Name != listener.Name {
				continue
			}
			current := false
			valid := true
			for _, condition := range listenerStatus.Conditions {
				switch condition.Type {
				case string(gatewayapiv1.ListenerEntryConditionAccepted), string(gatewayapiv1.ListenerEntryConditionConflicted):
					current = current || condition.ObservedGeneration == ls.Generation
					if condition.ObservedGeneration != ls.Generation || (condition.Type == "Accepted" && condition.Status == metav1.ConditionFalse) || (condition.Type == "Conflicted" && condition.Status == metav1.ConditionTrue) {
						valid = false
					}
				}
			}
			if current && valid {
				hostnames[listener.Name] = gatewayapiv1.Hostname(NormalizeDNSHostname(string(*listener.Hostname)))
			}
		}
	}
	return hostnames
}
