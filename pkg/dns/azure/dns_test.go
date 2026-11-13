package azure_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/Azure/go-autorest/autorest/to"
	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	"github.com/openshift/cluster-ingress-operator/pkg/dns/azure"
	"github.com/openshift/cluster-ingress-operator/pkg/dns/azure/client"
)

func fakeManager(fc *client.FakeDNSClient) (dns.Provider, error) {
	cfg := azure.Config{}
	mgr, err := azure.NewFakeProvider(cfg, fc)
	if err != nil {
		errors.New("failed to create manager")
	}
	return mgr, nil
}

func Test_Ensure(t *testing.T) {
	c := client.Config{}
	fc, _ := client.NewFake(c)
	mgr, err := fakeManager(fc)
	if err != nil {
		t.Error("failed to steup the manager under test")
	}
	rg := "test-rg"
	zone := "dnszone.io"
	ARecordName := "subdomain"
	record := iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			DNSName:    "subdomain.dnszone.io.",
			RecordType: iov1.ARecordType,
			Targets:    []string{"55.11.22.33"},
			RecordTTL:  120,
		},
	}
	dnsZone := configv1.DNSZone{
		ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io",
	}
	err = mgr.Ensure(&record, dnsZone)
	if err != nil {
		t.Fatal("failed to ensure dns")
		return
	}

	recordedCall, _ := fc.RecordedCall(rg, zone, ARecordName, client.RecordTypeA)

	if recordedCall != "PUT" {
		t.Fatalf("expected the dns client 'Delete' func to be called, but found %s instead", recordedCall)
	}
}

func Test_Delete(t *testing.T) {
	c := client.Config{}
	fc, err := client.NewFake(c)
	if err != nil {
		t.Error("failed to create manager")
		return
	}

	cfg := azure.Config{}
	mgr, err := azure.NewFakeProvider(cfg, fc)
	if err != nil {
		t.Error("failed to create manager")
		return
	}

	rg := "test-rg"
	zone := "dnszone.io"
	ARecordName := "subdomain"
	record := iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			DNSName:    "subdomain.dnszone.io.",
			RecordType: iov1.ARecordType,
			Targets:    []string{"55.11.22.33"},
		},
	}
	dnsZone := configv1.DNSZone{
		ID: "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io",
	}
	err = mgr.Delete(&record, dnsZone)
	if err != nil {
		t.Error("failed to ensure dns")
		return
	}

	recordedCall, _ := fc.RecordedCall(rg, zone, ARecordName, client.RecordTypeA)

	if recordedCall != "DELETE" {
		t.Fatalf("expected the dns client 'Delete' func to be called, but found %s instead", recordedCall)
	}
}

const testZoneID = "/subscriptions/E540B02D-5CCE-4D47-A13B-EB05A19D696E/resourceGroups/test-rg/providers/Microsoft.Network/dnszones/dnszone.io"

func azureRecord(recordType iov1.DNSRecordType, targets ...string) *iov1.DNSRecord {
	return &iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			DNSName:    "subdomain.dnszone.io.",
			RecordType: recordType,
			Targets:    targets,
			RecordTTL:  120,
		},
	}
}

func Test_Ensure_WritesARecordSetPerAddressFamily(t *testing.T) {
	fc, _ := client.NewFake(client.Config{})
	mgr, err := fakeManager(fc)
	if err != nil {
		t.Fatalf("failed to set up the manager under test: %v", err)
	}

	record := azureRecord(iov1.ARecordType, "55.11.22.33", "2603:1030:c02:9::7a7")
	if err := mgr.Ensure(record, configv1.DNSZone{ID: testZoneID}); err != nil {
		t.Fatalf("failed to ensure dns: %v", err)
	}

	for _, tc := range []struct {
		recordType client.RecordType
		addresses  []string
	}{
		{client.RecordTypeA, []string{"55.11.22.33"}},
		{client.RecordTypeAAAA, []string{"2603:1030:c02:9::7a7"}},
	} {
		call, ok := fc.RecordedCall("test-rg", "dnszone.io", "subdomain", tc.recordType)
		if !ok || call != "PUT" {
			t.Errorf("expected a PUT for the %s record set, got %q (recorded=%v)", tc.recordType, call, ok)
		}
		addrs, _ := fc.RecordedAddresses("test-rg", "dnszone.io", "subdomain", tc.recordType)
		if !reflect.DeepEqual(addrs, tc.addresses) {
			t.Errorf("expected %s addresses %v, got %v", tc.recordType, tc.addresses, addrs)
		}
	}
}

