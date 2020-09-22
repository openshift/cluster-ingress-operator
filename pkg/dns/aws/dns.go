package aws

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/client/metadata"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go/service/route53"

	kerrors "k8s.io/apimachinery/pkg/util/errors"

	configv1 "github.com/openshift/api/config/v1"
)

const (
	// Route53Service is the name of the Route 53 service.
	Route53Service = route53.ServiceName
	// ELBService is the name of the Elastic Load Balancing service.
	ELBService = elb.ServiceName
	// TaggingService is the name of the Resource Group Tagging service.
	TaggingService = resourcegroupstaggingapi.ServiceName
	// govCloudRoute53Region is the AWS GovCloud region for Route 53. See:
	// https://docs.aws.amazon.com/govcloud-us/latest/UserGuide/using-govcloud-endpoints.html
	govCloudRoute53Region = "us-gov"
	// govCloudTaggingEndpoint is the Group Tagging service endpoint used for AWS GovCloud.
	govCloudTaggingEndpoint = "https://tagging.us-gov-west-1.amazonaws.com"
	// chinaRoute53Endpoint is the Route 53 service endpoint used for AWS China regions.
	chinaRoute53Endpoint = "https://route53.amazonaws.com.cn"
)

var (
	_   dns.Provider = &Provider{}
	log              = logf.Logger.WithName("dns")
)

// Provider is a dns.Provider for AWS Route53. It only supports DNSRecords of
// type CNAME, and the CNAME records are implemented as A records using the
// Route53 Alias feature.
//
// TODO: Records are considered owned by the manager if they exist in a managed
// zone and if their names match expectations. This is relatively dangerous
// compared to storing additional metadata (like tags or TXT records).
type Provider struct {
	elb     *elb.ELB
	elbv2   *elbv2.ELBV2
	route53 *route53.Route53
	tags    *resourcegroupstaggingapi.ResourceGroupsTaggingAPI

	config Config

	// lock protects access to everything below.
	lock sync.RWMutex

	// idsToTags caches IDs and their associated tag set. There is an assumed 1:1
	// relationship between an ID and its set of tags, and tag sets are considered
	// equal if their maps are reflect.DeepEqual.
	idsToTags map[string]map[string]string

	// lbZones is a cache of load balancer DNS names to LB hosted zone IDs.
	lbZones map[string]string
}

// Config is the necessary input to configure the manager.
type Config struct {
	// AccessID is an AWS credential.
	AccessID string
	// AccessKey is an AWS credential.
	AccessKey string
	// Region is the AWS region ELBs are created in.
	Region string
	// ServiceEndpoints is the list of AWS API endpoints to use for
	// Provider clients.
	ServiceEndpoints []ServiceEndpoint
}

// ServiceEndpoint stores the configuration of a custom url to
// override existing defaults of AWS Service API endpoints.
type ServiceEndpoint struct {
	// name is the name of the AWS service. The full list of service
	// names can be found at:
	//   https://docs.aws.amazon.com/general/latest/gr/aws-service-information.html
	Name string
	// url is a fully qualified URI that overrides the default generated
	// AWS API endpoint.
	URL string
}

