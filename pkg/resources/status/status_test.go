package status

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func Test_computeLoadBalancerStatus(t *testing.T) {
	tests := []struct {
		name       string
		controller *operatorv1.IngressController
		service    *corev1.Service
		events     []corev1.Event
		expect     []operatorv1.OperatorCondition
	}{
		{
			name:       "lb provisioned",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			service:    provisionedLBservice("default"),
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy", clock.Now()),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionTrue, "LoadBalancerProvisioned", clock.Now()),
			},
		},
		{
			name:       "no events for current lb",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			service:    pendingLBService("default", "1"),
			events: []corev1.Event{
				schedulerEvent(),
				failedCreateLBEvent("secondary", "2"),
				failedCreateLBEvent("default", "3"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy", clock.Now()),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "LoadBalancerPending", clock.Now()),
			},
		},
		{
			name:       "lb pending, create failed events",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			service:    pendingLBService("default", "1"),
			events: []corev1.Event{
				schedulerEvent(),
				failedCreateLBEvent("secondary", "3"),
				failedCreateLBEvent("default", "1"),
			},
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy", clock.Now()),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "SyncLoadBalancerFailed", clock.Now()),
			},
		},
		{
			name:       "unmanaged",
			controller: ingressController("default", operatorv1.HostNetworkStrategyType),
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionFalse, "EndpointPublishingStrategyExcludesManagedLoadBalancer", clock.Now()),
			},
		},
		{
			name:       "lb service missing",
			controller: ingressController("default", operatorv1.LoadBalancerServiceStrategyType),
			expect: []operatorv1.OperatorCondition{
				cond(operatorv1.LoadBalancerManagedIngressConditionType, operatorv1.ConditionTrue, "WantedByEndpointPublishingStrategy", clock.Now()),
				cond(operatorv1.LoadBalancerReadyIngressConditionType, operatorv1.ConditionFalse, "ServiceNotFound", clock.Now()),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := ComputeLoadBalancerStatus(test.controller, test.service, test.events, false)

			conditionsCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "LastTransitionTime", "Message"),
				cmpopts.EquateEmpty(),
				cmpopts.SortSlices(func(a, b operatorv1.OperatorCondition) bool { return a.Type < b.Type }),
			}
			if !cmp.Equal(actual, test.expect, conditionsCmpOpts...) {
				t.Fatalf("expected:\n%#v\ngot:\n%#v", test.expect, actual)
			}
		})
	}
}

