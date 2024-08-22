package client

import (
	"errors"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnsrecordsv1"
)

type FakeDnsClient struct {
	CallHistory                  map[string]string
	DeleteDnsRecordInputOutput   DeleteDnsRecordInputOutput
	ListAllDnsRecordsInputOutput ListAllDnsRecordsInputOutput
	UpdateDnsRecordInputOutput   UpdateDnsRecordInputOutput
}

type DeleteDnsRecordInputOutput struct {
	InputId          string
	OutputError      error
	OutputStatusCode int
}

type UpdateDnsRecordInputOutput struct {
	InputId          string
	OutputError      error
	OutputStatusCode int
}

type ListAllDnsRecordsInputOutput struct {
	OutputError      error
	OutputStatusCode int
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
	fakeListDnsrecordsResp := &dnsrecordsv1.ListDnsrecordsResp{}

	fakeListDnsrecordsResp.Result = append(fakeListDnsrecordsResp.Result, dnsrecordsv1.DnsrecordDetails{ID: listAllDnsRecordsOptions.Name})

	resp := &core.DetailedResponse{
		StatusCode: fdc.ListAllDnsRecordsInputOutput.OutputStatusCode,
		Headers:    map[string][]string{},
		Result:     result,
		RawResult:  []byte{},
	}

	return fakeListDnsrecordsResp, resp, fdc.ListAllDnsRecordsInputOutput.OutputError
}

func (FakeDnsClient) CreateDnsRecord(createDnsRecordOptions *dnsrecordsv1.CreateDnsRecordOptions) (result *dnsrecordsv1.DnsrecordResp, response *core.DetailedResponse, err error) {
	return nil, nil, nil
}

func (fdc FakeDnsClient) DeleteDnsRecord(deleteDnsRecordOptions *dnsrecordsv1.DeleteDnsRecordOptions) (result *dnsrecordsv1.DeleteDnsrecordResp, response *core.DetailedResponse, err error) {
	if fdc.DeleteDnsRecordInputOutput.InputId != *deleteDnsRecordOptions.DnsrecordIdentifier {
		return nil, nil, errors.New("deleteDnsRecord: inputs don't match")
	}

	resp := &core.DetailedResponse{
		StatusCode: fdc.DeleteDnsRecordInputOutput.OutputStatusCode,
		Headers:    map[string][]string{},
		Result:     result,
		RawResult:  []byte{},
	}

	fdc.CallHistory[*deleteDnsRecordOptions.DnsrecordIdentifier] = "DELETE"
	return nil, resp, fdc.DeleteDnsRecordInputOutput.OutputError
}

func (fdc FakeDnsClient) UpdateDnsRecord(updateDnsRecordOptions *dnsrecordsv1.UpdateDnsRecordOptions) (result *dnsrecordsv1.DnsrecordResp, response *core.DetailedResponse, err error) {
	if fdc.UpdateDnsRecordInputOutput.InputId != *updateDnsRecordOptions.DnsrecordIdentifier {
		return nil, nil, errors.New("updateDnsRecord: inputs don't match")
	}

	resp := &core.DetailedResponse{
		StatusCode: fdc.UpdateDnsRecordInputOutput.OutputStatusCode,
		Headers:    map[string][]string{},
		Result:     result,
		RawResult:  []byte{},
	}

	fdc.CallHistory[*updateDnsRecordOptions.DnsrecordIdentifier] = "PUT"
	return nil, resp, fdc.UpdateDnsRecordInputOutput.OutputError
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
