package gatewaystatus

import (
	"context"
	"fmt"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	condutils "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
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
	gwFn = func(name string, listeners []gatewayapiv1.Listener, generation int64, conditions ...metav1.Condition) *gatewayapiv1.Gateway {
		listenerStatus := make([]gatewayapiv1.ListenerStatus, len(listeners))
		for i, l := range listeners {
			listenerStatus[i].Name = l.Name
		}
		return &gatewayapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "openshift-ingress",
				Name:       name,
				Generation: generation,
			},
			Spec: gatewayapiv1.GatewaySpec{
				Listeners: listeners,
			},
			Status: gatewayapiv1.GatewayStatus{
				Conditions: conditions,
				Listeners:  listenerStatus,
			},
		}
	}

	svcFn = func(name string, labels map[string]string, ingresses ...corev1.LoadBalancerIngress) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Labels:    labels,
				Namespace: "openshift-ingress",
				Name:      name,
				UID:       types.UID(fmt.Sprint(10)),
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: ingresses,
				},
			},
		}
	}
	eventFn = func(kind, apiversion, namespace, name, component, message, reason string) *corev1.Event {
		return &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "event",
			},
			InvolvedObject: corev1.ObjectReference{
				APIVersion: apiversion,
				Kind:       kind,
				Namespace:  namespace,
				Name:       name,
				UID:        types.UID(fmt.Sprint(10)),
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
		name                   string
		existingObjects        []runtime.Object
		expectedConditions     []metav1.Condition
		expectedListenerStatus []gatewayapiv1.ListenerStatus
		reconcileRequest       reconcile.Request
		expectError            string
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
				gwFn("example-gateway", nil, 1, exampleConditions...),
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
				gwFn("example-gateway", nil, 2, exampleConditions...),
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
				gwFn("example-gateway", []gatewayapiv1.Listener{
					{
						Name:     "listener1",
						Hostname: ptr.To(gatewayapiv1.Hostname("norecord.example.tld")),
					},
				}, 3, exampleConditions...),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionFalse,
					Reason:             "ServiceNotFound",
					Message:            "The LoadBalancer service resource is missing",
					ObservedGeneration: 3,
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "listener1",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 3,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionFalse,
							Reason:             "RecordNotFound",
							Message:            "The wildcard record resource was not found.",
							ObservedGeneration: 3,
						},
					},
				},
			},
			expectError: "",
		},
		{
			name: "missing dnsconfig zone should add the right condition",
			existingObjects: []runtime.Object{
				dnsConfigFn(false),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{
					{
						Name:     "listener1",
						Hostname: ptr.To(gatewayapiv1.Hostname("noconfig.example.tld")),
					},
				}, 4),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel, corev1.LoadBalancerIngress{
					IP: "10.10.10.10",
				}),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "LoadBalancerProvisioned",
					Message:            "The LoadBalancer service is provisioned",
					ObservedGeneration: 4,
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "listener1",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionFalse,
							Reason:             "NoDNSZones",
							Message:            "No DNS zones are defined in the cluster dns config.",
							ObservedGeneration: 4,
						},
					},
				},
			},
			expectError: "",
		},
		{
			name: "load balancer with missing ingress status should be reflected on gateway condition",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{
					{
						Name:     "listener1",
						Hostname: ptr.To(gatewayapiv1.Hostname("noconfig.example.tld")),
					},
				}, 5, exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionFalse,
					Reason:             "LoadBalancerPending",
					Message:            "The LoadBalancer service is pending",
					ObservedGeneration: 5,
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "listener1",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 5,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionFalse,
							Reason:             "RecordNotFound",
							Message:            "The wildcard record resource was not found.",
							ObservedGeneration: 5,
						},
					},
				},
			},
			expectError: "",
		},
		{
			name: "load balancer with missing ingress status and extra event should be reflected on gateway condition",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{}, 6, exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel),
				eventFn("Service", "v1", "openshift-ingress", "openshift-ingress-lb", "service-controller", "unavailable", "SyncLoadBalancerFailed"),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
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
				{
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionFalse,
					Reason:             "SyncLoadBalancerFailed",
					Message:            "The service-controller component is reporting SyncLoadBalancerFailed events like: unavailable\nThe cloud-controller-manager logs may contain more details.",
					ObservedGeneration: 6,
				},
			},
			expectError: "",
		},
		{
			name: "load balancer with valid ingress status should be reflected on gateway condition",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{}, 7, exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel, corev1.LoadBalancerIngress{
					Hostname: "gwapi.example.tld",
				}),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
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
				{
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "LoadBalancerProvisioned",
					Message:            "The LoadBalancer service is provisioned",
					ObservedGeneration: 7,
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns unmanaged should be properly reflected on conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{
					{
						Name:     "listener1",
						Hostname: ptr.To(gatewayapiv1.Hostname("gwapi.example.tld")),
					},
				}, 8, exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel, corev1.LoadBalancerIngress{
					Hostname: "something.somewhere.somehow",
				}),
				dnsRecordFn("openshift-ingress-dns", "gwapi.example.tld", iov1.UnmanagedDNS, exampleManagedGatewayLabel, iov1.DNSRecordStatus{}),
			},
			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
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
				{
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "LoadBalancerProvisioned",
					Message:            "The LoadBalancer service is provisioned",
					ObservedGeneration: 8,
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "listener1",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 8,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionUnknown,
							Reason:             "UnmanagedDNS",
							Message:            "The DNS management policy is set to Unmanaged.",
							ObservedGeneration: 8,
						},
					},
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns managed but no zones should reflect on conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{
					{
						Name:     "listener1",
						Hostname: ptr.To(gatewayapiv1.Hostname("gwapi.example.tld")),
					},
				}, 9, exampleConditions...),
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
				{
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "LoadBalancerProvisioned",
					Message:            "The LoadBalancer service is provisioned",
					ObservedGeneration: 9,
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "listener1",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 9,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionFalse,
							Reason:             "NoZones",
							Message:            "The record isn't present in any zones.",
							ObservedGeneration: 9,
						},
					},
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns managed with one failed zone should reflect on conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{
					{
						Name:     "listener1",
						Hostname: ptr.To(gatewayapiv1.Hostname("gwapi.example.tld")),
					}}, 10, exampleConditions...),
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
				{
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "LoadBalancerProvisioned",
					Message:            "The LoadBalancer service is provisioned",
					ObservedGeneration: 10,
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "listener1",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 10,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionFalse,
							Reason:             "FailedZones",
							Message:            "The record failed to provision in some zones: [{somezone.on.some.provider map[]}]",
							ObservedGeneration: 10,
						},
					},
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns managed with one zone in unknown state should reflect on conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{
					{
						Name:     "listener1",
						Hostname: ptr.To(gatewayapiv1.Hostname("gwapi.example.tld")),
					},
				}, 11, exampleConditions...),
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
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "LoadBalancerProvisioned",
					Message:            "The LoadBalancer service is provisioned",
					ObservedGeneration: 11,
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "listener1",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 11,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionFalse,
							Reason:             "UnknownZones",
							Message:            "Provisioning of the record is in an unknown state in some zones: [{somezone.on.some.provider map[]}]",
							ObservedGeneration: 11,
						},
					},
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns managed with one zone in valid state should reflect on conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{
					{
						Name:     "listener1",
						Hostname: ptr.To(gatewayapiv1.Hostname("gwapi.example.tld")),
					},
				}, 12, exampleConditions...),
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
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "LoadBalancerProvisioned",
					Message:            "The LoadBalancer service is provisioned",
					ObservedGeneration: 12,
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "listener1",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 12,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "NoFailedZones",
							Message:            "The record is provisioned in all reported zones.",
							ObservedGeneration: 12,
						},
					},
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns managed with two listeners, one dns record is valid in valid state and the other not should reflect the right condition",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{
					{
						Name:     "listener1",
						Hostname: ptr.To(gatewayapiv1.Hostname("gwapi.example.tld")),
					},
					{
						Name:     "listener2",
						Hostname: ptr.To(gatewayapiv1.Hostname("gwapifailed.example.tld")),
					},
				}, 13, exampleConditions...),
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
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "LoadBalancerProvisioned",
					Message:            "The LoadBalancer service is provisioned",
					ObservedGeneration: 13,
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "listener1",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 13,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "NoFailedZones",
							Message:            "The record is provisioned in all reported zones.",
							ObservedGeneration: 13,
						},
					},
				},
				{
					Name: "listener2",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 13,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionFalse,
							Reason:             "RecordNotFound",
							Message:            "The wildcard record resource was not found.",
							ObservedGeneration: 13,
						},
					},
				},
			},
			expectError: "",
		},
		{
			name: "load balancer valid, dns managed with two listeners with 'root' dot suffix and listeners with and without root suffix must set the right conditions",
			existingObjects: []runtime.Object{
				dnsConfigFn(true),
				infraConfigFn,
				gwFn("example-gateway", []gatewayapiv1.Listener{
					{
						Name:     "listener1",
						Hostname: ptr.To(gatewayapiv1.Hostname("gwapi.example.tld")),
					},
					{
						Name:     "listener2",
						Hostname: ptr.To(gatewayapiv1.Hostname("gwapi2.example.tld.")),
					},
				}, 14, exampleConditions...),
				svcFn("openshift-ingress-lb", exampleManagedGatewayLabel, corev1.LoadBalancerIngress{
					Hostname: "something.somewhere.somehow",
				}),
				dnsRecordFn("openshift-ingress-dns", "gwapi.example.tld.", iov1.ManagedDNS, exampleManagedGatewayLabel, iov1.DNSRecordStatus{
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
					},
				}),
				dnsRecordFn("openshift-ingress-dns-2", "gwapi2.example.tld", iov1.ManagedDNS, exampleManagedGatewayLabel, iov1.DNSRecordStatus{
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
					},
				}),
			},

			reconcileRequest: reqFn("openshift-ingress", "example-gateway"),
			expectedConditions: []metav1.Condition{
				{
					Type:               operatorv1.LoadBalancerReadyIngressConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "LoadBalancerProvisioned",
					Message:            "The LoadBalancer service is provisioned",
					ObservedGeneration: 14,
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "listener1",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 14,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "NoFailedZones",
							Message:            "The record is provisioned in all reported zones.",
							ObservedGeneration: 14,
						},
					},
				},
				{
					Name: "listener2",
					Conditions: []metav1.Condition{
						{
							Type:               operatorv1.DNSManagedIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "Normal",
							Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
							ObservedGeneration: 14,
						},
						{
							Type:               operatorv1.DNSReadyIngressConditionType,
							Status:             metav1.ConditionTrue,
							Reason:             "NoFailedZones",
							Message:            "The record is provisioned in all reported zones.",
							ObservedGeneration: 14,
						},
					},
				},
			},
			expectError: "",
		},
	}

	scheme := runtime.NewScheme()
	iov1.AddToScheme(scheme)
	corev1.AddToScheme(scheme)
	gatewayapiv1.Install(scheme)

	// fakeEventLister can be used to return an event based on the reconciled Gateway
	// API object.
	fakeEventLister := func(indexType string) func(o client.Object) []string {
		return func(o client.Object) []string {
			evt, ok := o.(*corev1.Event)
			if !ok {
				return []string{}
			}
			switch indexType {
			case "apiVersion":
				return []string{evt.InvolvedObject.APIVersion}
			case "kind":
				return []string{evt.InvolvedObject.Kind}
			case "uid":
				return []string{string(evt.InvolvedObject.UID)}
			case "source":
				return []string{string(evt.Source.Component)}
			default:
				return []string{}
			}
		}
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				WithStatusSubresource(&gatewayapiv1.Gateway{}).
				WithIndex(&corev1.Event{}, "involvedObject.apiVersion", fakeEventLister("apiVersion")).
				WithIndex(&corev1.Event{}, "involvedObject.kind", fakeEventLister("kind")).
				WithIndex(&corev1.Event{}, "involvedObject.uid", fakeEventLister("uid")).
				WithIndex(&corev1.Event{}, "source", fakeEventLister("source")).
				Build()
			cl := &testutil.FakeClientRecorder{
				Client:  fakeClient,
				T:       t,
				Updated: []client.Object{},
			}
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := testutil.FakeCache{Informers: &informer, Reader: cl}
			reconciler := &reconciler{
				cache:       cache,
				client:      cl,
				recorder:    record.NewFakeRecorder(1),
				eventreader: cl,
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

			if len(tc.expectedConditions) == 0 && len(tc.expectedListenerStatus) == 0 {
				return
			}

			gw := &gatewayapiv1.Gateway{}
			err = cl.Get(ctx, tc.reconcileRequest.NamespacedName, gw)
			assert.NoError(t, err)
			// Instead of diffing conditions we will try to search if the condition
			// we want exists and has the right values
			if len(tc.expectedConditions) > 0 {
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
			if len(tc.expectedListenerStatus) > 0 {
				// A map containing listener name and status to then check if expected listeners exists
				lsStatus := make(map[gatewayapiv1.SectionName]gatewayapiv1.ListenerStatus)
				for _, ls := range gw.Status.Listeners {
					lsStatus[ls.Name] = ls
				}
				for _, expectedListener := range tc.expectedListenerStatus {
					existingStatus, ok := lsStatus[expectedListener.Name]
					if !ok {
						assert.True(t, ok, "status per listener name not found")
						continue
					}
					for _, cond := range expectedListener.Conditions {
						found := condutils.FindStatusCondition(existingStatus.Conditions, cond.Type)
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
			}
		})
	}
}

// This test will verify that transition time on a listener or a loadbalancer condition
// just change if the condition has also changed
func TestReconcileTransition(t *testing.T) {
	scheme := runtime.NewScheme()
	iov1.AddToScheme(scheme)
	corev1.AddToScheme(scheme)
	gatewayapiv1.Install(scheme)

	ctx := context.Background()

	existingObjects := []runtime.Object{
		dnsConfigFn(true),
		infraConfigFn,
		gwFn("example-gateway", []gatewayapiv1.Listener{
			{
				Name:     "listener1",
				Hostname: ptr.To(gatewayapiv1.Hostname("test.example.tld")),
			},
		}, 15, exampleConditions...),
		svcFn("openshift-ingress-lb", exampleManagedGatewayLabel), // Initialize service without ingress status
	}

	fakeEventLister := func(o client.Object) []string {
		return []string{}
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(existingObjects...).
		WithStatusSubresource(&gatewayapiv1.Gateway{}).
		WithIndex(&corev1.Event{}, "involvedObject.apiVersion", fakeEventLister).
		WithIndex(&corev1.Event{}, "involvedObject.kind", fakeEventLister).
		WithIndex(&corev1.Event{}, "involvedObject.uid", fakeEventLister).
		WithIndex(&corev1.Event{}, "source", fakeEventLister).
		Build()
	cl := &testutil.FakeClientRecorder{
		Client:  fakeClient,
		T:       t,
		Updated: []client.Object{},
	}
	informer := informertest.FakeInformers{Scheme: scheme}
	cache := testutil.FakeCache{Informers: &informer, Reader: cl}
	reconciler := &reconciler{
		cache:       cache,
		client:      cl,
		recorder:    record.NewFakeRecorder(10000),
		eventreader: cl,
	}

	reconcileRequest := reqFn("openshift-ingress", "example-gateway")

	var initialtime *metav1.Time

	t.Run("initial reconciliation should add conditions with timestamps", func(t *testing.T) {
		_, err := reconciler.Reconcile(ctx, reconcileRequest)
		require.NoError(t, err)
		gw := &gatewayapiv1.Gateway{}
		err = cl.Get(ctx, reconcileRequest.NamespacedName, gw)
		require.NoError(t, err)

		expectedCondition := metav1.Condition{
			Type:               operatorv1.LoadBalancerReadyIngressConditionType,
			Status:             metav1.ConditionFalse,
			Reason:             "LoadBalancerPending",
			Message:            "The LoadBalancer service is pending",
			ObservedGeneration: 15,
		}

		found := condutils.FindStatusCondition(gw.Status.Conditions, expectedCondition.Type)
		require.NotNil(t, found, "condition not found %+v", expectedCondition)
		require.NotNil(t, found.LastTransitionTime)
		expectedCondition.LastTransitionTime = found.LastTransitionTime
		assert.Equal(t, expectedCondition, *found, "conditions are not equal")
		initialtime = &found.LastTransitionTime
	})

	t.Run("calling the reconciliation again should not change the transition time", func(t *testing.T) {
		_, err := reconciler.Reconcile(ctx, reconcileRequest)
		require.NoError(t, err)
		gw := &gatewayapiv1.Gateway{}
		err = cl.Get(ctx, reconcileRequest.NamespacedName, gw)
		require.NoError(t, err)

		expectedCondition := metav1.Condition{
			Type:               operatorv1.LoadBalancerReadyIngressConditionType,
			Status:             metav1.ConditionFalse,
			Reason:             "LoadBalancerPending",
			Message:            "The LoadBalancer service is pending",
			ObservedGeneration: 15,
			LastTransitionTime: *initialtime,
		}
		found := condutils.FindStatusCondition(gw.Status.Conditions, expectedCondition.Type)
		require.NotNil(t, found, "condition not found %+v", expectedCondition)
		require.NotNil(t, found.LastTransitionTime)
		assert.Equal(t, expectedCondition, *found, "conditions are not equal")
	})

	time.Sleep(time.Second) // Add a sleep because the transition time does not have microseconds...
	t.Run("changing the loadbalancer correctly and calling the reconciliation should change the condition and transition time", func(t *testing.T) {
		existingSvc := &corev1.Service{}
		err := cl.Get(ctx, types.NamespacedName{
			Namespace: "openshift-ingress",
			Name:      "openshift-ingress-lb",
		}, existingSvc)
		require.NoError(t, err)
		newObject := existingSvc.DeepCopy()
		newObject.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
			{
				Hostname: "something.somewhere.somehow",
			},
		}
		require.NoError(t, cl.Status().Patch(ctx, newObject, client.MergeFrom(existingSvc)))

		_, err = reconciler.Reconcile(ctx, reconcileRequest)
		require.NoError(t, err)
		gw := &gatewayapiv1.Gateway{}
		err = cl.Get(ctx, reconcileRequest.NamespacedName, gw)
		require.NoError(t, err)

		expectedCondition := metav1.Condition{
			Type:               operatorv1.LoadBalancerReadyIngressConditionType,
			Status:             metav1.ConditionTrue,
			Reason:             "LoadBalancerProvisioned",
			Message:            "The LoadBalancer service is provisioned",
			ObservedGeneration: 15,
		}
		found := condutils.FindStatusCondition(gw.Status.Conditions, expectedCondition.Type)
		expectedCondition.LastTransitionTime = found.LastTransitionTime // Fix just so this wont break on equality check
		require.NotNil(t, found, "condition not found %+v", expectedCondition)
		require.NotNil(t, found.LastTransitionTime)
		require.True(t, found.LastTransitionTime.After(initialtime.Time))
		assert.Equal(t, expectedCondition, *found, "conditions are not equal")
	})

}

func Test_gatewayFromResourceLabel(t *testing.T) {
	tests := []struct {
		name string
		o    client.Object
		want []reconcile.Request
	}{
		{
			name: "no labels should return empty",
			o:    &corev1.Secret{},
			want: []reconcile.Request{},
		},
		{
			name: "no matching labels should return empty",
			o: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"gateway.networking.k8s.io/gateway-name": "",
					},
				},
			},
			want: []reconcile.Request{},
		},
		{
			name: "matching labels should a reconciliation for resource on the same namespace",
			o: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"gateway.networking.k8s.io/gateway-name": "my-gateway",
					},
					Namespace: "somenamespace",
				},
			},
			want: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: "somenamespace",
						Name:      "my-gateway",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gatewayFromResourceLabel(context.Background(), tt.o)
			assert.Equal(t, tt.want, got, "returned resource reconciliation does not match")
		})
	}
}
