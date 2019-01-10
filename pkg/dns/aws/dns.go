package aws

import (
	"fmt"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
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
	tags    *resourcegroupstaggingapi.ResourceGroupsTaggingAPI

	config Config

	// lock protects access to everything below.
	lock sync.RWMutex
	// publicZoneID is the public zone shared by all clusters.
	publicZoneID string
	// privateZoneID is the private zone owned by the cluster.
	privateZoneID string
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

	// BaseDomain should be the *absolute* name shared by the zones.
	// Example: 'devcluster.openshift.com.'
	BaseDomain string
	// ClusterID is the value of the 'openshiftClusterID' tag identifying the
	// cluster.
	ClusterID string
}

func NewManager(config Config) (*Manager, error) {
	creds := credentials.NewStaticCredentials(config.AccessID, config.AccessKey, "")
	sess, err := session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Credentials: creds,
		},
		SharedConfigState: session.SharedConfigEnable,
	})
	if err != nil {
		return nil, fmt.Errorf("couldn't create AWS client session: %v", err)
	}

	region := aws.StringValue(sess.Config.Region)
	if len(region) > 0 {
		logrus.Infof("using region %q from shared config", region)
	} else {
		metadata := ec2metadata.New(sess)
		discovered, err := metadata.Region()
		if err != nil {
			return nil, fmt.Errorf("couldn't get region from metadata: %v", err)
		}
		region = discovered
		logrus.Infof("discovered region %q from metadata", region)
	}

	return &Manager{
		elb:     elb.New(sess, aws.NewConfig().WithRegion(region)),
		route53: route53.New(sess),
		// TODO: This API will only return hostedzone resources (which are global)
		// when the region is forced to us-east-1. We don't yet understand why.
		tags:           resourcegroupstaggingapi.New(sess, aws.NewConfig().WithRegion("us-east-1")),
		config:         config,
		updatedRecords: map[string]struct{}{},
	}, nil
}

// discoverZones finds the public and private zones to manage, setting the
// privateZoneID and publicZoneID fields. An error is returned if either zone
// can't be found.
func (m *Manager) discoverZones() error {
	m.lock.Lock()
	defer m.lock.Unlock()

	// Find the public zone, which is the non-private zone whose domain matches
	// the cluster base domain.
	if len(m.publicZoneID) == 0 {
		f := func(resp *route53.ListHostedZonesOutput, lastPage bool) (shouldContinue bool) {
			for _, zone := range resp.HostedZones {
				if aws.StringValue(zone.Name) == m.config.BaseDomain && !aws.BoolValue(zone.Config.PrivateZone) {
					m.publicZoneID = aws.StringValue(zone.Id)
					return false
				}
			}
			return true
		}
		err := m.route53.ListHostedZonesPages(&route53.ListHostedZonesInput{}, f)
		if err != nil {
			return fmt.Errorf("failed to list hosted zones: %v", err)
		}
		if len(m.publicZoneID) == 0 {
			return fmt.Errorf("couldn't find public hosted zone for %q", m.config.BaseDomain)
		}
		logrus.Infof("using public zone %s", m.publicZoneID)
	}

	// Find the private zone, which is the private zone whose openshiftClusterID tag
	// value matches the cluster ID.
	if len(m.privateZoneID) == 0 {
		logrus.Infof("using filter Name=tag:%s=Values=%s", "openshiftClusterID", m.config.ClusterID)
		zones, err := m.tags.GetResources(&resourcegroupstaggingapi.GetResourcesInput{
			ResourceTypeFilters: []*string{aws.String("route53:hostedzone")},
			TagFilters: []*resourcegroupstaggingapi.TagFilter{
				{
					Key:    aws.String("openshiftClusterID"),
					Values: []*string{aws.String(m.config.ClusterID)},
				},
			},
		})
		if err != nil {
			return fmt.Errorf("failed to get tagged resources: %v", err)
		}
		logrus.Infof("got %d zones", len(zones.ResourceTagMappingList))
		for i, zone := range zones.ResourceTagMappingList {
			logrus.Infof("zone %i: %#v", i, zone)
			zoneARN, err := arn.Parse(aws.StringValue(zone.ResourceARN))
			if err != nil {
				return fmt.Errorf("failed to parse hostedzone ARN %q: %v", aws.StringValue(zone.ResourceARN), err)
			}
			elems := strings.Split(zoneARN.Resource, "/")
			if len(elems) != 2 || elems[0] != "hostedzone" {
				return fmt.Errorf("got unexpected resource ARN: %v", zoneARN)
			}
			m.privateZoneID = elems[1]
			break
		}
		if len(m.privateZoneID) == 0 {
			return fmt.Errorf("couldn't find private hosted zone for cluster id %q", m.config.ClusterID)
		}
		logrus.Infof("using private zone %s", m.privateZoneID)
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
	err := m.discoverZones()
	if err != nil {
		return fmt.Errorf("failed to discover hosted zones: %v", err)
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
	for _, zoneID := range []string{m.publicZoneID, m.privateZoneID} {
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
