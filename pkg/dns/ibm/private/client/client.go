package client

import (
	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnssvcsv1"
)

// DnsClient is an interface to describe the methods defined in dnssvcsv1
// for the purpose of having an interface that we can use in the implementation and test code for cluster-ingress-operator's provider logic.
type DnsClient interface {
	NewListResourceRecordsOptions(instanceID string, dnszoneID string) *dnssvcsv1.ListResourceRecordsOptions
	ListResourceRecords(listResourceRecordsOptions *dnssvcsv1.ListResourceRecordsOptions) (result *dnssvcsv1.ListResourceRecords, response *core.DetailedResponse, err error)
	NewDeleteResourceRecordOptions(instanceID string, dnszoneID string, recordID string) *dnssvcsv1.DeleteResourceRecordOptions
	DeleteResourceRecord(deleteResourceRecordOptions *dnssvcsv1.DeleteResourceRecordOptions) (response *core.DetailedResponse, err error)
	NewUpdateResourceRecordOptions(instanceID string, dnszoneID string, recordID string) *dnssvcsv1.UpdateResourceRecordOptions
	NewResourceRecordUpdateInputRdataRdataCnameRecord(cname string) (_model *dnssvcsv1.ResourceRecordUpdateInputRdataRdataCnameRecord, err error)
	NewResourceRecordUpdateInputRdataRdataARecord(ip string) (_model *dnssvcsv1.ResourceRecordUpdateInputRdataRdataARecord, err error)
	UpdateResourceRecord(updateResourceRecordOptions *dnssvcsv1.UpdateResourceRecordOptions) (result *dnssvcsv1.ResourceRecord, response *core.DetailedResponse, err error)
	NewCreateResourceRecordOptions(instanceID string, dnszoneID string) *dnssvcsv1.CreateResourceRecordOptions
	NewResourceRecordInputRdataRdataCnameRecord(cname string) (_model *dnssvcsv1.ResourceRecordInputRdataRdataCnameRecord, err error)
	NewResourceRecordInputRdataRdataARecord(ip string) (_model *dnssvcsv1.ResourceRecordInputRdataRdataARecord, err error)
	CreateResourceRecord(createResourceRecordOptions *dnssvcsv1.CreateResourceRecordOptions) (result *dnssvcsv1.ResourceRecord, response *core.DetailedResponse, err error)
	NewGetDnszoneOptions(instanceID string, dnszoneID string) *dnssvcsv1.GetDnszoneOptions
	GetDnszone(getDnszoneOptions *dnssvcsv1.GetDnszoneOptions) (result *dnssvcsv1.Dnszone, response *core.DetailedResponse, err error)
}
