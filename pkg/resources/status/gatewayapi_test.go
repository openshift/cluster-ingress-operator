package status_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/resources/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
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

	listenerToHostname = map[gatewayapiv1.SectionName]gatewayapiv1.Hostname{
		"wildcard":    "*.example.com",
		"unexistent":  "unexistent.example.com", // This domain has no match on dnsrecord so dnsrecord will be null
		"unmanaged":   "unmanaged.example.com",
		"nozones":     "nozones.example.com",
		"noname":      "noname.example.com",
		"failedzone":  "failedzone.example.com",
		"unknownzone": "unknownzone.example.com",
	}

	hostnameToDNSRecord = map[string]*iov1.DNSRecord{
		"*.example.com": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "wildcard",
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
		"unmanaged.example.com": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "unmanaged",
			},
			Spec: iov1.DNSRecordSpec{
				DNSManagementPolicy: iov1.UnmanagedDNS,
			},
		},
		"nozones.example.com": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "nozones",
			},
			Spec: iov1.DNSRecordSpec{
				DNSManagementPolicy: iov1.ManagedDNS,
			},
			Status: iov1.DNSRecordStatus{
				Zones: []iov1.DNSZoneStatus{},
			},
		},
		"failedzone.example.com": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "failedzone",
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
		"noname.example.com": {
			Status: iov1.DNSRecordStatus{
				Zones: []iov1.DNSZoneStatus{},
			},
		},
		"unknownzone.example.com": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "unknownzone",
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
	}
)

