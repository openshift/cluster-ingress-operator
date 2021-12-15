package alibaba

import (
	"fmt"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/endpoints"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/responses"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/alidns"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/pvtz"
	"github.com/openshift/cluster-ingress-operator/pkg/dns/alibaba/util"
	"strings"
	"sync"
)

var (
	// defaultEndpoints saves the default endpoints (unrelated to region of the cluster) for the specified product.
	// Only public zone(alidns) and private zone(pvtz) will be called in dns provider, so there are two entries here.
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

// publicZoneService is an implementation of the Service interface for public zones,
// and the public zone is called "alidns" on AlibabaCloud platform.
type publicZoneService struct {
	client *Client
}

func (d *publicZoneService) Add(id, rr, recordType, target string, ttl int64) error {
	request := alidns.CreateAddDomainRecordRequest()
	request.Scheme = "https"
	request.DomainName = id
	request.RR = rr
	request.Type = recordType
	request.Value = target

	// A valid TTL for public zone must be in the range of 600 to 86400.
	clampedTTL := util.Clamp(ttl, 600, 86400)
	if clampedTTL != ttl {
		log.Info(fmt.Sprintf("record's TTL for public zone must be in the range of 600 to 86400, set it to %d", clampedTTL), "record", rr)
	}
	request.TTL = requests.NewInteger64(clampedTTL)

	response := alidns.CreateAddDomainRecordResponse()
	return d.client.DoActionWithSetDomain(request, response)
}

func (d *publicZoneService) Update(id, rr, recordType, target string, ttl int64) error {
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
	clampedTTL := util.Clamp(ttl, 600, 86400)
	if clampedTTL != ttl {
		log.Info(fmt.Sprintf("record's TTL for public zone must be in the range of 600 to 86400, set it to %d", clampedTTL), "record", rr)
	}
	request.TTL = requests.NewInteger64(clampedTTL)

	response := alidns.CreateUpdateDomainRecordResponse()
	return d.client.DoActionWithSetDomain(request, response)
}

func (d *publicZoneService) Delete(id, rr, target string) error {
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
func (d *publicZoneService) getRecordID(id, dnsName, target string) (string, error) {
	request := alidns.CreateDescribeDomainRecordsRequest()
	request.Scheme = "https"
	request.DomainName = id
	request.KeyWord = dnsName
	request.SearchMode = "EXACT"

	response := alidns.CreateDescribeDomainRecordsResponse()
	if err := d.client.DoActionWithSetDomain(request, response); err != nil {
		return "", fmt.Errorf("failed on describe domain records: %w", err)
	}

	for _, record := range response.DomainRecords.Record {
		if record.RR == dnsName && (target == "" || target == record.Value) {
			return record.RecordId, nil
		}
	}

	return "", fmt.Errorf("cannot find record %q for domain %q", dnsName, id)
}

// privateZoneService is an implementation of the Service interface for public zones,
// and the private zone is called "pvtz" on AlibabaCloud platform.
type privateZoneService struct {
	client *Client
	// pvtzIDs caches zone IDs with their associated zone names.
	pvtzIDs map[string]string
	mutex   sync.Mutex
}

func (p *privateZoneService) Add(zoneName, rr, recordType, target string, ttl int64) error {
	// The first argument "id" in Service is actually zone name in the implementation of private zone.
	// The zone name is used to lookup zone ID used in following requests.
	id, err := p.lookupPrivateZoneID(zoneName)
	if err != nil {
		return fmt.Errorf("failed lookup private zone id: %w", err)
	}

	request := pvtz.CreateAddZoneRecordRequest()
	request.Scheme = "https"
	request.ZoneId = id
	request.Rr = rr
	request.Type = recordType
	request.Value = target

	// A valid TTL for private zone must be in the range of 5 to 86400.
	clampedTTL := util.Clamp(ttl, 5, 86400)
	if clampedTTL != ttl {
		log.Info(fmt.Sprintf("record's TTL for private zone must be in the range of 600 to 86400, set it to %d", clampedTTL), "record", rr)
	}
	request.Ttl = requests.NewInteger64(clampedTTL)

	response := pvtz.CreateAddZoneRecordResponse()
	return p.client.DoActionWithSetDomain(request, response)
}

func (p *privateZoneService) Update(zoneName, rr, recordType, target string, ttl int64) error {
	id, err := p.lookupPrivateZoneID(zoneName)
	if err != nil {
		return fmt.Errorf("failed lookup private zone id: %w", err)
	}

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
	clampedTTL := util.Clamp(ttl, 5, 86400)
	if clampedTTL != ttl {
		log.Info(fmt.Sprintf("record's TTL for private zone must be in the range of 600 to 86400, set it to %d", clampedTTL), "record", rr)
	}
	request.Ttl = requests.NewInteger64(clampedTTL)

	response := pvtz.CreateUpdateZoneRecordResponse()
	return p.client.DoActionWithSetDomain(request, response)
}

func (p *privateZoneService) Delete(zoneName, rr, target string) error {
	id, err := p.lookupPrivateZoneID(zoneName)
	if err != nil {
		return fmt.Errorf("failed lookup private zone id: %w", err)
	}

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
func (p *privateZoneService) getRecordID(id, dnsName, target string) (int64, error) {
	request := pvtz.CreateDescribeZoneRecordsRequest()
	request.Scheme = "https"
	request.ZoneId = id
	request.Keyword = dnsName
	request.SearchMode = "EXACT"

	response := pvtz.CreateDescribeZoneRecordsResponse()
	if err := p.client.DoActionWithSetDomain(request, response); err != nil {
		return 0, fmt.Errorf("failed on describe pvtz records: %w", err)
	}

	for _, record := range response.Records.Record {
		if record.Rr == dnsName && (target == "" || target == record.Value) {
			return record.RecordId, nil
		}
	}

	return 0, fmt.Errorf("cannot find record %q for pvtz %q", dnsName, id)
}

// lookupPrivateZoneID finds zone ID, and caches it when the zone ID is retrieved successfully.
func (p *privateZoneService) lookupPrivateZoneID(zoneName string) (string, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// lookup zoneName in cache first
	if id, ok := p.pvtzIDs[zoneName]; ok {
		return id, nil
	}

	request := pvtz.CreateDescribeZonesRequest()
	request.Scheme = "https"
	request.QueryRegionId = p.client.RegionID
	request.Keyword = zoneName
	request.SearchMode = "EXACT"
	request.PageSize = requests.NewInteger(100)
	response := pvtz.CreateDescribeZonesResponse()

	if err := p.client.DoActionWithSetDomain(request, response); err != nil {
		return "", fmt.Errorf("failed on describe private zones: %w", err)
	}

	for _, zone := range response.Zones.Zone {
		if zoneName == zone.ZoneName {
			log.Info("found private zone ID %q for zone name %s, add it to cache", zone.ZoneId, zoneName)
			p.pvtzIDs[zoneName] = zone.ZoneId
			return zone.ZoneId, nil
		}
	}

	return "", fmt.Errorf("private zone id for name %q not found", zoneName)
}

func NewPublicZoneService(client *Client) Service {
	return &publicZoneService{
		client: client,
	}
}

func NewPrivateZoneService(client *Client) Service {
	return &privateZoneService{
		client:  client,
		pvtzIDs: make(map[string]string),
	}
}

// NewClient creates a new AlibabaCloud OpenAPI client
func NewClient(sdkClient *sdk.Client, regionID string) *Client {
	return &Client{
		Client:   sdkClient,
		RegionID: regionID,
	}
}

// DoActionWithSetDomain resolves the endpoint for the given API call, and does the request.
// For some reason, the SDK will return an error if there's no endpoint for this region,
// so it's necessary to set a default endpoint manually for now.
func (client *Client) DoActionWithSetDomain(request requests.AcsRequest, response responses.AcsResponse) error {
	endpoint, err := endpoints.Resolve(&endpoints.ResolveParam{
		Product:  strings.ToLower(request.GetProduct()),
		RegionId: strings.ToLower(client.RegionID),
	})
	if err != nil {
		var ok bool
		// although it should be guaranteed that product ID will always be found in defaultEndpoints,
		// to be safety, it will return error here instead of panic.
		endpoint, ok = defaultEndpoints[strings.ToLower(request.GetProduct())]
		if !ok {
			return fmt.Errorf("failed find default endpoint for product %s", request.GetProduct())
		}
	}
	request.SetDomain(endpoint)

	return client.DoAction(request, response)
}
