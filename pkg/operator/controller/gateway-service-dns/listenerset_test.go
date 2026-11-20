package gateway_service_dns

import (
	"context"
	"fmt"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	normalCache := testutil.FakeCache{Informers: &informertest.FakeInformers{Scheme: scheme}, Reader: cl}
	r := &reconciler{config: Config{OperandNamespace: parent.Namespace}, client: cl, cache: normalCache, listenerSetCache: normalCache, modeAccessor: mode}
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
	check(4) // This is a reconcile to check creation.
	check(4) // This is a second reconcile to check idempotence, i.e. we can run it twice and get same results.
	listenerSet.Status.Listeners[0].Conditions = append(listenerSet.Status.Listeners[0].Conditions, metav1.Condition{Type: "Conflicted", Status: metav1.ConditionTrue, ObservedGeneration: 2})
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(4)
	// The shared hostname remains desired by the Gateway's inline listener, but setting
	// a conflict on a ListenerSet-only hostname must remove its record.
	listenerSet.Status.Listeners[1].Conditions = append(listenerSet.Status.Listeners[1].Conditions, metav1.Condition{Type: "Conflicted", Status: metav1.ConditionTrue, ObservedGeneration: 2})
	require.NoError(t, cl.Update(ctx, listenerSet))
	check(3)
	// A Conflicted condition from an earlier generation must not block DNS.
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

	// A deleting parent clears all desired domains without consulting the
	// ListenerSet cache, even while a ListenerSet remains attached.
	parent.Finalizers = []string{"test.openshift.io/deletion"}
	require.NoError(t, cl.Update(ctx, parent))
	require.NoError(t, cl.Delete(ctx, parent))
	r.listenerSetCache = failingListenerSetListCache{FakeCache: normalCache}
	check(0)
}

// newDNSClaimGateway returns a managed Gateway with one listener per hostname.
func newDNSClaimGateway(name string, hostnames ...string) *gatewayapiv1.Gateway {
	listeners := make([]gatewayapiv1.Listener, 0, len(hostnames))
	for index, hostname := range hostnames {
		listeners = append(listeners, gatewayapiv1.Listener{
			Name:     gatewayapiv1.SectionName(fmt.Sprintf("listener-%d", index)),
			Hostname: ptr.To(gatewayapiv1.Hostname(hostname)),
		})
	}
	return &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: operatorcontroller.DefaultOperandNamespace, Name: name},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: "managed",
			Listeners:        listeners,
		},
	}
}

// newDNSClaimService returns a Gateway load-balancer Service fixture.
func newDNSClaimService(name, gatewayName string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: operatorcontroller.DefaultOperandNamespace,
			Name:      name,
			UID:       types.UID(name + "-uid"),
			Labels:    map[string]string{operatorcontroller.GatewayNameLabelKey: gatewayName},
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{Hostname: name + ".example.net"}},
			},
		},
	}
}

// newDNSClaimReconciler returns a reconciler and fake client for DNS claim tests.
func newDNSClaimReconciler(t *testing.T, objects ...client.Object) (*reconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{gatewayapiv1.AddToScheme, configv1.AddToScheme, corev1.AddToScheme, iov1.AddToScheme} {
		require.NoError(t, add(scheme))
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithIndex(&gatewayapiv1.ListenerSet{}, operatorcontroller.ListenerSetParentGatewayIndex, operatorcontroller.IndexListenerSetParent).
		Build()

	cache := testutil.FakeCache{
		Informers: &informertest.FakeInformers{Scheme: scheme},
		Reader:    cl,
	}
	mode := operatorcontroller.NewGatewayAPIModeAccessor(false)
	mode.SetCRDsEstablished(true)
	return &reconciler{
		config:           Config{OperandNamespace: operatorcontroller.DefaultOperandNamespace},
		client:           cl,
		cache:            cache,
		listenerSetCache: cache,
		modeAccessor:     mode,
	}, cl
}

// containsCanonicalHostname reports whether dnsName matches one of hostnames
// after DNS canonicalization.
func containsCanonicalHostname(hostnames []string, dnsName string) bool {
	for _, hostname := range hostnames {
		if dns.NormalizeHostname(hostname) == dns.NormalizeHostname(dnsName) {
			return true
		}
	}
	return false
}

// newDNSClaimRecord returns a preexisting DNSRecord owned by a Gateway Service.
func newDNSClaimRecord(
	name, dnsName, gatewayName string,
	service *corev1.Service,
	target string,
) *iov1.DNSRecord {
	return &iov1.DNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: service.Namespace,
			Name:      name,
			Labels: map[string]string{
				operatorcontroller.GatewayNameLabelKey: gatewayName,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Service",
				Name:       service.Name,
				UID:        service.UID,
			}},
		},
		Spec: iov1.DNSRecordSpec{
			DNSName:             dnsName,
			Targets:             []string{target},
			RecordType:          iov1.CNAMERecordType,
			RecordTTL:           30,
			DNSManagementPolicy: iov1.ManagedDNS,
		},
	}
}