func NewProvider(config Config, operatorReleaseVersion string) (*Provider, error) {
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
		Fn:   request.MakeAddToUserAgentHandler("openshift.io ingress-operator", operatorReleaseVersion),
	})

	var region string
	switch {
	case len(config.Region) > 0:
		region = config.Region
		log.Info("using region from operator config", "region name", region)
	case sess.Config.Region != nil:
		region = aws.StringValue(sess.Config.Region)
		log.Info("using region from shared config", "region name", region)
	default:
		return nil, fmt.Errorf("region is required")
	}

	r53Config := aws.NewConfig()
	// elb requires no special region treatment.
	elbConfig := aws.NewConfig().WithRegion(region)
	tagConfig := aws.NewConfig()

	// If the region is in aws china, cn-north-1 or cn-northwest-1, we should:
	// 1. hard code route53 api endpoint to https://route53.amazonaws.com.cn and region to "cn-northwest-1"
	//    as route53 is not GA in AWS China and aws sdk didn't have the endpoint.
	// 2. use the aws china region cn-northwest-1 to setup tagging api correctly instead of "us-east-1"
	switch region {
	case endpoints.CnNorth1RegionID, endpoints.CnNorthwest1RegionID:
		tagConfig = tagConfig.WithRegion(endpoints.CnNorthwest1RegionID)
		r53Config = r53Config.WithRegion(endpoints.CnNorthwest1RegionID).WithEndpoint(chinaRoute53Endpoint)
	case endpoints.UsGovEast1RegionID, endpoints.UsGovWest1RegionID:
		// Route53 for GovCloud uses the "us-gov-west-1" region id:
		// https://docs.aws.amazon.com/govcloud-us/latest/UserGuide/using-govcloud-endpoints.html
		r53Config = r53Config.WithRegion(endpoints.UsGovWest1RegionID)
		// As with other AWS partitions, the GovCloud Tagging client must be
		// in the same region as the Route53 client to find the hosted zone
		// of managed records.
		tagConfig = tagConfig.WithRegion(endpoints.UsGovWest1RegionID)
	default:
		// Since Route 53 is not a regionalized service, the Tagging API will
		// only return hosted zone resources when the region is "us-east-1".
		tagConfig = tagConfig.WithRegion(endpoints.UsEast1RegionID)
		// Use us-east-1 for Route 53 in AWS Regions other than China or GovCloud Regions.
		// See https://docs.aws.amazon.com/general/latest/gr/r53.html for details.
		r53Config = r53Config.WithRegion(endpoints.UsEast1RegionID)
	}
	if len(config.ServiceEndpoints) > 0 {
		route53Found := false
		elbFound := false
		tagFound := false
		for _, ep := range config.ServiceEndpoints {
			switch {
			case route53Found && elbFound && tagFound:
				break
			case ep.Name == Route53Service:
				route53Found = true
				r53Config = r53Config.WithEndpoint(ep.URL)
				log.Info("using route53 custom endpoint", "url", ep.URL)
			case ep.Name == TaggingService:
				tagFound = true
				url := ep.URL
				// route53 for govcloud is based out of us-gov-west-1,
				// so the tagging client must match.
				if strings.Contains(ep.URL, "us-gov-east-1") {
					url = govCloudTaggingEndpoint
				}
				tagConfig = tagConfig.WithEndpoint(url)
				log.Info("using group tagging custom endpoint", "url", url)
			case ep.Name == ELBService:
				elbFound = true
				elbConfig = elbConfig.WithEndpoint(ep.URL)
				log.Info("using elb custom endpoint", "url", ep.URL)
			}
		}
	}
	p := &Provider{
		elb: elb.New(sess, elbConfig),
		// TODO: Add custom endpoint support for elbv2. See the following for details:
		// https://docs.aws.amazon.com/general/latest/gr/elb.html
		elbv2:     elbv2.New(sess, aws.NewConfig().WithRegion(region)),
		route53:   route53.New(sess, r53Config),
		tags:      resourcegroupstaggingapi.New(sess, tagConfig),
		config:    config,
		idsToTags: map[string]map[string]string{},
		lbZones:   map[string]string{},
	}
	if err := validateServiceEndpoints(p); err != nil {
		return nil, fmt.Errorf("failed to validate aws provider service endpoints: %v", err)
	}
	return p, nil
}

