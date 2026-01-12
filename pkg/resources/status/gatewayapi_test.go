package status_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/resources/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultZoneID = "xxxx"
)

var (
	defaultDNSConfig = &configv1.DNS{
		Spec: configv1.DNSSpec{
			PublicZone: &configv1.DNSZone{
				ID: defaultZoneID,
			},
		},
	}
)

func TestComputeGatewayAPIDNSStatus(t *testing.T) {
	tests := []struct {
		name               string
		dnsRecord          *iov1.DNSRecord
		dnsConfig          *configv1.DNS
		generation         int64
		expectedConditions []metav1.Condition
	}{
		{
			name:       "a null dnsconfig should return DNSManaged=False and NoDNSZones",
			dnsConfig:  nil,
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               "DNSManaged",
					Status:             "False",
					Reason:             "NoDNSZones",
					Message:            "No DNS zones are defined in the cluster dns config.",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name:       "a null dnsrecord should return DNSManaged=True and DNSReady=False",
			dnsConfig:  defaultDNSConfig,
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               "DNSManaged",
					Status:             "True",
					Reason:             "Normal",
					Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
					ObservedGeneration: 1,
				},
				{
					Type:               "DNSReady",
					Status:             "False",
					Reason:             "RecordNotFound",
					Message:            "The wildcard record resource was not found.",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name:      "an unmanaged dnsrecord should return DNSManaged=True and DNSReady=False with Reason=UnmanagedDNS",
			dnsConfig: defaultDNSConfig,
			dnsRecord: &iov1.DNSRecord{
				ObjectMeta: metav1.ObjectMeta{
					Name: "somedns",
				},
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.UnmanagedDNS,
				},
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               "DNSManaged",
					Status:             "True",
					Reason:             "Normal",
					Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
					ObservedGeneration: 1,
				},
				{
					Type:               "DNSReady",
					Status:             "Unknown",
					Reason:             "UnmanagedDNS",
					Message:            "The DNS management policy is set to Unmanaged.",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name:      "a dnsrecord without zones should return DNSManaged=True and DNSReady=False with Reason=NoZones",
			dnsConfig: defaultDNSConfig,
			dnsRecord: &iov1.DNSRecord{
				ObjectMeta: metav1.ObjectMeta{
					Name: "somedns",
				},
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{},
				},
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               "DNSManaged",
					Status:             "True",
					Reason:             "Normal",
					Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
					ObservedGeneration: 1,
				},
				{
					Type:               "DNSReady",
					Status:             "False",
					Reason:             "NoZones",
					Message:            "The record isn't present in any zones.",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name:      "a dnsrecord with failed zones should return DNSManaged=True and DNSReady=False with Reason=FailedZones",
			dnsConfig: defaultDNSConfig,
			dnsRecord: &iov1.DNSRecord{
				ObjectMeta: metav1.ObjectMeta{
					Name: "somedns",
				},
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{
								ID: defaultZoneID,
							},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:   "Published",
									Status: "False",
								},
							},
						},
					},
				},
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               "DNSManaged",
					Status:             "True",
					Reason:             "Normal",
					Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
					ObservedGeneration: 1,
				},
				{
					Type:               "DNSReady",
					Status:             "False",
					Reason:             "FailedZones",
					Message:            "The record failed to provision in some zones: [{xxxx map[]}]",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name:      "a dnsrecord with unknown zones should return DNSManaged=True and DNSReady=False with Reason=UnknownZones",
			dnsConfig: defaultDNSConfig,
			dnsRecord: &iov1.DNSRecord{
				ObjectMeta: metav1.ObjectMeta{
					Name: "somedns",
				},
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{
								ID: defaultZoneID,
							},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:   "Published",
									Status: "Unknown",
								},
							},
						},
					},
				},
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               "DNSManaged",
					Status:             "True",
					Reason:             "Normal",
					Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
					ObservedGeneration: 1,
				},
				{
					Type:               "DNSReady",
					Status:             "False",
					Reason:             "UnknownZones",
					Message:            "Provisioning of the record is in an unknown state in some zones: [{xxxx map[]}]",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name:      "a dnsrecord with valid zones should return DNSManaged=True and DNSReady=False with Reason=NoFailedZones",
			dnsConfig: defaultDNSConfig,
			dnsRecord: &iov1.DNSRecord{
				ObjectMeta: metav1.ObjectMeta{
					Name: "somedns",
				},
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{
								ID: defaultZoneID,
							},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:   "Published",
									Status: "True",
								},
							},
						},
					},
				},
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               "DNSManaged",
					Status:             "True",
					Reason:             "Normal",
					Message:            "DNS management is supported and zones are specified in the cluster DNS config.",
					ObservedGeneration: 1,
				},
				{
					Type:               "DNSReady",
					Status:             "True",
					Reason:             "NoFailedZones",
					Message:            "The record is provisioned in all reported zones.",
					ObservedGeneration: 1,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conditions := make([]metav1.Condition, 0)
			status.ComputeGatewayAPIDNSStatus(tt.dnsRecord, tt.dnsConfig, tt.generation, &conditions)
			conditionsCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.EquateEmpty(),
				cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
			}
			if !cmp.Equal(conditions, tt.expectedConditions, conditionsCmpOpts...) {
				t.Fatalf("expected:\n%#v\ngot:\n%#v", tt.expectedConditions, conditions)
			}
		})
	}
}

