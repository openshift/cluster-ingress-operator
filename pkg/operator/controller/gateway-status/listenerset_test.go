package gatewaystatus

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestListenerSetStatus(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, gatewayapiv1.AddToScheme(scheme))
	require.NoError(t, configv1.AddToScheme(scheme))
	require.NoError(t, iov1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up the parent Gateway, GatewayClass, and a ListenerSet for testing.
	gw := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: operatorcontroller.DefaultOperandNamespace},
		Spec: gatewayapiv1.GatewaySpec{GatewayClassName: "managed"}}
	gwc := &gatewayapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "managed"},
		Spec: gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName}}
	listenerSet := &gatewayapiv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Name: "listeners", Namespace: "tenant", Generation: 3},
		Spec: gatewayapiv1.ListenerSetSpec{
			ParentRef: gatewayapiv1.ParentGatewayReference{Name: "gateway", Namespace: ptr.To(gatewayapiv1.Namespace(gw.Namespace))},
			Listeners: []gatewayapiv1.ListenerEntry{{Name: "good", Hostname: ptr.To(gatewayapiv1.Hostname("example.com"))}, {Name: "bad", Hostname: ptr.To(gatewayapiv1.Hostname("bad.example.com"))}},
		},
		Status: gatewayapiv1.ListenerSetStatus{
			// The Accepted condition is one generation behind the spec. CIO still
			// trusts it while waiting for Istio to update the current generation.
			Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 2}, {Type: "Programmed", Status: metav1.ConditionTrue, ObservedGeneration: 3}},
			Listeners: []gatewayapiv1.ListenerEntryStatus{
				{Name: "good", Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 2}}},
				{Name: "bad", Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionFalse, ObservedGeneration: 2}}},
			},
		}}

	// Set up a DNSRecord that already belongs to another service.
	alreadyClaimed := &iov1.DNSRecord{ObjectMeta: metav1.ObjectMeta{Name: "preclaiming-gateway-record", Namespace: gw.Namespace,
		Labels:          map[string]string{operatorcontroller.GatewayNameLabelKey: "preclaiming-gateway"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: "preclaiming-gateway-service", UID: "preclaiming-gateway-service-uid"}}},
		Spec: iov1.DNSRecordSpec{DNSName: "example.com.", DNSManagementPolicy: iov1.ManagedDNS, RecordTTL: 30, RecordType: iov1.CNAMERecordType}}

	// Set up test environment.
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(listenerSet, gw).WithObjects(gw, gwc, listenerSet, alreadyClaimed, &configv1.DNS{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: configv1.DNSSpec{PublicZone: &configv1.DNSZone{ID: "zone"}}}).WithIndex(&gatewayapiv1.ListenerSet{}, operatorcontroller.ListenerSetParentGatewayIndex, operatorcontroller.IndexListenerSetParent).Build()
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	r := &listenerSetReconciler{client: cl, cache: testutil.FakeCache{Informers: &informertest.FakeInformers{Scheme: scheme}, Reader: cl}, modeAccessor: mode}
	gatewayReconciler := &reconciler{client: cl, cache: testutil.FakeCache{Reader: cl}, eventreader: cl, modeAccessor: mode}

	// Test helper to validate that the expected DNSReady condition exists.
	checkGatewayDNS := func(expected metav1.ConditionStatus) {
		t.Helper()
		_, err := gatewayReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(gw)})
		require.NoError(t, err)
		require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(gw), gw))
		require.Equal(t, expected, meta.FindStatusCondition(gw.Status.Conditions, "DNSReady").Status)
	}

	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(listenerSet)}
	reconcileStatus := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, request)
		require.NoError(t, err)
		require.NoError(t, cl.Get(ctx, request.NamespacedName, listenerSet))
	}

	// A claim owned by the preclaiming Gateway must not satisfy this Gateway's
	// listener. Stale Accepted conditions still identify desired hostnames.
	reconcileStatus()
	require.True(t, meta.IsStatusConditionFalse(listenerSet.Status.Conditions, operatorv1.DNSReadyIngressConditionType))
	dnsReady := meta.FindStatusCondition(listenerSet.Status.Listeners[0].Conditions, operatorv1.DNSReadyIngressConditionType)
	require.NotNil(t, dnsReady)
	require.Equal(t, metav1.ConditionFalse, dnsReady.Status)
	require.Equal(t, "RecordNotFound", dnsReady.Reason)
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Listeners[1].Conditions, operatorv1.DNSReadyIngressConditionType))
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Conditions, "Programmed"))
	checkGatewayDNS(metav1.ConditionFalse)
	require.Equal(t, []reconcile.Request{request}, r.requestsForParent(ctx, client.ObjectKeyFromObject(gw)))

	// Reconciling unchanged status verifies that status updates are idempotent.
	before := listenerSet.DeepCopy()
	reconcileStatus()
	require.Equal(t, before.Status, listenerSet.Status)
	record := &iov1.DNSRecord{ObjectMeta: metav1.ObjectMeta{Name: "noncanonical-record", Namespace: gw.Namespace, Labels: map[string]string{operatorcontroller.GatewayNameLabelKey: gw.Name}}, Spec: iov1.DNSRecordSpec{DNSName: "EXAMPLE.com.", DNSManagementPolicy: iov1.ManagedDNS, RecordTTL: 30, RecordType: iov1.CNAMERecordType, Targets: []string{"lb.example.com"}}, Status: iov1.DNSRecordStatus{Zones: []iov1.DNSZoneStatus{{DNSZone: configv1.DNSZone{ID: "zone"}, Conditions: []iov1.DNSZoneCondition{{Type: "Published", Status: "True"}}}}}}
	require.NoError(t, cl.Create(ctx, record))

	// This reconcile verifies that DNSName lookup normalizes capitalization and
	// the trailing dot, independently of the DNSRecord object's name.
	reconcileStatus()
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Listeners[0].Conditions, operatorv1.DNSReadyIngressConditionType), "status lookup must use normalized DNSName, not the object's name")
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Conditions, "DNSReady"))
	checkGatewayDNS(metav1.ConditionTrue)

	// A Conflicted=True condition blocks DNS even when it is from an older
	// generation. This avoids retaining a hostname after Istio reports a conflict.
	listenerSet.Status.Listeners[0].Conditions = append(listenerSet.Status.Listeners[0].Conditions, metav1.Condition{Type: "Conflicted", Status: metav1.ConditionTrue, ObservedGeneration: 1})
	require.NoError(t, cl.Status().Update(ctx, listenerSet))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Listeners[0].Conditions, operatorv1.DNSReadyIngressConditionType), "stale conflicts must remove listener DNS status")
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Conditions, operatorv1.DNSReadyIngressConditionType), "stale conflicts must remove aggregate DNS status")
	// Refresh the Gateway aggregate after ListenerSet DNS status was removed.
	_, err := gatewayReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(gw)})
	require.NoError(t, err)
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(gw), gw))
	require.Nil(t, meta.FindStatusCondition(gw.Status.Conditions, operatorv1.DNSReadyIngressConditionType), "stale conflicts must remove Gateway DNS status")

	// A stale Conflicted=False condition does not block DNS.
	listenerSet.Status.Listeners[0].Conditions[1].Status = metav1.ConditionFalse
	require.NoError(t, cl.Status().Update(ctx, listenerSet))
	reconcileStatus()
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Listeners[0].Conditions, operatorv1.DNSReadyIngressConditionType))
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Conditions, operatorv1.DNSReadyIngressConditionType))

	// A listener Accepted=False condition blocks DNS even when it is stale.
	listenerSet.Status.Listeners[0].Conditions[0].Status = metav1.ConditionFalse
	require.NoError(t, cl.Status().Update(ctx, listenerSet))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Listeners[0].Conditions, operatorv1.DNSReadyIngressConditionType))
	listenerSet.Status.Listeners[0].Conditions[0].Status = metav1.ConditionTrue
	require.NoError(t, cl.Status().Update(ctx, listenerSet))
	reconcileStatus()
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Listeners[0].Conditions, operatorv1.DNSReadyIngressConditionType))

	// A ListenerSet Accepted=False condition blocks DNS even when it is stale.
	listenerSet.Status.Conditions[0].Status = metav1.ConditionFalse
	require.NoError(t, cl.Status().Update(ctx, listenerSet))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Conditions, operatorv1.DNSReadyIngressConditionType))
	listenerSet.Status.Conditions[0].Status = metav1.ConditionTrue
	require.NoError(t, cl.Status().Update(ctx, listenerSet))
	reconcileStatus()
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Conditions, operatorv1.DNSReadyIngressConditionType))

	listenerSet.Spec.ParentRef.Kind = ptr.To(gatewayapiv1.Kind("Service"))
	require.NoError(t, cl.Update(ctx, listenerSet))
	// A non-Gateway parent removes aggregate DNSReady status.
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Conditions, "DNSReady"))
	listenerSet.Spec.ParentRef.Kind = nil
	require.NoError(t, cl.Update(ctx, listenerSet))

	// Restoring the Gateway parent returns the ListenerSet to Gateway handling.
	reconcileStatus()

	// A listener without a hostname loses listener-level DNSReady status.
	listenerSet.Spec.Listeners[0].Hostname = nil
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Listeners[0].Conditions, "DNSReady"))

	// Restoring the hostname makes the listener eligible for DNS status again.
	listenerSet.Spec.Listeners[0].Hostname = ptr.To(gatewayapiv1.Hostname("example.com"))
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()

	// A parent outside the managed namespace removes aggregate DNSReady status.
	listenerSet.Spec.ParentRef.Namespace = ptr.To(gatewayapiv1.Namespace("unmanaged"))
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Conditions, "DNSReady"))

	// Restoring the managed parent namespace returns the ListenerSet to Gateway handling.
	listenerSet.Spec.ParentRef.Namespace = ptr.To(gatewayapiv1.Namespace(gw.Namespace))
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()

	// An absent parent removes aggregate DNSReady status.
	require.NoError(t, cl.Delete(ctx, gw))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Conditions, "DNSReady"))

	// Reconciling a deleted ListenerSet succeeds as a no-op.
	require.NoError(t, cl.Delete(ctx, listenerSet))
	_, err = r.Reconcile(ctx, request)
	require.NoError(t, err)
}