// validateServiceEndpoints validates that provider clients can communicate with
// associated API endpoints by having each client make a list/describe/get call.
func validateServiceEndpoints(provider *Provider) error {
	var errs []error
	zoneInput := route53.ListHostedZonesInput{MaxItems: aws.String("1")}
	if _, err := provider.route53.ListHostedZones(&zoneInput); err != nil {
		errs = append(errs, fmt.Errorf("failed to list route53 hosted zones: %v", err))
	}
	elbInput := elb.DescribeLoadBalancersInput{PageSize: aws.Int64(int64(1))}
	if _, err := provider.elb.DescribeLoadBalancers(&elbInput); err != nil {
		errs = append(errs, fmt.Errorf("failed to describe elb load balancers: %v", err))
	}
	elbv2Input := elbv2.DescribeLoadBalancersInput{PageSize: aws.Int64(int64(1))}
	if _, err := provider.elbv2.DescribeLoadBalancers(&elbv2Input); err != nil {
		errs = append(errs, fmt.Errorf("failed to describe elbv2 load balancers: %v", err))
	}
	tagInput := resourcegroupstaggingapi.GetResourcesInput{TagsPerPage: aws.Int64(int64(100))}
	if _, err := provider.tags.GetResources(&tagInput); err != nil {
		errs = append(errs, fmt.Errorf("failed to get group tagging resources: %v", err))
	}
	return kerrors.NewAggregate(errs)
}