func Test_Ensure_MultipleTargetsOfOneFamilyShareARecordSet(t *testing.T) {
	fc, _ := client.NewFake(client.Config{})
	mgr, err := fakeManager(fc)
	if err != nil {
		t.Fatalf("failed to set up the manager under test: %v", err)
	}

	record := azureRecord(iov1.ARecordType, "55.11.22.33", "55.11.22.34")
	if err := mgr.Ensure(record, configv1.DNSZone{ID: testZoneID}); err != nil {
		t.Fatalf("failed to ensure dns: %v", err)
	}
	addrs, _ := fc.RecordedAddresses("test-rg", "dnszone.io", "subdomain", client.RecordTypeA)
	if expected := []string{"55.11.22.33", "55.11.22.34"}; !reflect.DeepEqual(addrs, expected) {
		t.Errorf("expected A addresses %v, got %v", expected, addrs)
	}
}

func Test_Ensure_NormalizesIPv4MappedAddresses(t *testing.T) {
	fc, _ := client.NewFake(client.Config{})
	mgr, err := fakeManager(fc)
	if err != nil {
		t.Fatalf("failed to set up the manager under test: %v", err)
	}

	record := azureRecord(iov1.ARecordType, "::ffff:55.11.22.33")
	if err := mgr.Ensure(record, configv1.DNSZone{ID: testZoneID}); err != nil {
		t.Fatalf("failed to ensure dns: %v", err)
	}
	addrs, _ := fc.RecordedAddresses("test-rg", "dnszone.io", "subdomain", client.RecordTypeA)
	if expected := []string{"55.11.22.33"}; !reflect.DeepEqual(addrs, expected) {
		t.Errorf("expected an A record set with %v, got %v", expected, addrs)
	}
	if _, ok := fc.RecordedCall("test-rg", "dnszone.io", "subdomain", client.RecordTypeAAAA); ok {
		t.Error("expected no AAAA record set for an IPv4-mapped address")
	}
}

func Test_Ensure_RejectsNonAddressRecordTypes(t *testing.T) {
	fc, _ := client.NewFake(client.Config{})
	mgr, err := fakeManager(fc)
	if err != nil {
		t.Fatalf("failed to set up the manager under test: %v", err)
	}

	record := azureRecord(iov1.CNAMERecordType, "lb.cloud.example.com")
	if err := mgr.Ensure(record, configv1.DNSZone{ID: testZoneID}); err == nil {
		t.Error("expected an error for a CNAME record, got nil")
	}
}

func Test_Delete_SucceedsWithoutUsableTargets(t *testing.T) {
	// Deleting a record set needs only its name and type, so a record with
	// no usable target must still be deletable; otherwise its finalizer
	// could never be cleared.
	for _, targets := range [][]string{nil, {""}, {"not-an-ip"}} {
		fc, _ := client.NewFake(client.Config{})
		mgr, err := fakeManager(fc)
		if err != nil {
			t.Fatalf("failed to set up the manager under test: %v", err)
		}

		record := azureRecord(iov1.ARecordType, targets...)
		if err := mgr.Delete(record, configv1.DNSZone{ID: testZoneID}); err != nil {
			t.Errorf("targets %v: expected delete to succeed, got %v", targets, err)
		}
		if call, _ := fc.RecordedCall("test-rg", "dnszone.io", "subdomain", client.RecordTypeA); call != "DELETE" {
			t.Errorf("targets %v: expected a DELETE for the A record set, got %q", targets, call)
		}
	}
}

func Test_Delete_UsesTheRecordTypeFromTheSpec(t *testing.T) {
	fc, _ := client.NewFake(client.Config{})
	mgr, err := fakeManager(fc)
	if err != nil {
		t.Fatalf("failed to set up the manager under test: %v", err)
	}

	record := azureRecord(iov1.AAAARecordType, "2603:1030:c02:9::7a7")
	if err := mgr.Delete(record, configv1.DNSZone{ID: testZoneID}); err != nil {
		t.Fatalf("failed to delete dns: %v", err)
	}
	if call, _ := fc.RecordedCall("test-rg", "dnszone.io", "subdomain", client.RecordTypeAAAA); call != "DELETE" {
		t.Errorf("expected a DELETE for the AAAA record set, got %q", call)
	}
}

func fakeManagerWithIPFamily(fc *client.FakeDNSClient, ipFamily configv1.IPFamilyType) (dns.Provider, error) {
	return azure.NewFakeProvider(azure.Config{IPFamily: ipFamily}, fc)
}

