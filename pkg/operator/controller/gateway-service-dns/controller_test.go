package gateway_service_dns

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
)

func Test_Reconcile(t *testing.T) {
	dnsConfig := &configv1.DNS{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.DNSSpec{
			BaseDomain: "example.com",
		},
	}
	infraConfig := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
		},
	}
	gw := func(name string, listeners ...gatewayapiv1.Listener) *gatewayapiv1.Gateway {
		return &gatewayapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "openshift-ingress",
				Name:      name,
			},
			Spec: gatewayapiv1.GatewaySpec{
				Listeners: listeners,
			},
		}
	}
	l := func(name, hostname string, port int) gatewayapiv1.Listener {
		h := gatewayapiv1.Hostname(hostname)
		return gatewayapiv1.Listener{
			Name:     gatewayapiv1.SectionName(name),
			Hostname: &h,
			Port:     gatewayapiv1.PortNumber(port),
		}
	}
	svc := func(name string, labels, selector map[string]string, ingresses ...corev1.LoadBalancerIngress) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Labels:    labels,
				Namespace: "openshift-ingress",
				Name:      name,
			},
			Spec: corev1.ServiceSpec{
				Selector: selector,
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: ingresses,
				},
			},
		}
	}
	gatewayManagedLabel := map[string]string{
		"gateway.istio.io/managed": "example-gateway",
	}
	exampleGatewayLabel := map[string]string{
		"istio.io/gateway-name": "example-gateway",
	}
	ingHost := func(hostname string) corev1.LoadBalancerIngress {
		return corev1.LoadBalancerIngress{
			Hostname: hostname,
		}
	}
	dnsrecord := func(name, dnsName string, policy iov1.DNSManagementPolicy, labels map[string]string, targets ...string) *iov1.DNSRecord {
		return &iov1.DNSRecord{
			ObjectMeta: metav1.ObjectMeta{
				Labels:    labels,
				Namespace: "openshift-ingress",
				Name:      name,
			},
			Spec: iov1.DNSRecordSpec{
				DNSName:             dnsName,
				RecordType:          iov1.CNAMERecordType,
				Targets:             targets,
				RecordTTL:           30,
				DNSManagementPolicy: policy,
			},
		}
	}
	req := func(ns, name string) reconcile.Request {
		return reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: ns,
				Name:      name,
			},
		}
	}
	tests := []struct {
		name             string
		existingObjects  []runtime.Object
		reconcileRequest reconcile.Request
		expectCreate     []client.Object
		expectUpdate     []client.Object
		expectDelete     []client.Object
		expectError      string
	}{
		{
			name: "missing dns config",
			existingObjects: []runtime.Object{
				infraConfig,
				gw("example-gateway", l("stage-http", "*.stage.example.com", 80)),
				svc("example-gateway", gatewayManagedLabel, exampleGatewayLabel, ingHost("lb.example.com")),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate:     []client.Object{},
			expectUpdate:     []client.Object{},
			expectDelete:     []client.Object{},
			expectError:      `dnses.config.openshift.io "cluster" not found`,
		},
		{
			name: "missing infrastructure config",
			existingObjects: []runtime.Object{
				dnsConfig,
				gw("example-gateway", l("stage-http", "*.stage.example.com", 80)),
				svc("example-gateway", gatewayManagedLabel, exampleGatewayLabel, ingHost("lb.example.com")),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate:     []client.Object{},
			expectUpdate:     []client.Object{},
			expectDelete:     []client.Object{},
			expectError:      `infrastructures.config.openshift.io "cluster" not found`,
		},
		{
			name: "gateway with no listeners",
			existingObjects: []runtime.Object{
				dnsConfig, infraConfig,
				gw("example-gateway"),
				svc("example-gateway", gatewayManagedLabel, exampleGatewayLabel, ingHost("lb.example.com")),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate:     []client.Object{},
			expectUpdate:     []client.Object{},
			expectDelete:     []client.Object{},
		},
		{
			name: "gateway with three listeners and two unique host names, no dnsrecords",
			existingObjects: []runtime.Object{
				dnsConfig, infraConfig,
				gw(
					"example-gateway",
					l("stage-http", "*.stage.example.com", 80),
					l("stage-https", "*.stage.example.com", 443),
					l("prod-https", "*.prod.example.com", 443),
				),
				svc("example-gateway", gatewayManagedLabel, exampleGatewayLabel, ingHost("lb.example.com")),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate: []client.Object{
				dnsrecord("example-gateway-76456f8647-wildcard", "*.prod.example.com.", iov1.ManagedDNS, exampleGatewayLabel, "lb.example.com"),
				dnsrecord("example-gateway-64754456b8-wildcard", "*.stage.example.com.", iov1.ManagedDNS, exampleGatewayLabel, "lb.example.com"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "gateway with two listeners and one dnsrecord with a stale target, hostname already has trailing dot",
			existingObjects: []runtime.Object{
				dnsConfig, infraConfig,
				gw(
					"example-gateway",
					l("http", "*.example.com", 80),
					l("https", "*.example.com", 443),
				),
				svc("example-gateway", gatewayManagedLabel, exampleGatewayLabel, ingHost("newlb.example.com")),
				dnsrecord("example-gateway-7bdcfc8f68-wildcard", "*.example.com.", iov1.ManagedDNS, exampleGatewayLabel, "oldlb.example.com"),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate:     []client.Object{},
			expectUpdate: []client.Object{
				dnsrecord("example-gateway-7bdcfc8f68-wildcard", "*.example.com.", iov1.ManagedDNS, exampleGatewayLabel, "newlb.example.com"),
			},
			expectDelete: []client.Object{},
		},
		{
			name: "gateway with a stale dnsrecord",
			existingObjects: []runtime.Object{
				dnsConfig, infraConfig,
				gw(
					"example-gateway",
					l("http", "*.new.example.com", 80),
				),
				svc("example-gateway", gatewayManagedLabel, exampleGatewayLabel, ingHost("lb.example.com")),
				dnsrecord("example-gateway-64754456b8-wildcard", "*.old.example.com.", iov1.ManagedDNS, exampleGatewayLabel, "lb.example.com"),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate: []client.Object{
				dnsrecord("example-gateway-68ffc6d64-wildcard", "*.new.example.com.", iov1.ManagedDNS, exampleGatewayLabel, "lb.example.com"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{
				dnsrecord("example-gateway-64754456b8-wildcard", "*.old.example.com.", iov1.ManagedDNS, exampleGatewayLabel, "lb.example.com"),
			},
		},
		{
			name: "gateway with two listeners and one host name, no dnsrecords, name ends up with trailing dot",
			existingObjects: []runtime.Object{
				dnsConfig, infraConfig,
				gw("example-gateway", l("stage-http", "*.stage.example.com", 80), l("stage-https", "*.stage.example.com", 443)),
				svc("example-gateway", gatewayManagedLabel, exampleGatewayLabel, ingHost("lb.example.com")),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate: []client.Object{
				dnsrecord("example-gateway-64754456b8-wildcard", "*.stage.example.com.", iov1.ManagedDNS, exampleGatewayLabel, "lb.example.com"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name: "gateway with a listener with an unmanaged domain, no dnsrecords",
			existingObjects: []runtime.Object{
				dnsConfig, infraConfig,
				gw("example-gateway", l("http", "*.foo.com", 80)),
				svc("example-gateway", gatewayManagedLabel, exampleGatewayLabel, ingHost("lb.example.com")),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate: []client.Object{
				dnsrecord("example-gateway-795d4b47fd-wildcard", "*.foo.com.", iov1.UnmanagedDNS, exampleGatewayLabel, "lb.example.com"),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
	}

	scheme := runtime.NewScheme()
	iov1.AddToScheme(scheme)
	corev1.AddToScheme(scheme)
	gatewayapiv1.AddToScheme(scheme)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				Build()
			cl := &testutil.FakeClientRecorder{fakeClient, t, []client.Object{}, []client.Object{}, []client.Object{}}
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := testutil.FakeCache{Informers: &informer, Reader: cl}
			reconciler := &reconciler{
				config: Config{
					OperandNamespace: "openshift-ingress",
				},
				cache:  cache,
				client: cl,
			}
			res, err := reconciler.Reconcile(context.Background(), tc.reconcileRequest)
			if tc.expectError == "" {
				if assert.NoError(t, err) {
					assert.Equal(t, reconcile.Result{}, res)
				}
			} else {
				if assert.Error(t, err) {
					assert.Contains(t, err.Error(), tc.expectError)
				}
			}
			cmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Finalizers", "Labels", "OwnerReferences", "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
			}
			if diff := cmp.Diff(tc.expectCreate, cl.Added, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual creates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectUpdate, cl.Updated, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual updates: %s", diff)
			}
			// A deleted object has zero spec.
			delCmpOpts := append(cmpOpts, cmpopts.IgnoreTypes(iov1.DNSRecordSpec{}))
			if diff := cmp.Diff(tc.expectDelete, cl.Deleted, delCmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual deletes: %s", diff)
			}
		})
	}
}

func Test_gatewayListenersHostnamesChanged(t *testing.T) {
	l := func(name, hostname string) gatewayapiv1.Listener {
		h := gatewayapiv1.Hostname(hostname)
		return gatewayapiv1.Listener{
			Name:     gatewayapiv1.SectionName(name),
			Hostname: &h,
		}
	}
	tests := []struct {
		name     string
		old, new []gatewayapiv1.Listener
		expect   bool
	}{
		{
			name:   "no listeners",
			old:    []gatewayapiv1.Listener{},
			new:    []gatewayapiv1.Listener{},
			expect: false,
		},
		{
			name: "three listeners, no changes",
			old: []gatewayapiv1.Listener{
				l("http", "xyz.xyz"),
				l("https", "xyz.xyz"),
				l("foo", "bar.baz"),
			},
			new: []gatewayapiv1.Listener{
				l("http", "xyz.xyz"),
				l("https", "xyz.xyz"),
				l("foo", "bar.baz"),
			},
			expect: false,
		},
		{
			name:   "add a listener",
			old:    []gatewayapiv1.Listener{},
			new:    []gatewayapiv1.Listener{l("http", "xyz.xyz")},
			expect: true,
		},
		{
			name:   "remove a listener",
			old:    []gatewayapiv1.Listener{l("http", "xyz.xyz")},
			new:    []gatewayapiv1.Listener{},
			expect: true,
		},
		{
			name:   "rename a listener",
			old:    []gatewayapiv1.Listener{l("http", "xyz.xyz")},
			new:    []gatewayapiv1.Listener{l("https", "xyz.xyz")},
			expect: true,
		},
		{
			name:   "change a listener's hostname",
			old:    []gatewayapiv1.Listener{l("https", "xyz.xyz")},
			new:    []gatewayapiv1.Listener{l("https", "abc.xyz")},
			expect: true,
		},
		{
			name:   "replace a listener",
			old:    []gatewayapiv1.Listener{l("http", "xyz.xyz")},
			new:    []gatewayapiv1.Listener{l("https", "abc.xyz")},
			expect: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, gatewayListenersHostnamesChanged(tc.old, tc.new))
		})
	}
}
