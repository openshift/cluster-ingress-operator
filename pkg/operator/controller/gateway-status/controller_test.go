package gatewaystatus

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	condutils "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
)

var (
	commonDNSZone = configv1.DNSZone{
		ID: "somezone.on.some.provider",
	}
)

// common functions and vars for tests
var (
	dnsConfigFn = func(addzone bool) *configv1.DNS {
		cfg := &configv1.DNS{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: configv1.DNSSpec{
				BaseDomain: "example.tld",
			},
		}
		if addzone {
			cfg.Spec.PublicZone = commonDNSZone.DeepCopy()
		}
		return cfg
	}

	infraConfigFn = &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
		},
	}
	gwFn = func(name string, conditions ...metav1.Condition) *gatewayapiv1.Gateway {
		return &gatewayapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "openshift-ingress",
				Name:      name,
			},
			Status: gatewayapiv1.GatewayStatus{
				Conditions: conditions,
			},
		}
	}

	svcFn = func(name string, labels map[string]string, ingresses ...corev1.LoadBalancerIngress) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Labels:    labels,
				Namespace: "openshift-ingress",
				Name:      name,
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: ingresses,
				},
			},
		}
	}
	eventFn = func(kind, namespace, name, component, message, reason string) *corev1.Event {
		return &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
			},
			InvolvedObject: corev1.ObjectReference{
				Kind:      kind,
				Namespace: namespace,
				Name:      name,
			},
			Message: message,
			Reason:  reason,
			Source: corev1.EventSource{
				Component: component,
			},
		}
	}

	exampleManagedGatewayLabel = map[string]string{
		"gateway.istio.io/managed":               "openshift.io-gateway-controller",
		"gateway.networking.k8s.io/gateway-name": "example-gateway",
	}

	exampleConditions = []metav1.Condition{
		{
			Type:    string(gatewayapiv1.GatewayConditionAccepted),
			Reason:  string(gatewayapiv1.GatewayReasonAccepted),
			Status:  metav1.ConditionTrue,
			Message: "Resource accepted",
		},
		{
			Type:    string(gatewayapiv1.GatewayConditionProgrammed),
			Reason:  string(gatewayapiv1.GatewayReasonProgrammed),
			Status:  metav1.ConditionTrue,
			Message: "Resource programmed, service created",
		},
	}

	ingHostFn = func(hostname string) corev1.LoadBalancerIngress {
		return corev1.LoadBalancerIngress{
			Hostname: hostname,
		}
	}

	dnsRecordFn = func(name, dnsName string, policy iov1.DNSManagementPolicy, labels map[string]string, status iov1.DNSRecordStatus) *iov1.DNSRecord {
		return &iov1.DNSRecord{
			ObjectMeta: metav1.ObjectMeta{
				Labels:    labels,
				Namespace: "openshift-ingress",
				Name:      name,
			},
			Spec: iov1.DNSRecordSpec{
				DNSName:             dnsName,
				RecordType:          iov1.CNAMERecordType,
				RecordTTL:           30,
				DNSManagementPolicy: policy,
			},
			Status: status,
		}
	}

	reqFn = func(ns, name string) reconcile.Request {
		return reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: ns,
				Name:      name,
			},
		}
	}
)

