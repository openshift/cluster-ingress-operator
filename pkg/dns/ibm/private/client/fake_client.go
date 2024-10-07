package client

import (
	"errors"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnssvcsv1"
)

type FakeDnsClient struct {
	CallHistory                  map[string]string
	DeleteDnsRecordInputOutput   DeleteDnsRecordInputOutput
	ListAllDnsRecordsInputOutput ListAllDnsRecordsInputOutput
	UpdateDnsRecordInputOutput   UpdateDnsRecordInputOutput
	CreateDnsRecordInputOutput   CreateDnsRecordInputOutput
}

type DeleteDnsRecordInputOutput struct {
	InputId        string
	OutputError    error
	OutputResponse *core.DetailedResponse
}

type UpdateDnsRecordInputOutput struct {
	InputId        string
	OutputError    error
	OutputResponse *core.DetailedResponse
}
type CreateDnsRecordInputOutput struct {
	InputId        string
	OutputError    error
	OutputResponse *core.DetailedResponse
}

type ListAllDnsRecordsInputOutput struct {
	OutputResult   *dnssvcsv1.ListResourceRecords
	OutputError    error
	OutputResponse *core.DetailedResponse
}

func NewFake() (*FakeDnsClient, error) {
	return &FakeDnsClient{CallHistory: map[string]string{}}, nil
}

func (c *FakeDnsClient) RecordedCall(zoneID string) (string, bool) {
	call, ok := c.CallHistory[zoneID]
	return call, ok
}

func (c *FakeDnsClient) ClearCallHistory() {
	c.CallHistory = map[string]string{}
}

func (FakeDnsClient) NewListResourceRecordsOptions(instanceID string, dnszoneID string) *dnssvcsv1.ListResourceRecordsOptions {
	return &dnssvcsv1.ListResourceRecordsOptions{}
}
func (fdc FakeDnsClient) ListResourceRecords(listResourceRecordsOptions *dnssvcsv1.ListResourceRecordsOptions) (result *dnssvcsv1.ListResourceRecords, response *core.DetailedResponse, err error) {
	return fdc.ListAllDnsRecordsInputOutput.OutputResult, fdc.ListAllDnsRecordsInputOutput.OutputResponse, fdc.ListAllDnsRecordsInputOutput.OutputError
}
func (FakeDnsClient) NewDeleteResourceRecordOptions(instanceID string, dnszoneID string, recordID string) *dnssvcsv1.DeleteResourceRecordOptions {
	return &dnssvcsv1.DeleteResourceRecordOptions{InstanceID: &instanceID, DnszoneID: &dnszoneID, RecordID: &recordID}
}
func (fdc FakeDnsClient) DeleteResourceRecord(deleteResourceRecordOptions *dnssvcsv1.DeleteResourceRecordOptions) (response *core.DetailedResponse, err error) {
	if fdc.DeleteDnsRecordInputOutput.InputId != *deleteResourceRecordOptions.RecordID {
		return nil, errors.New("deleteDnsRecord: inputs don't match")
	}

	fdc.CallHistory[*deleteResourceRecordOptions.RecordID] = "DELETE"
	return fdc.DeleteDnsRecordInputOutput.OutputResponse, fdc.DeleteDnsRecordInputOutput.OutputError
}
func (FakeDnsClient) NewUpdateResourceRecordOptions(instanceID string, dnszoneID string, recordID string) *dnssvcsv1.UpdateResourceRecordOptions {
	return &dnssvcsv1.UpdateResourceRecordOptions{InstanceID: &instanceID, DnszoneID: &dnszoneID, RecordID: &recordID}
}
func (FakeDnsClient) NewResourceRecordUpdateInputRdataRdataCnameRecord(cname string) (_model *dnssvcsv1.ResourceRecordUpdateInputRdataRdataCnameRecord, err error) {
	return &dnssvcsv1.ResourceRecordUpdateInputRdataRdataCnameRecord{Cname: &cname}, nil
}
func (FakeDnsClient) NewResourceRecordUpdateInputRdataRdataARecord(ip string) (_model *dnssvcsv1.ResourceRecordUpdateInputRdataRdataARecord, err error) {
	return &dnssvcsv1.ResourceRecordUpdateInputRdataRdataARecord{Ip: &ip}, nil
}
func (fdc FakeDnsClient) UpdateResourceRecord(updateResourceRecordOptions *dnssvcsv1.UpdateResourceRecordOptions) (result *dnssvcsv1.ResourceRecord, response *core.DetailedResponse, err error) {
	if fdc.UpdateDnsRecordInputOutput.InputId != *updateResourceRecordOptions.RecordID {
		return nil, nil, errors.New("updateDnsRecord: inputs don't match")
	}

	fdc.CallHistory[*updateResourceRecordOptions.RecordID] = "PUT"
	return nil, fdc.UpdateDnsRecordInputOutput.OutputResponse, fdc.UpdateDnsRecordInputOutput.OutputError
}
func (FakeDnsClient) NewCreateResourceRecordOptions(instanceID string, dnszoneID string) *dnssvcsv1.CreateResourceRecordOptions {
	return &dnssvcsv1.CreateResourceRecordOptions{}
}
func (FakeDnsClient) NewResourceRecordInputRdataRdataCnameRecord(cname string) (_model *dnssvcsv1.ResourceRecordInputRdataRdataCnameRecord, err error) {
	return nil, nil
}
func (FakeDnsClient) NewResourceRecordInputRdataRdataARecord(ip string) (_model *dnssvcsv1.ResourceRecordInputRdataRdataARecord, err error) {
	return nil, nil
}
func (fdc FakeDnsClient) CreateResourceRecord(createResourceRecordOptions *dnssvcsv1.CreateResourceRecordOptions) (result *dnssvcsv1.ResourceRecord, response *core.DetailedResponse, err error) {
	if fdc.CreateDnsRecordInputOutput.InputId != *createResourceRecordOptions.Name {
		return nil, nil, errors.New("createResourceRecord: inputs don't match")
	}

	fdc.CallHistory[*createResourceRecordOptions.Name] = "CREATE"
	return nil, fdc.CreateDnsRecordInputOutput.OutputResponse, fdc.CreateDnsRecordInputOutput.OutputError
}
func (FakeDnsClient) NewGetDnszoneOptions(instanceID string, dnszoneID string) *dnssvcsv1.GetDnszoneOptions {
	return nil
}
func (FakeDnsClient) GetDnszone(getDnszoneOptions *dnssvcsv1.GetDnszoneOptions) (result *dnssvcsv1.Dnszone, response *core.DetailedResponse, err error) {
	return nil, nil, nil
}
