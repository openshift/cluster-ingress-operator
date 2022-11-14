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

	recordedCall, _ := fc.RecordedCall(rg, zone, ARecordName)

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

	recordedCall, _ := fc.RecordedCall(rg, zone, ARecordName)

	if recordedCall != "DELETE" {
		t.Fatalf("expected the dns client 'Delete' func to be called, but found %s instead", recordedCall)
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
