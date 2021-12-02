package alibaba

import (
	"fmt"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/endpoints"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/responses"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/alidns"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/pvtz"
	"strings"
)

var (
	defaultEndpoints = map[string]string{
		"pvtz":   "pvtz.aliyuncs.com",
		"alidns": "alidns.aliyuncs.com",
	}
)

type Service interface {
	Add(id, rr, recordType, target string, ttl int64) error
	Update(id, rr, recordType, target string, ttl int64) error
	Delete(id, rr, target string) error
}

type Client struct {
	*sdk.Client
	RegionID string
}

type dnsService struct {
	client *Client
}

func (d *dnsService) Add(id, rr, recordType, target string, ttl int64) error {
	request := alidns.CreateAddDomainRecordRequest()
	request.Scheme = "https"
	request.DomainName = id
	request.RR = rr
	request.Type = recordType
	request.Value = target

	// A valid TTL for public zone must be in the range of 600 to 86400.
	if ttl < 600 {
		log.Info("record's TTL for public zone  can't be smaller than 600, set it to 600.", "record", rr)
		ttl = 600
	}
	if ttl > 86400 {
		log.Info("record's TTL for public zone  can't be greater than 86400, set it to 86400.", "record", rr)
		ttl = 86400
	}
	request.TTL = requests.NewInteger64(ttl)

	response := alidns.CreateAddDomainRecordResponse()
	return d.client.DoActionWithSetDomain(request, response)
}

func (d *dnsService) Update(id, rr, recordType, target string, ttl int64) error {
	recordID, err := d.getRecordID(id, rr, "")
	if err != nil {
		return err
	}

	request := alidns.CreateUpdateDomainRecordRequest()
	request.Scheme = "https"
	request.RecordId = recordID
	request.RR = rr
	request.Type = recordType
	request.Value = target

	// A valid TTL for public zone must be in the range of 600 to 86400.
	if ttl < 600 {
		log.Info("record's TTL for public zone  can't be smaller than 600, set it to 600.", "record", rr)
		ttl = 600
	}
	if ttl > 86400 {
		log.Info("record's TTL for public zone can't be greater than 86400, set it to 86400.", "record", rr)
		ttl = 86400
	}
	request.TTL = requests.NewInteger64(ttl)

	response := alidns.CreateUpdateDomainRecordResponse()
	return d.client.DoActionWithSetDomain(request, response)
}

func (d *dnsService) Delete(id, rr, target string) error {
	recordID, err := d.getRecordID(id, rr, target)
	if err != nil {
		return err
	}

	request := alidns.CreateDeleteDomainRecordRequest()
	request.Scheme = "https"
	request.RecordId = recordID

	response := alidns.CreateDeleteDomainRecordResponse()

	return d.client.DoActionWithSetDomain(request, response)
}

// getRecordID finds the ID by dns name and an optional argument target.
func (d *dnsService) getRecordID(id, dnsName, target string) (string, error) {
	request := alidns.CreateDescribeDomainRecordsRequest()
	request.Scheme = "https"
	request.DomainName = id
	request.KeyWord = dnsName
	request.SearchMode = "EXACT"

	response := alidns.CreateDescribeDomainRecordsResponse()
	err := d.client.DoActionWithSetDomain(request, response)
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
	client *Client
}

func (p *pvtzService) Add(id, rr, recordType, target string, ttl int64) error {
	request := pvtz.CreateAddZoneRecordRequest()
	request.Scheme = "https"
	request.ZoneId = id
	request.Rr = rr
	request.Type = recordType
	request.Value = target

	// A valid TTL for private zone must be in the range of 5 to 86400.
	if ttl < 5 {
		log.Info("record's TTL for private zone can't be smaller than 5, set it to 5.", "record", rr)
		ttl = 5
	}
	if ttl > 86400 {
		log.Info("record's TTL for private zone can't be greater than 86400, set it to 86400.", "record", rr)
		ttl = 86400
	}
	request.Ttl = requests.NewInteger64(ttl)

	response := pvtz.CreateAddZoneRecordResponse()
	return p.client.DoActionWithSetDomain(request, response)
}

func (p *pvtzService) Update(id, rr, recordType, target string, ttl int64) error {
	recordID, err := p.getRecordID(id, rr, "")
	if err != nil {
		return err
	}

	request := pvtz.CreateUpdateZoneRecordRequest()
	request.Scheme = "https"
	request.RecordId = requests.NewInteger64(recordID)
	request.Rr = rr
	request.Type = recordType
	request.Value = target

	// A valid TTL for private zone must be in the range of 5 to 86400.
	if ttl < 5 {
		log.Info("record's TTL for private zone can't be smaller than 5, set it to 5.", "record", rr)
		ttl = 5
	}
	if ttl > 86400 {
		log.Info("record's TTL for private zone can't be greater than 86400, set it to 86400.", "record", rr)
		ttl = 86400
	}
	request.Ttl = requests.NewInteger64(ttl)

	response := pvtz.CreateUpdateZoneRecordResponse()

	return p.client.DoActionWithSetDomain(request, response)
}

func (p *pvtzService) Delete(id, rr, target string) error {
	recordID, err := p.getRecordID(id, rr, target)
	if err != nil {
		return err
	}

	request := pvtz.CreateDeleteZoneRecordRequest()
	request.Scheme = "https"
	request.RecordId = requests.NewInteger64(recordID)

	response := pvtz.CreateDeleteZoneRecordResponse()
	return p.client.DoActionWithSetDomain(request, response)
}

// getRecordID finds the ID by dns name and an optional argument target.
func (p *pvtzService) getRecordID(id, dnsName, target string) (int64, error) {
	request := pvtz.CreateDescribeZoneRecordsRequest()
	request.Scheme = "https"
	request.ZoneId = id
	request.Keyword = dnsName
	request.SearchMode = "EXACT"

	response := pvtz.CreateDescribeZoneRecordsResponse()
	err := p.client.DoActionWithSetDomain(request, response)
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

func NewDNSService(client *sdk.Client, regionID string) Service {
	return &dnsService{
		client: &Client{
			Client:   client,
			RegionID: regionID,
		},
	}
}

func NewPvtzService(client *sdk.Client, regionID string) Service {
	return &pvtzService{
		client: &Client{
			Client:   client,
			RegionID: regionID,
		},
	}
}

func (client *Client) DoActionWithSetDomain(request requests.AcsRequest, response responses.AcsResponse) (err error) {
	endpoint, err := endpoints.Resolve(&endpoints.ResolveParam{
		Product:  strings.ToLower(request.GetProduct()),
		RegionId: strings.ToLower(client.RegionID),
	})

	if err != nil {
		endpoint = defaultEndpoints[strings.ToLower(request.GetProduct())]
	}

	request.SetDomain(endpoint)
	err = client.DoAction(request, response)
	return
}
