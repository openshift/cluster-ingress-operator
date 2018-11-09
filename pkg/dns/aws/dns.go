package aws

import (
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/route53"
)

var _ dns.Manager = &Manager{}

// Manager provides AWS DNS record management. The implementation is tightly
// coupled to the DNS zone topology created by the OpenShift installer.
// Specifically, this implies:
//
// 1. A public zone shared by all clusters with a domain name equal to the cluster base domain
// 2. A private zone for the cluster with a domain name equal to the cluster base domain
//
// Significantly, records in the public zone are considered owned by a cluster
// if a record with the same name exists in the cluster's private zone. Any
// records Manager creates in the public zone will also be created in the
// private zone to ensure public zone records for the cluster can be identified
// and cleaned up later.
type Manager struct {
	elb     *elb.ELB
	route53 *route53.Route53

	config Config
	lock   sync.RWMutex
	// zones is a cache of all known DNS zones and their associated tags.
	zones []*zoneWithTags
	// updatedRecords is a cache of records which have been created during the
	// life of this manager. The key is zoneID+domain+target. This is a quick hack
	// to minimize AWS API calls for now.
	updatedRecords map[string]struct{}
}

// Config is the necessary input to configure the manager.
type Config struct {
	// AccessID is an AWS credential.
	AccessID string
	// AccessKey is an AWS credential.
	AccessKey string
	// Region is an AWS config.
	Region string

	// BaseDomain should be the *absolute* name shared by the zones.
	// Example: 'devcluster.openshift.com.'
	BaseDomain string
	// ClusterID is the value of the 'tectonicClusterID' tag identifying the
	// cluster.
	ClusterID string
}

func NewManager(config Config) (*Manager, error) {
	creds := credentials.NewStaticCredentials(config.AccessID, config.AccessKey, "")
	sess, err := session.NewSession(&aws.Config{
		Credentials: creds,
		Region:      aws.String(config.Region),
	})
	if err != nil {
		return nil, fmt.Errorf("couldn't create AWS client session: %v", err)
	}
	return &Manager{
		elb:     elb.New(sess),
		route53: route53.New(sess),

		config:         config,
		zones:          []*zoneWithTags{},
		updatedRecords: map[string]struct{}{},
	}, nil
}

type zoneWithTags struct {
	zone *route53.HostedZone
	tags []*route53.Tag
}

// updateZoneCache caches all zones and their tags.
func (m *Manager) updateZoneCache() error {
	m.lock.Lock()
	defer m.lock.Unlock()

	// There's no reason to update this more than once as we're only interested in
	// the zones scoped to this cluster.
	if len(m.zones) > 0 {
		return nil
	}

	// Find and cache all zones and their tags.
	// TODO: probably only need to store the two zones we care about.
	zones := []*zoneWithTags{}
	listedAllTags := true
	f := func(resp *route53.ListHostedZonesOutput, lastPage bool) (shouldContinue bool) {
		for _, zone := range resp.HostedZones {
			tagsList, err := m.route53.ListTagsForResource(&route53.ListTagsForResourceInput{
				ResourceType: aws.String("hostedzone"),
				ResourceId:   zone.Id,
			})
			if err != nil {
				logrus.Errorf("failed to list tags for hosted zone %s: %v", zone.Id, err)
				listedAllTags = false
				return false
			}
			zones = append(zones, &zoneWithTags{zone: zone, tags: tagsList.ResourceTagSet.Tags})
		}
		return true
	}
	err := m.route53.ListHostedZonesPages(&route53.ListHostedZonesInput{}, f)
	if err != nil {
		return fmt.Errorf("failed to list hosted zones: %v", err)
	}
	if !listedAllTags {
		return fmt.Errorf("failed to list all hosted zone tags")
	}

	m.zones = zones
	return nil
}

// findClusterPublicZone finds the public zone with the base domain from config.
func (m *Manager) findClusterPublicZone() *route53.HostedZone {
	m.lock.Lock()
	defer m.lock.Unlock()
	for _, zwt := range m.zones {
		if aws.StringValue(zwt.zone.Name) == m.config.BaseDomain && !aws.BoolValue(zwt.zone.Config.PrivateZone) {
			return zwt.zone
		}
	}
	return nil
}