func Test_computeDNSStatus(t *testing.T) {
	tests := []struct {
		name           string
		controller     *operatorv1.IngressController
		record         *iov1.DNSRecord
		platformStatus *configv1.PlatformStatus
		dnsConfig      *configv1.DNS
		expect         []operatorv1.OperatorCondition
	}{
		{
			name: "DNSManaged false due to NoDNSZones",
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					PublicZone:  nil,
					PrivateZone: nil,
				},
			},
			expect: []operatorv1.OperatorCondition{{
				Type:   "DNSManaged",
				Status: operatorv1.ConditionFalse,
				Reason: "NoDNSZones",
			}},
		},
		{
			name: "DNSManaged false due to UnsupportedEndpointPublishingStrategy",
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.HostNetworkStrategyType,
					},
				},
			},
			expect: []operatorv1.OperatorCondition{{
				Type:   "DNSManaged",
				Status: operatorv1.ConditionFalse,
				Reason: "UnsupportedEndpointPublishingStrategy",
			}},
		},
		{
			name: "DNSManaged false due to UnmanagedLoadBalancerDNS",
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.UnmanagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.UnmanagedDNS,
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionFalse,
					Reason: "UnmanagedLoadBalancerDNS",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionUnknown,
					Reason: "UnmanagedDNS",
				},
			},
		},
		{
			name: "DNSManaged true due to dnsManagementPolicy=Managed, and DNSReady is true due to NoFailedZones",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionTrue),
									LastTransitionTime: metav1.Now(),
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain:  "basedomain.com",
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionTrue,
					Reason: "NoFailedZones",
				},
			},
		},
		{
			name: "DNSManaged true due to nil status.endpointPublishingStrategy.loadBalancer, and DNSReady is true due to NoFailedZones",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type:         operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: nil,
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionTrue),
									LastTransitionTime: metav1.Now(),
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain:  "basedomain.com",
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionTrue,
					Reason: "NoFailedZones",
				},
			},
		},
		{
			name: "DNSManaged true but DNSReady is false due to RecordNotFound",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: nil,
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain:  "basedomain.com",
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionFalse,
					Reason: "RecordNotFound",
				},
			},
		},
		{
			name: "DNSManaged true but DNSReady is false due to NoZones",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain:  "basedomain.com",
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionFalse,
					Reason: "NoZones",
				},
			},
		},
		{
			name: "DNSManaged true but DNSReady is Unknown due to UnmanagedDNS",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.UnmanagedDNS,
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain:  "basedomain.com",
					PublicZone:  &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionUnknown,
					Reason: "UnmanagedDNS",
				},
			},
		},
		{
			name: "DNSManaged true and DNSReady is false due to FailedZones",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionFalse),
									LastTransitionTime: metav1.Now(),
									Reason:             "FailedZones",
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: "basedomain.com",
					PublicZone: &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{
						ID: "zone1",
					},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionFalse,
					Reason: "FailedZones",
				},
			},
		},
		{
			name: "DNSManaged true and DNSReady is true with even with previously FailedZones",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordFailedConditionType,
									Status:             string(operatorv1.ConditionTrue),
									LastTransitionTime: metav1.NewTime(time.Now().Add(5 * time.Minute)),
									Reason:             "FailedZones",
								},
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionTrue),
									LastTransitionTime: metav1.NewTime(time.Now().Add(15 * time.Minute)),
									Reason:             "NoFailedZones",
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: "basedomain.com",
					PublicZone: &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{
						ID: "zone1",
					},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionTrue,
					Reason: "NoFailedZones",
				},
			},
		},
		{
			name: "DNSManaged true and DNSReady is false due to unknown condition",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionUnknown),
									LastTransitionTime: metav1.NewTime(time.Now().Add(15 * time.Minute)),
									Reason:             "UnknownZones",
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: "basedomain.com",
					PublicZone: &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{
						ID: "zone1",
					},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionFalse,
					Reason: "UnknownZones",
				},
			},
		},
		{
			// This text checks if precedence is given to failed zones over unknown zones.
			name: "DNSManaged true and DNSReady is false due to failed and unknown conditions",
			controller: &operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Domain: "apps.basedomain.com",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type: operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							DNSManagementPolicy: operatorv1.ManagedLoadBalancerDNS,
						},
					},
				},
			},
			record: &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{
					Zones: []iov1.DNSZoneStatus{
						{
							DNSZone: configv1.DNSZone{ID: "zone1"},
							Conditions: []iov1.DNSZoneCondition{
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionUnknown),
									LastTransitionTime: metav1.NewTime(time.Now().Add(15 * time.Minute)),
									Reason:             "UnknownZones",
								},
								{
									Type:               iov1.DNSRecordPublishedConditionType,
									Status:             string(operatorv1.ConditionFalse),
									LastTransitionTime: metav1.NewTime(time.Now().Add(15 * time.Minute)),
									Reason:             "FailedZones",
								},
							},
						},
					},
				},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
			dnsConfig: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: "basedomain.com",
					PublicZone: &configv1.DNSZone{},
					PrivateZone: &configv1.DNSZone{
						ID: "zone1",
					},
				},
			},
			expect: []operatorv1.OperatorCondition{
				{
					Type:   "DNSManaged",
					Status: operatorv1.ConditionTrue,
					Reason: "Normal",
				},
				{
					Type:   "DNSReady",
					Status: operatorv1.ConditionFalse,
					Reason: "FailedZones",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actualConditions := ComputeDNSStatus(tc.controller, tc.record, tc.platformStatus, tc.dnsConfig, false)
			opts := cmpopts.IgnoreFields(operatorv1.OperatorCondition{}, "Message", "LastTransitionTime")
			if !cmp.Equal(actualConditions, tc.expect, opts) {
				t.Fatalf("found diff between actual and expected operator condition:\n%s", cmp.Diff(actualConditions, tc.expect, opts))
			}
		})
	}
}

func ingressController(name string, t operatorv1.EndpointPublishingStrategyType) *operatorv1.IngressController {
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: t,
			},
		},
	}
}

func pendingLBService(owner string, UID types.UID) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: owner,
			Labels: map[string]string{
				manifests.OwningIngressControllerLabel: owner,
			},
			UID: UID,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
		},
	}
}

func provisionedLBservice(owner string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: owner,
			Labels: map[string]string{
				manifests.OwningIngressControllerLabel: owner,
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{Hostname: "lb.cloudprovider.example.com"},
				},
			},
		},
	}
}

func failedCreateLBEvent(service string, UID types.UID) corev1.Event {
	return corev1.Event{
		Type:    "Warning",
		Reason:  "SyncLoadBalancerFailed",
		Message: "failed to ensure load balancer for service openshift-ingress/router-default: TooManyLoadBalancers: Exceeded quota of account",
		Source: corev1.EventSource{
			Component: "service-controller",
		},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Service",
			Name: service,
			UID:  UID,
		},
	}
}

func schedulerEvent() corev1.Event {
	return corev1.Event{
		Type:   "Normal",
		Reason: "Scheduled",
		Source: corev1.EventSource{
			Component: "default-scheduler",
		},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod",
			Name: "router-default-1",
		},
	}
}

func cond(t string, status operatorv1.ConditionStatus, reason string, lt time.Time) operatorv1.OperatorCondition {
	return operatorv1.OperatorCondition{
		Type:               t,
		Status:             status,
		Reason:             reason,
		LastTransitionTime: metav1.NewTime(lt),
	}
}
