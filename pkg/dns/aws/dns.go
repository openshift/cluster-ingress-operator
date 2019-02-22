package aws

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	"github.com/openshift/cluster-ingress-operator/version"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go/service/route53"

	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	configv1 "github.com/openshift/api/config/v1"
)

var (
	_   dns.Manager = &Manager{}
	log             = logf.Logger.WithName("aws-dns-manager")
)

// Manager provides AWS DNS record management. In this implementation, calling
// Ensure will create records in any zone specified in the DNS configuration.
//
// TODO: Records are considered owned by the manager if they exist in a managed
// zone and if their names match expectations. This is relatively dangerous
// compared to storing additional metadata (like tags or TXT records).
type Manager struct {
	elb     *elb.ELB
	route53 *route53.Route53
	tags    *resourcegroupstaggingapi.ResourceGroupsTaggingAPI

	config Config

	// lock protects access to everything below.
	lock sync.RWMutex

	// idsToTags caches IDs and their associated tag set. There is an assumed 1:1
	// relationship between an ID and its set of tags, and tag sets are considered
	// equal if their maps are reflect.DeepEqual.
	idsToTags map[string]map[string]string

	// updatedRecords is a cache of records which have been created or updated
	// during the life of this manager. The key is zoneID+domain+target. This is a
	// quick hack to minimize AWS API calls, and also prevent changes to existing
	// records (something not yet supported).
	updatedRecords sets.String
}

// Config is the necessary input to configure the manager.
type Config struct {
	// AccessID is an AWS credential.
	AccessID string
	// AccessKey is an AWS credential.
	AccessKey string
	// DNS is public and private DNS zone configuration for the cluster.
	DNS *configv1.DNS
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
	sess.Handlers.Build.PushBackNamed(request.NamedHandler{
		Name: "openshift.io/ingress-operator",
		Fn:   request.MakeAddToUserAgentHandler("openshift.io ingress-operator", version.OperatorVersion),
	})

	region := aws.StringValue(sess.Config.Region)
	if len(region) > 0 {
		log.Info("using region from shared config", "region name", region)
	} else {
		metadata := ec2metadata.New(sess)
		discovered, err := metadata.Region()
		if err != nil {
			return nil, fmt.Errorf("couldn't get region from metadata: %v", err)
		}
		region = discovered
		log.Info("discovered region from metadata", "region name", region)
	}

	return &Manager{
		elb:     elb.New(sess, aws.NewConfig().WithRegion(region)),
		route53: route53.New(sess),
		// TODO: This API will only return hostedzone resources (which are global)
		// when the region is forced to us-east-1. We don't yet understand why.
		tags:           resourcegroupstaggingapi.New(sess, aws.NewConfig().WithRegion("us-east-1")),
		config:         config,
		idsToTags:      map[string]map[string]string{},
		updatedRecords: sets.NewString(),
	}, nil
}

// getZoneID finds the ID of given zoneConfig in Route53. If an ID is already
// known, return that; otherwise, use tags to search for the zone. Returns an
// error if the zone can't be found.
func (m *Manager) getZoneID(zoneConfig configv1.DNSZone) (string, error) {
	m.lock.Lock()
	defer m.lock.Unlock()

	// If the config specifies the ID already, use it
	if len(zoneConfig.ID) > 0 {
		return zoneConfig.ID, nil
	}

	// If the ID for these tags is already cached, use it
	for id, tags := range m.idsToTags {
		if reflect.DeepEqual(tags, zoneConfig.Tags) {
			return id, nil
		}
	}

	// Look up and cache the ID for these tags.
	var id string

	// Even though we use filters when getting resources, the resources are still
	// paginated as though no filter were applied.  If the desired resource is not
	// on the first page, then GetResources will not return it.  We need to use
	// GetResourcesPages and possibly go through one or more empty pages of
	// resources till we find a resource that gets through the filters.
	var innerError error
	f := func(resp *resourcegroupstaggingapi.GetResourcesOutput, lastPage bool) (shouldContinue bool) {
		for _, zone := range resp.ResourceTagMappingList {
			zoneARN, err := arn.Parse(aws.StringValue(zone.ResourceARN))
			if err != nil {
				innerError = fmt.Errorf("failed to parse hostedzone ARN %q: %v", aws.StringValue(zone.ResourceARN), err)
				return false
			}
			elems := strings.Split(zoneARN.Resource, "/")
			if len(elems) != 2 || elems[0] != "hostedzone" {
				innerError = fmt.Errorf("got unexpected resource ARN: %v", zoneARN)
				return false
			}
			id = elems[1]
			return false
		}
		return true
	}
	tagFilters := []*resourcegroupstaggingapi.TagFilter{}
	for k, v := range zoneConfig.Tags {
		tagFilters = append(tagFilters, &resourcegroupstaggingapi.TagFilter{
			Key:    aws.String(k),
			Values: []*string{aws.String(v)},
		})
	}
	outerError := m.tags.GetResourcesPages(&resourcegroupstaggingapi.GetResourcesInput{
		ResourceTypeFilters: []*string{aws.String("route53:hostedzone")},
		TagFilters:          tagFilters,
	}, f)
	if err := kerrors.NewAggregate([]error{innerError, outerError}); err != nil {
		return id, fmt.Errorf("failed to get tagged resources: %v", err)
	}
	if len(id) == 0 {
		return id, fmt.Errorf("no matching hosted zone found")
	}

	// Update the cache
	m.idsToTags[id] = zoneConfig.Tags
	log.Info("found hosted zone using tags", "zone id", id, "tags", zoneConfig.Tags)

	return id, nil
}

