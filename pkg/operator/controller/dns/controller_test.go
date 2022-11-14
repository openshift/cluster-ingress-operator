package dns

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPublishRecordToZones(t *testing.T) {
	tests := []struct {
		name         string
		zones        []configv1.DNSZone
		unManagedDNS bool
		expect       []iov1.DNSZoneStatus
	}{
		{
			name: "testing for a successful update of 1 record",
			zones: []configv1.DNSZone{
				{ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io"},
			},
			expect: []iov1.DNSZoneStatus{{
				DNSZone: configv1.DNSZone{
					ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io",
				},
				Conditions: []iov1.DNSZoneCondition{
					{
						Type:   "Published",
						Status: "True",
					},
				},
			}},
		},
		{
			name:   "when no zones available",
			zones:  []configv1.DNSZone{},
			expect: nil,
		},
		{
			name: "when one zone available and one not available",
			zones: []configv1.DNSZone{
				{ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io"},
				{ID: ""},
			},
			expect: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io"},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "True",
						},
					},
				},
				{
					DNSZone: configv1.DNSZone{ID: ""},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "True",
						},
					},
				},
			},
		},
		{
			name: "when one zone available and one not available and unmanaged DNS policy",
			zones: []configv1.DNSZone{
				{ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io"},
				{ID: ""},
			},
			unManagedDNS: true,
			expect: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io"},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "Unknown",
						},
					},
				},
				{
					DNSZone: configv1.DNSZone{ID: ""},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "Unknown",
						},
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSName:             "subdomain.dnszone.io.",
					RecordType:          iov1.ARecordType,
					DNSManagementPolicy: iov1.ManagedDNS,
					Targets:             []string{"55.11.22.33"},
				},
			}
			if test.unManagedDNS {
				record.Spec.DNSManagementPolicy = iov1.UnmanagedDNS
			}
			r := &reconciler{
				// TODO To write a fake provider that can return errors and add more test cases.
				dnsProvider: &dns.FakeProvider{},
			}

			_, actual := r.publishRecordToZones(test.zones, record)
			opts := cmpopts.IgnoreFields(iov1.DNSZoneCondition{}, "Reason", "Message", "LastTransitionTime")
			if !cmp.Equal(actual, test.expect, opts) {
				t.Fatalf("found diff between actual and expected:\n%s", cmp.Diff(actual, test.expect, opts))
			}
		})
	}
}

// TestPublishRecordToZonesMergesStatus verifies that publishRecordToZones
// correctly merges status updates.
func TestPublishRecordToZonesMergesStatus(t *testing.T) {
	testCases := []struct {
		description     string
		oldZoneStatuses []iov1.DNSZoneStatus
		expectChange    bool
	}{
		{
			description:  "update if old value does not have the zone",
			expectChange: true,
		},
		{
			description: "update if value does not have the condition",
			oldZoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone2",
					},
					Conditions: []iov1.DNSZoneCondition{},
				},
			},
			expectChange: true,
		},
		{
			description: "update if old value has a non-matching zone",
			oldZoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "True",
						},
					},
				},
			},
			expectChange: true,
		},
		{
			description: "update if status condition changes",
			oldZoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone2",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "False",
						},
					},
				},
			},
			expectChange: true,
		},
		{
			description: "no update if status condition does not change",
			oldZoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone2",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:    "Published",
							Status:  "True",
							Reason:  "ProviderSuccess",
							Message: "The DNS provider succeeded in ensuring the record",
						},
					},
				},
			},
			expectChange: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			record := &iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSManagementPolicy: iov1.ManagedDNS,
				},
				Status: iov1.DNSRecordStatus{Zones: tc.oldZoneStatuses},
			}
			r := &reconciler{dnsProvider: &dns.FakeProvider{}}
			zone := []configv1.DNSZone{{ID: "zone2"}}
			oldStatuses := record.Status.DeepCopy().Zones
			_, newStatuses := r.publishRecordToZones(zone, record)
			if !dnsZoneStatusSlicesEqual(oldStatuses, tc.oldZoneStatuses) {
				t.Fatalf("publishRecordToZones mutated the record's status conditions\nold: %#v\nnew: %#v", oldStatuses, tc.oldZoneStatuses)
			}
			if equal := dnsZoneStatusSlicesEqual(oldStatuses, newStatuses); !equal != tc.expectChange {
				t.Fatalf("expected old and new status equal to be %v, got %v\nold: %#v\nnew: %#v", tc.expectChange, equal, oldStatuses, newStatuses)
			}
		})
	}
}