// getZoneID finds the ID of given zoneConfig in Route53. If an ID is already
// known, return that; otherwise, use tags to search for the zone. Returns an
// error if the zone can't be found.
func (m *Provider) getZoneID(zoneConfig configv1.DNSZone) (string, error) {
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

// getLBHostedZone finds the hosted zone ID of an ELB whose DNS name matches the
// name parameter. Results are cached.
func (m *Provider) getLBHostedZone(name string) (string, error) {
	m.lock.Lock()
	defer m.lock.Unlock()

	if id, exists := m.lbZones[name]; exists {
		return id, nil
	}

	var id string
	elbFn := func(resp *elb.DescribeLoadBalancersOutput, lastPage bool) (shouldContinue bool) {
		for _, lb := range resp.LoadBalancerDescriptions {
			dnsName := aws.StringValue(lb.DNSName)
			zoneID := aws.StringValue(lb.CanonicalHostedZoneNameID)
			log.V(2).Info("found classic load balancer", "name", aws.StringValue(lb.LoadBalancerName), "dns name", dnsName, "hosted zone ID", zoneID)
			if dnsName == name {
				id = zoneID
				return false
			}
		}
		return true
	}
	err := m.elb.DescribeLoadBalancersPages(&elb.DescribeLoadBalancersInput{}, elbFn)
	if err != nil {
		return "", fmt.Errorf("failed to describe classic load balancers: %v", err)
	}
	if len(id) == 0 {
		elbv2Fn := func(resp *elbv2.DescribeLoadBalancersOutput, lastPage bool) (shouldContinue bool) {
			for _, lb := range resp.LoadBalancers {
				dnsName := aws.StringValue(lb.DNSName)
				zoneID := aws.StringValue(lb.CanonicalHostedZoneId)
				log.V(2).Info("found network load balancer", "name", aws.StringValue(lb.LoadBalancerName), "dns name", dnsName, "hosted zone ID", zoneID)
				if dnsName == name {
					id = zoneID
					return false
				}
			}
			return true
		}
		err := m.elbv2.DescribeLoadBalancersPages(&elbv2.DescribeLoadBalancersInput{}, elbv2Fn)
		if err != nil {
			return "", fmt.Errorf("failed to describe network load balancers: %v", err)
		}
	}
	if len(id) == 0 {
		return "", fmt.Errorf("couldn't find hosted zone ID of ELB %s", name)
	}
	log.V(2).Info("associating load balancer with hosted zone", "dns name", name, "zone", id)
	m.lbZones[name] = id
	return id, nil
}

type action string

const (
	upsertAction action = "UPSERT"
	deleteAction action = "DELETE"
)

func (m *Provider) Ensure(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return m.change(record, zone, upsertAction)
}

func (m *Provider) Delete(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return m.change(record, zone, deleteAction)
}

// change will perform an action on a record. The target must correspond to the
// hostname of an ELB which will be automatically discovered.
func (m *Provider) change(record *iov1.DNSRecord, zone configv1.DNSZone, action action) error {
	if record.Spec.RecordType != iov1.CNAMERecordType {
		return fmt.Errorf("unsupported record type %s", record.Spec.RecordType)
	}
	// TODO: handle >0 targets
	domain, target := record.Spec.DNSName, record.Spec.Targets[0]
	if len(domain) == 0 {
		return fmt.Errorf("domain is required")
	}
	if len(target) == 0 {
		return fmt.Errorf("target is required")
	}

	zoneID, err := m.getZoneID(zone)
	if err != nil {
		return fmt.Errorf("failed to find hosted zone for record: %v", err)
	}

	// Find the target hosted zone of the load balancer attached to the service.
	targetHostedZoneID, err := m.getLBHostedZone(target)
	if err != nil {
		return fmt.Errorf("failed to get hosted zone for load balancer target %q: %v", target, err)
	}

	// Configure records.
	err = m.updateRecord(domain, zoneID, target, targetHostedZoneID, string(action), record.Spec.RecordTTL)
	if err != nil {
		return fmt.Errorf("failed to update alias in zone %s: %v", zoneID, err)
	}
	switch action {
	case upsertAction:
		log.Info("upserted DNS record", "record", record.Spec, "zone", zone)
	case deleteAction:
		log.Info("deleted DNS record", "record", record.Spec, "zone", zone)
	}
	return nil
}

// updateRecord creates or updates a DNS record for domain in zoneID pointed at
// target in targetHostedZoneID. An Alias record type is used for all regions
// other than GovCloud (CNAME). See the following for additional details:
// https://docs.aws.amazon.com/govcloud-us/latest/UserGuide/govcloud-r53.html
func (m *Provider) updateRecord(domain, zoneID, target, targetHostedZoneID, action string, ttl int64) error {
	input := route53.ChangeResourceRecordSetsInput{HostedZoneId: aws.String(zoneID)}
	if clientEndpointIsGovCloud(&m.route53.Client.ClientInfo) {
		record := route53.ResourceRecord{Value: aws.String(target)}
		input.ChangeBatch = &route53.ChangeBatch{
			Changes: []*route53.Change{
				{
					Action: aws.String(action),
					ResourceRecordSet: &route53.ResourceRecordSet{
						Name:            aws.String(domain),
						Type:            aws.String(route53.RRTypeCname),
						TTL:             aws.Int64(ttl),
						ResourceRecords: []*route53.ResourceRecord{&record},
					},
				},
			},
		}
	} else {
		input.ChangeBatch = &route53.ChangeBatch{
			Changes: []*route53.Change{
				{
					Action: aws.String(action),
					ResourceRecordSet: &route53.ResourceRecordSet{
						Name: aws.String(domain),
						Type: aws.String(route53.RRTypeA),
						AliasTarget: &route53.AliasTarget{
							HostedZoneId:         aws.String(targetHostedZoneID),
							DNSName:              aws.String(target),
							EvaluateTargetHealth: aws.Bool(false),
						},
					},
				},
			},
		}
	}
	resp, err := m.route53.ChangeResourceRecordSets(&input)
	if err != nil {
		if action == string(deleteAction) {
			if aerr, ok := err.(awserr.Error); ok {
				if strings.Contains(aerr.Message(), "not found") {
					log.Info("record not found", "zone id", zoneID, "domain", domain, "target", target)
					return nil
				}
			}
		}
		return fmt.Errorf("couldn't update DNS record in zone %s: %v", zoneID, err)
	}
	log.Info("updated DNS record", "zone id", zoneID, "domain", domain, "target", target, "response", resp)
	return nil
}

// clientEndpointIsGovCloud returns true if the provided client info
// references a US GovCloud API endpoint.
func clientEndpointIsGovCloud(clientInfo *metadata.ClientInfo) bool {
	return strings.Contains(clientInfo.Endpoint, govCloudRoute53Region)
}
