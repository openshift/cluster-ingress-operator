package gatewaystatus

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
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
	parent := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: operatorcontroller.DefaultOperandNamespace}, Spec: gatewayapiv1.GatewaySpec{GatewayClassName: "managed"}}
	class := &gatewayapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "managed"}, Spec: gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName}}
	listenerSet := &gatewayapiv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Name: "listeners", Namespace: "tenant", Generation: 3}, Spec: gatewayapiv1.ListenerSetSpec{
		ParentRef: gatewayapiv1.ParentGatewayReference{Name: "gateway", Namespace: ptr.To(gatewayapiv1.Namespace(parent.Namespace))},
		Listeners: []gatewayapiv1.ListenerEntry{{Name: "good", Hostname: ptr.To(gatewayapiv1.Hostname("example.com"))}, {Name: "bad", Hostname: ptr.To(gatewayapiv1.Hostname("bad.example.com"))}},
	}, Status: gatewayapiv1.ListenerSetStatus{
		Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 3}, {Type: "Programmed", Status: metav1.ConditionTrue, ObservedGeneration: 3}},
		Listeners: []gatewayapiv1.ListenerEntryStatus{
			{Name: "good", Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 3}, {Type: "Conflicted", Status: metav1.ConditionTrue, ObservedGeneration: 2}}},
			{Name: "bad", Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionFalse, ObservedGeneration: 3}}},
		},
	}}
	istioStatus := listenerSet.DeepCopy().Status
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(listenerSet, parent).WithObjects(parent, class, listenerSet, &configv1.DNS{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: configv1.DNSSpec{PublicZone: &configv1.DNSZone{ID: "zone"}}}).WithIndex(&gatewayapiv1.ListenerSet{}, operatorcontroller.ListenerSetParentGatewayIndex, operatorcontroller.IndexListenerSetParent).Build()
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	r := &listenerSetReconciler{client: cl, cache: testutil.FakeCache{Informers: &informertest.FakeInformers{Scheme: scheme}, Reader: cl}, modeAccessor: mode}
	gatewayReconciler := &reconciler{client: cl, cache: testutil.FakeCache{Reader: cl}, eventreader: cl, modeAccessor: mode}
	checkGatewayDNS := func(expected metav1.ConditionStatus) {
		t.Helper()
		_, err := gatewayReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(parent)})
		require.NoError(t, err)
		require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(parent), parent))
		require.Equal(t, expected, meta.FindStatusCondition(parent.Status.Conditions, "DNSReady").Status)
	}
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(listenerSet)}
	reconcileStatus := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, request)
		require.NoError(t, err)
		require.NoError(t, cl.Get(ctx, request.NamespacedName, listenerSet))
	}
	reconcileStatus()
	require.True(t, meta.IsStatusConditionFalse(listenerSet.Status.Conditions, "DNSReady"))
	require.Equal(t, "RecordNotFound", meta.FindStatusCondition(listenerSet.Status.Listeners[0].Conditions, "DNSReady").Reason)
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Listeners[1].Conditions, "DNSReady"))
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Conditions, "Programmed"))
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Listeners[0].Conditions, "Conflicted"))
	require.Equal(t, int64(2), meta.FindStatusCondition(listenerSet.Status.Listeners[0].Conditions, "Conflicted").ObservedGeneration)
	checkGatewayDNS(metav1.ConditionFalse)
	require.Equal(t, []reconcile.Request{request}, r.requestsForParent(ctx, client.ObjectKeyFromObject(parent)))
	before := listenerSet.DeepCopy()
	reconcileStatus()
	require.Equal(t, before.Status, listenerSet.Status)
	record := &iov1.DNSRecord{ObjectMeta: metav1.ObjectMeta{Name: "noncanonical-record", Namespace: parent.Namespace, Labels: map[string]string{operatorcontroller.GatewayNameLabelKey: parent.Name}}, Spec: iov1.DNSRecordSpec{DNSName: "EXAMPLE.com.", DNSManagementPolicy: iov1.ManagedDNS, RecordTTL: 30, RecordType: iov1.CNAMERecordType, Targets: []string{"lb.example.com"}}, Status: iov1.DNSRecordStatus{Zones: []iov1.DNSZoneStatus{{DNSZone: configv1.DNSZone{ID: "zone"}, Conditions: []iov1.DNSZoneCondition{{Type: "Published", Status: "True"}}}}}}
	require.NoError(t, cl.Create(ctx, record))
	reconcileStatus()
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Listeners[0].Conditions, "DNSReady"), "status lookup must use normalized DNSName, not the object's name")
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Conditions, "DNSReady"))
	require.Equal(t, istioStatus.Conditions, listenerSet.Status.Conditions[:len(istioStatus.Conditions)])
	for index, listener := range istioStatus.Listeners {
		require.Equal(t, listener.Conditions, listenerSet.Status.Listeners[index].Conditions[:len(listener.Conditions)])
	}
	checkGatewayDNS(metav1.ConditionTrue)
	listenerSet.Spec.ParentRef.Kind = ptr.To(gatewayapiv1.Kind("Service"))
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Conditions, "DNSReady"))
	listenerSet.Spec.ParentRef.Kind = nil
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()
	listenerSet.Spec.Listeners[0].Hostname = nil
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Listeners[0].Conditions, "DNSReady"))
	listenerSet.Spec.Listeners[0].Hostname = ptr.To(gatewayapiv1.Hostname("example.com"))
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()
	listenerSet.Spec.ParentRef.Namespace = ptr.To(gatewayapiv1.Namespace("unmanaged"))
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Conditions, "DNSReady"))
	listenerSet.Spec.ParentRef.Namespace = ptr.To(gatewayapiv1.Namespace(parent.Namespace))
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()
	require.NoError(t, cl.Delete(ctx, parent))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Conditions, "DNSReady"))
	require.NoError(t, cl.Delete(ctx, listenerSet))
	_, err := r.Reconcile(ctx, request)
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
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(gateway).WithObjects(class, gateway, &configv1.DNS{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}).
		WithIndex(&gatewayapiv1.ListenerSet{}, operatorcontroller.ListenerSetParentGatewayIndex, operatorcontroller.IndexListenerSetParent).
		WithIndex(&corev1.Event{}, "involvedObject.apiVersion", func(client.Object) []string { return nil }).
		WithIndex(&corev1.Event{}, "involvedObject.kind", func(client.Object) []string { return nil }).
		WithIndex(&corev1.Event{}, "involvedObject.uid", func(client.Object) []string { return nil }).
		WithIndex(&corev1.Event{}, "source", func(client.Object) []string { return nil }).Build()
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	r := &reconciler{client: cl, cache: testutil.FakeCache{Reader: cl}, eventreader: cl, modeAccessor: mode}
	before := gateway.DeepCopy()
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(gateway), before))
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(gateway)})
	require.NoError(t, err)
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(gateway), gateway))
	require.Equal(t, before, gateway, "foreign Gateway must not be patched")
	listenerSet := &gatewayapiv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant"}, Spec: gatewayapiv1.ListenerSetSpec{ParentRef: gatewayapiv1.ParentGatewayReference{Name: "foreign", Namespace: ptr.To(gatewayapiv1.Namespace(gateway.Namespace))}}}
	require.Empty(t, r.requestsForListenerSet(context.Background(), listenerSet))
	class.Spec.ControllerName = operatorcontroller.OpenShiftGatewayClassControllerName
	require.NoError(t, cl.Update(context.Background(), class))
	require.Equal(t, []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(gateway)}}, r.requestsForListenerSet(context.Background(), listenerSet))
}
