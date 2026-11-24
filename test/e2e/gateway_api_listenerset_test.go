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
	gwc, err := createGatewayClass(t, operatorcontroller.OpenShiftDefaultGatewayClassName, operatorcontroller.OpenShiftGatewayClassControllerName)
	require.NoError(t, err)

	prefix := names.SimpleNameGenerator.GenerateName("ls-")
	parentNamespace := operatorcontroller.DefaultOperandNamespace
	firstHostname := prefix + "." + dnsConfig.Spec.BaseDomain
	secondHostname := "second." + firstHostname

	// Set up a listenerSet with parentRef named ls-xyz-one and listener with hostname ls-xyz.basedomain.
	first := makeListenerSet(namespace.Name, parentNamespace, "first", prefix+"-one", firstHostname)
	first.Spec.Listeners = append(first.Spec.Listeners, gatewayapiv1.ListenerEntry{Name: "shared", Hostname: ptr.To(gatewayapiv1.Hostname(firstHostname)), Port: 8080, Protocol: gatewayapiv1.HTTPProtocolType})
	require.NoError(t, kclient.Create(ctx, first))

	// Set the initial status as Accepted=false on the first listenerSet.
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := kclient.Get(ctx, client.ObjectKeyFromObject(first), first); err != nil {
			return err
		}
		meta.SetStatusCondition(&first.Status.Conditions,
			metav1.Condition{Type: "Accepted", Status: metav1.ConditionFalse, Reason: "UnsupportedByController",
				Message: "Previously rejected by CIO", ObservedGeneration: first.Generation})
		return kclient.Status().Update(ctx, first)
	}))

	// Set up two Gateways for testing cross-Gateway listenerSet conflicts.
	var gateways []*gatewayapiv1.Gateway
	for _, suffix := range []string{"-one", "-two"} {
		// Each gateway allows listeners from any namespace and has the required dummy listener.
		gateway := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: parentNamespace, Name: prefix + suffix},
			Spec: gatewayapiv1.GatewaySpec{
				GatewayClassName: gatewayapiv1.ObjectName(gwc.Name),
				AllowedListeners: &gatewayapiv1.AllowedListeners{Namespaces: &gatewayapiv1.ListenerNamespaces{From: ptr.To(gatewayapiv1.NamespacesFromAll)}},
				Listeners:        []gatewayapiv1.Listener{{Name: "dummy", Port: 8088, Protocol: gatewayapiv1.HTTPProtocolType}},
			}}
		require.NoError(t, kclient.Create(ctx, gateway))
		t.Cleanup(func() { require.NoError(t, client.IgnoreNotFound(kclient.Delete(context.Background(), gateway))) })
		gateways = append(gateways, gateway)
	}

	// Set up a second listenerSet with gateway one as the parentRef.
	second := makeListenerSet(namespace.Name, parentNamespace, "second", gateways[0].Name, secondHostname)
	require.NoError(t, kclient.Create(ctx, second))

	// If the cluster has a managedPrivateZone or a managedPublicZone, there is managed DNS.
	managedDNS := isDNSManagementSupported(t)

	// Wait until the first and second listenerSets are accepted.
	waitListenerSet(t, ctx, managedDNS, first, true)
	waitListenerSet(t, ctx, managedDNS, second, true)
	// Wait until DNS is available for the first hostname.
	waitDNS(t, ctx, gateways[0], firstHostname, true)
	// Wait until the first gateway is tagged with DNSReady.
	if managedDNS {
		require.Eventually(t, func() bool {
			if err := kclient.Get(ctx, client.ObjectKeyFromObject(gateways[0]), gateways[0]); err != nil {
				return false
			}
			return meta.IsStatusConditionTrue(gateways[0].Status.Conditions, "DNSReady")
		}, 5*time.Minute, 2*time.Second, "Gateway DNSReady must aggregate ListenerSets without inline hostnames")
	}
	// Validate that 2 ListenerSet entries with same hostname share a DNSRecord.
	var sharedRecords iov1.DNSRecordList
	require.NoError(t, kclient.List(ctx, &sharedRecords, client.InNamespace(parentNamespace),
		client.MatchingLabels{operatorcontroller.GatewayNameLabelKey: gateways[0].Name}))
	require.Len(t, sharedRecords.Items, 2, "two ListenerSet entries sharing a hostname must use one record")

	// Create an HTTPRoute that uses a hostname in the listenerSet on the first gateway.
	route, err := createHttpRoute(t, namespace.Name, "listenerset-route", parentNamespace, firstHostname, "listenerset-backend", gateways[0])
	require.NoError(t, err)
	// Make sure that traffic can be sent to both the first and second hostnames.
	verifyListenerSetTraffic(t, ctx, route, first, gateways[0], firstHostname)
	verifyListenerSetTraffic(t, ctx, route, second, gateways[0], secondHostname)

	// Create a ListenerSet with an intentional conflict on second hostname.
	conflict := makeListenerSet(namespace.Name, parentNamespace, "conflict", gateways[0].Name, secondHostname)
	require.NoError(t, kclient.Create(ctx, conflict))
	// Wait for 3 minutes for Istio to mark it conflicted.
	require.Eventually(t, func() bool {
		if err := kclient.Get(ctx, client.ObjectKeyFromObject(conflict), conflict); err != nil {
			return false
		}
		if len(conflict.Status.Listeners) != len(conflict.Spec.Listeners) {
			return false
		}
		for _, listener := range conflict.Status.Listeners {
			condition := meta.FindStatusCondition(listener.Conditions, "Conflicted")
			if condition == nil || condition.ObservedGeneration != conflict.Generation || condition.Status != metav1.ConditionTrue {
				return false
			}
		}
		return true
	}, 3*time.Minute, time.Second)
	// Ensure no Degraded conditions on operators.
	require.NoError(t, waitForClusterOperatorConditions(t, kclient, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse}))
	require.NoError(t, waitForIngressControllerCondition(t, kclient, 2*time.Minute, client.ObjectKey{Namespace: operatorcontroller.DefaultOperatorNamespace, Name: "default"}, operatorv1.OperatorCondition{Type: "Degraded", Status: operatorv1.ConditionFalse}))
	// Ensure Conflicted status does not report DNSReady.
	require.Nil(t, meta.FindStatusCondition(conflict.Status.Listeners[0].Conditions, operatorv1.DNSReadyIngressConditionType), "conflicted listener must not report DNSReady")
	require.Nil(t, meta.FindStatusCondition(conflict.Status.Conditions, operatorv1.DNSReadyIngressConditionType), "conflicted ListenerSet must not report DNSReady")
	// Ensure the pre-claimed hostname still works for its original purpose.
	waitDNS(t, ctx, gateways[0], secondHostname, true)
	verifyListenerSetTraffic(t, ctx, route, first, gateways[0], firstHostname)
	verifyListenerSetTraffic(t, ctx, route, second, gateways[0], secondHostname)
	require.NoError(t, kclient.Delete(ctx, conflict))

	// Gateway one already owns firstHostname. Gateway two must not be able to
	// claim a wildcard that covers it, and its listener must report that its own
	// DNSRecord is missing rather than adopting Gateway one's record.
	crossGatewayWildcard := makeListenerSet(namespace.Name, parentNamespace, "cross-gateway-wildcard", gateways[1].Name, "*."+firstHostname)
	// The expected reason depends on whether the cluster can manage DNS at all.
	// With no configured zones, status stops at NoDNSZones; otherwise the
	// rejected claim has no DNSRecord and reports RecordNotFound.
	dnsZonesAreUnset := dnsConfig.Spec.PrivateZone == nil && dnsConfig.Spec.PublicZone == nil
	expectedDNSNotReadyReason := "RecordNotFound"
	if dnsZonesAreUnset {
		expectedDNSNotReadyReason = "NoDNSZones"
	}
	require.NoError(t, kclient.Create(ctx, crossGatewayWildcard))
	require.Eventually(t, func() bool {
		if err := kclient.Get(ctx, client.ObjectKeyFromObject(crossGatewayWildcard), crossGatewayWildcard); err != nil {
			return false
		}
		if !meta.IsStatusConditionTrue(crossGatewayWildcard.Status.Conditions, "Accepted") {
			return false
		}
		for _, listener := range crossGatewayWildcard.Status.Listeners {
			if !meta.IsStatusConditionTrue(listener.Conditions, "Accepted") {
				return false
			}

			dnsReady := meta.FindStatusCondition(listener.Conditions, operatorv1.DNSReadyIngressConditionType)
			if dnsReady == nil ||
				dnsReady.Status != metav1.ConditionFalse ||
				dnsReady.Reason != expectedDNSNotReadyReason {
				return false
			}
		}

		return meta.IsStatusConditionFalse(crossGatewayWildcard.Status.Conditions, operatorv1.DNSReadyIngressConditionType)
	}, 3*time.Minute, 2*time.Second,
		"cross-Gateway wildcard conflict did not report DNSReady=False with reason %s: %+v",
		expectedDNSNotReadyReason, crossGatewayWildcard.Status)

	// The losing Gateway must not leave a DNSRecord behind for its wildcard claim.
	waitDNS(t, ctx, gateways[1], "*."+firstHostname, false)

	require.NoError(t, kclient.Delete(ctx, crossGatewayWildcard))
	require.Eventually(t, func() bool {
		return errors.IsNotFound(kclient.Get(ctx, client.ObjectKeyFromObject(crossGatewayWildcard), crossGatewayWildcard))
	}, 2*time.Minute, 2*time.Second, "cross-Gateway wildcard ListenerSet was not deleted")

	// CIO's default wildcard DNSRecord already claims every hostname below
	// apps.<base-domain>. A ListenerSet must not create a more-specific record
	// that would override that claim.
	cioWildcardDNSName := "*.apps." + dnsConfig.Spec.BaseDomain + "."
	var operandRecords iov1.DNSRecordList
	require.NoError(t, kclient.List(ctx, &operandRecords, client.InNamespace(parentNamespace)))
	cioWildcard := false
	for _, record := range operandRecords.Items {
		if record.Spec.DNSName == cioWildcardDNSName && len(record.OwnerReferences) == 1 && record.OwnerReferences[0].Kind == "IngressController" {
			cioWildcard = true
			break
		}
	}
	require.True(t, cioWildcard, "CIO default wildcard DNSRecord %s was not found", cioWildcardDNSName)
	// Request a more-specific hostname beneath CIO's default apps wildcard to cause an intentional conflict.
	cioWildcardHostname := "listenerset-blocked.apps." + dnsConfig.Spec.BaseDomain
	cioWildcardConflict := makeListenerSet(namespace.Name, parentNamespace, "cio-wildcard-conflict", gateways[1].Name, cioWildcardHostname)
	require.NoError(t, kclient.Create(ctx, cioWildcardConflict))
	require.Eventually(t, func() bool {
		if err := kclient.Get(ctx, client.ObjectKeyFromObject(cioWildcardConflict), cioWildcardConflict); err != nil {
			return false
		}
		if !meta.IsStatusConditionTrue(cioWildcardConflict.Status.Conditions, "Accepted") {
			return false
		}
		for _, listener := range cioWildcardConflict.Status.Listeners {
			if !meta.IsStatusConditionTrue(listener.Conditions, "Accepted") {
				return false
			}
			dnsReady := meta.FindStatusCondition(listener.Conditions, operatorv1.DNSReadyIngressConditionType)
			if dnsReady == nil || dnsReady.Status != metav1.ConditionFalse || dnsReady.Reason != expectedDNSNotReadyReason {
				return false
			}
		}
		return meta.IsStatusConditionFalse(cioWildcardConflict.Status.Conditions, operatorv1.DNSReadyIngressConditionType)
	}, 3*time.Minute, 2*time.Second,
		"CIO wildcard conflict did not report DNSReady=False with reason %s: %+v",
		expectedDNSNotReadyReason, cioWildcardConflict.Status)
	// Wait to make sure it doesn't get created.
	waitDNS(t, ctx, gateways[1], cioWildcardHostname, false)
	require.NoError(t, kclient.Delete(ctx, cioWildcardConflict))
	require.Eventually(t, func() bool {
		return errors.IsNotFound(kclient.Get(ctx, client.ObjectKeyFromObject(cioWildcardConflict), cioWildcardConflict))
	}, 2*time.Minute, 2*time.Second, "CIO wildcard conflict ListenerSet was not deleted")

	// Update the Gateway parentRef on the listenerSet.
	updateListenerSet(t, ctx, first, func(spec *gatewayapiv1.ListenerSetSpec) {
		spec.ParentRef.Name = gatewayapiv1.ObjectName(gateways[1].Name)
	})
	waitListenerSet(t, ctx, managedDNS, first, true)
	waitDNS(t, ctx, gateways[0], firstHostname, false)
	waitDNS(t, ctx, gateways[1], firstHostname, true)
	verifyListenerSetTraffic(t, ctx, route, first, gateways[1], firstHostname)
	// Change the hostname on the listener and wait to see the DNS changes.
	renamed := "renamed." + firstHostname
	updateListenerSet(t, ctx, first, func(spec *gatewayapiv1.ListenerSetSpec) {
		for index := range spec.Listeners {
			spec.Listeners[index].Hostname = ptr.To(gatewayapiv1.Hostname(renamed))
		}
	})
	waitListenerSet(t, ctx, managedDNS, first, true)
	waitDNS(t, ctx, gateways[1], firstHostname, false)
	waitDNS(t, ctx, gateways[1], renamed, true)
	verifyListenerSetTraffic(t, ctx, route, first, gateways[1], renamed)
	require.NoError(t, updateGatewaySpecWithRetry(t, client.ObjectKeyFromObject(gateways[1]), 2*time.Minute, func(spec *gatewayapiv1.GatewaySpec) { spec.AllowedListeners = nil }))
	waitListenerSet(t, ctx, managedDNS, first, false)
	waitDNS(t, ctx, gateways[1], renamed, false)
	// Update the AllowedListeners configuration to All namespace.
	require.NoError(t, updateGatewaySpecWithRetry(t, client.ObjectKeyFromObject(gateways[1]), 2*time.Minute, func(spec *gatewayapiv1.GatewaySpec) {
		spec.AllowedListeners = &gatewayapiv1.AllowedListeners{Namespaces: &gatewayapiv1.ListenerNamespaces{From: ptr.To(gatewayapiv1.NamespacesFromAll)}}
	}))
	waitListenerSet(t, ctx, managedDNS, first, true)
	waitDNS(t, ctx, gateways[1], renamed, true)

	// Verify that deleting a ListenerSet removes its DNSRecord, and deleting its parent Gateway removes both
	// the parent’s DNSRecord and the ListenerSet’s DNS-ready status.
	// Delete first listener set and make sure its DNSRecord is removed.
	require.NoError(t, kclient.Delete(ctx, first))
	waitDNS(t, ctx, gateways[1], renamed, false)
	// Delete the parent Gateway of the second listener set and make sure its DNSRecord is removed.
	require.NoError(t, kclient.Delete(ctx, gateways[0]))
	waitDNS(t, ctx, gateways[0], secondHostname, false)
	// Ensure the DNSReady condition gets set to empty on the second ListenerSet.
	require.Eventually(t, func() bool {
		if err := kclient.Get(ctx, client.ObjectKeyFromObject(second), second); err != nil {
			return false
		}
		return meta.FindStatusCondition(second.Status.Conditions, operatorv1.DNSReadyIngressConditionType) == nil
	}, 3*time.Minute, time.Second)
}

