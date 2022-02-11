package client

import (
	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnsrecordsv1"
)

// DnsClient is an interface to describe the methods defined in dnsrecordsv1
// for the purpose of having an interface that we can use in the implementation and test code for cluster-ingress-operator's provider logic.
type DnsClient interface {
	ListAllDnsRecords(listAllDnsRecordsOptions *dnsrecordsv1.ListAllDnsRecordsOptions) (result *dnsrecordsv1.ListDnsrecordsResp, response *core.DetailedResponse, err error)
	CreateDnsRecord(createDnsRecordOptions *dnsrecordsv1.CreateDnsRecordOptions) (result *dnsrecordsv1.DnsrecordResp, response *core.DetailedResponse, err error)
	DeleteDnsRecord(deleteDnsRecordOptions *dnsrecordsv1.DeleteDnsRecordOptions) (result *dnsrecordsv1.DeleteDnsrecordResp, response *core.DetailedResponse, err error)
	UpdateDnsRecord(updateDnsRecordOptions *dnsrecordsv1.UpdateDnsRecordOptions) (result *dnsrecordsv1.DnsrecordResp, response *core.DetailedResponse, err error)
	NewCreateDnsRecordOptions() *dnsrecordsv1.CreateDnsRecordOptions
	NewDeleteDnsRecordOptions(dnsrecordIdentifier string) *dnsrecordsv1.DeleteDnsRecordOptions
	NewListAllDnsRecordsOptions() *dnsrecordsv1.ListAllDnsRecordsOptions
	NewUpdateDnsRecordOptions(dnsrecordIdentifier string) *dnsrecordsv1.UpdateDnsRecordOptions
}