// findClusterPrivateZone finds the private zone with the base domain and
// cluster ID from config.
func (m *Manager) findClusterPrivateZone() *route53.HostedZone {
	m.lock.Lock()
	defer m.lock.Unlock()
	for _, zwt := range m.zones {
		if aws.StringValue(zwt.zone.Name) == m.config.BaseDomain && aws.BoolValue(zwt.zone.Config.PrivateZone) {
			for _, tag := range zwt.tags {
				if aws.StringValue(tag.Key) == "tectonicClusterID" && aws.StringValue(tag.Value) == m.config.ClusterID {
					return zwt.zone
				}
			}
		}
	}
	return nil
}

// EnsureAlias will create an A record for domain/target in both the public and
// private zones. The target must correspond to the hostname of an ELB which
// will be automatically discovered.
func (m *Manager) EnsureAlias(domain, target string) error {
	if len(domain) == 0 {
		return fmt.Errorf("domain is required")
	}
	if len(target) == 0 {
		return fmt.Errorf("target is required")
	}

	// Ensure the zone cache is up to date.
	err := m.updateZoneCache()
	if err != nil {
		return fmt.Errorf("failed to update hosted zone cache: %v", err)
	}

	publicZone := m.findClusterPublicZone()
	privateZone := m.findClusterPrivateZone()
	if publicZone == nil {
		return fmt.Errorf("failed to find public zone for cluster")
	}
	if privateZone == nil {
		return fmt.Errorf("failed to find private zone for cluster")
	}

	// Find the target hosted zone of the load balancer attached to the service.
	// TODO: cache it?
	var targetHostedZoneID string
	loadBalancers, err := m.elb.DescribeLoadBalancers(&elb.DescribeLoadBalancersInput{})
	if err != nil {
		return fmt.Errorf("failed to describe load balancers: %v", err)
	}
	for _, lb := range loadBalancers.LoadBalancerDescriptions {
		logrus.Debugf("found load balancer: %s (DNS name: %s, zone: %s)", aws.StringValue(lb.LoadBalancerName), aws.StringValue(lb.DNSName), aws.StringValue(lb.CanonicalHostedZoneNameID))
		if aws.StringValue(lb.CanonicalHostedZoneName) == target {
			targetHostedZoneID = aws.StringValue(lb.CanonicalHostedZoneNameID)
			break
		}
	}
	if len(targetHostedZoneID) == 0 {
		return fmt.Errorf("couldn't find hosted zone ID of target ELB with domain name %s", target)
	}

	// Configure alias records for both the public and private zones. Cache
	// successful updates.
	// TODO: handle the caching/diff detection in a better way.
	m.lock.Lock()
	defer m.lock.Unlock()
	for _, zoneID := range []string{aws.StringValue(publicZone.Id), aws.StringValue(privateZone.Id)} {
		key := zoneID + domain + target
		// Only process these once, for now.
		if _, exists := m.updatedRecords[key]; exists {
			continue
		}
		err := m.updateAlias(domain, zoneID, target, targetHostedZoneID)
		if err != nil {
			return fmt.Errorf("failed to update alias in zone %s: %v", zoneID, err)
		}
		m.updatedRecords[key] = struct{}{}
	}
	return nil
}

// updateAlias creates or updates an alias for domain in zoneID pointed at
// target in targetHostedZoneID.
func (m *Manager) updateAlias(domain, zoneID, target, targetHostedZoneID string) error {
	resp, err := m.route53.ChangeResourceRecordSets(&route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &route53.ChangeBatch{
			Changes: []*route53.Change{
				{
					Action: aws.String("UPSERT"),
					ResourceRecordSet: &route53.ResourceRecordSet{
						Name: aws.String(domain),
						Type: aws.String("A"),
						AliasTarget: &route53.AliasTarget{
							HostedZoneId:         aws.String(targetHostedZoneID),
							DNSName:              aws.String(target),
							EvaluateTargetHealth: aws.Bool(false),
						},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("couldn't update DNS record in zone %s: %v", zoneID, err)
	}
	logrus.Infof("updated DNS record in zone %s, %s -> %s: %v", zoneID, domain, target, resp)
	return nil
}