// makelistenerset creates a listenerset with one http listener in namespace and parentgatewaynamespace.
func makeListenerSet(namespace, parentGatewayNamespace, name, parent, hostname string) *gatewayapiv1.ListenerSet {
	return &gatewayapiv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: gatewayapiv1.ListenerSetSpec{
		ParentRef: gatewayapiv1.ParentGatewayReference{Name: gatewayapiv1.ObjectName(parent), Namespace: ptr.To(gatewayapiv1.Namespace(parentGatewayNamespace))},
		Listeners: []gatewayapiv1.ListenerEntry{{Name: "http", Hostname: ptr.To(gatewayapiv1.Hostname(hostname)), Port: 80, Protocol: gatewayapiv1.HTTPProtocolType}},
	}}
}

// waitListenerSet waits until the ListenerSet reaches the expected Accepted and DNS status.
func waitListenerSet(t *testing.T, ctx context.Context, managedDNS bool, listenerSet *gatewayapiv1.ListenerSet, accepted bool) {
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
		return len(listenerSet.Status.Listeners) == len(listenerSet.Spec.Listeners) && condition != nil &&
			condition.ObservedGeneration == listenerSet.Generation && (!managedDNS || condition.Status == metav1.ConditionTrue)
	}, 10*time.Minute, 2*time.Second, "ListenerSet %s did not reach expected acceptance/DNS status: %+v", listenerSet.Name, listenerSet.Status)
}

