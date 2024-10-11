package client

import (
	"errors"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnsrecordsv1"
)

type FakeDnsClient struct {
	CallHistory                  map[string]string
	DeleteDnsRecordInputOutput   DeleteDnsRecordInputOutput
	CreateDnsRecordInputOutput   CreateDnsRecordInputOutput
	ListAllDnsRecordsInputOutput ListAllDnsRecordsInputOutput
	UpdateDnsRecordInputOutput   UpdateDnsRecordInputOutput
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
	OutputResult   *dnsrecordsv1.ListDnsrecordsResp
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

func (fdc FakeDnsClient) ListAllDnsRecords(listAllDnsRecordsOptions *dnsrecordsv1.ListAllDnsRecordsOptions) (result *dnsrecordsv1.ListDnsrecordsResp, response *core.DetailedResponse, err error) {
	return fdc.ListAllDnsRecordsInputOutput.OutputResult, fdc.ListAllDnsRecordsInputOutput.OutputResponse, fdc.ListAllDnsRecordsInputOutput.OutputError
}

func (fdc FakeDnsClient) CreateDnsRecord(createDnsRecordOptions *dnsrecordsv1.CreateDnsRecordOptions) (result *dnsrecordsv1.DnsrecordResp, response *core.DetailedResponse, err error) {
	if fdc.CreateDnsRecordInputOutput.InputId != *createDnsRecordOptions.Name {
		return nil, nil, errors.New("createDnsRecord: inputs don't match")
	}

	fdc.CallHistory[*createDnsRecordOptions.Name] = "CREATE"
	return nil, fdc.CreateDnsRecordInputOutput.OutputResponse, fdc.CreateDnsRecordInputOutput.OutputError
}

func (fdc FakeDnsClient) DeleteDnsRecord(deleteDnsRecordOptions *dnsrecordsv1.DeleteDnsRecordOptions) (result *dnsrecordsv1.DeleteDnsrecordResp, response *core.DetailedResponse, err error) {
	if fdc.DeleteDnsRecordInputOutput.InputId != *deleteDnsRecordOptions.DnsrecordIdentifier {
		return nil, nil, errors.New("deleteDnsRecord: inputs don't match")
	}

	fdc.CallHistory[*deleteDnsRecordOptions.DnsrecordIdentifier] = "DELETE"
	return nil, fdc.DeleteDnsRecordInputOutput.OutputResponse, fdc.DeleteDnsRecordInputOutput.OutputError
}

func (fdc FakeDnsClient) UpdateDnsRecord(updateDnsRecordOptions *dnsrecordsv1.UpdateDnsRecordOptions) (result *dnsrecordsv1.DnsrecordResp, response *core.DetailedResponse, err error) {
	if fdc.UpdateDnsRecordInputOutput.InputId != *updateDnsRecordOptions.DnsrecordIdentifier {
		return nil, nil, errors.New("updateDnsRecord: inputs don't match")
	}

	fdc.CallHistory[*updateDnsRecordOptions.DnsrecordIdentifier] = "PUT"
	return nil, fdc.UpdateDnsRecordInputOutput.OutputResponse, fdc.UpdateDnsRecordInputOutput.OutputError
}

func (FakeDnsClient) NewCreateDnsRecordOptions() *dnsrecordsv1.CreateDnsRecordOptions {
	return &dnsrecordsv1.CreateDnsRecordOptions{}
}

func (FakeDnsClient) NewDeleteDnsRecordOptions(dnsrecordIdentifier string) *dnsrecordsv1.DeleteDnsRecordOptions {
	return &dnsrecordsv1.DeleteDnsRecordOptions{DnsrecordIdentifier: &dnsrecordIdentifier}
}

func (FakeDnsClient) NewListAllDnsRecordsOptions() *dnsrecordsv1.ListAllDnsRecordsOptions {
	return &dnsrecordsv1.ListAllDnsRecordsOptions{}
}

func (FakeDnsClient) NewUpdateDnsRecordOptions(dnsrecordIdentifier string) *dnsrecordsv1.UpdateDnsRecordOptions {
	return &dnsrecordsv1.UpdateDnsRecordOptions{DnsrecordIdentifier: &dnsrecordIdentifier}
}