// TestDNSRecordsAreSeparateAcrossGateways verifies that equivalent Gateway and
// ListenerSet hostnames share one DNSRecord claim.
func TestDNSRecordsAreSeparateAcrossGateways(t *testing.T) {
	ctx := context.Background()
	listenerSet := func(name string, gatewayName string) *gatewayapiv1.ListenerSet {
		return &gatewayapiv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: name, Generation: 1},
			Spec: gatewayapiv1.ListenerSetSpec{
				ParentRef: gatewayapiv1.ParentGatewayReference{
					Name:      gatewayapiv1.ObjectName(gatewayName),
					Namespace: ptr.To(gatewayapiv1.Namespace(operatorcontroller.DefaultOperandNamespace)),
				},
				Listeners: []gatewayapiv1.ListenerEntry{{
					Name:     "attached",
					Hostname: ptr.To(gatewayapiv1.Hostname("attached.example.com")),
				}},
			},
			Status: gatewayapiv1.ListenerSetStatus{
				Conditions: []metav1.Condition{{
					Type:               "Accepted",
					Status:             metav1.ConditionTrue,
					ObservedGeneration: 1,
				}},
				Listeners: []gatewayapiv1.ListenerEntryStatus{{
					Name: "attached",
					Conditions: []metav1.Condition{{
						Type:               "Accepted",
						Status:             metav1.ConditionTrue,
						ObservedGeneration: 1,
					}},
				}},
			},
		}
	}

	gatewayA := newDNSClaimGateway("gateway-a", "inline.example.com")
	gatewayB := newDNSClaimGateway("gateway-b", "inline.example.com")
	serviceA := newDNSClaimService("service-a", gatewayA.Name)
	serviceB := newDNSClaimService("service-b", gatewayB.Name)
	objects := []client.Object{
		gatewayA,
		gatewayB,
		&gatewayapiv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "managed"},
			Spec:       gatewayapiv1.GatewayClassSpec{ControllerName: operatorcontroller.OpenShiftGatewayClassControllerName},
		},
		serviceA,
		serviceB,
		listenerSet("listeners-a", gatewayA.Name),
		listenerSet("listeners-b", gatewayB.Name),
		&configv1.DNS{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}},
		&configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status:     configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType}},
		},
	}
	r, cl := newDNSClaimReconciler(t, objects...)

	// Gateway A establishes the first valid claims through both listener sources.
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(serviceA)})
	require.NoError(t, err)

	// Gateway B must not create a second claim for either equivalent hostname.
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(serviceB)})
	require.ErrorContains(t, err, "duplicate DNSRecord would be created")

	var records iov1.DNSRecordList
	require.NoError(t, cl.List(ctx, &records))
	recordsByDNSName := map[string]types.UID{}

	for _, record := range records.Items {
		require.Len(t, record.OwnerReferences, 1)
		require.Equal(t, serviceA.UID, record.OwnerReferences[0].UID)
		require.Equal(t, []string{"service-a.example.net"}, record.Spec.Targets)
		recordsByDNSName[record.Spec.DNSName] = record.OwnerReferences[0].UID
	}

	require.Len(t, recordsByDNSName, 2, "each DNS name must have one DNSRecord claim")
	for _, dnsName := range []string{"inline.example.com.", "attached.example.com."} {
		require.Contains(t, recordsByDNSName, dnsName)
		require.Equal(t, serviceA.UID, recordsByDNSName[dnsName])
	}
}

