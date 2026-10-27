package gateway_service_dns

import (
	"context"
	"fmt"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestListenerSetDNSLifecycle(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{gatewayapiv1.AddToScheme, configv1.AddToScheme, corev1.AddToScheme, iov1.AddToScheme} {
		require.NoError(t, add(scheme))
	}
	parent := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: operatorcontroller.DefaultOperandNamespace, Name: "gateway"}, Spec: gatewayapiv1.GatewaySpec{Listeners: []gatewayapiv1.Listener{{Name: "inline", Hostname: ptr.To(gatewayapiv1.Hostname("shared.example.com"))}}}}
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
	t.Run("Gateway watch updates", func(t *testing.T) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		sibling := listenerSet.DeepCopy()
		sibling.Name = "sibling"
		sibling.ResourceVersion = ""
		sibling.Spec.Listeners = []gatewayapiv1.ListenerEntry{{Name: "unique", Hostname: ptr.To(gatewayapiv1.Hostname("sibling.example.com"))}}
		sibling.Status = gatewayapiv1.ListenerSetStatus{}
		require.NoError(t, cl.Create(ctx, sibling))
		defer func() { require.NoError(t, cl.Delete(ctx, sibling)) }()
		listCalls, failOnCall := 0, 0
		r.listenerSetCache = testutil.FakeCache{Reader: interceptor.NewClient(cl, interceptor.Funcs{List: func(ctx context.Context, underlying client.WithWatch, objects client.ObjectList, options ...client.ListOption) error {
			listCalls++
			listOptions := &client.ListOptions{}
			listOptions.ApplyOptions(options)
			require.Empty(t, listOptions.Namespace)
			require.Equal(t, operatorcontroller.ListenerSetParentGatewayIndex+"="+client.ObjectKeyFromObject(parent).String(), listOptions.FieldSelector.String())
			if listCalls == failOnCall {
				return fmt.Errorf("indexed ListenerSet lookup failed")
			}
			return underlying.List(ctx, objects, options...)
		}})}
		defer func() { r.listenerSetCache = r.cache }()
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		defer queue.ShutDown()
		watch := r.gatewaySource()
		require.NoError(t, watch.Start(ctx, queue))
		require.NoError(t, watch.WaitForSync(ctx))
		informer, err := r.cache.(testutil.FakeCache).Informers.(*informertest.FakeInformers).FakeInformerFor(ctx, parent)
		require.NoError(t, err)
		for _, scenario := range []struct {
			name   string
			mutate func(*gatewayapiv1.Gateway, *gatewayapiv1.Gateway)
			queued bool
			fail   int
		}{
			{"unchanged", func(old, updated *gatewayapiv1.Gateway) {}, false, 0},
			{"class only", func(old, updated *gatewayapiv1.Gateway) { updated.Spec.GatewayClassName = "foreign" }, false, 0},
			{"allowed listeners only", func(old, updated *gatewayapiv1.Gateway) {
				updated.Spec.AllowedListeners = &gatewayapiv1.AllowedListeners{}
			}, false, 0},
			{"status only", func(old, updated *gatewayapiv1.Gateway) {
				updated.Status.Conditions = []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionFalse}}
			}, false, 0},
			{"deletion only", func(old, updated *gatewayapiv1.Gateway) { updated.DeletionTimestamp = ptr.To(metav1.Now()) }, false, 0},
			{"same name distinct hostnames", func(old, updated *gatewayapiv1.Gateway) {
				updated.Spec.Listeners[0].Hostname = ptr.To(gatewayapiv1.Hostname("changed.example.com"))
			}, true, 0},
			{"rename", func(old, updated *gatewayapiv1.Gateway) { updated.Spec.Listeners[0].Name = "renamed" }, true, 0},
			{"remove inline still in ListenerSet", func(old, updated *gatewayapiv1.Gateway) {
				old.Spec.Listeners[0].Hostname = ptr.To(gatewayapiv1.Hostname("unique.example.com"))
				updated.Spec.Listeners = nil
			}, false, 0},
			{"add inline already in ListenerSet", func(old, updated *gatewayapiv1.Gateway) {
				old.Spec.Listeners = nil
				updated.Spec.Listeners[0].Hostname = ptr.To(gatewayapiv1.Hostname("sibling.example.com"))
			}, false, 0},
			{"old lookup fails", func(old, updated *gatewayapiv1.Gateway) {}, true, 1},
			{"new lookup fails", func(old, updated *gatewayapiv1.Gateway) {}, true, 2},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				old := parent.DeepCopy()
				old.Spec.Listeners = make([]gatewayapiv1.Listener, 1, 2)
				old.Spec.Listeners[0] = gatewayapiv1.Listener{Name: "unique", Hostname: ptr.To(gatewayapiv1.Hostname("inline.example.com"))}
				backing := old.Spec.Listeners[:2]
				backing[1] = gatewayapiv1.Listener{Name: "sentinel"}
				updated := old.DeepCopy()
				updated.Generation++
				scenario.mutate(old, updated)
				beforeOld, beforeUpdated := old.DeepCopy(), updated.DeepCopy()
				listCalls, failOnCall = 0, scenario.fail
				informer.Update(old, updated)
				require.Equal(t, scenario.queued, queue.Len() != 0)
				if scenario.queued {
					request, _ := queue.Get()
					require.Equal(t, client.ObjectKeyFromObject(service), request.NamespacedName)
					queue.Done(request)
				}
				expectedCalls := 2
				if scenario.fail == 1 {
					expectedCalls = 1
				}
				require.Equal(t, expectedCalls, listCalls)
				require.Equal(t, beforeOld, old)
				require.Equal(t, beforeUpdated, updated)
				require.Equal(t, gatewayapiv1.Listener{Name: "sentinel"}, backing[1])
			})
		}
		var unchanged gatewayapiv1.ListenerSet
		require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(listenerSet), &unchanged))
		require.Equal(t, listenerSet.Spec, unchanged.Spec)
		require.Equal(t, listenerSet.Status, unchanged.Status)
	})
	check := func(expected int) {
		t.Helper()
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(service)})
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
	listenerSet.Status.Listeners[0].Conditions = append(listenerSet.Status.Listeners[0].Conditions, metav1.Condition{Type: "Conflicted", Status: metav1.ConditionTrue, ObservedGeneration: 2})
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(4)
	listenerSet.Status.Listeners[1].Conditions = append(listenerSet.Status.Listeners[1].Conditions, metav1.Condition{Type: "Conflicted", Status: metav1.ConditionTrue, ObservedGeneration: 2})
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(4)
	listenerSet.Status.Listeners[1].Conditions[1].ObservedGeneration = 1
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(4)
	listenerSet.Status.Listeners[1].Conditions[0].ObservedGeneration = 1
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(3)
	listenerSet.Status.Listeners[1].Conditions[0].ObservedGeneration = 2
	listenerSet.Status.Listeners[1].Conditions[0].Status = metav1.ConditionFalse
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
