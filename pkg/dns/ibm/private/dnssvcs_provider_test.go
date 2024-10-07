package private

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	dnsclient "github.com/openshift/cluster-ingress-operator/pkg/dns/ibm/private/client"

	"k8s.io/utils/pointer"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnssvcsv1"
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
			desc:         "delete happy path A record type",
			recordedCall: "DELETE",
			DNSName:      "testDelete",
			target:       "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						ID:    pointer.String("testDelete"),
						Name:  pointer.String("testDelete"),
						Rdata: map[string]interface{}{"ip": "11.22.33.44"},
						Type:  pointer.String(string(iov1.ARecordType)),
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
			desc:         "delete happy path CNAME record type",
			recordedCall: "DELETE",
			DNSName:      "testDelete",
			target:       "example.com",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						ID:    pointer.String("testDelete"),
						Name:  pointer.String("testDelete"),
						Rdata: map[string]interface{}{"cname": "example.com"},
						Type:  pointer.String(string(iov1.CNAMERecordType)),
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
			target:  "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusNotFound},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{},
				},
			},
		},
		{
			desc:    "list failure (not 404)",
			DNSName: "testDelete",
			target:  "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusRequestTimeout},
			},
			expectErrorContains: "error in ListAllDnsRecords",
		},
		{
			desc:    "list success but missing ID",
			DNSName: "testList",
			target:  "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusRequestTimeout},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						Name:  pointer.String("testList"),
						Rdata: map[string]interface{}{"ip": "11.22.33.44"},
						Type:  pointer.String(string(iov1.ARecordType)),
					}},
				},
			},
			expectErrorContains: "delete: record id is nil",
		},
		{
			desc:         "delete failure with 404",
			recordedCall: "DELETE",
			DNSName:      "testDelete",
			target:       "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						ID:    pointer.String("testDelete"),
						Name:  pointer.String("testDelete"),
						Rdata: map[string]interface{}{"ip": "11.22.33.44"},
						Type:  pointer.String(string(iov1.ARecordType)),
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
			recordedCall: "DELETE",
			DNSName:      "testDelete",
			target:       "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						ID:    pointer.String("testDelete"),
						Name:  pointer.String("testDelete"),
						Rdata: map[string]interface{}{"ip": "11.22.33.44"},
						Type:  pointer.String(string(iov1.ARecordType)),
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
			desc:    "list success but mismatching target",
			DNSName: "testDelete",
			target:  "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						ID:    pointer.String("testDelete"),
						Name:  pointer.String("testDelete"),
						Rdata: map[string]interface{}{"ip": "22.33.44.55"},
						Type:  pointer.String(string(iov1.ARecordType)),
					}},
				},
			},
		},
		{
			desc:    "list success but record type is nil",
			DNSName: "testDelete",
			target:  "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						ID:    pointer.String("testDelete"),
						Name:  pointer.String("testDelete"),
						Rdata: map[string]interface{}{"ip": "11.22.33.44"},
					}},
				},
			},
			expectErrorContains: "delete: failed to get resource type, resourceRecord.Type is nil",
		},
		{
			desc:    "list success but nil results",
			DNSName: "testDelete",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
			},
			expectErrorContains: "delete: ListResourceRecords returned nil as result",
		},
		{
			desc:    "list success but nil dns record list",
			DNSName: "testDelete",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult:   &dnssvcsv1.ListResourceRecords{},
			},
		},
		{
			desc:    "list failure and nil response",
			DNSName: "testList",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: nil,
				OutputResult:   &dnssvcsv1.ListResourceRecords{},
			},
			expectErrorContains: "delete: failed to list the dns record: error in ListAllDnsRecords",
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
			dnsService.ClearCallHistory()

			record := iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSName:    tc.DNSName,
					RecordType: iov1.ARecordType,
					Targets:    []string{tc.target},
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
	provider.dnsService = dnsService

	testCases := []struct {
		desc                         string
		DNSName                      string
		target                       string
		recordedCall                 string
		listAllDnsRecordsInputOutput dnsclient.ListAllDnsRecordsInputOutput
		updateDnsRecordInputOutput   dnsclient.UpdateDnsRecordInputOutput
		createDnsRecordInputOutput   dnsclient.CreateDnsRecordInputOutput
		expectErrorContains          string
	}{
		{
			desc:         "update happy path A record type",
			DNSName:      "testUpdate",
			target:       "11.22.33.44",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						ID:    pointer.String("testUpdate"),
						Name:  pointer.String("testUpdate"),
						Rdata: map[string]interface{}{"ip": "11.22.33.44"},
						Type:  pointer.String(string(iov1.ARecordType)),
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
			desc:         "update happy path CNAME record type",
			DNSName:      "testUpdate",
			target:       "example.com",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						ID:    pointer.String("testUpdate"),
						Name:  pointer.String("testUpdate"),
						Rdata: map[string]interface{}{"cname": "example.com"},
						Type:  pointer.String(string(iov1.CNAMERecordType)),
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
			target:       "11.22.33.44",
			recordedCall: "CREATE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusNotFound},
				OutputResult:   &dnssvcsv1.ListResourceRecords{},
			},
			createDnsRecordInputOutput: dnsclient.CreateDnsRecordInputOutput{
				InputId:     "testCreate",
				OutputError: nil,
				OutputResponse: &core.DetailedResponse{
					StatusCode: http.StatusOK,
				},
			},
		},
		{
			desc:    "list failure (not 404)",
			DNSName: "testUpdate",
			target:  "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusRequestTimeout},
			},
			expectErrorContains: "error in ListAllDnsRecords",
		},
		{
			desc:    "list success but missing ID",
			DNSName: "testList",
			target:  "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusRequestTimeout},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						Name:  pointer.String("testList"),
						Rdata: map[string]interface{}{"ip": "11.22.33.44"},
						Type:  pointer.String(string(iov1.ARecordType)),
					}},
				},
			},
			expectErrorContains: "createOrUpdateDNSRecord: record id is nil",
		},
		{
			desc:         "update error",
			DNSName:      "testUpdate",
			target:       "11.22.33.44",
			recordedCall: "PUT",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						ID:    pointer.String("testUpdate"),
						Name:  pointer.String("testUpdate"),
						Rdata: map[string]interface{}{"ip": "11.22.33.44"},
						Type:  pointer.String(string(iov1.ARecordType)),
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
			target:       "11.22.33.44",
			recordedCall: "CREATE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult:   &dnssvcsv1.ListResourceRecords{},
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
				OutputResult:   &dnssvcsv1.ListResourceRecords{},
			},
			createDnsRecordInputOutput: dnsclient.CreateDnsRecordInputOutput{
				InputId:        "testCreate",
				OutputError:    errors.New("error in CreateDnsRecord"),
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusRequestTimeout},
			},
			expectErrorContains: "error in CreateDnsRecord",
		},
		{
			desc:    "list success but record type is nil",
			DNSName: "testList",
			target:  "11.22.33.44",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult: &dnssvcsv1.ListResourceRecords{
					ResourceRecords: []dnssvcsv1.ResourceRecord{{
						ID:    pointer.String("testList"),
						Name:  pointer.String("testList"),
						Rdata: map[string]interface{}{"ip": "11.22.33.44"},
					}},
				},
			},
			expectErrorContains: "createOrUpdateDNSRecord: failed to get resource type, resourceRecord.Type is nil",
		},
		{
			desc:    "list success but nil results",
			DNSName: "testList",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
			},
			expectErrorContains: "createOrUpdateDNSRecord: ListResourceRecords returned nil as result",
		},
		{
			desc:         "list success but nil dns record list",
			DNSName:      "testCreate",
			recordedCall: "CREATE",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
				OutputResult:   &dnssvcsv1.ListResourceRecords{},
			},
			createDnsRecordInputOutput: dnsclient.CreateDnsRecordInputOutput{
				InputId:        "testCreate",
				OutputError:    nil,
				OutputResponse: &core.DetailedResponse{StatusCode: http.StatusOK},
			},
		},
		{
			desc:    "list failure and nil response",
			DNSName: "testList",
			listAllDnsRecordsInputOutput: dnsclient.ListAllDnsRecordsInputOutput{
				OutputError:    errors.New("error in ListAllDnsRecords"),
				OutputResponse: nil,
				OutputResult:   &dnssvcsv1.ListResourceRecords{},
			},
			expectErrorContains: "createOrUpdateDNSRecord: failed to list the dns record: error in ListAllDnsRecords",
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
			dnsService.ClearCallHistory()

			record := iov1.DNSRecord{
				Spec: iov1.DNSRecordSpec{
					DNSName:    tc.DNSName,
					RecordType: iov1.ARecordType,
					Targets:    []string{tc.target},
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
