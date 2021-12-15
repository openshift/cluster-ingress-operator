package alibaba

import (
	"fmt"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"strings"
)

// zoneType is a type of DNS zone: public or private.
type zoneType string

// action is an action that can be performed on a DNS record by this DNS provider.
type action string

const (
	zoneTypePublicZone  zoneType = "public"
	zoneTypePrivateZone zoneType = "private"

	actionEnsure  action = "ensure"
	actionReplace action = "replace"
	actionDelete  action = "delete"
)

var (
	log = logf.Logger.WithName("dns")
)

type Config struct {
	Region       string
	AccessKeyID  string
	AccessSecret string
}

type ZoneInfo struct {
	// Type is type of the zone.
	Type zoneType
	// ID is the value used in OpenAPI to leverage dns records via Service.
	// In public zone, it should be domain name. In private zone, it should be zone name.
	ID string
	// Domain is domain name of the zone
	Domain string
}

type provider struct {
	config   Config
	services map[zoneType]Service
}

func NewProvider(config Config) (dns.Provider, error) {
	sdkClient, err := sdk.NewClientWithAccessKey(config.Region, config.AccessKeyID, config.AccessSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create alibabacloud api service: %w", err)
	}

	client := NewClient(sdkClient, config.Region)
	return &provider{
		config: config,
		services: map[zoneType]Service{
			zoneTypePublicZone:  NewPublicZoneService(client),
			zoneTypePrivateZone: NewPrivateZoneService(client),
		},
	}, nil
}

// parseZone parses the zone id to ZoneInfo.
func (p *provider) parseZone(zone configv1.DNSZone) (ZoneInfo, error) {
	typeString, ok := zone.Tags["type"]
	if !ok {
		return ZoneInfo{}, fmt.Errorf("cannot find tag \"type\" in DNSZone")
	}

	return ZoneInfo{
		Type: zoneType(typeString),
		ID:   zone.ID,
		// For now, Domain is equal to zone ID,
		Domain: zone.ID,
	}, nil
}

// getRR should get record name from a full qualified domain name.
// If dnsName is not a subdomain of domainName,
// it will return the dnsName instead (without the trailing dot).
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

func (p *provider) doRequest(zone configv1.DNSZone, record *iov1.DNSRecord, action action) error {
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
	default:
		err = fmt.Errorf("unknown action %q", action)
	}

	return err
}
