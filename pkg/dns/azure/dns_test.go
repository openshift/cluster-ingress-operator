package azure_test

import (
	"testing"

	v1 "github.com/openshift/api/config/v1"
	dns "github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/dns/azure"
	"github.com/openshift/cluster-ingress-operator/pkg/dns/azure/client"
	"github.com/pkg/errors"
)

func fakeManager(fc *client.FakeDNSClient) (dns.Manager, error) {
	cfg := azure.Config{}
	mgr, err := azure.NewFakeManager(cfg, fc)
	if err != nil {
		errors.New("failed to create manager")
	}
	return mgr, nil
}

func TestEnsureDNS(t *testing.T) {
	c := client.Config{}
	fc, _ := client.NewFake(c)
	mgr, err := fakeManager(fc)
	if err != nil {
		t.Error("failed to steup the manager under test")
	}
	rg := "test-rg"
	zone := "dnszone.io"
	ARecordName := "subdomain"
	record := dns.Record{
		Zone: v1.DNSZone{
			ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io",
		},
		Type: dns.ARecordType,
		ARecord: &dns.ARecord{
			Domain:  "subdomain.dnszone.io",
			Address: "55.11.22.33",
		},
	}
	err = mgr.Ensure(&record)
	if err != nil {
		t.Fatal("failed to ensure dns")
		return
	}

	recordedCall, _ := fc.RecordedCall(rg, zone, ARecordName)

	if recordedCall != "PUT" {
		t.Fatalf("expected the dns client 'Delete' func to be called, but found %s instead", recordedCall)
	}
}

func TestDeleteDNS(t *testing.T) {
	c := client.Config{}
	fc, err := client.NewFake(c)
	if err != nil {
		t.Error("failed to create manager")
		return
	}

	cfg := azure.Config{}
	mgr, err := azure.NewFakeManager(cfg, fc)
	if err != nil {
		t.Error("failed to create manager")
		return
	}

	rg := "test-rg"
	zone := "dnszone.io"
	ARecordName := "subdomain"
	record := dns.Record{
		Zone: v1.DNSZone{
			ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io",
		},
		Type: dns.ARecordType,
		ARecord: &dns.ARecord{
			Domain:  "subdomain.dnszone.io",
			Address: "55.11.22.33",
		},
	}
	err = mgr.Delete(&record)
	if err != nil {
		t.Error("failed to ensure dns")
		return
	}

	recordedCall, _ := fc.RecordedCall(rg, zone, ARecordName)

	if recordedCall != "DELETE" {
		t.Fatalf("expected the dns client 'Delete' func to be called, but found %s instead", recordedCall)
	}
}
