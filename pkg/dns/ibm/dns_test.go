package ibm

import (
	"errors"
	"net/http"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"

	dnsclient "github.com/openshift/cluster-ingress-operator/pkg/dns/ibm/client"
)

func TestDelete(t *testing.T) {
	zone := configv1.DNSZone{
		ID: "zoneID",
	}

	dnsService, err := dnsclient.NewFake()
	if err != nil {
		t.Fatal("failed to create fakeClient")
	}

	provider := &Provider{}
	provider.dnsServices = map[string]dnsclient.DnsClient{
		zone.ID: dnsService,
	}

	testCases := []struct {
		desc                         string
		DNSName                      string
		recordedCall                 string
		listAllDnsRecordsInputOutput dnsclient.ListAllDnsRecordsInputOutput
		deleteDnsRecordInputOutput   dnsclient.DeleteDnsRecordInputOutput
		expectedErr                  bool
	}{
		{
			desc:         "happy path",
			DNSName:      "testDelete",
			recordedCall: "DELETE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:          "testDelete",
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			expectedErr: false,
		},
		{
			desc:         "listFailNotFound",
			DNSName:      "testDelete",
			recordedCall: "DELETE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusNotFound,
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:          "testDelete",
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			expectedErr: false,
		},
		{
			desc:         "listFailError",
			DNSName:      "testDelete",
			recordedCall: "DELETE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      errors.New("Error in ListAllDnsRecords"),
				OutputStatusCode: http.StatusRequestTimeout,
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:          "testDelete",
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			expectedErr: true,
		},
		{
			desc:         "deleteRecordNotFound",
			DNSName:      "testDelete",
			recordedCall: "DELETE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:          "testDelete",
				OutputError:      errors.New("Error in DeleteDnsRecord"),
				OutputStatusCode: http.StatusNotFound,
			},
			expectedErr: false,
		},
		{
			desc:         "deleteError",
			DNSName:      "testDelete",
			recordedCall: "DELETE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:          "testDelete",
				OutputError:      errors.New("Error in DeleteDnsRecord"),
				OutputStatusCode: http.StatusRequestTimeout,
			},
			expectedErr: true,
		},
		{
			desc:         "empty DNSName",
			DNSName:      "",
			recordedCall: "",
			expectedErr:  true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {

			record := iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSName:    tc.DNSName,
					RecordType: iov1.ARecordType,
					Targets:    []string{"11.22.33.44"},
					RecordTTL:  120,
				},
			}

			dnsService.ListAllDnsRecordsInputOutput = tc.listAllDnsRecordsInputOutput

			dnsService.DeleteDnsRecordInputOutput = tc.deleteDnsRecordInputOutput

			err = provider.Delete(&record, zone)

			if tc.expectedErr && err == nil {
				t.Error("expected error, but err is nil")
			}

			if !tc.expectedErr && err != nil {
				t.Errorf("expected nil err, got %v", err)
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
		t.Fatal("failed to create fakeClient")
	}

	provider := &Provider{}
	provider.dnsServices = map[string]dnsclient.DnsClient{
		zone.ID: dnsService,
	}

	testCases := []struct {
		desc                         string
		DNSName                      string
		recordedCall                 string
		listAllDnsRecordsInputOutput dnsclient.ListAllDnsRecordsInputOutput
		updateDnsRecordInputOutput   dnsclient.UpdateDnsRecordInputOutput
		expectedErr                  bool
	}{
		{
			desc:         "happy path",
			DNSName:      "testUpdate",
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
			expectedErr: false,
		},
		{
			desc:         "listFail",
			DNSName:      "testUpdate",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      errors.New("Error in ListAllDnsRecords"),
				OutputStatusCode: http.StatusNotFound,
			},
			updateDnsRecordInputOutput: dnsclient.UpdateDnsRecordInputOutput{
				InputId:          "testUpdate",
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			expectedErr: false,
		},
		{
			desc:         "listFailError",
			DNSName:      "testUpdate",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      errors.New("Error in ListAllDnsRecords"),
				OutputStatusCode: http.StatusRequestTimeout,
			},
			expectedErr: true,
		},
		{
			desc:         "updateError",
			DNSName:      "testUpdate",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:      nil,
				OutputStatusCode: http.StatusOK,
			},
			updateDnsRecordInputOutput: dnsclient.UpdateDnsRecordInputOutput{
				InputId:          "testUpdate",
				OutputError:      errors.New("Error in UpdateDnsRecord"),
				OutputStatusCode: http.StatusRequestTimeout,
			},
			expectedErr: true,
		},
		{
			desc:         "empty DNSName",
			DNSName:      "",
			recordedCall: "",
			expectedErr:  true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {

			record := iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSName:    tc.DNSName,
					RecordType: iov1.ARecordType,
					Targets:    []string{"11.22.33.44"},
					RecordTTL:  120,
				},
			}

			dnsService.ListAllDnsRecordsInputOutput = tc.listAllDnsRecordsInputOutput

			dnsService.UpdateDnsRecordInputOutput = tc.updateDnsRecordInputOutput

			err = provider.createOrUpdateDNSRecord(&record, zone)

			if tc.expectedErr && err == nil {
				t.Error("expected error, but err is nil")
			}

			if !tc.expectedErr && err != nil {
				t.Errorf("expected nil err, got %v", err)
			}

			recordedCall, _ := dnsService.RecordedCall(record.Spec.DNSName)

			if recordedCall != tc.recordedCall {
				t.Errorf("expected the dns client %q func to be called, but found %q instead", tc.recordedCall, recordedCall)
			}
		})
	}
}