// TestDNSRecordWildcardClaims verifies cross-Gateway wildcard conflicts and the
// exceptions for apex names and hostnames owned by the same Gateway.
// An apex name is the wildcard's base name without the wildcard label; for
// example, "example.com" is the apex of "*.example.com".
func TestDNSRecordWildcardClaims(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		firstHosts  []string
		secondHosts []string
		sameGateway bool
		conflicts   bool
	}{
		{
			name:        "wildcard before subdomain",
			firstHosts:  []string{"*.example.com"},
			secondHosts: []string{"foo.example.com"},
			conflicts:   true,
		},
		{
			name:        "subdomain before wildcard",
			firstHosts:  []string{"foo.example.com"},
			secondHosts: []string{"*.example.com"},
			conflicts:   true,
		},
		{
			name:        "wildcard and apex",
			firstHosts:  []string{"*.example.com"},
			secondHosts: []string{"example.com"},
		},
		{
			name:        "wildcard and subdomain on one Gateway",
			firstHosts:  []string{"*.example.com", "foo.example.com"},
			secondHosts: []string{"foo.example.com"},
			sameGateway: true,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()

			gatewayA := newDNSClaimGateway("gateway-a", scenario.firstHosts...)
			gatewayB := newDNSClaimGateway("gateway-b", scenario.secondHosts...)
			serviceA := newDNSClaimService("service-a", gatewayA.Name)
			serviceB := newDNSClaimService("service-b", gatewayB.Name)
			if scenario.sameGateway {
				gatewayB = gatewayA
				serviceB = serviceA
			}
			objects := []client.Object{
				gatewayA,
				serviceA,
				&configv1.DNS{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}},
				&configv1.Infrastructure{
					ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
					Status:     configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType}},
				},
			}
			if !scenario.sameGateway {
				objects = append(objects, gatewayB, serviceB)
			}
			r, cl := newDNSClaimReconciler(t, objects...)

			// The first reconcile establishes the preclaiming Gateway's DNSRecord.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(serviceA)})
			require.NoError(t, err)

			// A second reconcile is only meaningful when another Gateway is claiming.
			if !scenario.sameGateway {
				_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(serviceB)})
			}
			if scenario.conflicts {
				require.ErrorContains(t, err, "overlaps a claim owned by another Service")
				require.NotContains(t, err.Error(), scenario.firstHosts[0], "errors must omit DNS payloads")
			} else {
				require.NoError(t, err)
			}

			var records iov1.DNSRecordList
			require.NoError(t, cl.List(ctx, &records))
			recordsByDNSName := map[string]types.UID{}
			for _, record := range records.Items {
				require.Len(t, record.OwnerReferences, 1)
				recordsByDNSName[record.Spec.DNSName] = record.OwnerReferences[0].UID
			}

			expectedCount := len(scenario.firstHosts)
			if !scenario.conflicts && !scenario.sameGateway {
				expectedCount += len(scenario.secondHosts)
			}
			require.Len(t, recordsByDNSName, expectedCount, "each DNS name must have one DNSRecord claim")

			for dnsName, ownerUID := range recordsByDNSName {
				if containsCanonicalHostname(scenario.firstHosts, dnsName) {
					require.Equal(t, serviceA.UID, ownerUID, "the first valid claim must win")
				} else {
					require.Equal(t, serviceB.UID, ownerUID)
				}
			}
		})
	}
}

// TestExistingDNSRecordUpdatesWithOverlappingClaim verifies that an existing
// claim remains reconcilable when another overlapping claim already exists.
// Overlap rejection applies to new claims; it must not block updates to the
// Gateway's own existing DNSRecord.
func TestExistingDNSRecordUpdatesWithOverlappingClaim(t *testing.T) {
	ctx := context.Background()
	gatewayA := newDNSClaimGateway("gateway-a", "foo.example.com")
	gatewayB := newDNSClaimGateway("gateway-b", "*.example.com")
	serviceA := newDNSClaimService("service-a", gatewayA.Name)
	serviceB := newDNSClaimService("service-b", gatewayB.Name)
	serviceA.Status.LoadBalancer.Ingress[0].Hostname = "new-target.example.net"

	existingRecord := newDNSClaimRecord(
		"existing-record",
		"foo.example.com.",
		gatewayA.Name,
		serviceA,
		"old-target.example.net",
	)
	overlappingRecord := newDNSClaimRecord(
		"overlapping-record",
		"*.example.com.",
		gatewayB.Name,
		serviceB,
		"service-b.example.net",
	)
	objects := []client.Object{
		gatewayA,
		gatewayB,
		serviceA,
		serviceB,
		existingRecord,
		overlappingRecord,
		&configv1.DNS{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}},
		&configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status:     configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType}},
		},
	}
	r, cl := newDNSClaimReconciler(t, objects...)

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(serviceA)})
	require.NoError(t, err, "the existing claim must remain updatable")

	var updatedExistingRecord iov1.DNSRecord
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(existingRecord), &updatedExistingRecord))
	require.Equal(t, []string{"new-target.example.net"}, updatedExistingRecord.Spec.Targets)

	var unchangedOverlappingClaim iov1.DNSRecord
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(overlappingRecord), &unchangedOverlappingClaim))
	require.Equal(t, []string{"service-b.example.net"}, unchangedOverlappingClaim.Spec.Targets)
}