// waitDNS waits until the Gateway's DNSRecord for hostname exists or is absent.
func waitDNS(t *testing.T, ctx context.Context, gateway *gatewayapiv1.Gateway, hostname string, present bool) {
	t.Helper()
	key := operatorcontroller.GatewayDNSRecordName(gateway, hostname+".")
	require.Eventually(t, func() bool {
		var record iov1.DNSRecord
		err := kclient.Get(ctx, key, &record)
		if !present {
			return errors.IsNotFound(err)
		}
		return err == nil && record.Spec.DNSName == hostname+"." && len(record.OwnerReferences) == 1 &&
			record.OwnerReferences[0].Kind == "Service"
	}, 5*time.Minute, 2*time.Second, "DNSRecord %s present=%t", key, present)
}

// updateListenerSet applies change to the ListenerSet spec, retrying on update conflicts.
func updateListenerSet(t *testing.T, ctx context.Context, listenerSet *gatewayapiv1.ListenerSet, change func(*gatewayapiv1.ListenerSetSpec)) {
	t.Helper()
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := kclient.Get(ctx, client.ObjectKeyFromObject(listenerSet), listenerSet); err != nil {
			return err
		}
		change(&listenerSet.Spec)
		return kclient.Update(ctx, listenerSet)
	}))
}

// verifyListenerSetTraffic attaches route to the ListenerSet and verifies that requests reach the Gateway.
func verifyListenerSetTraffic(t *testing.T, ctx context.Context, route *gatewayapiv1.HTTPRoute, listenerSet *gatewayapiv1.ListenerSet, gateway *gatewayapiv1.Gateway, hostname string) {
	t.Helper()
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := kclient.Get(ctx, client.ObjectKeyFromObject(route), route); err != nil {
			return err
		}
		route.Spec.ParentRefs = []gatewayapiv1.ParentReference{{Group: ptr.To(gatewayapiv1.Group("gateway.networking.k8s.io")),
			Kind: ptr.To(gatewayapiv1.Kind("ListenerSet")), Name: gatewayapiv1.ObjectName(listenerSet.Name)}}
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