func TestComputeGatewayAPILoadBalancerStatus(t *testing.T) {
	tests := []struct {
		name               string
		service            *corev1.Service
		events             []corev1.Event
		generation         int64
		expectedConditions []metav1.Condition
	}{
		{
			name:       "null service should return LoadBalancerReady=False with reason=ServiceNotFound",
			service:    nil,
			generation: 1,
			events:     []corev1.Event{},
			expectedConditions: []metav1.Condition{
				{
					Type:               "LoadBalancerReady",
					Status:             "False",
					Reason:             "ServiceNotFound",
					Message:            "The LoadBalancer service resource is missing",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name: "service provisioned and with status should return LoadBalancerReady=True with reason=LoadBalancerProvisioned",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: "somesvc",
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{
								Hostname: "xpto.tld",
							},
						},
					},
				},
			},
			generation: 1,
			events:     []corev1.Event{},
			expectedConditions: []metav1.Condition{
				{
					Type:               "LoadBalancerReady",
					Status:             "True",
					Reason:             "LoadBalancerProvisioned",
					Message:            "The LoadBalancer service is provisioned",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name: "service with missing status and missing events should return LoadBalancerReady=False with reason=LoadBalancerPending",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: "somesvc",
				},
				Status: corev1.ServiceStatus{},
			},
			generation: 1,
			events:     []corev1.Event{},
			expectedConditions: []metav1.Condition{
				{
					Type:               "LoadBalancerReady",
					Status:             "False",
					Reason:             "LoadBalancerPending",
					Message:            "The LoadBalancer service is pending",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name: "service with missing status and related events should return LoadBalancerReady=False with reason=SyncLoadBalancerFailed",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "someservice",
					Namespace: "somens",
					UID:       "2",
				},
				Status: corev1.ServiceStatus{},
			},
			generation: 1,
			events: []corev1.Event{
				{
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Service",
						Namespace: "somens",
						Name:      "someservice",
						UID:       "2",
					},
					Source: corev1.EventSource{
						Component: "service-controller",
					},
					Reason:  "SyncLoadBalancerFailed",
					Message: "failed",
				},
			},
			expectedConditions: []metav1.Condition{
				{
					Type:               "LoadBalancerReady",
					Status:             "False",
					Reason:             "SyncLoadBalancerFailed",
					Message:            "The service-controller component is reporting SyncLoadBalancerFailed events like: failed\nThe cloud-controller-manager logs may contain more details.",
					ObservedGeneration: 1,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conditions := make([]metav1.Condition, 0)
			status.ComputeGatewayAPILoadBalancerStatus(tt.service, tt.events, tt.generation, &conditions)
			conditionsCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.EquateEmpty(),
				cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
			}
			if !cmp.Equal(conditions, tt.expectedConditions, conditionsCmpOpts...) {
				t.Fatalf("expected:\n%#v\ngot:\n%#v", tt.expectedConditions, conditions)
			}
		})
	}
}
