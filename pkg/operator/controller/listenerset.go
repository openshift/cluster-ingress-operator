package controller

import (
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const ListenerSetParentGatewayIndex = "spec.parentRef.gateway"

// ParentRef namespace overrides the ListenerSet namespace when set.
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

// A listenerSet must target a Gateway for us to process it.  Gateway API doesn't
// enforce this.  For the time being, we defensively ignore a listenerSet that
// targets an explicit Kind that is not Gateway.
func listenerSetTargetsGateway(ls *gatewayapiv1.ListenerSet) bool {
	parent := ls.Spec.ParentRef
	return (parent.Group == nil || *parent.Group == gatewayapiv1.Group(gatewayapiv1.GroupName)) &&
		(parent.Kind == nil || *parent.Kind == "Gateway")
}

// ListenerSetDNSHostnames consults the given ListenerSet's Accepted status, and returns hostnames
// if the listenerSet is Accepted=true, targets a gateway, is not being deleted.  If the listenerSet
// has been changed and the Accepted status is stale, we'll catch up later, because Istio is likely
// to take longer to reconcile and update status than the CIO controller takes to observe the change.
// If we checked for Accepted status to be synced with every ListenerSet update, then every listenerSet
// update could cause all DNSRecords to be deleted and recreated during the reconcile.
func ListenerSetDNSHostnames(ls *gatewayapiv1.ListenerSet) map[gatewayapiv1.SectionName]gatewayapiv1.Hostname {
	hostnames := map[gatewayapiv1.SectionName]gatewayapiv1.Hostname{}
	accepted := meta.FindStatusCondition(ls.Status.Conditions, string(gatewayapiv1.ListenerSetConditionAccepted))
	// No hostnames returned if this listenerSet doesn't target a gateway, is being deleted,
	// or is not Accepted=true.
	if !listenerSetTargetsGateway(ls) || !ls.DeletionTimestamp.IsZero() ||
		accepted == nil || accepted.Status != metav1.ConditionTrue {
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
			conflicted := meta.FindStatusCondition(listenerStatus.Conditions, string(gatewayapiv1.ListenerEntryConditionConflicted))
			// It's ok for this listener to have a stale Conflicted status when we consider ommitting its hostname.
			if conflicted != nil && conflicted.Status == metav1.ConditionTrue {
				continue
			}
			accepted := meta.FindStatusCondition(listenerStatus.Conditions, string(gatewayapiv1.ListenerEntryConditionAccepted))
			// It's ok for this listener to have a stale Accepted status when we collect hostnames.
			if accepted != nil && accepted.Status == metav1.ConditionTrue {
				hostnames[listener.Name] = gatewayapiv1.Hostname(dns.NormalizeHostname(string(*listener.Hostname)))
			}
		}
	}
	return hostnames
}