func TestMigrateDNSRecordStatus(t *testing.T) {
	tests := []struct {
		name       string
		conditions []iov1.DNSZoneCondition
		expected   []iov1.DNSZoneCondition
		changed    bool
	}{
		{
			name: "DNS record has previously failed records",
			conditions: []iov1.DNSZoneCondition{
				{
					Type:   iov1.DNSRecordFailedConditionType,
					Status: string(operatorv1.ConditionTrue),
				},
			},
			expected: []iov1.DNSZoneCondition{
				{
					Type:   iov1.DNSRecordPublishedConditionType,
					Status: string(operatorv1.ConditionFalse),
				},
			},
			changed: true,
		},
		{
			name: "DNS record has previously succeeded records",
			conditions: []iov1.DNSZoneCondition{
				{
					Type:   iov1.DNSRecordFailedConditionType,
					Status: string(operatorv1.ConditionFalse),
				},
			},
			expected: []iov1.DNSZoneCondition{
				{
					Type:   iov1.DNSRecordPublishedConditionType,
					Status: string(operatorv1.ConditionTrue),
				},
			},
			changed: true,
		},
		{
			name: "DNS record has unrelated status condition",
			conditions: []iov1.DNSZoneCondition{
				{
					Type:   "UnrelatedType",
					Status: string(operatorv1.ConditionFalse),
				},
			},
			expected: []iov1.DNSZoneCondition{
				{
					Type:   "UnrelatedType",
					Status: string(operatorv1.ConditionFalse),
				},
			},
			changed: false,
		},
		{
			name: "DNS record has unrelated status and Failed condition",
			conditions: []iov1.DNSZoneCondition{
				{
					Type:   "UnrelatedType",
					Status: string(operatorv1.ConditionFalse),
				},
				{
					Type:   iov1.DNSRecordFailedConditionType,
					Status: string(operatorv1.ConditionFalse),
				},
			},
			expected: []iov1.DNSZoneCondition{
				{
					Type:   "UnrelatedType",
					Status: string(operatorv1.ConditionFalse),
				},
				{
					Type:   iov1.DNSRecordPublishedConditionType,
					Status: string(operatorv1.ConditionTrue),
				},
			},
			changed: true,
		},
		{
			name: "DNS record has Published condition and Failed condition",
			conditions: []iov1.DNSZoneCondition{
				{
					Type:   iov1.DNSRecordPublishedConditionType,
					Status: string(operatorv1.ConditionFalse),
				},
				{
					Type:   iov1.DNSRecordFailedConditionType,
					Status: string(operatorv1.ConditionFalse),
				},
			},
			expected: []iov1.DNSZoneCondition{
				{
					Type:   iov1.DNSRecordPublishedConditionType,
					Status: string(operatorv1.ConditionTrue),
				},
			},
			changed: true,
		},
	}

	scheme := runtime.NewScheme()
	iov1.Install(scheme)

	testDNSRecord := &iov1.DNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sample-dns-record",
		},
		Status: iov1.DNSRecordStatus{
			Zones: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{ID: "sample-zone"},
				},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(testDNSRecord).
		WithRuntimeObjects(testDNSRecord).
		Build()
	r := reconciler{client: client}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testDNSRecord.Status.Zones[0].Conditions = tc.conditions
			changed, _ := r.migrateRecordStatusConditions(testDNSRecord)
			if changed != tc.changed {
				t.Fatalf("DNS record status not updated, expected status condition to be updated")
			}

			t.Logf("\n%+v", testDNSRecord.Status)

			opts := cmpopts.IgnoreFields(iov1.DNSZoneCondition{}, "Reason", "Message", "LastTransitionTime")
			if !cmp.Equal(testDNSRecord.Status.Zones[0].Conditions, tc.expected, opts) {
				t.Fatalf("status condition found diff:\n%s", cmp.Diff(testDNSRecord.Status.Zones[0].Conditions, tc.expected, opts))
			}
		})
	}
}

