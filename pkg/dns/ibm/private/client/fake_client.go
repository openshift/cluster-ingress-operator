package client

import (
	"errors"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnssvcsv1"
	iov1 "github.com/openshift/api/operatoringress/v1"
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
	RecordName       string
	RecordTarget     string
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

func (FakeDnsClient) NewListResourceRecordsOptions(instanceID string, dnszoneID string) *dnssvcsv1.ListResourceRecordsOptions {
	return &dnssvcsv1.ListResourceRecordsOptions{}
}
func (fdc FakeDnsClient) ListResourceRecords(listResourceRecordsOptions *dnssvcsv1.ListResourceRecordsOptions) (result *dnssvcsv1.ListResourceRecords, response *core.DetailedResponse, err error) {
	fakeListDnsrecordsResp := &dnssvcsv1.ListResourceRecords{}
	recordType := string(iov1.ARecordType)
	rData := map[string]interface{}{"ip": fdc.ListAllDnsRecordsInputOutput.RecordTarget}

	fakeListDnsrecordsResp.ResourceRecords = append(fakeListDnsrecordsResp.ResourceRecords, dnssvcsv1.ResourceRecord{ID: &fdc.ListAllDnsRecordsInputOutput.RecordName, Name: &fdc.ListAllDnsRecordsInputOutput.RecordName, Type: &recordType, Rdata: rData})

	resp := &core.DetailedResponse{
		StatusCode: fdc.ListAllDnsRecordsInputOutput.OutputStatusCode,
		Headers:    map[string][]string{},
		Result:     result,
		RawResult:  []byte{},
	}

	return fakeListDnsrecordsResp, resp, fdc.ListAllDnsRecordsInputOutput.OutputError
}
func (FakeDnsClient) NewDeleteResourceRecordOptions(instanceID string, dnszoneID string, recordID string) *dnssvcsv1.DeleteResourceRecordOptions {
	return &dnssvcsv1.DeleteResourceRecordOptions{InstanceID: &instanceID, DnszoneID: &dnszoneID, RecordID: &recordID}
}
func (fdc FakeDnsClient) DeleteResourceRecord(deleteResourceRecordOptions *dnssvcsv1.DeleteResourceRecordOptions) (response *core.DetailedResponse, err error) {
	if fdc.DeleteDnsRecordInputOutput.InputId != *deleteResourceRecordOptions.RecordID {
		return nil, errors.New("deleteDnsRecord: inputs don't match")
	}

	resp := &core.DetailedResponse{
		StatusCode: fdc.DeleteDnsRecordInputOutput.OutputStatusCode,
		Headers:    map[string][]string{},
		Result:     response,
		RawResult:  []byte{},
	}

	fdc.CallHistory[*deleteResourceRecordOptions.RecordID] = "DELETE"
	return resp, fdc.DeleteDnsRecordInputOutput.OutputError
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

	resp := &core.DetailedResponse{
		StatusCode: fdc.UpdateDnsRecordInputOutput.OutputStatusCode,
		Headers:    map[string][]string{},
		Result:     response,
		RawResult:  []byte{},
	}

	fdc.CallHistory[*updateResourceRecordOptions.RecordID] = "PUT"
	return nil, resp, fdc.UpdateDnsRecordInputOutput.OutputError
}
func (FakeDnsClient) NewCreateResourceRecordOptions(instanceID string, dnszoneID string) *dnssvcsv1.CreateResourceRecordOptions {
	return nil
}
func (FakeDnsClient) NewResourceRecordInputRdataRdataCnameRecord(cname string) (_model *dnssvcsv1.ResourceRecordInputRdataRdataCnameRecord, err error) {
	return nil, nil
}
func (FakeDnsClient) NewResourceRecordInputRdataRdataARecord(ip string) (_model *dnssvcsv1.ResourceRecordInputRdataRdataARecord, err error) {
	return nil, nil
}
func (FakeDnsClient) CreateResourceRecord(createResourceRecordOptions *dnssvcsv1.CreateResourceRecordOptions) (result *dnssvcsv1.ResourceRecord, response *core.DetailedResponse, err error) {
	return nil, nil, nil
}
func (FakeDnsClient) NewGetDnszoneOptions(instanceID string, dnszoneID string) *dnssvcsv1.GetDnszoneOptions {
	return nil
}
func (FakeDnsClient) GetDnszone(getDnszoneOptions *dnssvcsv1.GetDnszoneOptions) (result *dnssvcsv1.Dnszone, response *core.DetailedResponse, err error) {
	return nil, nil, nil
}
