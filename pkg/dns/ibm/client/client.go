package client

import (
	"github.com/IBM/go-sdk-core/v4/core"
	"github.com/IBM/networking-go-sdk/dnsrecordsv1"
)

type DnsClient interface {
	ListAllDnsRecords(listAllDnsRecordsOptions *dnsrecordsv1.ListAllDnsRecordsOptions) (result *dnsrecordsv1.ListDnsrecordsResp, response *core.DetailedResponse, err error)
	CreateDnsRecord(createDnsRecordOptions *dnsrecordsv1.CreateDnsRecordOptions) (result *dnsrecordsv1.DnsrecordResp, response *core.DetailedResponse, err error)
	DeleteDnsRecord(deleteDnsRecordOptions *dnsrecordsv1.DeleteDnsRecordOptions) (result *dnsrecordsv1.DeleteDnsrecordResp, response *core.DetailedResponse, err error)
	UpdateDnsRecord(updateDnsRecordOptions *dnsrecordsv1.UpdateDnsRecordOptions) (result *dnsrecordsv1.DnsrecordResp, response *core.DetailedResponse, err error)
	NewCreateDnsRecordOptions() *dnsrecordsv1.CreateDnsRecordOptions
	NewDeleteDnsRecordOptions(dnsrecordIdentifier string) *dnsrecordsv1.DeleteDnsRecordOptions
	NewGetDnsRecordOptions(dnsrecordIdentifier string) *dnsrecordsv1.GetDnsRecordOptions
	NewListAllDnsRecordsOptions() *dnsrecordsv1.ListAllDnsRecordsOptions
	NewUpdateDnsRecordOptions(dnsrecordIdentifier string) *dnsrecordsv1.UpdateDnsRecordOptions
}
