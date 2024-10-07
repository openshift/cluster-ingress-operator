package public

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	dnsclient "github.com/openshift/cluster-ingress-operator/pkg/dns/ibm/public/client"

	"k8s.io/utils/pointer"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnsrecordsv1"
	"github.com/stretchr/testify/assert"
)

func Test_Delete(t *testing.T) {
	zone := configv1.DNSZone{
		ID: "zoneID",
	}

	dnsService, err := dnsclient.NewFake()
	if err != nil {
		t.Fatalf("failed to create fakeClient: %v", err)
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
		expectErrorContains          string
	}{
		{
			desc:         "delete happy path",
			DNSName:      "testDelete",
			recordedCall: "DELETE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{{
						ID:      pointer.String("testDelete"),
						Name:    pointer.String("testDelete"),
						Content: pointer.String("11.22.33.44"),
					}},
				},
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:        "testDelete",
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
			},
		},
		{
			desc:    "list failure with 404",
			DNSName: "testDelete",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusNotFound},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{{}},
				},
			},
		},
		{
			desc:    "list failure (not 404)",
			DNSName: "testDelete",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusRequestTimeout},
			},
			expectErrorContains: "error in ListAllDnsRecords",
		},
		{
			desc:         "delete failure with 404",
			DNSName:      "testDelete",
			recordedCall: "DELETE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{{
						ID:      pointer.String("testDelete"),
						Name:    pointer.String("testDelete"),
						Content: pointer.String("11.22.33.44"),
					}},
				},
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:        "testDelete",
				OutputError:    errors.New("error in DeleteDnsRecord"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusNotFound},
			},
		},
		{
			desc:         "delete failure (not 404)",
			DNSName:      "testDelete",
			recordedCall: "DELETE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{{
						ID:      pointer.String("testDelete"),
						Name:    pointer.String("testDelete"),
						Content: pointer.String("11.22.33.44"),
					}},
				},
			},
			deleteDnsRecordInputOutput: dnsclient.DeleteDnsRecordInputOutput{
				InputId:        "testDelete",
				OutputError:    errors.New("error in DeleteDnsRecord"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusRequestTimeout},
			},
			expectErrorContains: "error in DeleteDnsRecord",
		},
		{
			desc:    "list success but missing ID",
			DNSName: "testDelete",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{{
						Name:    pointer.String("testDelete"),
						Content: pointer.String("11.22.33.44"),
					}},
				},
			},
			expectErrorContains: "delete: record id is nil",
		},
		{
			desc:    "list success but nil results",
			DNSName: "testDelete",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
			},
			expectErrorContains: "delete: ListAllDnsRecords returned nil as result",
		},
		{
			desc:    "list success but nil dns record list",
			DNSName: "testDelete",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult:   &dnsrecordsv1.ListDnsrecordsResp{},
			},
			expectErrorContains: "delete: ListAllDnsRecords returned nil as result",
		},
		{
			desc:    "list failure and nil response",
			DNSName: "testDelete",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: nil,
				OutputResult:   &dnsrecordsv1.ListDnsrecordsResp{},
			},
			expectErrorContains: "delete: failed to list the dns record: error in ListAllDnsRecords",
		},
		{
			desc:                "empty DNSName",
			DNSName:             "",
			recordedCall:        "",
			expectErrorContains: "invalid dns input data",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			dnsService.ClearCallHistory()

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

func Test_createOrUpdateDNSRecord(t *testing.T) {
	zone := configv1.DNSZone{
		ID: "zoneID",
	}

	dnsService, err := dnsclient.NewFake()
	if err != nil {
		t.Fatalf("failed to create fakeClient: %v", err)
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
		createDnsRecordInputOutput   dnsclient.CreateDnsRecordInputOutput
		expectErrorContains          string
	}{
		{
			desc:         "happy path",
			DNSName:      "testUpdate",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{{
						ID:      pointer.String("testUpdate"),
						Name:    pointer.String("testUpdate"),
						Content: pointer.String("11.22.33.44"),
					}},
				},
			},
			updateDnsRecordInputOutput: dnsclient.UpdateDnsRecordInputOutput{
				InputId:        "testUpdate",
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
			},
		},
		{
			desc:         "list failure with 404",
			DNSName:      "testCreate",
			recordedCall: "CREATE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusNotFound},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{},
				},
			},
			createDnsRecordInputOutput: dnsclient.CreateDnsRecordInputOutput{
				InputId:        "testCreate",
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
			},
		},
		{
			desc:    "list failure (not 404)",
			DNSName: "testUpdate",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusRequestTimeout},
			},
			expectErrorContains: "error in ListAllDnsRecords",
		},
		{
			desc:         "update error",
			DNSName:      "testUpdate",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{{
						ID:      pointer.String("testUpdate"),
						Name:    pointer.String("testUpdate"),
						Content: pointer.String("11.22.33.44"),
					}},
				},
			},
			updateDnsRecordInputOutput: dnsclient.UpdateDnsRecordInputOutput{
				InputId:        "testUpdate",
				OutputError:    errors.New("error in UpdateDnsRecord"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusRequestTimeout},
			},
			expectErrorContains: "error in UpdateDnsRecord",
		},
		{
			desc:         "create happy path",
			DNSName:      "testCreate",
			recordedCall: "CREATE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{},
				},
			},
			createDnsRecordInputOutput: dnsclient.CreateDnsRecordInputOutput{
				InputId:        "testCreate",
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
			},
		},
		{
			desc:         "create error",
			DNSName:      "testCreate",
			recordedCall: "CREATE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{},
				}},
			createDnsRecordInputOutput: dnsclient.CreateDnsRecordInputOutput{
				InputId:        "testCreate",
				OutputError:    errors.New("error in CreateDnsRecord"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusRequestTimeout},
			},
			expectErrorContains: "error in CreateDnsRecord",
		},
		{
			desc:    "list success but missing ID",
			DNSName: "testList",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnsrecordsv1.ListDnsrecordsResp{
					Result: []dnsrecordsv1.DnsrecordDetails{{
						Name:    pointer.String("testList"),
						Content: pointer.String("11.22.33.44"),
					}},
				},
			},
			expectErrorContains: "createOrUpdateDNSRecord: record id is nil",
		},
		{
			desc:    "list success but nil results",
			DNSName: "testList",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
			},
			expectErrorContains: "createOrUpdateDNSRecord: ListAllDnsRecords returned nil as result",
		},
		{
			desc:    "list success but nil dns record list",
			DNSName: "testList",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult:   &dnsrecordsv1.ListDnsrecordsResp{},
			},
			expectErrorContains: "createOrUpdateDNSRecord: ListAllDnsRecords returned nil as result",
		},
		{
			desc:    "list failure and nil response",
			DNSName: "testList",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: nil,
				OutputResult:   &dnsrecordsv1.ListDnsrecordsResp{},
			},
			expectErrorContains: "createOrUpdateDNSRecord: failed to list the dns record: error in ListAllDnsRecords",
		},
		{
			desc:                "empty DNSName",
			DNSName:             "",
			recordedCall:        "",
			expectErrorContains: "invalid dns input data",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			dnsService.ClearCallHistory()

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
			dnsService.CreateDnsRecordInputOutput = tc.createDnsRecordInputOutput

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
