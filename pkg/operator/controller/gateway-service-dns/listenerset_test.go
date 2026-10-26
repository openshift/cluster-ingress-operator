package gateway_service_dns

import (
	"context"
	"errors"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type listenerSetReader struct {
	client.Reader
	calls, failOnCall int
}

func (reader *listenerSetReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	reader.calls++
	if reader.calls == reader.failOnCall {
		return errors.New("ListenerSet list failed")
	}
	return reader.Reader.List(ctx, list, opts...)
}

func TestListenerSetDNSLifecycle(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{gatewayapiv1.AddToScheme, configv1.AddToScheme, corev1.AddToScheme, iov1.AddToScheme} {
		require.NoError(t, add(scheme))
	}
	parent := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: operatorcontroller.DefaultOperandNamespace, Name: "gateway"}, Spec: gatewayapiv1.GatewaySpec{Listeners: []gatewayapiv1.Listener{{Name: "shared", Hostname: ptr.To(gatewayapiv1.Hostname("shared.example.com"))}}}}
	parent.Spec.GatewayClassName = "managed"
	class := &gatewayapiv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "managed"}, Spec: gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: parent.Namespace, Name: "service", UID: "service-uid", Labels: map[string]string{operatorcontroller.GatewayNameLabelKey: parent.Name}}, Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}}}}}
	listenerSet := &gatewayapiv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "listeners", Generation: 2}, Spec: gatewayapiv1.ListenerSetSpec{ParentRef: gatewayapiv1.ParentGatewayReference{Name: "gateway", Namespace: ptr.To(gatewayapiv1.Namespace(parent.Namespace))}}, Status: gatewayapiv1.ListenerSetStatus{Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 2}}}}
	for index, hostname := range []string{"shared.example.com", "unique.example.com", "*.example.com", "example.com"} {
		name := gatewayapiv1.SectionName([]string{"shared", "unique", "wildcard", "apex"}[index])
		listenerSet.Spec.Listeners = append(listenerSet.Spec.Listeners, gatewayapiv1.ListenerEntry{Name: name, Hostname: ptr.To(gatewayapiv1.Hostname(hostname))})
		listenerSet.Status.Listeners = append(listenerSet.Status.Listeners, gatewayapiv1.ListenerEntryStatus{Name: name, Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue, ObservedGeneration: 2}}})
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(parent, class, service, listenerSet,
		&configv1.DNS{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}},
		&configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType}}}).
		WithIndex(&gatewayapiv1.ListenerSet{}, operatorcontroller.ListenerSetParentGatewayIndex, operatorcontroller.IndexListenerSetParent).Build()
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	r := &reconciler{config: Config{OperandNamespace: parent.Namespace}, client: cl, cache: testutil.FakeCache{Informers: &informertest.FakeInformers{Scheme: scheme}, Reader: cl}, modeAccessor: mode}
	r.listenerSetCache = r.cache
	backing := make([]gatewayapiv1.Listener, 4)
	copy(backing, parent.Spec.Listeners)
	parent.Spec.Listeners = backing[:1]
	before := parent.DeepCopy()
	beforeBacking := append([]gatewayapiv1.Listener(nil), backing...)
	listeners, err := r.gatewayListeners(ctx, parent)
	require.NoError(t, err)
	require.Len(t, listeners, 5)
	require.Equal(t, before, parent)
	require.Equal(t, beforeBacking, backing, "aggregation must not overwrite cached slice capacity")
	for _, scenario := range []struct {
		name   string
		change func(*gatewayapiv1.Gateway)
		want   bool
	}{
		{"same-named inline hostname", func(gateway *gatewayapiv1.Gateway) {
			gateway.Spec.Listeners[0].Hostname = ptr.To(gatewayapiv1.Hostname("different.example.com"))
		}, true},
		{"listener rename", func(gateway *gatewayapiv1.Gateway) { gateway.Spec.Listeners[0].Name = "renamed" }, true},
		{"normalized hostname", func(gateway *gatewayapiv1.Gateway) {
			gateway.Spec.Listeners[0].Hostname = ptr.To(gatewayapiv1.Hostname("SHARED.example.com."))
		}, false},
		{"class only", func(gateway *gatewayapiv1.Gateway) { gateway.Spec.GatewayClassName = "foreign" }, false},
		{"authorization only", func(gateway *gatewayapiv1.Gateway) { gateway.Spec.AllowedListeners = &gatewayapiv1.AllowedListeners{} }, false},
		{"status only", func(gateway *gatewayapiv1.Gateway) {
			gateway.Status.Conditions = []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue}}
		}, false},
		{"deletion timestamp only", func(gateway *gatewayapiv1.Gateway) { gateway.DeletionTimestamp = ptr.To(metav1.Now()) }, false},
		{"port only", func(gateway *gatewayapiv1.Gateway) { gateway.Spec.Listeners[0].Port = 8080 }, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			updated := parent.DeepCopy()
			scenario.change(updated)
			reader := &listenerSetReader{Reader: cl}
			r.listenerSetCache = testutil.FakeCache{Reader: reader}
			require.Equal(t, scenario.want, r.gatewayListenersChanged(event.UpdateEvent{ObjectOld: parent, ObjectNew: updated}))
			require.Equal(t, 2, reader.calls, "aggregate both old and new specs")
			require.Equal(t, scenario.want, r.gatewayListenersChanged(event.UpdateEvent{ObjectOld: updated, ObjectNew: parent}))
			require.Equal(t, before, parent)
			require.Equal(t, beforeBacking, backing)
		})
	}
	for _, failOnCall := range []int{1, 2} {
		r.listenerSetCache = testutil.FakeCache{Reader: &listenerSetReader{Reader: cl, failOnCall: failOnCall}}
		require.True(t, r.gatewayListenersChanged(event.UpdateEvent{ObjectOld: parent, ObjectNew: parent}), "list error must enqueue")
	}
	r.listenerSetCache = r.cache
	var previous *gatewayapiv1.ListenerSet
	check := func(expected int) {
		t.Helper()
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		defer queue.ShutDown()
		mapper := handler.EnqueueRequestsFromMapFunc(r.requestsForListenerSet)
		var current gatewayapiv1.ListenerSet
		if cl.Get(ctx, client.ObjectKeyFromObject(listenerSet), &current) == nil {
			if previous == nil {
				mapper.Create(ctx, event.CreateEvent{Object: &current}, queue)
			} else {
				mapper.Update(ctx, event.UpdateEvent{ObjectOld: previous, ObjectNew: &current}, queue)
			}
			previous = current.DeepCopy()
		} else {
			mapper.Delete(ctx, event.DeleteEvent{Object: previous}, queue)
		}
		require.Equal(t, 1, queue.Len())
		request, _ := queue.Get()
		require.Equal(t, client.ObjectKeyFromObject(service), request.NamespacedName)
		_, err := r.Reconcile(ctx, request)
		queue.Done(request)
		require.NoError(t, err)
		var records iov1.DNSRecordList
		require.NoError(t, cl.List(ctx, &records))
		for index := range records.Items {
			if !records.Items[index].DeletionTimestamp.IsZero() {
				records.Items[index].Finalizers = nil
				require.NoError(t, cl.Update(ctx, &records.Items[index]))
			}
		}
		require.NoError(t, cl.List(ctx, &records))
		require.Len(t, records.Items, expected)
		for _, record := range records.Items {
			require.Equal(t, parent.Namespace, record.Namespace)
			require.Equal(t, service.UID, record.OwnerReferences[0].UID)
			require.Equal(t, []string{"lb.example.com"}, record.Spec.Targets)
		}
	}
	check(4)
	check(4)
	second := listenerSet.DeepCopy()
	second.Name = "second"
	second.ResourceVersion = ""
	second.Spec.Listeners = second.Spec.Listeners[:1]
	second.Spec.Listeners[0].Hostname = ptr.To(gatewayapiv1.Hostname("second.example.com"))
	second.Status.Listeners = second.Status.Listeners[:1]
	require.NoError(t, cl.Create(ctx, second))
	check(5)
	require.NoError(t, cl.Delete(ctx, second))
	check(4)
	var recordsBefore iov1.DNSRecordList
	require.NoError(t, cl.List(ctx, &recordsBefore))
	r.listenerSetCache = testutil.FakeCache{Reader: &listenerSetReader{Reader: cl, failOnCall: 1}}
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(service)})
	require.ErrorContains(t, err, "ListenerSet list failed")
	var recordsAfter iov1.DNSRecordList
	require.NoError(t, cl.List(ctx, &recordsAfter))
	require.Equal(t, recordsBefore.Items, recordsAfter.Items, "list failure must not update or clean up DNS")
	r.listenerSetCache = r.cache
	listenerSet.Status.Listeners[0].Conditions = append(listenerSet.Status.Listeners[0].Conditions, metav1.Condition{Type: "Conflicted", Status: metav1.ConditionTrue, ObservedGeneration: 2})
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(4)
	listenerSet.Status.Listeners[1].Conditions = append(listenerSet.Status.Listeners[1].Conditions, metav1.Condition{Type: "Conflicted", Status: metav1.ConditionTrue, ObservedGeneration: 2})
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(3)
	listenerSet.Status.Conditions[0].Status = metav1.ConditionFalse
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(1)
	listenerSet.Status.Conditions[0].Status = metav1.ConditionTrue
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(3)
	listenerSet.Spec.Listeners[2].Hostname = ptr.To(gatewayapiv1.Hostname("changed.example.com"))
	listenerSet.Generation++
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(1)
	listenerSet.Status.Conditions[0].ObservedGeneration = listenerSet.Generation
	for index := range listenerSet.Status.Listeners {
		for conditionIndex := range listenerSet.Status.Listeners[index].Conditions {
			listenerSet.Status.Listeners[index].Conditions[conditionIndex].ObservedGeneration = listenerSet.Generation
		}
	}
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(3)
	listenerSet.Spec.ParentRef.Name = "other-gateway"
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(1)
	listenerSet.Spec.ParentRef.Name = gatewayapiv1.ObjectName(parent.Name)
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(3)
	require.NoError(t, cl.Delete(ctx, listenerSet))
	check(1)
	require.NoError(t, cl.Delete(ctx, parent))
	check(0)
}

