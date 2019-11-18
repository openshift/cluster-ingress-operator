package dns

import (
	"github.com/google/go-cmp/cmp"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"testing"
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
