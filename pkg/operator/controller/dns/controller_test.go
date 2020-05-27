package dns

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPublishRecordToZones(t *testing.T) {
	var tests = []struct {
		name   string
		zones  []configv1.DNSZone
		expect []string
	}{
		{
			name: "testing for a successful update of 1 record",
			zones: []configv1.DNSZone{{
				ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io"},
			},
			expect: []string{string(operatorv1.ConditionFalse)},
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
				{ID: ""}},
			expect: []string{string(operatorv1.ConditionFalse), string(operatorv1.ConditionFalse)},
		},
	}

	for _, test := range tests {
		record := &iov1.DNSRecord{
			Spec: iov1.DNSRecordSpec{
				DNSName:    "subdomain.dnszone.io.",
				RecordType: iov1.ARecordType,
				Targets:    []string{"55.11.22.33"},
			},
		}
		r := &reconciler{
			//TODO To write a fake provider that can return errors and add more test cases.
			dnsProvider: &dns.FakeProvider{},
		}
		actual, _ := r.publishRecordToZones(test.zones, record)
		var conditions []string
		for _, dnsStatus := range actual {
			for _, condition := range dnsStatus.Conditions {
				conditions = append(conditions, condition.Status)
			}
		}
		if !cmp.Equal(conditions, test.expect) {
			t.Fatalf("%q: expected:\n%#v\ngot:\n%#v", test.name, test.expect, conditions)
		}
	}
}

// TestPublishRecordToZonesMergesStatus verifies that publishRecordToZones
// correctly merges status updates.
func TestPublishRecordToZonesMergesStatus(t *testing.T) {
	var testCases = []struct {
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
							Type:   "Failed",
							Status: "False",
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
							Type:   "Failed",
							Status: "True",
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
							Type:    "Failed",
							Status:  "False",
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
		record := &iov1.DNSRecord{
			Status: iov1.DNSRecordStatus{Zones: tc.oldZoneStatuses},
		}
		r := &reconciler{dnsProvider: &dns.FakeProvider{}}
		zone := []configv1.DNSZone{{ID: "zone2"}}
		oldStatuses := record.Status.DeepCopy().Zones
		newStatuses, _ := r.publishRecordToZones(zone, record)
		if equal := dnsZoneStatusSlicesEqual(oldStatuses, newStatuses); !equal != tc.expectChange {
			t.Fatalf("%q: expected old and new status equal to be %v, got %v\nold: %#v\nnew: %#v", tc.description, tc.expectChange, equal, oldStatuses, newStatuses)
		}
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
							Type:   "Failed",
							Status: "False",
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
							Type:   "Failed",
							Status: "False",
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
							Type:   "Failed",
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
							Type:   "Failed",
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
							Type:   "Failed",
							Status: "False",
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
							Type:               "Failed",
							Status:             "True",
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
							Type:               "Failed",
							Status:             "True",
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
							Type:   "Failed",
							Status: "True",
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
							Type:   "Failed",
							Status: "True",
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
							Type:   "Failed",
							Status: "False",
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
							Type:   "Failed",
							Status: "False",
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
							Type:   "Failed",
							Status: "False",
						},
						{
							Type:   "Failed",
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
							Type:   "Failed",
							Status: "False",
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		if actual := dnsZoneStatusSlicesEqual(tc.a, tc.b); actual != tc.expected {
			t.Fatalf("%q: expected %v, got %v", tc.description, tc.expected, actual)
		}
	}
}

func TestRecordIsAlreadyPublishedToZone(t *testing.T) {
	var (
		zoneWithId  = configv1.DNSZone{ID: "foo"}
		zoneWithTag = configv1.DNSZone{Tags: map[string]string{"foo": "bar"}}
	)
	var testCases = []struct {
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
			description: "status.zones has an entry with matching id but Failed=Unknown",
			zone:        &zoneWithId,
			zoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: zoneWithId,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Failed",
							Status: "Unknown",
						},
					},
				},
				{
					DNSZone: zoneWithTag,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Failed",
							Status: "Unknown",
						},
					},
				},
			},
			expect: false,
		},
		{
			description: "status.zones has an entry with matching id but Failed=True",
			zone:        &zoneWithId,
			zoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: zoneWithId,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Failed",
							Status: "True",
						},
					},
				},
				{
					DNSZone: zoneWithTag,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Failed",
							Status: "True",
						},
					},
				},
			},
			expect: false,
		},
		{
			description: "status.zones has an entry with matching tag but Failed=True",
			zone:        &zoneWithTag,
			zoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: zoneWithId,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Failed",
							Status: "True",
						},
					},
				},
				{
					DNSZone: zoneWithTag,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Failed",
							Status: "True",
						},
					},
				},
			},
			expect: false,
		},
		{
			description: "status.zones has an entry with matching id and Failed=False",
			zone:        &zoneWithId,
			zoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: zoneWithId,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Failed",
							Status: "False",
						},
					},
				},
				{
					DNSZone: zoneWithTag,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Failed",
							Status: "True",
						},
					},
				},
			},
			expect: true,
		},
		{
			description: "status.zones has an entry with matching tag and Failed=False",
			zone:        &zoneWithTag,
			zoneStatuses: []iov1.DNSZoneStatus{
				{
					DNSZone: zoneWithId,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Failed",
							Status: "True",
						},
					},
				},
				{
					DNSZone: zoneWithTag,
					Conditions: []iov1.DNSZoneCondition{
						{
							Type:   "Failed",
							Status: "False",
						},
					},
				},
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		record := &iov1.DNSRecord{
			Status: iov1.DNSRecordStatus{Zones: tc.zoneStatuses},
		}
		actual := recordIsAlreadyPublishedToZone(record, tc.zone)
		if actual != tc.expect {
			t.Errorf("%q: expected %t, got %t", tc.description, tc.expect, actual)
		}
	}
}