func Test_Reconcile(t *testing.T) {

	tests := []struct {
		name               string
		existingObjects    []runtime.Object
		expectedConditions []metav1.Condition
		reconcileRequest   reconcile.Request
		expectError        string
	}{
		{
			name: "gateway not found, should not return any error",
			existingObjects: []runtime.Object{
				infraConfigFn,
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectError:      "",
		},
		{
			name: "missing dns config",
			existingObjects: []runtime.Object{
				infraConfigFn,
				gwFn("example-gateway", exampleConditions...),
				svcFn("example-gateway", exampleManagedGatewayLabel, ingHostFn("gwapi.example.tld")),
			},
			reconcileRequest:   reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: exampleConditions,
			expectError:        `dnses.config.openshift.io "cluster" not found`,
		},
		{
			name: "missing infrastructure config",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				gwFn("example-gateway", exampleConditions...),
				svcFn("example-gateway", exampleManagedGatewayLabel, ingHostFn("gwapi.example.tld")),
			},
			reconcileRequest:   reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: exampleConditions,
			expectError:        `infrastructures.config.openshift.io "cluster" not found`,
		},
		{
			name: "missing loadbalancer and dnsrecord should add the right condition about missing both resources",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", exampleConditions...),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:    operatorv1.LoadBalancerManagedIngressConditionType,
					Status:  metav1.ConditionTrue,
					Reason:  "WantedByEndpointPublishingStrategy",
					Message: "The endpoint publishing strategy supports a managed load balancer",
				},
				{
					Type:    operatorv1.LoadBalancerReadyIngressConditionType,
					Status:  metav1.ConditionFalse,
					Reason:  "ServiceNotFound",
					Message: "The LoadBalancer service resource is missing",
				},
				{
					Type:    operatorv1.DNSManagedIngressConditionType,
					Status:  metav1.ConditionTrue,
					Reason:  "Normal",
					Message: "DNS management is supported and zones are specified in the cluster DNS config.",
				},
				{
					Type:    operatorv1.DNSReadyIngressConditionType,
					Status:  metav1.ConditionFalse,
					Reason:  "RecordNotFound",
					Message: "The wildcard record resource was not found.",
				},
			},
			expectError: "",
		},
		{
			name: "missing dnsconfig zone should add the right condition",
			existingObjects: []runtime.Object{
				dnsConfigFn(false),
				infraConfigFn,
				gwFn("example-gateway"),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:    operatorv1.DNSManagedIngressConditionType,
					Status:  metav1.ConditionFalse,
					Reason:  "NoDNSZones",
					Message: "No DNS zones are defined in the cluster dns config.",
				},
			},
			expectError: "",
		},
		{
			name: "load balancer with missing ingress status should be reflected on gateway condition",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:    operatorv1.LoadBalancerReadyIngressConditionType,
					Status:  metav1.ConditionFalse,
					Reason:  "LoadBalancerPending",
					Message: "The LoadBalancer service is pending",
				},
			},
			expectError: "",
		},
		{
			name: "load balancer with missing ingress status and extra event should be reflected on gateway condition",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel),
				eventFn("Service", "openshift-ingress", "openshift-ingress-lb", "service-controller", "unavailable", "SyncLoadBalancerFailed"),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:    operatorv1.LoadBalancerReadyIngressConditionType,
					Status:  metav1.ConditionFalse,
					Reason:  "SyncLoadBalancerFailed",
					Message: "The service-controller component is reporting SyncLoadBalancerFailed events like: unavailable\nThe cloud-controller-manager logs may contain more details.",
				},
			},
			expectError: "",
		},
		{
			name: "load balancer with valid ingress status should be reflected on gateway condition",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel, corev1.LoadBalancerIngress{
					Hostname: "gwapi.example.tld",
				}),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:    operatorv1.LoadBalancerReadyIngressConditionType,
					Status:  metav1.ConditionTrue,
					Reason:  "LoadBalancerProvisioned",
					Message: "The LoadBalancer service is provisioned",
				},
				{
					Type:    operatorv1.DNSReadyIngressConditionType,
					Status:  metav1.ConditionFalse,
					Reason:  "RecordNotFound",
					Message: "The wildcard record resource was not found.",
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns unmanaged should be properly reflected on conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel, corev1.LoadBalancerIngress{
					Hostname: "something.somewhere.somehow",
				}),
				dnsRecordFn("openshift-ingress-dns", "gwapi.example.tld", iov1.UnmanagedDNS, exampleManagedGatewayLabel, iov1.DNSRecordStatus{}),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:    operatorv1.LoadBalancerReadyIngressConditionType,
					Status:  metav1.ConditionTrue,
					Reason:  "LoadBalancerProvisioned",
					Message: "The LoadBalancer service is provisioned",
				},
				{
					Type:    operatorv1.DNSReadyIngressConditionType,
					Status:  metav1.ConditionUnknown,
					Reason:  "UnmanagedDNS",
					Message: "The DNS management policy is set to Unmanaged.",
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns managed but no zones should reflect on conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel, corev1.LoadBalancerIngress{
					Hostname: "something.somewhere.somehow",
				}),
				dnsRecordFn("openshift-ingress-dns", "gwapi.example.tld", iov1.ManagedDNS, exampleManagedGatewayLabel, iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{},
				}),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:    operatorv1.LoadBalancerReadyIngressConditionType,
					Status:  metav1.ConditionTrue,
					Reason:  "LoadBalancerProvisioned",
					Message: "The LoadBalancer service is provisioned",
				},
				{
					Type:    operatorv1.DNSReadyIngressConditionType,
					Status:  metav1.ConditionFalse,
					Reason:  "NoZones",
					Message: "The record isn't present in any zones.",
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns managed with one failed zone should reflect on conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel, corev1.LoadBalancerIngress{
					Hostname: "something.somewhere.somehow",
				}),
				dnsRecordFn("openshift-ingress-dns", "gwapi.example.tld", iov1.ManagedDNS, exampleManagedGatewayLabel, iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: *commonDNSZone.DeepCopy(),
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:   iov1.DNSRecordPublishedConditionType,
									Status: string(operatorv1.ConditionFalse),
								},
							},
						},
					},
				}),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:    operatorv1.LoadBalancerReadyIngressConditionType,
					Status:  metav1.ConditionTrue,
					Reason:  "LoadBalancerProvisioned",
					Message: "The LoadBalancer service is provisioned",
				},
				{
					Type:    operatorv1.DNSReadyIngressConditionType,
					Status:  metav1.ConditionFalse,
					Reason:  "FailedZones",
					Message: "The record failed to provision in some zones: [{somezone.on.some.provider map[]}]",
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns managed with one zone in unknown state should reflect on conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel, corev1.LoadBalancerIngress{
					Hostname: "something.somewhere.somehow",
				}),
				dnsRecordFn("openshift-ingress-dns", "gwapi.example.tld", iov1.ManagedDNS, exampleManagedGatewayLabel, iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: *commonDNSZone.DeepCopy(),
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:   iov1.DNSRecordPublishedConditionType,
									Status: string(operatorv1.ConditionUnknown),
								},
							},
						},
					},
				}),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:    operatorv1.LoadBalancerReadyIngressConditionType,
					Status:  metav1.ConditionTrue,
					Reason:  "LoadBalancerProvisioned",
					Message: "The LoadBalancer service is provisioned",
				},
				{
					Type:    operatorv1.DNSReadyIngressConditionType,
					Status:  metav1.ConditionFalse,
					Reason:  "UnknownZones",
					Message: "Provisioning of the record is in an unknown state in some zones: [{somezone.on.some.provider map[]}]",
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns managed with one zone in valid state should reflect on conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel, corev1.LoadBalancerIngress{
					Hostname: "something.somewhere.somehow",
				}),
				dnsRecordFn("openshift-ingress-dns", "gwapi.example.tld", iov1.ManagedDNS, exampleManagedGatewayLabel, iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: *commonDNSZone.DeepCopy(),
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:   iov1.DNSRecordPublishedConditionType,
									Status: string(operatorv1.ConditionTrue),
								},
								{
									Type:   iov1.DNSRecordFailedConditionType,
									Status: string(operatorv1.ConditionFalse),
								},
							},
						},
						{
							DNSZone: configv1.DNSZone{
								ID: "not.one.that.we.manage",
							},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:   iov1.DNSRecordPublishedConditionType,
									Status: string(operatorv1.ConditionTrue),
								},
							},
						},
					},
				}),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:    operatorv1.LoadBalancerReadyIngressConditionType,
					Status:  metav1.ConditionTrue,
					Reason:  "LoadBalancerProvisioned",
					Message: "The LoadBalancer service is provisioned",
				},
				{
					Type:    operatorv1.DNSReadyIngressConditionType,
					Status:  metav1.ConditionTrue,
					Reason:  "NoFailedZones",
					Message: "The record is provisioned in all reported zones.",
				},
			},
			expectError: "",
		},
	}

	scheme := runtime.NewScheme()
	iov1.AddToScheme(scheme)
	corev1.AddToScheme(scheme)
	gatewayapiv1.Install(scheme)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				WithStatusSubresource(&gatewayapiv1.Gateway{}).
				Build()
			cl := &testutil.FakeClientRecorder{
				Client:  fakeClient,
				T:       t,
				Updated: []client.Object{},
			}
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := testutil.FakeCache{Informers: &informer, Reader: cl}
			reconciler := &reconciler{
				cache:    cache,
				client:   cl,
				recorder: record.NewFakeRecorder(1),
			}
			res, err := reconciler.Reconcile(ctx, tc.reconcileRequest)
			if tc.expectError == "" {
				if assert.NoError(t, err) {
					assert.Equal(t, reconcile.Result{}, res)
				}
			} else {
				if assert.Error(t, err) {
					assert.Contains(t, err.Error(), tc.expectError)
				}
			}

			if len(tc.expectedConditions) > 0 {
				gw := &gatewayapiv1.Gateway{}
				err := cl.Get(ctx, tc.reconcileRequest.NamespacedName, gw)
				assert.NoError(t, err)
				// Instead of diffing conditions we will try to search if the condition
				// we want exists and has the right values
				for _, cond := range tc.expectedConditions {
					found := condutils.FindStatusCondition(gw.Status.Conditions, cond.Type)
					if found == nil {
						assert.NotNil(t, found, "condition not found %+v", cond)
					} else {
						// Reset the transition time just because we don't care about it now
						// On go 1.25 we can run this inside synctest
						now := metav1.Now()
						found.LastTransitionTime = now
						cond.LastTransitionTime = now
						assert.Equal(t, cond, *found, "conditions are not equal")
					}
				}
			}

		})
	}
}