func Test_Ensure_DualStackReportsMissingAddressFamily(t *testing.T) {
	fc, _ := client.NewFake(client.Config{})
	mgr, err := fakeManagerWithIPFamily(fc, configv1.DualStackIPv4Primary)
	if err != nil {
		t.Fatalf("failed to set up the manager under test: %v", err)
	}

	// The load balancer has not reported its IPv6 address yet.
	record := azureRecord(iov1.ARecordType, "55.11.22.33")
	err = mgr.Ensure(record, configv1.DNSZone{ID: testZoneID})
	if err == nil {
		t.Error("expected an error for a dual-stack record with no IPv6 target, got nil")
	}

	// The IPv4 address is still published, so the record is not held back
	// entirely while waiting for the second family.
	if call, _ := fc.RecordedCall("test-rg", "dnszone.io", "subdomain", client.RecordTypeA); call != "PUT" {
		t.Errorf("expected the A record set to be published anyway, got %q", call)
	}
	if _, ok := fc.RecordedCall("test-rg", "dnszone.io", "subdomain", client.RecordTypeAAAA); ok {
		t.Error("expected no AAAA record set to be written")
	}
}

func Test_Ensure_SingleStackIsUnaffectedByDualStackSupport(t *testing.T) {
	fc, _ := client.NewFake(client.Config{})
	mgr, err := fakeManagerWithIPFamily(fc, configv1.IPv4)
	if err != nil {
		t.Fatalf("failed to set up the manager under test: %v", err)
	}

	record := azureRecord(iov1.ARecordType, "55.11.22.33")
	if err := mgr.Ensure(record, configv1.DNSZone{ID: testZoneID}); err != nil {
		t.Fatalf("failed to ensure dns: %v", err)
	}
	if call, _ := fc.RecordedCall("test-rg", "dnszone.io", "subdomain", client.RecordTypeA); call != "PUT" {
		t.Errorf("expected a PUT for the A record set, got %q", call)
	}
	if _, ok := fc.RecordedCall("test-rg", "dnszone.io", "subdomain", client.RecordTypeAAAA); ok {
		t.Error("expected no AAAA record set on a single-stack cluster")
	}
}

func Test_Delete_DualStackRemovesBothRecordTypes(t *testing.T) {
	fc, _ := client.NewFake(client.Config{})
	mgr, err := fakeManagerWithIPFamily(fc, configv1.DualStackIPv4Primary)
	if err != nil {
		t.Fatalf("failed to set up the manager under test: %v", err)
	}

	// Only the IPv4 target remains in the spec; the AAAA record set must
	// still be deleted so it is not orphaned in the zone.
	record := azureRecord(iov1.ARecordType, "55.11.22.33")
	if err := mgr.Delete(record, configv1.DNSZone{ID: testZoneID}); err != nil {
		t.Fatalf("failed to delete dns: %v", err)
	}

	for _, recordType := range []client.RecordType{client.RecordTypeA, client.RecordTypeAAAA} {
		if call, _ := fc.RecordedCall("test-rg", "dnszone.io", "subdomain", recordType); call != "DELETE" {
			t.Errorf("expected a DELETE for the %s record set, got %q", recordType, call)
		}
	}
}

func Test_GetTagList(t *testing.T) {
	infra := configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			InfrastructureName: "test-sdfhy",
			PlatformStatus: &configv1.PlatformStatus{
				Azure: &configv1.AzurePlatformStatus{
					ResourceGroupName: "test-sdfhy-rg",
					CloudName:         configv1.AzureStackCloud,
				},
			},
		},
	}

	testCases := []struct {
		name         string
		userTags     []configv1.AzureResourceTag
		expectedTags map[string]*string
	}{
		{
			name:     "user tags not defined in Infrastructure.Status",
			userTags: nil,
			expectedTags: map[string]*string{
				fmt.Sprintf("%s.%v", azure.OCPClusterIDTagKeyPrefix, infra.Status.InfrastructureName): to.StringPtr(azure.OCPClusterIDTagValue),
			},
		},
		{
			name: "user tags defined in Infrastructure.Status",
			userTags: []configv1.AzureResourceTag{
				{
					Key:   "environment",
					Value: "test",
				},
			},
			expectedTags: map[string]*string{
				fmt.Sprintf("%s.%v", azure.OCPClusterIDTagKeyPrefix, infra.Status.InfrastructureName): to.StringPtr(azure.OCPClusterIDTagValue),
				"environment": to.StringPtr("test"),
			},
		},
	}

	for _, tc := range testCases {
		infra.Status.PlatformStatus.Azure.ResourceTags = tc.userTags
		t.Run(tc.name, func(t *testing.T) {
			tags := azure.GetTagList(&infra.Status)

			if !reflect.DeepEqual(tags, tc.expectedTags) {
				t.Errorf("Expected %+v, got: %+v infra: %+v", tc.expectedTags, tags, infra)
			}
		})
	}
}