func TestDuplicateDNSRecordCreation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, iov1.AddToScheme(scheme))
	parent := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "ingress", Name: "gateway"}}
	existing := &iov1.DNSRecord{ObjectMeta: metav1.ObjectMeta{Namespace: parent.Namespace, Name: "unexpected-name", Labels: map[string]string{operatorcontroller.GatewayNameLabelKey: parent.Name}}, Spec: iov1.DNSRecordSpec{DNSName: "same.example.com."}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	r := &reconciler{client: cl}
	errors := r.ensureDNSRecordsForGateway(context.Background(), parent, &corev1.Service{}, []string{"same.example.com."}, nil, nil)
	require.Len(t, errors, 1)
	require.ErrorContains(t, errors[0], "duplicate DNSRecord")
	require.NotContains(t, errors[0].Error(), "same.example.com")
	require.NotContains(t, errors[0].Error(), existing.Name)
	var records iov1.DNSRecordList
	require.NoError(t, cl.List(context.Background(), &records))
	require.Len(t, records.Items, 1, "must not create another record for an already represented hostname")
}

func TestListenerSetReparentEnqueuesBothServices(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	var services []client.Object
	for _, name := range []string{"old", "new"} {
		services = append(services, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ingress", Labels: map[string]string{operatorcontroller.GatewayNameLabelKey: name}}})
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(services...).Build()
	r := &reconciler{config: Config{OperandNamespace: "ingress"}, cache: testutil.FakeCache{Informers: &informertest.FakeInformers{Scheme: scheme}, Reader: cl}}
	old := &gatewayapiv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant"}, Spec: gatewayapiv1.ListenerSetSpec{ParentRef: gatewayapiv1.ParentGatewayReference{Name: "old", Namespace: ptr.To(gatewayapiv1.Namespace("ingress"))}}}
	updated := old.DeepCopy()
	updated.Spec.ParentRef.Name = "new"
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()
	handler.EnqueueRequestsFromMapFunc(r.requestsForListenerSet).Update(context.Background(), event.UpdateEvent{ObjectOld: old, ObjectNew: updated}, queue)
	require.Equal(t, 2, queue.Len())
	var actual []string
	for queue.Len() > 0 {
		request, _ := queue.Get()
		actual = append(actual, request.Name)
		queue.Done(request)
	}
	require.ElementsMatch(t, []string{"old", "new"}, actual)
	updated.Spec.ParentRef.Namespace = ptr.To(gatewayapiv1.Namespace("not-operand"))
	require.Empty(t, r.requestsForListenerSet(context.Background(), updated))
}
