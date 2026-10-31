//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func testGatewayAPIListenerSets(t *testing.T) {
	ctx := t.Context()
	namespace := createNamespace(t, names.SimpleNameGenerator.GenerateName("listenerset-"))
	class, err := createGatewayClass(t, operatorcontroller.OpenShiftDefaultGatewayClassName, operatorcontroller.OpenShiftGatewayClassControllerName)
	require.NoError(t, err)
	prefix := names.SimpleNameGenerator.GenerateName("ls-")
	parentNamespace := operatorcontroller.DefaultOperandNamespace
	firstHostname := prefix + "." + dnsConfig.Spec.BaseDomain
	secondHostname := "second." + firstHostname
	makeListenerSet := func(name, parent, hostname string) *gatewayapiv1.ListenerSet {
		return &gatewayapiv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace.Name}, Spec: gatewayapiv1.ListenerSetSpec{
			ParentRef: gatewayapiv1.ParentGatewayReference{Name: gatewayapiv1.ObjectName(parent), Namespace: ptr.To(gatewayapiv1.Namespace(parentNamespace))},
			Listeners: []gatewayapiv1.ListenerEntry{{Name: "http", Hostname: ptr.To(gatewayapiv1.Hostname(hostname)), Port: 80, Protocol: gatewayapiv1.HTTPProtocolType}},
		}}
	}
	first := makeListenerSet("first", prefix+"-one", firstHostname)
	first.Spec.Listeners = append(first.Spec.Listeners, gatewayapiv1.ListenerEntry{Name: "shared", Hostname: ptr.To(gatewayapiv1.Hostname(firstHostname)), Port: 8080, Protocol: gatewayapiv1.HTTPProtocolType})
	require.NoError(t, kclient.Create(ctx, first))
	// Seed the status persisted by the old CIO before making the parent available.
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := kclient.Get(ctx, client.ObjectKeyFromObject(first), first); err != nil {
			return err
		}
		meta.SetStatusCondition(&first.Status.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionFalse, Reason: "UnsupportedByController", Message: "Previously rejected by CIO", ObservedGeneration: first.Generation})
		return kclient.Status().Update(ctx, first)
	}))
	var gateways []*gatewayapiv1.Gateway
	for _, suffix := range []string{"-one", "-two"} {
		gateway := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: parentNamespace, Name: prefix + suffix}, Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: gatewayapiv1.ObjectName(class.Name),
			AllowedListeners: &gatewayapiv1.AllowedListeners{Namespaces: &gatewayapiv1.ListenerNamespaces{From: ptr.To(gatewayapiv1.NamespacesFromAll)}},
			Listeners:        []gatewayapiv1.Listener{{Name: "dummy", Port: 8088, Protocol: gatewayapiv1.HTTPProtocolType}},
		}}
		require.NoError(t, kclient.Create(ctx, gateway))
		t.Cleanup(func() { require.NoError(t, client.IgnoreNotFound(kclient.Delete(context.Background(), gateway))) })
		gateways = append(gateways, gateway)
	}
	second := makeListenerSet("second", gateways[0].Name, secondHostname)
	require.NoError(t, kclient.Create(ctx, second))
	managedDNS := isDNSManagementSupported(t)
	waitListenerSet := func(listenerSet *gatewayapiv1.ListenerSet, accepted bool) {
		t.Helper()
		require.Eventually(t, func() bool {
			if err := kclient.Get(ctx, client.ObjectKeyFromObject(listenerSet), listenerSet); err != nil {
				return false
			}
			condition := meta.FindStatusCondition(listenerSet.Status.Conditions, "Accepted")
			if condition == nil || condition.ObservedGeneration != listenerSet.Generation || (condition.Status == metav1.ConditionTrue) != accepted {
				return false
			}
			if !accepted {
				return condition.Status == metav1.ConditionFalse && meta.FindStatusCondition(listenerSet.Status.Conditions, "DNSReady") == nil
			}
			for _, listener := range listenerSet.Status.Listeners {
				if !meta.IsStatusConditionTrue(listener.Conditions, "Accepted") {
					return false
				}
				if managedDNS && !meta.IsStatusConditionTrue(listener.Conditions, "DNSReady") {
					return false
				}
			}
			condition = meta.FindStatusCondition(listenerSet.Status.Conditions, "DNSReady")
			return len(listenerSet.Status.Listeners) == len(listenerSet.Spec.Listeners) && condition != nil && condition.ObservedGeneration == listenerSet.Generation && (!managedDNS || condition.Status == metav1.ConditionTrue)
		}, 10*time.Minute, 2*time.Second, "ListenerSet %s did not reach expected acceptance/DNS status: %+v", listenerSet.Name, listenerSet.Status)
	}
	waitDNS := func(gateway *gatewayapiv1.Gateway, hostname string, present bool) {
		t.Helper()
		key := operatorcontroller.GatewayDNSRecordName(gateway, hostname+".")
		require.Eventually(t, func() bool {
			var record iov1.DNSRecord
			err := kclient.Get(ctx, key, &record)
			if !present {
				return errors.IsNotFound(err)
			}
			return err == nil && record.Spec.DNSName == hostname+"." && len(record.OwnerReferences) == 1 && record.OwnerReferences[0].Kind == "Service"
		}, 5*time.Minute, 2*time.Second, "DNSRecord %s present=%t", key, present)
	}
	updateListenerSet := func(listenerSet *gatewayapiv1.ListenerSet, change func(*gatewayapiv1.ListenerSetSpec)) {
		t.Helper()
		require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := kclient.Get(ctx, client.ObjectKeyFromObject(listenerSet), listenerSet); err != nil {
				return err
			}
			change(&listenerSet.Spec)
			return kclient.Update(ctx, listenerSet)
		}))
	}
	waitListenerSet(first, true)
	waitListenerSet(second, true)
	waitDNS(gateways[0], firstHostname, true)
	if managedDNS {
		require.Eventually(t, func() bool {
			if err := kclient.Get(ctx, client.ObjectKeyFromObject(gateways[0]), gateways[0]); err != nil {
				return false
			}
			return meta.IsStatusConditionTrue(gateways[0].Status.Conditions, "DNSReady")
		}, 5*time.Minute, 2*time.Second, "Gateway DNSReady must aggregate ListenerSets without inline hostnames")
	}
	var sharedRecords iov1.DNSRecordList
	require.NoError(t, kclient.List(ctx, &sharedRecords, client.InNamespace(parentNamespace), client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: gateways[0].Name}))
	require.Len(t, sharedRecords.Items, 2, "two ListenerSet entries sharing a hostname must use one record")
	route, err := createHttpRoute(t, namespace.Name, "listenerset-route", parentNamespace, firstHostname, "listenerset-backend", gateways[0])
	require.NoError(t, err)
	traffic := func(listenerSet *gatewayapiv1.ListenerSet, gateway *gatewayapiv1.Gateway, hostname string) {
		t.Helper()
		require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := kclient.Get(ctx, client.ObjectKeyFromObject(route), route); err != nil {
				return err
			}
			route.Spec.ParentRefs = []gatewayapiv1.ParentReference{{Group: ptr.To(gatewayapiv1.Group("gateway.networking.k8s.io")), Kind: ptr.To(gatewayapiv1.Kind("ListenerSet")), Name: gatewayapiv1.ObjectName(listenerSet.Name)}}
			route.Spec.Hostnames = nil
			return kclient.Update(ctx, route)
		}))
		require.Eventually(t, func() bool {
			if err := kclient.Get(ctx, client.ObjectKeyFromObject(route), route); err != nil {
				return false
			}
			for _, parent := range route.Status.Parents {
				if parent.ParentRef.Name == gatewayapiv1.ObjectName(listenerSet.Name) {
					condition := meta.FindStatusCondition(parent.Conditions, "Accepted")
					return condition != nil && condition.ObservedGeneration == route.Generation && condition.Status == metav1.ConditionTrue
				}
			}
			return false
		}, 3*time.Minute, time.Second)
		current, err := assertGatewaySuccessful(t, gateway.Namespace, gateway.Name)
		require.NoError(t, err)
		// The existing traffic helper derives DNSRecord identity from its first listener.
		current.Spec.Listeners[0].Hostname = ptr.To(gatewayapiv1.Hostname(hostname))
		require.NoError(t, assertHttpRouteConnection(t, hostname, current))
	}
	traffic(first, gateways[0], firstHostname)
	traffic(second, gateways[0], secondHostname)
	conflict := makeListenerSet("conflict", gateways[0].Name, secondHostname)
	require.NoError(t, kclient.Create(ctx, conflict))
	require.Eventually(t, func() bool {
		if err := kclient.Get(ctx, client.ObjectKeyFromObject(conflict), conflict); err != nil {
			return false
		}
		accepted := meta.FindStatusCondition(conflict.Status.Conditions, "Accepted")
		return accepted != nil && accepted.ObservedGeneration == conflict.Generation && (accepted.Status == metav1.ConditionTrue || accepted.Status == metav1.ConditionFalse)
	}, 3*time.Minute, time.Second)
	require.NoError(t, waitForClusterOperatorConditions(t, kclient, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse}))
	require.NoError(t, waitForIngressControllerCondition(t, kclient, 2*time.Minute, client.ObjectKey{Namespace: operatorcontroller.DefaultOperatorNamespace, Name: "default"}, operatorv1.OperatorCondition{Type: "Degraded", Status: operatorv1.ConditionFalse}))
	waitDNS(gateways[0], secondHostname, true)
	traffic(first, gateways[0], firstHostname)
	traffic(second, gateways[0], secondHostname)
	require.NoError(t, kclient.Delete(ctx, conflict))
	updateListenerSet(first, func(spec *gatewayapiv1.ListenerSetSpec) {
		spec.ParentRef.Name = gatewayapiv1.ObjectName(gateways[1].Name)
	})
	waitListenerSet(first, true)
	waitDNS(gateways[0], firstHostname, false)
	waitDNS(gateways[1], firstHostname, true)
	traffic(first, gateways[1], firstHostname)
	renamed := "renamed." + firstHostname
	updateListenerSet(first, func(spec *gatewayapiv1.ListenerSetSpec) {
		for index := range spec.Listeners {
			spec.Listeners[index].Hostname = ptr.To(gatewayapiv1.Hostname(renamed))
		}
	})
	waitListenerSet(first, true)
	waitDNS(gateways[1], firstHostname, false)
	waitDNS(gateways[1], renamed, true)
	traffic(first, gateways[1], renamed)
	require.NoError(t, updateGatewaySpecWithRetry(t, client.ObjectKeyFromObject(gateways[1]), 2*time.Minute, func(spec *gatewayapiv1.GatewaySpec) { spec.AllowedListeners = nil }))
	waitListenerSet(first, false)
	waitDNS(gateways[1], renamed, false)
	require.NoError(t, updateGatewaySpecWithRetry(t, client.ObjectKeyFromObject(gateways[1]), 2*time.Minute, func(spec *gatewayapiv1.GatewaySpec) {
		spec.AllowedListeners = &gatewayapiv1.AllowedListeners{Namespaces: &gatewayapiv1.ListenerNamespaces{From: ptr.To(gatewayapiv1.NamespacesFromAll)}}
	}))
	waitListenerSet(first, true)
	waitDNS(gateways[1], renamed, true)
	require.NoError(t, kclient.Delete(ctx, first))
	waitDNS(gateways[1], renamed, false)
	require.NoError(t, kclient.Delete(ctx, gateways[0]))
	waitDNS(gateways[0], secondHostname, false)
	require.Eventually(t, func() bool {
		if err := kclient.Get(ctx, client.ObjectKeyFromObject(second), second); err != nil {
			return false
		}
		return meta.FindStatusCondition(second.Status.Conditions, operatorv1.DNSReadyIngressConditionType) == nil
	}, 3*time.Minute, time.Second)
}
