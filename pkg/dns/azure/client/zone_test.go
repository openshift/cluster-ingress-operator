package client_test

import (
	"errors"
	"testing"

	"github.com/openshift/cluster-ingress-operator/pkg/dns/azure/client"
)

func TestFunction(t *testing.T) {
	var zoneTests = []struct {
		desc     string
		id       string
		err      error
		subID    string
		rg       string
		provider string
		name     string
	}{
		{"TestValidZoneID", "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/test-rg.dnszone.io",
			nil, "E540B02D-5CCE-4D47-A13B-EB05A19D696E", "test-rg", "Microsoft.Network/dnszones", "test-rg.dnszone.io"},
		{"TestValidZoneID", "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/privateDnsZones/test-rg.dnszone.io",
			nil, "E540B02D-5CCE-4D47-A13B-EB05A19D696E", "test-rg", "Microsoft.Network/privateDnsZones", "test-rg.dnszone.io"},
		{"TestInvalidZoneID", "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones",
			errors.New("invalid azure dns zone id"), "E540B02D-5CCE-4D47-A13B-EB05A19D696E", "test-rg", "Microsoft.Network/dnszones", "test-rg.dnszone.io"},
		{"TestEmptyZoneID", "", errors.New("invalid azure dns zone id"),
			"E540B02D-5CCE-4D47-A13B-EB05A19D696E", "test-rg", "Microsoft.Network/dnszones", "test-rg.dnszone.io"},
	}
	for _, tt := range zoneTests {
		t.Run(tt.desc, func(t *testing.T) {
			zone, err := client.ParseZone(tt.id)
			switch tt.err {
			case nil:
				if err != nil {
					t.Errorf("%s: expected [%s] actual [%s]", tt.desc, tt.err, err)
					return
				}
			default:
				if tt.err.Error() != err.Error() {
					t.Errorf("%s: expected [%s] actual [%s]", tt.desc, tt.err, err)
				}
				return
			}
			if zone.SubscriptionID != tt.subID {
				t.Errorf("expected [%s] actual [%s]", tt.subID, zone.SubscriptionID)
			}
			if zone.ResourceGroup != tt.rg {
				t.Errorf("expected [%s] actual [%s]", tt.rg, zone.ResourceGroup)
			}
			if zone.Provider != tt.provider {
				t.Errorf("expected [%s] actual [%s]", tt.provider, zone.Provider)
			}
			if zone.Name != tt.name {
				t.Errorf("expected [%s] actual [%s]", tt.name, zone.Name)
			}
		})
	}
}
