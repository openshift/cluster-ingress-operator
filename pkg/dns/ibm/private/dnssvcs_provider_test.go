package private

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	dnsclient "github.com/openshift/cluster-ingress-operator/pkg/dns/ibm/private/client"
	"github.com/stretchr/testify/assert"
)

func TestDelete(t *testing.T) {
	zone := configv1.DNSZone{
		ID: "zoneID",
	}

	dnsService, err := dnsclient.NewFake()
	if err != nil {
		t.Fatalf("failed to create fakeClient: %v", err)
	}

	provider := &Provider{}
	provider.dnsService = dnsService

	testCases := []struct {
		desc                         string
		recordedCall                 string
		DNSName                      string
		target                       string
		listAllDnsRecordsInputOutput dnsclient.ListAllDnsRecordsInputOutput
		deleteDnsRecordInputOutput   dnsclient.DeleteDnsRecordInputOutput
		expectErrorContains          string
	}{
		{
			desc:         "happy path",
			recordedCall: "DELETE",
			DNSName:      "testDelete",
			target:       "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:          "testDelete",
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
		},
		{
			desc:         "listFail",
			recordedCall: "DELETE",
			DNSName:      "testDelete",
			target:       "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusNotFound,
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:          "testDelete",
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
		},
		{
			desc:         "listFailError",
			recordedCall: "DELETE",
			DNSName:      "testDelete",
			target:       "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      errors.New("error in ListAllDnsRecords"),
				OutputStatusCode: http.StatusRequestTimeout,
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:          "testDelete",
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			expectErrorContains: "error in ListAllDnsRecords",
		},
		{
			desc:         "deleteRecordNotFound",
			recordedCall: "DELETE",
			DNSName:      "testDelete",
			target:       "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:          "testDelete",
				OutputError:      errors.New("error in DeleteDnsRecord"),
				OutputStatusCode: http.StatusNotFound,
			},
		},
		{
			desc:         "deleteError",
			recordedCall: "DELETE",
			DNSName:      "testDelete",
			target:       "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:          "testDelete",
				OutputError:      errors.New("error in DeleteDnsRecord"),
				OutputStatusCode: http.StatusRequestTimeout,
			},
			expectErrorContains: "error in DeleteDnsRecord",
		},
		{
			desc:                "empty DNSName",
			DNSName:             "",
			target:              "",
			recordedCall:        "",
			expectErrorContains: "invalid dns input data",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {

			record := iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSName:    tc.DNSName,
					RecordType: iov1.ARecordType,
					Targets:    []string{tc.target},
					RecordTTL:  120,
				},
			}

			tc.listAllDnsRecordsInputOutput.RecordName = tc.DNSName
			tc.listAllDnsRecordsInputOutput.RecordTarget = tc.target
			dnsService.ListAllDnsRecordsInputOutput = tc.listAllDnsRecordsInputOutput

			dnsService.DeleteDnsRecordInputOutput = tc.deleteDnsRecordInputOutput

			err = provider.Delete(&record, zone)

			if len(tc.expectErrorContains) != 0 && !strings.Contains(err.Error(), tc.expectErrorContains) {
				t.Errorf("expected message to include %q, got %q", tc.expectErrorContains, err.Error())
			}

			if len(tc.expectErrorContains) == 0 {
				assert.NoError(t, err, "unexpected error")
			}

			recordedCall, _ := dnsService.RecordedCall(record.Spec.DNSName)

			if recordedCall != tc.recordedCall {
				t.Errorf("expected the dns client %q func to be called, but found %q instead", tc.recordedCall, recordedCall)
			}
		})
	}
}

func TestCreateOrUpdate(t *testing.T) {
	zone := configv1.DNSZone{
		ID: "zoneID",
	}

	dnsService, err := dnsclient.NewFake()
	if err != nil {
		t.Fatalf("failed to create fakeClient: %v", err)
	}

	provider := &Provider{}
	provider.dnsService = dnsService

	testCases := []struct {
		desc                         string
		DNSName                      string
		target                       string
		recordedCall                 string
		listAllDnsRecordsInputOutput dnsclient.ListAllDnsRecordsInputOutput
		updateDnsRecordInputOutput   dnsclient.UpdateDnsRecordInputOutput
		expectErrorContains          string
	}{
		{
			desc:         "happy path",
			DNSName:      "testUpdate",
			target:       "11.22.33.44",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			updateDnsRecordInputOutput: dnsclient.UpdateDnsRecordInputOutput{
				InputId:          "testUpdate",
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
		},
		{
			desc:         "listFail",
			DNSName:      "testUpdate",
			target:       "11.22.33.44",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      errors.New("error in ListAllDnsRecords"),
				OutputStatusCode: http.StatusNotFound,
			},
			updateDnsRecordInputOutput: dnsclient.UpdateDnsRecordInputOutput{
				InputId:          "testUpdate",
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
		},
		{
			desc:         "listFailError",
			DNSName:      "testUpdate",
			target:       "11.22.33.44",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      errors.New("error in ListAllDnsRecords"),
				OutputStatusCode: http.StatusRequestTimeout,
			},
			expectErrorContains: "error in ListAllDnsRecords",
		},
		{
			desc:         "updateError",
			DNSName:      "testUpdate",
			target:       "11.22.33.44",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			updateDnsRecordInputOutput: dnsclient.UpdateDnsRecordInputOutput{
				InputId:          "testUpdate",
				OutputError:      errors.New("error in UpdateDnsRecord"),
				OutputStatusCode: http.StatusRequestTimeout,
			},
			expectErrorContains: "error in UpdateDnsRecord",
		},
		{
			desc:                "empty DNSName",
			DNSName:             "",
			target:              "11.22.33.44",
			recordedCall:        "",
			expectErrorContains: "invalid dns input data",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {

			record := iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSName:    tc.DNSName,
					RecordType: iov1.ARecordType,
					Targets:    []string{tc.target},
					RecordTTL:  120,
				},
			}

			tc.listAllDnsRecordsInputOutput.RecordName = tc.DNSName
			tc.listAllDnsRecordsInputOutput.RecordTarget = tc.target

			dnsService.ListAllDnsRecordsInputOutput = tc.listAllDnsRecordsInputOutput

			dnsService.UpdateDnsRecordInputOutput = tc.updateDnsRecordInputOutput

			err = provider.createOrUpdateDNSRecord(&record, zone)

			if len(tc.expectErrorContains) != 0 && !strings.Contains(err.Error(), tc.expectErrorContains) {
				t.Errorf("expected message to include %q, got %q", tc.expectErrorContains, err.Error())
			}

			if len(tc.expectErrorContains) == 0 {
				assert.NoError(t, err, "unexpected error")
			}

			recordedCall, _ := dnsService.RecordedCall(record.Spec.DNSName)

			if recordedCall != tc.recordedCall {
				t.Errorf("expected the dns client %q func to be called, but found %q instead", tc.recordedCall, recordedCall)
			}
		})
	}
}
