package gatewaystatus

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
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

func TestListenerSetStatusAndMetrics(t *testing.T) {
	dnsConflicts.Reset()
	t.Cleanup(dnsConflicts.Reset)
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, gatewayapiv1.AddToScheme(scheme))
	require.NoError(t, configv1.AddToScheme(scheme))
	require.NoError(t, iov1.AddToScheme(scheme))
	parent := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: operatorcontroller.DefaultOperandNamespace}, Spec: gatewayapiv1.GatewaySpec{GatewayClassName: "managed"}}
	class := &gatewayapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "managed"}, Spec: gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName}}
	listenerSet := &gatewayapiv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Name: "listeners", Namespace: "tenant", Generation: 3}, Spec: gatewayapiv1.ListenerSetSpec{
		ParentRef: gatewayapiv1.ParentGatewayReference{Name: "gateway", Namespace: ptr.To(gatewayapiv1.Namespace(parent.Namespace))},
		Listeners: []gatewayapiv1.ListenerEntry{{Name: "good", Hostname: ptr.To(gatewayapiv1.Hostname("example.com"))}, {Name: "bad", Hostname: ptr.To(gatewayapiv1.Hostname("bad.example.com"))}},
	}, Status: gatewayapiv1.ListenerSetStatus{
		Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 3}, {Type: "Programmed", Status: metav1.ConditionTrue, ObservedGeneration: 3}},
		Listeners: []gatewayapiv1.ListenerEntryStatus{
			{Name: "good", Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 3}}},
			{Name: "bad", Conditions: []metav1.Condition{{Type: "Conflicted", Status: metav1.ConditionTrue, ObservedGeneration: 3}}},
		},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(listenerSet).WithObjects(parent, class, listenerSet, &configv1.DNS{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: configv1.DNSSpec{PublicZone: &configv1.DNSZone{ID: "zone"}}}).WithIndex(&gatewayapiv1.ListenerSet{}, operatorcontroller.ListenerSetParentGatewayIndex, operatorcontroller.IndexListenerSetParent).Build()
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	r := &listenerSetReconciler{client: cl, cache: testutil.FakeCache{Informers: &informertest.FakeInformers{Scheme: scheme}, Reader: cl}, modeAccessor: mode}
	request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(listenerSet)}
	reconcileStatus := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, request)
		require.NoError(t, err)
		require.NoError(t, cl.Get(ctx, request.NamespacedName, listenerSet))
	}
	reconcileStatus()
	require.Equal(t, float64(1), promtest.ToFloat64(dnsConflicts.WithLabelValues("tenant", "listeners")))
	require.True(t, meta.IsStatusConditionFalse(listenerSet.Status.Conditions, "DNSReady"))
	require.Equal(t, "RecordNotFound", meta.FindStatusCondition(listenerSet.Status.Listeners[0].Conditions, "DNSReady").Reason)
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Listeners[1].Conditions, "DNSReady"))
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Conditions, "Programmed"))
	require.True(t, meta.IsStatusConditionTrue(listenerSet.Status.Listeners[1].Conditions, "Conflicted"))
	require.Equal(t, []reconcile.Request{request}, r.requestsForParent(ctx, client.ObjectKeyFromObject(parent)))
	before := listenerSet.DeepCopy()
	reconcileStatus()
	require.Equal(t, before.Status, listenerSet.Status)
	listenerSet.Status.Listeners[1].Conditions[0].Status = metav1.ConditionFalse
	require.NoError(t, cl.Status().Update(ctx, listenerSet))
	reconcileStatus()
	require.Equal(t, float64(0), promtest.ToFloat64(dnsConflicts.WithLabelValues("tenant", "listeners")))
	listenerSet.Spec.ParentRef.Kind = ptr.To(gatewayapiv1.Kind("Service"))
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Conditions, "DNSReady"))
	require.Equal(t, 0, promtest.CollectAndCount(dnsConflicts))
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
	require.Equal(t, 0, promtest.CollectAndCount(dnsConflicts))
	listenerSet.Spec.ParentRef.Namespace = ptr.To(gatewayapiv1.Namespace(parent.Namespace))
	require.NoError(t, cl.Update(ctx, listenerSet))
	reconcileStatus()
	require.Equal(t, 1, promtest.CollectAndCount(dnsConflicts))
	require.NoError(t, cl.Delete(ctx, parent))
	reconcileStatus()
	require.Equal(t, 0, promtest.CollectAndCount(dnsConflicts))
	require.Nil(t, meta.FindStatusCondition(listenerSet.Status.Conditions, "DNSReady"))
	require.NoError(t, cl.Delete(ctx, listenerSet))
	_, err := r.Reconcile(ctx, request)
	require.NoError(t, err)
	require.Equal(t, 0, promtest.CollectAndCount(dnsConflicts))
}

func TestDuplicateGatewayDNSRecords(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, iov1.AddToScheme(scheme))
	var records []client.Object
	for _, name := range []string{"one", "two"} {
		records = append(records, &iov1.DNSRecord{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ingress", Labels: map[string]string{operatorcontroller.GatewayNameLabelKey: "gateway"}}, Spec: iov1.DNSRecordSpec{DNSName: "same.example.com."}})
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(records...).Build()
	_, err := dnsRecordsForGateway(context.Background(), cl, client.ObjectKey{Namespace: "ingress", Name: "gateway"})
	require.ErrorContains(t, err, "duplicate DNSRecords")
}