func TestComputeGatewayAPIListenerDNSStatus(t *testing.T) {
	tests := []struct {
		name                   string
		listenerToHostname     map[gatewayapiv1.SectionName]gatewayapiv1.Hostname
		hostnameToDNSRecord    map[string]*iov1.DNSRecord
		gwstatus               *gatewayapiv1.GatewayStatus
		dnsConfig              *configv1.DNS
		generation             int64
		expectedListenerStatus []gatewayapiv1.ListenerStatus
	}{
		{
			name:               "a null listenerToHostname map should not mutate the listeners status",
			generation:         1,
			listenerToHostname: nil,
			gwstatus: &gatewayapiv1.GatewayStatus{
				Listeners: []gatewayapiv1.ListenerStatus{
					{
						Name: "wildcard",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond",
								Reason: "somereason",
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "wildcard",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond",
							Reason: "somereason",
							Status: metav1.ConditionTrue,
						},
					},
				},
			},
		},
		{
			name:                "a null DNSConfig should return DNSReady=False and NoDNSZones",
			dnsConfig:           nil,
			generation:          2,
			listenerToHostname:  listenerToHostname,
			hostnameToDNSRecord: hostnameToDNSRecord,
			gwstatus: &gatewayapiv1.GatewayStatus{
				Listeners: []gatewayapiv1.ListenerStatus{
					{
						Name: "wildcard",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond",
								Reason: "somereason",
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "wildcard",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond",
							Reason: "somereason",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "NoDNSZones",
							Status:             metav1.ConditionFalse,
							Message:            "No DNS zones are defined in the cluster dns config.",
							ObservedGeneration: 2,
						},
					},
				},
			},
		},
		{
			name:                "a null DNSRecord (unexistent) should return DNSReady=False",
			dnsConfig:           defaultDNSConfig,
			generation:          3,
			listenerToHostname:  listenerToHostname,
			hostnameToDNSRecord: hostnameToDNSRecord,
			gwstatus: &gatewayapiv1.GatewayStatus{
				Listeners: []gatewayapiv1.ListenerStatus{
					{
						Name: "unexistent",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond2",
								Reason: "somereason2",
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "unexistent",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond2",
							Reason: "somereason2",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "RecordNotFound",
							Status:             metav1.ConditionFalse,
							Message:            "The wildcard record resource was not found.",
							ObservedGeneration: 3,
						},
					},
				},
			},
		},
		{
			name:                "a null DNSRecord (no name) should return DNSReady=False",
			dnsConfig:           defaultDNSConfig,
			generation:          4,
			listenerToHostname:  listenerToHostname,
			hostnameToDNSRecord: hostnameToDNSRecord,
			gwstatus: &gatewayapiv1.GatewayStatus{
				Listeners: []gatewayapiv1.ListenerStatus{
					{
						Name: "noname",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond3",
								Reason: "somereason3",
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "noname",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond3",
							Reason: "somereason3",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "RecordNotFound",
							Status:             metav1.ConditionFalse,
							Message:            "The wildcard record resource was not found.",
							ObservedGeneration: 4,
						},
					},
				},
			},
		},
		{
			name:                "an unmanaged dnsrecord should return DNSReady=False with Reason=UnmanagedDNS",
			dnsConfig:           defaultDNSConfig,
			generation:          5,
			listenerToHostname:  listenerToHostname,
			hostnameToDNSRecord: hostnameToDNSRecord,
			gwstatus: &gatewayapiv1.GatewayStatus{
				Listeners: []gatewayapiv1.ListenerStatus{
					{
						Name: "unmanaged",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond3",
								Reason: "somereason3",
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "unmanaged",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond3",
							Reason: "somereason3",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "UnmanagedDNS",
							Status:             metav1.ConditionUnknown,
							Message:            "The DNS management policy is set to Unmanaged.",
							ObservedGeneration: 5,
						},
					},
				},
			},
		},
		// This test verifies 2 listeners, one should be unmanaged, the other should have no zones
		{
			name:                "a dnsrecord without zones should return DNSReady=False with Reason=NoZones",
			dnsConfig:           defaultDNSConfig,
			generation:          5,
			listenerToHostname:  listenerToHostname,
			hostnameToDNSRecord: hostnameToDNSRecord,
			gwstatus: &gatewayapiv1.GatewayStatus{
				Listeners: []gatewayapiv1.ListenerStatus{
					{
						Name: "unmanaged",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond3",
								Reason: "somereason3",
								Status: metav1.ConditionTrue,
							},
						},
					},
					{
						Name: "nozones",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond4",
								Reason: "somereason4",
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "unmanaged",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond3",
							Reason: "somereason3",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "UnmanagedDNS",
							Status:             metav1.ConditionUnknown,
							Message:            "The DNS management policy is set to Unmanaged.",
							ObservedGeneration: 5,
						},
					},
				},
				{
					Name: "nozones",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond4",
							Reason: "somereason4",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "NoZones",
							Status:             metav1.ConditionFalse,
							Message:            "The record isn't present in any zones.",
							ObservedGeneration: 5,
						},
					},
				},
			},
		},
		{
			name:                "a dnsrecord with failed zones should return DNSReady=False with Reason=FailedZones",
			dnsConfig:           defaultDNSConfig,
			generation:          6,
			listenerToHostname:  listenerToHostname,
			hostnameToDNSRecord: hostnameToDNSRecord,
			gwstatus: &gatewayapiv1.GatewayStatus{
				Listeners: []gatewayapiv1.ListenerStatus{
					{
						Name: "nozones",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond6",
								Reason: "somereason6",
								Status: metav1.ConditionTrue,
							},
						},
					},
					{
						Name: "failedzone",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond6",
								Reason: "somereason6",
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "nozones",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond6",
							Reason: "somereason6",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "NoZones",
							Status:             metav1.ConditionFalse,
							Message:            "The record isn't present in any zones.",
							ObservedGeneration: 6,
						},
					},
				},
				{
					Name: "failedzone",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond6",
							Reason: "somereason6",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "FailedZones",
							Status:             metav1.ConditionFalse,
							Message:            "The record failed to provision in some zones: [{xxxx map[]}]",
							ObservedGeneration: 6,
						},
					},
				},
			},
		},
		{
			name:                "a dnsrecord with unknown zones should return DNSReady=False with Reason=UnknownZones",
			dnsConfig:           defaultDNSConfig,
			generation:          7,
			listenerToHostname:  listenerToHostname,
			hostnameToDNSRecord: hostnameToDNSRecord,
			gwstatus: &gatewayapiv1.GatewayStatus{
				Listeners: []gatewayapiv1.ListenerStatus{
					{
						Name: "unknownzone",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond7",
								Reason: "somereason7",
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "unknownzone",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond7",
							Reason: "somereason7",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "UnknownZones",
							Status:             metav1.ConditionFalse,
							Message:            "Provisioning of the record is in an unknown state in some zones: [{xxxx map[]}]",
							ObservedGeneration: 7,
						},
					},
				},
			},
		},
		// This test checks for 2 listeners, one should be working, the other one not. This shows status
		// being granular to listeners
		{
			name:                "a dnsrecord with valid zones should return DNSReady=True with Reason=NoFailedZones",
			dnsConfig:           defaultDNSConfig,
			generation:          8,
			listenerToHostname:  listenerToHostname,
			hostnameToDNSRecord: hostnameToDNSRecord,
			gwstatus: &gatewayapiv1.GatewayStatus{
				Listeners: []gatewayapiv1.ListenerStatus{
					{
						Name: "unknownzone",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond8",
								Reason: "somereason8",
								Status: metav1.ConditionTrue,
							},
						},
					},
					{
						Name: "wildcard",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond8",
								Reason: "somereason8",
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "unknownzone",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond8",
							Reason: "somereason8",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "UnknownZones",
							Status:             metav1.ConditionFalse,
							Message:            "Provisioning of the record is in an unknown state in some zones: [{xxxx map[]}]",
							ObservedGeneration: 8,
						},
					},
				},
				{
					Name: "wildcard",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond8",
							Reason: "somereason8",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "NoFailedZones",
							Status:             metav1.ConditionTrue,
							Message:            "The record is provisioned in all reported zones.",
							ObservedGeneration: 8,
						},
					},
				},
			},
		},
		{
			name:                "a dnsrecord that has DNS status should have the condition removed in case listener does not have DNSRecord anymore",
			dnsConfig:           defaultDNSConfig,
			generation:          9,
			listenerToHostname:  listenerToHostname,
			hostnameToDNSRecord: hostnameToDNSRecord,
			gwstatus: &gatewayapiv1.GatewayStatus{
				Listeners: []gatewayapiv1.ListenerStatus{
					{
						Name: "goawaycondition", // This listener does not exist on listenerToHostname, meaning it doesn't have a hostname anymore
						Conditions: []metav1.Condition{
							{
								Type:   "somecond9",
								Reason: "somereason9",
								Status: metav1.ConditionTrue,
							},
							{
								Type:               "DNSReady",
								Reason:             "NoFailedZones",
								Status:             metav1.ConditionTrue,
								Message:            "The record is provisioned in all reported zones.",
								ObservedGeneration: 9,
							},
						},
					},
					{
						Name: "wildcard",
						Conditions: []metav1.Condition{
							{
								Type:   "somecond9",
								Reason: "somereason9",
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectedListenerStatus: []gatewayapiv1.ListenerStatus{
				{
					Name: "goawaycondition",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond9",
							Reason: "somereason9",
							Status: metav1.ConditionTrue,
						},
					},
				},
				{
					Name: "wildcard",
					Conditions: []metav1.Condition{
						{
							Type:   "somecond9",
							Reason: "somereason9",
							Status: metav1.ConditionTrue,
						},
						{
							Type:               "DNSReady",
							Reason:             "NoFailedZones",
							Status:             metav1.ConditionTrue,
							Message:            "The record is provisioned in all reported zones.",
							ObservedGeneration: 9,
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status.ComputeGatewayAPIListenerDNSStatus(tt.dnsConfig, tt.generation, tt.gwstatus, tt.listenerToHostname, tt.hostnameToDNSRecord)
			conditionsCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.EquateEmpty(),
				cmpopts.SortSlices(func(a, b gatewayapiv1.ListenerStatus) bool { return a.Name < b.Name }),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			}
			if !cmp.Equal(tt.gwstatus.Listeners, tt.expectedListenerStatus, conditionsCmpOpts...) {
				t.Errorf("diff: %s", cmp.Diff(tt.expectedListenerStatus, tt.gwstatus.Listeners, conditionsCmpOpts...))
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
			name: "nameless service should return LoadBalancerReady=False with reason=ServiceNotFound",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: "",
				},
			},
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
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			}
			if !cmp.Equal(conditions, tt.expectedConditions, conditionsCmpOpts...) {
				t.Fatalf("expected:\n%#v\ngot:\n%#v", tt.expectedConditions, conditions)
			}
		})
	}
}