func TestForeignGatewayStatusIsUntouched(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, gatewayapiv1.AddToScheme(scheme))
	require.NoError(t, configv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, iov1.AddToScheme(scheme))
	class := &gatewayapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "foreign"}, Spec: gatewayapiv1.GatewayClassSpec{ControllerName: "example.com/other-controller"}}
	gateway := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: operatorcontroller.DefaultOperandNamespace}, Spec: gatewayapiv1.GatewaySpec{GatewayClassName: "foreign"}}

	// Build a client with the indices the reconciler uses to look up Gateways,
	// ListenerSets, and events. The GatewayClass is initially owned by another controller.
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(gateway).WithObjects(class, gateway, &configv1.DNS{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}).
		WithIndex(&gatewayapiv1.ListenerSet{}, operatorcontroller.ListenerSetParentGatewayIndex, operatorcontroller.IndexListenerSetParent).
		WithIndex(&corev1.Event{}, "involvedObject.apiVersion", func(client.Object) []string { return nil }).
		WithIndex(&corev1.Event{}, "involvedObject.kind", func(client.Object) []string { return nil }).
		WithIndex(&corev1.Event{}, "involvedObject.uid", func(client.Object) []string { return nil }).
		WithIndex(&corev1.Event{}, "source", func(client.Object) []string { return nil }).Build()
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	r := &reconciler{client: cl, cache: testutil.FakeCache{Reader: cl}, eventreader: cl, modeAccessor: mode}

	// Capture the Gateway before reconciliation to detect any unintended status mutation.
	before := gateway.DeepCopy()
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(gateway), before))

	// Reconciling a Gateway whose class belongs to another controller must be a no-op.
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(gateway)})
	require.NoError(t, err)
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(gateway), gateway))
	require.Equal(t, before, gateway, "foreign Gateway must not be patched")

	// A ListenerSet attached to that Gateway must not enqueue reconciliation.
	listenerSet := &gatewayapiv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant"}, Spec: gatewayapiv1.ListenerSetSpec{ParentRef: gatewayapiv1.ParentGatewayReference{Name: "foreign", Namespace: ptr.To(gatewayapiv1.Namespace(gateway.Namespace))}}}
	require.Empty(t, r.requestsForListenerSet(context.Background(), listenerSet))

	// Once the class is owned by this controller, the ListenerSet must enqueue
	// its parent Gateway for reconciliation.
	class.Spec.ControllerName = operatorcontroller.OpenShiftGatewayClassControllerName
	require.NoError(t, cl.Update(context.Background(), class))
	require.Equal(t, []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(gateway)}}, r.requestsForListenerSet(context.Background(), listenerSet))
}