type action string

const (
	upsertAction action = "UPSERT"
	deleteAction action = "DELETE"
)

func (m *Manager) Ensure(record *dns.Record) error {
	return m.change(record, upsertAction)
}

func (m *Manager) Delete(record *dns.Record) error {
	return m.change(record, deleteAction)
}

// change will perform an action on a record. The target must correspond to the
// hostname of an ELB which will be automatically discovered.
func (m *Manager) change(record *dns.Record, action action) error {
	if record.Type != dns.ALIASRecord {
		return fmt.Errorf("unsupported record type %s", record.Type)
	}
	alias := record.Alias
	if alias == nil {
		return fmt.Errorf("missing alias record")
	}
	domain, target := alias.Domain, alias.Target
	if len(domain) == 0 {
		return fmt.Errorf("domain is required")
	}
	if len(target) == 0 {
		return fmt.Errorf("target is required")
	}

	zoneID, err := m.getZoneID(record.Zone)
	if err != nil {
		return fmt.Errorf("failed to find hosted zone for record %v: %v", record, err)
	}

	// Find the target hosted zone of the load balancer attached to the service.
	// TODO: cache it?
	var targetHostedZoneID string
	loadBalancers, err := m.elb.DescribeLoadBalancers(&elb.DescribeLoadBalancersInput{})
	if err != nil {
		return fmt.Errorf("failed to describe load balancers: %v", err)
	}
	for _, lb := range loadBalancers.LoadBalancerDescriptions {
		log.Info("found load balancer", "name", aws.StringValue(lb.LoadBalancerName), "dns name", aws.StringValue(lb.DNSName), "hosted zone ID", aws.StringValue(lb.CanonicalHostedZoneNameID))
		if aws.StringValue(lb.CanonicalHostedZoneName) == target {
			targetHostedZoneID = aws.StringValue(lb.CanonicalHostedZoneNameID)
			break
		}
	}
	if len(targetHostedZoneID) == 0 {
		return fmt.Errorf("couldn't find hosted zone ID of target ELB with domain name %s", target)
	}

	// Configure records and cache updates.
	// TODO: handle the caching/diff detection in a better way.
	m.lock.Lock()
	defer m.lock.Unlock()
	key := zoneID + domain + target
	// Only process updates once for now because we're not diffing.
	if m.updatedRecords.Has(key) && action == upsertAction {
		log.Info("skipping DNS record update", "record", record)
		return nil
	}
	err = m.updateAlias(domain, zoneID, target, targetHostedZoneID, string(action))
	if err != nil {
		return fmt.Errorf("failed to update alias in zone %s: %v", zoneID, err)
	}
	switch action {
	case upsertAction:
		m.updatedRecords.Insert(key)
		log.Info("upserted DNS record", "record", record)
	case deleteAction:
		m.updatedRecords.Delete(key)
		log.Info("deleted DNS record", "record", record)
	}
	return nil
}

// updateAlias creates or updates an alias for domain in zoneID pointed at
// target in targetHostedZoneID.
func (m *Manager) updateAlias(domain, zoneID, target, targetHostedZoneID, action string) error {
	resp, err := m.route53.ChangeResourceRecordSets(&route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &route53.ChangeBatch{
			Changes: []*route53.Change{
				{
					Action: aws.String(action),
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
	log.Info("updated DNS record", "zone id", zoneID, "domain", domain, "target", target, "response", resp)
	return nil
}
