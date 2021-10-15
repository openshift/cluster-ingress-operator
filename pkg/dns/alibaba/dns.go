package alibaba

import (
	"fmt"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/pvtz"
	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
)

const (
	zoneTypeDNS         = "public"
	zoneTypePrivateZone = "private"

	actionEnsure  = "ensure"
	actionReplace = "replace"
	actionDelete  = "delete"
)

type Config struct {
	Region       string
	AccessKeyID  string
	AccessSecret string
	// PrivateZones is array of private zone names.
	PrivateZones []string
}

type ZoneInfo struct {
	// Type is the type of zone,
	// should be 'dns' or 'pvtz'
	Type string
	// ID is the value used in OpenAPI to leverage dns records.
	// In DNS(public zone), it should be domain name. And in private zone, it should be ZoneID
	ID string
}

type provider struct {
	config   Config
	services map[string]Service
	pvtzIDs  map[string]string
}

func NewProvider(config Config) (dns.Provider, error) {
	client, err := sdk.NewClientWithAccessKey(config.Region, config.AccessKeyID, config.AccessSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create alibaba cloud api service: %w", err)
	}

	pvtzIDs := make(map[string]string)
	for _, zoneName := range config.PrivateZones {
		request := pvtz.CreateDescribeZonesRequest()
		request.Scheme = "https"
		request.QueryRegionId = config.Region
		request.Keyword = zoneName
		request.SearchMode = "EXACT"
		request.PageSize = requests.NewInteger(100)
		response := pvtz.CreateDescribeZonesResponse()
		err := client.DoAction(request, response)
		if err != nil {
			return nil, fmt.Errorf("failed on describe private zones: %w", err)
		}

		for _, zones := range response.Zones.Zone {
			if zoneName == zones.ZoneName {
				pvtzIDs[zoneName] = zones.ZoneId
				break
			}
		}

		if _, ok := pvtzIDs[zoneName]; !ok {
			return nil, fmt.Errorf("zone id for name '%s' not found", zoneName)
		}
	}
	return &provider{
		config: config,
		services: map[string]Service{
			zoneTypeDNS:         NewDNSService(client),
			zoneTypePrivateZone: NewPvtzService(client),
		},
		pvtzIDs: pvtzIDs,
	}, nil

}

// parseZone parses the zone id to ZoneInfo
func (p *provider) parseZone(zone configv1.DNSZone) (ZoneInfo, error) {
	zoneType, ok := zone.Tags["type"]
	if !ok {
		return ZoneInfo{}, fmt.Errorf("can not find 'tag' type in DNSZone")
	}

	zoneID := zone.ID
	if zoneType == zoneTypePrivateZone {
		// lookup private zone id in pvtzIDs
		id, ok := p.pvtzIDs[zoneID]
		if !ok {
			return ZoneInfo{}, fmt.Errorf("can not get private zone id for name '%s'", zoneID)
		}

		zoneID = id
	}

	return ZoneInfo{
		Type: zoneType,
		ID:   zoneID,
	}, nil
}

func (p *provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return p.doRequest(zone, record, actionEnsure)
}

func (p *provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return p.doRequest(zone, record, actionDelete)
}

func (p *provider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return p.doRequest(zone, record, actionReplace)
}

func (p *provider) doRequest(zone configv1.DNSZone, record *iov1.DNSRecord, action string) (err error) {
	zoneInfo, err := p.parseZone(zone)
	if err != nil {
		return err
	}

	service, ok := p.services[zoneInfo.Type]
	if !ok {
		return fmt.Errorf("unknown zone type %s", zoneInfo.Type)
	}

	switch action {
	case actionEnsure:
		err = service.Add(zoneInfo.ID, record.Spec.DNSName, string(record.Spec.RecordType), record.Spec.Targets[0], record.Spec.RecordTTL)
	case actionReplace:
		err = service.Update(zoneInfo.ID, record.Spec.DNSName, string(record.Spec.RecordType), record.Spec.Targets[0], record.Spec.RecordTTL)
	case actionDelete:
		err = service.Delete(zoneInfo.ID, record.Spec.DNSName, record.Spec.Targets[0])
	}

	return
}
