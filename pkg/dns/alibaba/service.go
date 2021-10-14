package alibaba

import (
	"fmt"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/alidns"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/pvtz"
)

type Service interface {
	Add(id, dnsName, recordType, target string, ttl int64) error
	Update(id, dnsName, recordType, target string, ttl int64) error
	Delete(id, dnsName, target string) error
}

type dnsService struct {
	client *sdk.Client
}

func (d *dnsService) Add(id, dnsName, recordType, target string, ttl int64) error {
	request := alidns.CreateAddDomainRecordRequest()
	request.Scheme = "https"
	request.DomainName = id
	request.RR = dnsName
	request.Type = recordType
	request.Value = target
	request.TTL = requests.NewInteger64(ttl)

	response := alidns.CreateAddDomainRecordResponse()
	return d.client.DoAction(request, response)
}

func (d *dnsService) Update(id, dnsName, recordType, target string, ttl int64) error {
	recordID, err := d.getRecordID(id, dnsName, "")
	if err != nil {
		return err
	}

	request := alidns.CreateUpdateDomainRecordRequest()
	request.Scheme = "https"
	request.RecordId = recordID
	request.RR = dnsName
	request.Type = recordType
	request.Value = target
	request.TTL = requests.NewInteger64(ttl)

	response := alidns.CreateUpdateDomainRecordResponse()
	return d.client.DoAction(request, response)
}

func (d *dnsService) Delete(id, dnsName, target string) error {
	recordID, err := d.getRecordID(id, dnsName, target)
	if err != nil {
		return err
	}

	request := alidns.CreateDeleteDomainRecordRequest()
	request.Scheme = "https"
	request.RecordId = recordID

	response := alidns.CreateDeleteDomainRecordResponse()

	return d.client.DoAction(request, response)
}

// getRecordID finds the ID by dns name and an optional argument target.
func (d *dnsService) getRecordID(id, dnsName, target string) (string, error) {
	request := alidns.CreateDescribeDomainRecordsRequest()
	request.Scheme = "https"
	request.DomainName = id
	request.KeyWord = dnsName
	request.SearchMode = "EXACT"

	response := alidns.CreateDescribeDomainRecordsResponse()
	err := d.client.DoAction(request, response)
	if err != nil {
		return "", fmt.Errorf("failed on describe domain records: %w", err)
	}

	for _, record := range response.DomainRecords.Record {
		if record.RR == dnsName && (target == "" || target == record.Value) {
			return record.RecordId, nil
		}
	}

	return "", fmt.Errorf("can not find record '%s' for domain '%s'", dnsName, id)
}

type pvtzService struct {
	client *sdk.Client
}

func (p *pvtzService) Add(id, dnsName, recordType, target string, ttl int64) error {
	request := pvtz.CreateAddZoneRecordRequest()
	request.Scheme = "https"
	request.ZoneId = id
	request.Rr = dnsName
	request.Type = recordType
	request.Value = target
	request.Ttl = requests.NewInteger64(ttl)

	response := pvtz.CreateAddZoneRecordResponse()
	return p.client.DoAction(request, response)
}

func (p *pvtzService) Update(id, dnsName, recordType, target string, ttl int64) error {
	recordID, err := p.getRecordID(id, dnsName, "")
	if err != nil {
		return err
	}

	request := pvtz.CreateUpdateZoneRecordRequest()
	request.Scheme = "https"
	request.RecordId = requests.NewInteger64(recordID)
	request.Rr = dnsName
	request.Type = recordType
	request.Value = target
	request.Ttl = requests.NewInteger64(ttl)

	response := pvtz.CreateUpdateZoneRecordResponse()

	return p.client.DoAction(request, response)
}

func (p *pvtzService) Delete(id, dnsName, target string) error {
	recordID, err := p.getRecordID(id, dnsName, target)
	if err != nil {
		return err
	}

	request := pvtz.CreateDeleteZoneRecordRequest()
	request.Scheme = "https"
	request.RecordId = requests.NewInteger64(recordID)

	response := pvtz.CreateDeleteZoneRecordResponse()
	return p.client.DoAction(request, response)
}

// getRecordID finds the ID by dns name and an optional argument target.
func (p *pvtzService) getRecordID(id, dnsName, target string) (int64, error) {
	request := pvtz.CreateDescribeZoneRecordsRequest()
	request.Scheme = "https"
	request.ZoneId = id
	request.Keyword = dnsName
	request.SearchMode = "EXACT"

	response := pvtz.CreateDescribeZoneRecordsResponse()
	err := p.client.DoAction(request, response)
	if err != nil {
		return 0, fmt.Errorf("failed on describe pvtz records: %w", err)
	}

	for _, record := range response.Records.Record {
		if record.Rr == dnsName && (target == "" || target == record.Value) {
			return record.RecordId, nil
		}
	}

	return 0, fmt.Errorf("can not find record '%s' for pvtz '%s'", dnsName, id)
}

func NewDNSService(client *sdk.Client) Service {
	return &dnsService{client: client}
}

func NewPvtzService(client *sdk.Client) Service {
	return &pvtzService{client: client}
}
