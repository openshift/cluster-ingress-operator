package gateway_service_dns

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"

	gatewayapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	iov1 "github.com/openshift/api/operatoringress/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func Test_Reconcile(t *testing.T) {
	gw := func(name string, listeners ...gatewayapiv1beta1.Listener) *gatewayapiv1beta1.Gateway {
		return &gatewayapiv1beta1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "openshift-ingress",
				Name:      name,
			},
			Spec: gatewayapiv1beta1.GatewaySpec{
				Listeners: listeners,
			},
		}
	}
	l := func(name, hostname string, port int) gatewayapiv1beta1.Listener {
		h := gatewayapiv1beta1.Hostname(hostname)
		return gatewayapiv1beta1.Listener{
			Name:     gatewayapiv1beta1.SectionName(name),
			Hostname: &h,
			Port:     gatewayapiv1beta1.PortNumber(port),
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
	ingHost := func(hostname string) corev1.LoadBalancerIngress {
		return corev1.LoadBalancerIngress{
			Hostname: hostname,
		}
	}
	dnsrecord := func(name, dnsName string, recordType string, targets ...string) *iov1.DNSRecord {
		return &iov1.DNSRecord{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "openshift-ingress",
				Name:      name,
			},
			Spec: iov1.DNSRecordSpec{
				DNSName:             dnsName,
				RecordType:          iov1.DNSRecordType(recordType),
				Targets:             targets,
				RecordTTL:           30,
				DNSManagementPolicy: iov1.ManagedDNS,
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
	}{
		{
			name: "gateway with no listeners",
			existingObjects: []runtime.Object{
				gw("example-gateway"),
				svc(
					"example-gateway",
					map[string]string{
						"gateway.istio.io/managed": "example-gateway",
					},
					map[string]string{
						"istio.io/gateway-name": "example-gateway",
					},
					ingHost("lb.example.com"),
				),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate:     []client.Object{},
			expectUpdate:     []client.Object{},
		},
		{
			name: "gateway with three listeners and two unique host names, no dnsrecords",
			existingObjects: []runtime.Object{
				gw(
					"example-gateway",
					l("stage-http", "*.stage.example.com", 80),
					l("stage-https", "*.stage.example.com", 443),
					l("prod-https", "*.prod.example.com", 443),
				),
				svc(
					"example-gateway",
					map[string]string{
						"gateway.istio.io/managed": "example-gateway",
					},
					map[string]string{
						"istio.io/gateway-name": "example-gateway",
					},
					ingHost("lb.example.com"),
				),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate: []client.Object{
				dnsrecord("example-gateway-76456f8647-wildcard", "*.prod.example.com.", "CNAME", "lb.example.com"),
				dnsrecord("example-gateway-64754456b8-wildcard", "*.stage.example.com.", "CNAME", "lb.example.com"),
			},
			expectUpdate: []client.Object{},
		},
		{
			name: "gateway with two listeners and one dnsrecord with a stale target, hostname already has trailing dot",
			existingObjects: []runtime.Object{
				gw(
					"example-gateway",
					l("http", "*.example.com", 80),
					l("https", "*.example.com", 443),
				),
				svc(
					"example-gateway",
					map[string]string{
						"gateway.istio.io/managed": "example-gateway",
					},
					map[string]string{
						"istio.io/gateway-name": "example-gateway",
					},
					ingHost("newlb.example.com"),
				),
				dnsrecord("example-gateway-7bdcfc8f68-wildcard", "*.example.com.", "CNAME", "oldlb.example.com"),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate:     []client.Object{},
			expectUpdate: []client.Object{
				dnsrecord("example-gateway-7bdcfc8f68-wildcard", "*.example.com.", "CNAME", "newlb.example.com"),
			},
		},
		{
			name: "gateway with two listeners and one host name, no dnsrecords, name ends up with trailing dot",
			existingObjects: []runtime.Object{
				gw(
					"example-gateway",
					l("stage-http", "*.stage.example.com", 80),
					l("stage-https", "*.stage.example.com", 443),
				),
				svc(
					"example-gateway",
					map[string]string{
						"gateway.istio.io/managed": "example-gateway",
					},
					map[string]string{
						"istio.io/gateway-name": "example-gateway",
					},
					ingHost("lb.example.com"),
				),
			},
			reconcileRequest: req("openshift-ingress", "example-gateway"),
			expectCreate: []client.Object{
				dnsrecord("example-gateway-64754456b8-wildcard", "*.stage.example.com.", "CNAME", "lb.example.com"),
			},
			expectUpdate: []client.Object{},
		},
	}

	scheme := runtime.NewScheme()
	iov1.AddToScheme(scheme)
	corev1.AddToScheme(scheme)
	gatewayapiv1beta1.AddToScheme(scheme)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				Build()
			cl := &fakeClientRecorder{fakeClient, t, []client.Object{}, []client.Object{}}
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := fakeCache{Informers: &informer, Reader: cl}
			reconciler := &reconciler{
				config: Config{
					OperandNamespace: "openshift-ingress",
				},
				cache:  cache,
				client: cl,
			}
			res, err := reconciler.Reconcile(context.Background(), tc.reconcileRequest)
			if assert.NoError(t, err) {
				assert.Equal(t, reconcile.Result{}, res)
			}
			cmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Finalizers", "Labels", "OwnerReferences", "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
			}
			if diff := cmp.Diff(tc.expectCreate, cl.added, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual creates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectUpdate, cl.updated, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual updates: %s", diff)
			}
		})
	}
}

type fakeCache struct {
	cache.Informers
	client.Reader
}

type fakeClientRecorder struct {
	client.Client
	*testing.T

	added   []client.Object
	updated []client.Object
}

func (c *fakeClientRecorder) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *fakeClientRecorder) List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error {
	return c.Client.List(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Scheme() *runtime.Scheme {
	return c.Client.Scheme()
}

func (c *fakeClientRecorder) RESTMapper() meta.RESTMapper {
	return c.Client.RESTMapper()
}

func (c *fakeClientRecorder) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.added = append(c.added, obj)
	c.T.Log(obj)
	return c.Client.Create(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return c.Client.Delete(ctx, obj, opts...)
}

func (c *fakeClientRecorder) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return c.Client.DeleteAllOf(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.updated = append(c.updated, obj)
	return c.Client.Update(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c *fakeClientRecorder) Status() client.StatusWriter {
	return c.Client.Status()
}

func Test_gatewayListenersHostnamesChanged(t *testing.T) {
	l := func(name, hostname string) gatewayapiv1beta1.Listener {
		h := gatewayapiv1beta1.Hostname(hostname)
		return gatewayapiv1beta1.Listener{
			Name:     gatewayapiv1beta1.SectionName(name),
			Hostname: &h,
		}
	}
	tests := []struct {
		name     string
		old, new []gatewayapiv1beta1.Listener
		expect   bool
	}{
		{
			name:   "no listeners",
			old:    []gatewayapiv1beta1.Listener{},
			new:    []gatewayapiv1beta1.Listener{},
			expect: false,
		},
		{
			name: "three listeners, no changes",
			old: []gatewayapiv1beta1.Listener{
				l("http", "xyz.xyz"),
				l("https", "xyz.xyz"),
				l("foo", "bar.baz"),
			},
			new: []gatewayapiv1beta1.Listener{
				l("http", "xyz.xyz"),
				l("https", "xyz.xyz"),
				l("foo", "bar.baz"),
			},
			expect: false,
		},
		{
			name:   "add a listener",
			old:    []gatewayapiv1beta1.Listener{},
			new:    []gatewayapiv1beta1.Listener{l("http", "xyz.xyz")},
			expect: true,
		},
		{
			name:   "remove a listener",
			old:    []gatewayapiv1beta1.Listener{l("http", "xyz.xyz")},
			new:    []gatewayapiv1beta1.Listener{},
			expect: true,
		},
		{
			name:   "rename a listener",
			old:    []gatewayapiv1beta1.Listener{l("http", "xyz.xyz")},
			new:    []gatewayapiv1beta1.Listener{l("https", "xyz.xyz")},
			expect: true,
		},
		{
			name:   "change a listener's hostname",
			old:    []gatewayapiv1beta1.Listener{l("https", "xyz.xyz")},
			new:    []gatewayapiv1beta1.Listener{l("https", "abc.xyz")},
			expect: true,
		},
		{
			name:   "replace a listener",
			old:    []gatewayapiv1beta1.Listener{l("http", "xyz.xyz")},
			new:    []gatewayapiv1beta1.Listener{l("https", "abc.xyz")},
			expect: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, gatewayListenersHostnamesChanged(tc.old, tc.new))
		})
	}
}
