package gateway_service_dns

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestDNSRecordRecovery(t *testing.T) {
	for _, scenario := range []struct {
		name                 string
		canonical            bool
		foreign              bool
		unowned              bool
		duplicate            bool
		unlabeled            bool
		terminating          bool
		malformed            bool
		sharedOwner          bool
		ownerChangedOnDelete bool
		controllerOwner      bool
	}{
		{name: "owned noncanonical record"},
		{name: "owned canonical record", canonical: true},
		{name: "Service controller ownership", controllerOwner: true},
		{name: "foreign noncanonical record", foreign: true},
		{name: "unowned noncanonical record", unowned: true},
		{name: "foreign canonical record", canonical: true, foreign: true},
		{name: "unlabeled canonical record", canonical: true, unlabeled: true},
		{name: "true duplicate records", duplicate: true},
		{name: "canonical and noncanonical duplicates", canonical: true, duplicate: true},
		{name: "shared ownership", sharedOwner: true},
		{name: "terminating record", terminating: true},
		{name: "malformed foreign record", malformed: true, foreign: true},
		{name: "ownership changes during cleanup", ownerChangedOnDelete: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{gatewayapiv1.AddToScheme, configv1.AddToScheme, corev1.AddToScheme, iov1.AddToScheme} {
				require.NoError(t, add(scheme))
			}
			gateway := &gatewayapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: operatorcontroller.DefaultOperandNamespace}, Spec: gatewayapiv1.GatewaySpec{Listeners: []gatewayapiv1.Listener{
				{Name: "existing", Hostname: ptr.To(gatewayapiv1.Hostname("existing.example.com"))},
				{Name: "other", Hostname: ptr.To(gatewayapiv1.Hostname("other.example.com"))},
			}}}
			service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "service", Namespace: gateway.Namespace, UID: "service-uid", Labels: map[string]string{operatorcontroller.GatewayNameLabelKey: gateway.Name}}, Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{Hostname: "new-target.example.net"}}}}}
			record := &iov1.DNSRecord{ObjectMeta: metav1.ObjectMeta{Name: "legacy-record", Namespace: gateway.Namespace, UID: "record-uid", Labels: service.Labels, Finalizers: []string{"test.example.com/cleanup"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: service.Name, UID: service.UID}}}, Spec: iov1.DNSRecordSpec{DNSName: "EXISTING.example.com.", Targets: []string{"old-target.example.net"}, RecordType: iov1.CNAMERecordType, RecordTTL: 30, DNSManagementPolicy: iov1.ManagedDNS}, Status: iov1.DNSRecordStatus{ObservedGeneration: 3}}
			if scenario.canonical {
				record.Name = operatorcontroller.GatewayDNSRecordName(gateway, "existing.example.com.").Name
			}
			if scenario.foreign {
				record.OwnerReferences[0].UID = "foreign-service-uid"
			}
			if scenario.controllerOwner {
				record.OwnerReferences[0].Controller = ptr.To(true)
			}
			if scenario.unowned {
				record.OwnerReferences = nil
			}
			if scenario.sharedOwner {
				record.OwnerReferences = append(record.OwnerReferences, metav1.OwnerReference{APIVersion: "v1", Kind: "Service", Name: "other", UID: "other-uid"})
			}
			if scenario.unlabeled {
				record.Labels = nil
			}
			if scenario.terminating {
				record.DeletionTimestamp = ptr.To(metav1.NewTime(time.Unix(1, 0)))
				record.Finalizers = []string{"test.example.com/cleanup"}
			}
			if scenario.malformed {
				record.Spec.DNSName = ""
			}
			objects := []client.Object{gateway, service, record,
				&configv1.DNS{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: configv1.DNSSpec{BaseDomain: "example.com"}},
				&configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType}}},
			}
			if scenario.duplicate {
				duplicate := record.DeepCopy()
				duplicate.Name, duplicate.UID, duplicate.Spec.DNSName = "duplicate-record", "duplicate-uid", "existing.example.com."
				objects = append(objects, duplicate)
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(service).
				WithInterceptorFuncs(interceptor.Funcs{Delete: func(ctx context.Context, underlying client.WithWatch, object client.Object, options ...client.DeleteOption) error {
					if scenario.ownerChangedOnDelete && object.GetName() == record.Name {
						var changed iov1.DNSRecord
						require.NoError(t, underlying.Get(ctx, client.ObjectKeyFromObject(object), &changed))
						changed.OwnerReferences[0].UID = "foreign-service-uid"
						require.NoError(t, underlying.Update(ctx, &changed))
					}
					return underlying.Delete(ctx, object, options...)
				}}).
				WithIndex(&gatewayapiv1.ListenerSet{}, operatorcontroller.ListenerSetParentGatewayIndex, operatorcontroller.IndexListenerSetParent).Build()
			mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
			mode.SetCRDsEstablished(true)
			r := &reconciler{config: Config{OperandNamespace: gateway.Namespace}, client: cl, cache: testutil.FakeCache{Reader: cl}, listenerSetCache: testutil.FakeCache{Reader: cl}, modeAccessor: mode}
			blocked := scenario.foreign || scenario.unowned || scenario.sharedOwner || scenario.duplicate || scenario.unlabeled || scenario.terminating
			for _, target := range []string{"new-target.example.net", "changed-target.example.net", "changed-target.example.net"} {
				require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(service), service))
				service.Status.LoadBalancer.Ingress[0].Hostname = target
				require.NoError(t, cl.Status().Update(ctx, service))
				_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(service)})
				if blocked {
					require.Error(t, err)
					require.NotContains(t, err.Error(), "existing.example.com")
					if record.Spec.DNSName != "" {
						require.NotContains(t, err.Error(), record.Spec.DNSName, "errors must omit DNS payloads")
					}
				} else {
					require.NoError(t, err)
				}
				var current iov1.DNSRecord
				require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(record), &current))
				require.Equal(t, record.UID, current.UID)
				require.Equal(t, record.OwnerReferences, current.OwnerReferences)
				require.Equal(t, record.Finalizers, current.Finalizers)
				require.Equal(t, record.Status, current.Status)
				require.Equal(t, record.DeletionTimestamp, current.DeletionTimestamp)
				if blocked {
					require.Equal(t, record.Spec, current.Spec, "blocked records must not be adopted or rewritten")
				} else {
					require.Equal(t, []string{target}, current.Spec.Targets)
					require.Equal(t, "existing.example.com.", current.Spec.DNSName)
				}
				require.NoError(t, cl.Get(ctx, operatorcontroller.GatewayDNSRecordName(gateway, "other.example.com."), &current))
				require.Equal(t, []string{target}, current.Spec.Targets, "unrelated hostname must progress on every reconcile")
				var records iov1.DNSRecordList
				require.NoError(t, cl.List(ctx, &records))
				count := 2
				if scenario.duplicate || scenario.malformed {
					count++
				}
				require.Len(t, records.Items, count, "must not create a second record for a represented hostname")
				for _, found := range records.Items {
					if found.Name == "duplicate-record" {
						expected := record.Spec
						expected.DNSName = "existing.example.com."
						require.Equal(t, expected, found.Spec, "neither duplicate claim may be rewritten")
					}
				}
			}
			gateway.Spec.Listeners = gateway.Spec.Listeners[1:]
			require.NoError(t, cl.Update(ctx, gateway))
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(service)})
			if scenario.ownerChangedOnDelete {
				require.Error(t, err)
				var protected iov1.DNSRecord
				require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(record), &protected))
				require.True(t, protected.DeletionTimestamp.IsZero(), "concurrent ownership change must prevent deletion")
				require.Equal(t, "foreign-service-uid", string(protected.OwnerReferences[0].UID))
			} else if scenario.foreign || scenario.unowned || scenario.sharedOwner {
				require.Error(t, err)
				var unchanged iov1.DNSRecord
				require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(record), &unchanged))
				require.Equal(t, record.Spec, unchanged.Spec, "cleanup must not delete foreign or unowned records")
			} else {
				require.NoError(t, err)
			}
			if !scenario.foreign && !scenario.unowned && !scenario.sharedOwner && !scenario.unlabeled && !scenario.ownerChangedOnDelete {
				var deleting iov1.DNSRecord
				require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(record), &deleting))
				require.False(t, deleting.DeletionTimestamp.IsZero(), "owned stale records must enter normal finalizer cleanup")
			}
		})
	}
}