func TestDnsZoneStatusSlicesEqual(t *testing.T) {
	testCases := []struct {
		description string
		expected    bool
		a, b        []iov1.DNSZoneStatus
	}{
		{
			description: "nil slices are equal",
			expected:    true,
		},
		{
			description: "nil and non-nil slices are equal",
			expected:    true,
			a:           []iov1.DNSZoneStatus{},
		},
		{
			description: "empty slices are equal",
			expected:    true,
			a:           []iov1.DNSZoneStatus{},
			b:           []iov1.DNSZoneStatus{},
		},
		{
			description: "zone is not ignored",
			expected:    false,
			a: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "True",
						},
					},
				},
			},
			b: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone2",
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
		{
			description: "condition type is not ignored",
			expected:    false,
			a: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "False",
						},
					},
				},
			},
			b: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Ready",
							Status: "True",
						},
					},
				},
			},
		},
		{
			description: "condition status is not ignored",
			expected:    false,
			a: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "False",
						},
					},
				},
			},
			b: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
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
		{
			description: "condition LastTransitionTime is not ignored",
			expected:    false,
			a: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:               "Published",
							Status:             "False",
							LastTransitionTime: metav1.Unix(0, 0),
						},
					},
				},
			},
			b: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:               "Published",
							Status:             "False",
							LastTransitionTime: metav1.Unix(1, 0),
						},
					},
				},
			},
		},
		{
			description: "condition reason is not ignored",
			expected:    false,
			a: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "False",
							Reason: "foo",
						},
					},
				},
			},
			b: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "False",
							Reason: "bar",
						},
					},
				},
			},
		},
		{
			description: "condition ordering is ignored",
			expected:    true,
			a: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "True",
						},
						{
							Type:   "Ready",
							Status: "True",
						},
					},
				},
			},
			b: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Ready",
							Status: "True",
						},
						{
							Type:   "Published",
							Status: "True",
						},
					},
				},
			},
		},
		{
			description: "condition duplicate is not ignored",
			expected:    false,
			a: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
					},
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "True",
						},
						{
							Type:   "Published",
							Status: "True",
						},
					},
				},
			},
			b: []iov1.DNSZoneStatus{
				{
					DNSZone: configv1.DNSZone{
						ID: "zone1",
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
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			if actual := dnsZoneStatusSlicesEqual(tc.a, tc.b); actual != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, actual)
			}
		})
	}
}

func TestRecordIsAlreadyPublishedToZone(t *testing.T) {
	var (
		zoneWithId  = configv1.DNSZone{ID: "foo"}
		zoneWithTag = configv1.DNSZone{Tags: map[string]string{"foo": "bar"}}
	)
	testCases := []struct {
		description  string
		zone         *configv1.DNSZone
		zoneStatuses []iov1.DNSZoneStatus
		expect       bool
	}{
		{
			description:  "status.zones is empty",
			zone:         &zoneWithId,
			zoneStatuses: []iov1.DNSZoneStatus{},
			expect:       false,
		},
		{
			description: "status.zones has an entry with matching id but Published=Unknown",
			zone:        &zoneWithId,
			zoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: zoneWithId,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "Unknown",
						},
					},
				},
				{
					DNSZone: zoneWithTag,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "Unknown",
						},
					},
				},
			},
			expect: false,
		},
		{
			description: "status.zones has an entry with matching id but Published=False",
			zone:        &zoneWithId,
			zoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: zoneWithId,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "False",
						},
					},
				},
				{
					DNSZone: zoneWithTag,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "False",
						},
					},
				},
			},
			expect: false,
		},
		{
			description: "status.zones has an entry with matching tag but Published=False",
			zone:        &zoneWithTag,
			zoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: zoneWithId,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "False",
						},
					},
				},
				{
					DNSZone: zoneWithTag,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "False",
						},
					},
				},
			},
			expect: false,
		},
		{
			description: "status.zones has an entry with matching id and Published=True",
			zone:        &zoneWithId,
			zoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: zoneWithId,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "True",
						},
					},
				},
				{
					DNSZone: zoneWithTag,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "True",
						},
					},
				},
			},
			expect: true,
		},
		{
			description: "status.zones has an entry with matching tag and Published=True",
			zone:        &zoneWithTag,
			zoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: zoneWithId,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "False",
						},
					},
				},
				{
					DNSZone: zoneWithTag,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Published",
							Status: "True",
						},
					},
				},
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			record := &iov1.DNSRecord{
				Status: iov1.DNSRecordStatus{Zones: tc.zoneStatuses},
			}
			actual := recordIsAlreadyPublishedToZone(record, tc.zone)
			if actual != tc.expect {
				t.Errorf("expected %t, got %t", tc.expect, actual)
			}
		})
	}
}

func TestCustomCABundle(t *testing.T) {
	cases := []struct {
		name             string
		cm               *corev1.ConfigMap
		expectedCABundle string
	}{
		{
			name: "no configmap",
		},
		{
			name: "no CA bundle in configmap",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "openshift-config-managed",
					Name:      "kube-cloud-config",
				},
				Data: map[string]string{
					"other-key": "other-data",
				},
			},
		},
		{
			name: "custom CA bundle",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "openshift-config-managed",
					Name:      "kube-cloud-config",
				},
				Data: map[string]string{
					"ca-bundle.pem": "a custom bundle",
				},
			},
			expectedCABundle: "a custom bundle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			corev1.AddToScheme(scheme)
			resources := []runtime.Object{}
			if tc.cm != nil {
				resources = append(resources, tc.cm)
			}
			client := fake.NewFakeClientWithScheme(scheme, resources...)
			r := reconciler{client: client}
			actualCABundle, err := r.customCABundle()
			if err != nil {
				t.Fatalf("unexpected error from customCABundle: %v", err)
			}
			if a, e := actualCABundle, tc.expectedCABundle; a != e {
				t.Errorf("unexpected CA bundle: expected=%s; got %s", e, a)
			}
		})
	}
}
