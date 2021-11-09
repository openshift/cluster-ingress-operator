package alibaba

import (
	"fmt"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/endpoints"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/pvtz"
	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"strings"
)

const (
	zoneTypeDNS         = "public"
	zoneTypePrivateZone = "private"

	actionEnsure  = "ensure"
	actionReplace = "replace"
	actionDelete  = "delete"
)

var (
	log = logf.Logger.WithName("dns")
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
	// DomainName is domain of the zone
	Domain string
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

		// resolve endpoint
		endpoint, err := endpoints.Resolve(&endpoints.ResolveParam{
			Product:  strings.ToLower(request.GetProduct()),
			RegionId: strings.ToLower(config.Region),
		})

		if err != nil {
			endpoint = defaultEndpoints[strings.ToLower(request.GetProduct())]
		}
		request.SetDomain(endpoint)

		err = client.DoAction(request, response)
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
			zoneTypeDNS:         NewDNSService(client, config.Region),
			zoneTypePrivateZone: NewPvtzService(client, config.Region),
		},
		pvtzIDs: pvtzIDs,
	}, nil
}

// parseZone parses the zone id to ZoneInfo
func (p *provider) parseZone(zone configv1.DNSZone) (ZoneInfo, error) {
	zoneType, ok := zone.Tags["type"]
	if !ok {
		return ZoneInfo{}, fmt.Errorf("can not find tag 'type' in DNSZone")
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
		Type:   zoneType,
		ID:     zoneID,
		Domain: zone.ID,
	}, nil
}

// getRR should get record name from a full qualified domain name
// if dnsName is not a subdomain of domainName,
// it will return the dnsName instead (without the trailing dot)
func getRR(dnsName, domainName string) string {
	dnsName = strings.TrimSuffix(dnsName, ".")
	return strings.TrimSuffix(dnsName, "."+domainName)
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

	rr := getRR(record.Spec.DNSName, zoneInfo.Domain)

	switch action {
	case actionEnsure:
		err = service.Add(zoneInfo.ID, rr, string(record.Spec.RecordType), record.Spec.Targets[0], record.Spec.RecordTTL)
	case actionReplace:
		err = service.Update(zoneInfo.ID, rr, string(record.Spec.RecordType), record.Spec.Targets[0], record.Spec.RecordTTL)
	case actionDelete:
		err = service.Delete(zoneInfo.ID, rr, record.Spec.Targets[0])
	}

	return
}
