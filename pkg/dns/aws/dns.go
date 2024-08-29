package aws

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"

	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/client/metadata"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go/service/route53"

	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"

	configv1 "github.com/openshift/api/config/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
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
	// targetHostedZoneIdAnnotationKey is the key of an annotation that this
	// provider adds to DNSRecord CRs to track the target hosted zone id of
	// the ELB that is associated with the record, which is needed when
	// deleting the record.
	targetHostedZoneIdAnnotationKey = "ingress.operator.openshift.io/target-hosted-zone-id"
)

var (
	_   dns.Provider = &Provider{}
	log              = logf.Logger.WithName("dns")

	hostedZoneIDRegex = regexp.MustCompile("^/?hostedzone/([^/]+)$")
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
	// SharedCredentialFile is the path to the aws shared credential file
	// that is used by SDK to configure the credentials.
	SharedCredentialFile string

	// RoleARN is an optional ARN to use for the AWS client session that is
	// intended to only provide access to another account's Route 53 service.
	RoleARN string

	// Region is the AWS region ELBs are created in.
	Region string
	// ServiceEndpoints is the list of AWS API endpoints to use for
	// Provider clients.
	ServiceEndpoints []ServiceEndpoint
	// CustomCABundle is a custom CA bundle to use when accessing the AWS API
	CustomCABundle string

	// Client is a Kubernetes client, which the provider uses to annotate
	// DNSRecord CRs.
	Client client.Client
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
	sessionOpts := session.Options{
		SharedConfigState: session.SharedConfigEnable,
		SharedConfigFiles: []string{config.SharedCredentialFile},
	}
	if config.CustomCABundle != "" {
		sessionOpts.CustomCABundle = strings.NewReader(config.CustomCABundle)
	}
	var region string
	if len(config.Region) > 0 {
		region = config.Region
		sessionOpts.Config.Region = aws.String(config.Region)
		log.Info("using region from operator config", "region name", region)
	}
	sess, err := session.NewSessionWithOptions(sessionOpts)
	if err != nil {
		return nil, fmt.Errorf("couldn't create AWS client session: %v", err)
	}
	sess.Handlers.Build.PushBackNamed(request.NamedHandler{
		Name: "openshift.io/ingress-operator",
		Fn:   request.MakeAddToUserAgentHandler("openshift.io ingress-operator", operatorReleaseVersion),
	})

	if len(region) == 0 {
		if sess.Config.Region != nil {
			region = aws.StringValue(sess.Config.Region)
			log.Info("using region from shared config", "region name", region)
		} else {
			return nil, fmt.Errorf("region is required")
		}
	}

	// When RoleARN is provided, make a copy of the Route 53 session and configure it to use RoleARN.
	// RoleARN is intended to only provide access to another account's Route 53 service, not for ELBs.
	sessRoute53 := sess
	if config.RoleARN != "" {
		sessRoute53 = sess.Copy()
		sessRoute53.Config.WithCredentials(stscreds.NewCredentials(sessRoute53, config.RoleARN))
	}

	r53Config := aws.NewConfig()
	// elb requires no special region treatment.
	elbConfig := aws.NewConfig().WithRegion(region)
	tagConfig := aws.NewConfig()

	partition, ok := endpoints.PartitionForRegion(endpoints.DefaultPartitions(), region)
	if !ok {
		log.Info("unable to determine partition from region", "region name", region)
	}

	// If the region is in aws china, cn-north-1 or cn-northwest-1, we should:
	// 1. hard code route53 api endpoint to https://route53.amazonaws.com.cn and region to "cn-northwest-1"
	//    as route53 is not GA in AWS China and aws sdk didn't have the endpoint.
	// 2. use the aws china region cn-northwest-1 to setup tagging api correctly instead of "us-east-1"
	switch partition.ID() {
	case endpoints.AwsCnPartitionID:
		tagConfig = tagConfig.WithRegion(endpoints.CnNorthwest1RegionID)
		r53Config = r53Config.WithRegion(endpoints.CnNorthwest1RegionID).WithEndpoint(chinaRoute53Endpoint)
	case endpoints.AwsUsGovPartitionID:
		// Route53 for GovCloud uses the "us-gov-west-1" region id:
		// https://docs.aws.amazon.com/govcloud-us/latest/UserGuide/using-govcloud-endpoints.html
		r53Config = r53Config.WithRegion(endpoints.UsGovWest1RegionID)
		// As with other AWS partitions, the GovCloud Tagging client must be
		// in the same region as the Route53 client to find the hosted zone
		// of managed records.
		tagConfig = tagConfig.WithRegion(endpoints.UsGovWest1RegionID)
	case endpoints.AwsIsoPartitionID, endpoints.AwsIsoBPartitionID:
		// The resourcetagging API is not available in C2S or SC2S
		tagConfig = nil
		// Do not override the region in C2S or SC2S
		r53Config = r53Config.WithRegion(region)
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
			// TODO: Add custom endpoint support for elbv2. See the following for details:
			// https://docs.aws.amazon.com/general/latest/gr/elb.html
			switch ep.Name {
			case Route53Service:
				route53Found = true
				r53Config = r53Config.WithEndpoint(ep.URL)
				log.Info("using route53 custom endpoint", "url", ep.URL)
			case TaggingService:
				if tagConfig == nil {
					log.Info("found resourcegroupstaggingapi custom endpoint which will be ignored since the %s region does not support that API", region)
					continue
				}
				tagFound = true
				url := ep.URL
				// route53 for govcloud is based out of us-gov-west-1,
				// so the tagging client must match.
				if strings.Contains(ep.URL, "us-gov-east-1") {
					url = govCloudTaggingEndpoint
				}
				tagConfig = tagConfig.WithEndpoint(url)
				log.Info("using group tagging custom endpoint", "url", url)
			case ELBService:
				elbFound = true
				elbConfig = elbConfig.WithEndpoint(ep.URL)
				log.Info("using elb custom endpoint", "url", ep.URL)
			}
			// Once the three service endpoints have been found,
			// ignore any further service endpoint specifications.
			if route53Found && elbFound && tagFound {
				break
			}
		}
	}
	var tags *resourcegroupstaggingapi.ResourceGroupsTaggingAPI
	if tagConfig != nil {
		tags = resourcegroupstaggingapi.New(sess, tagConfig)
	}
	p := &Provider{
		elb:       elb.New(sess, elbConfig),
		elbv2:     elbv2.New(sess, aws.NewConfig().WithRegion(region)),
		route53:   route53.New(sessRoute53, r53Config),
		tags:      tags,
		config:    config,
		idsToTags: map[string]map[string]string{},
		lbZones:   map[string]string{},
	}
	if err := validateServiceEndpointsFn(p); err != nil {
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
	if provider.tags != nil {
		tagInput := resourcegroupstaggingapi.GetResourcesInput{TagsPerPage: aws.Int64(int64(100))}
		if _, err := provider.tags.GetResources(&tagInput); err != nil {
			errs = append(errs, fmt.Errorf("failed to get group tagging resources: %v", err))
		}
	}
	return kerrors.NewAggregate(errs)
}

// validateServiceEndpointsFn is an alias for validateServiceEndpoints in normal
// operation but can be overridden in unit tests.
var validateServiceEndpointsFn = validateServiceEndpoints

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
	var err error
	if m.tags != nil {
		id, err = m.lookupZoneID(zoneConfig)
	} else {
		id, err = m.lookupZoneIDWithoutResourceTagging(zoneConfig)
	}
	if err != nil {
		return id, err
	}

	// Update the cache
	m.idsToTags[id] = zoneConfig.Tags
	log.Info("found hosted zone using tags", "zone id", id, "tags", zoneConfig.Tags)

	return id, nil
}

func (m *Provider) lookupZoneID(zoneConfig configv1.DNSZone) (string, error) {
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
			id, innerError = zoneIDFromResource(zoneARN.Resource)
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
	return id, nil
}

func (m *Provider) lookupZoneIDWithoutResourceTagging(zoneConfig configv1.DNSZone) (string, error) {
	var id string
	var innerError error
	searchZones := func(resp *route53.ListHostedZonesOutput, lastPage bool) (shouldContinue bool) {
		input := &route53.ListTagsForResourcesInput{
			ResourceIds:  make([]*string, len(resp.HostedZones)),
			ResourceType: aws.String("hostedzone"),
		}
		for i, zone := range resp.HostedZones {
			zoneID, err := zoneIDFromResource(aws.StringValue(zone.Id))
			if err != nil {
				innerError = err
				return false
			}
			input.ResourceIds[i] = &zoneID
		}
		output, err := m.route53.ListTagsForResources(input)
		if err != nil {
			innerError = err
			return false
		}
		for _, tagSet := range output.ResourceTagSets {
			if zoneMatchesTags(tagSet.Tags, zoneConfig) {
				id = aws.StringValue(tagSet.ResourceId)
				return false
			}
		}
		return true
	}
	// the maximum page size is limited to 10 because the call to ListTagsForResources only supports 10 resources in a single call.
	outerError := m.route53.ListHostedZonesPages(
		&route53.ListHostedZonesInput{MaxItems: aws.String("10")},
		searchZones,
	)
	if err := kerrors.NewAggregate([]error{innerError, outerError}); err != nil {
		return id, fmt.Errorf("failed to get tagged resources: %v", err)
	}
	if len(id) == 0 {
		return id, fmt.Errorf("no matching hosted zone found")
	}
	return id, nil
}

func zoneMatchesTags(tags []*route53.Tag, zoneConfig configv1.DNSZone) bool {
	for k, v := range zoneConfig.Tags {
		tagMatches := false
		for _, tagPointer := range tags {
			if tagPointer == nil {
				continue
			}
			tag := *tagPointer
			if k != aws.StringValue(tag.Key) {
				continue
			}
			if v != aws.StringValue(tag.Value) {
				return false
			}
			tagMatches = true
			break
		}
		if !tagMatches {
			return false
		}
	}
	return true
}

func zoneIDFromResource(resource string) (string, error) {
	submatches := hostedZoneIDRegex.FindStringSubmatch(resource)
	if len(submatches) < 2 {
		return "", fmt.Errorf("got unexpected resource: %s", resource)
	}
	return submatches[1], nil
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

func (m *Provider) Replace(record *iov1.DNSRecord, zone configv1.DNSZone) error {
	return m.change(record, zone, upsertAction)
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
		err = fmt.Errorf("failed to get hosted zone for load balancer target %q: %v", target, err)
		if v, ok := record.Annotations[targetHostedZoneIdAnnotationKey]; !ok {
			return err
		} else {
			log.Error(err, "falling back to the "+targetHostedZoneIdAnnotationKey+" annotation", "value", v)
			targetHostedZoneID = v
		}
	}
	// If this is an upsert, store the target hosted zone id in an
	// annotation on the DNSRecord CR in case we later on need the id
	// and for whatever reason cannot look it up using the AWS API.
	if action == upsertAction {
		var current iov1.DNSRecord
		name := types.NamespacedName{
			Namespace: record.Namespace,
			Name:      record.Name,
		}
		if err := m.config.Client.Get(context.TODO(), name, &current); err != nil {
			// Log the error and continue.  The annotation is only
			// needed as a fallback mechanism, and anyway we might
			// succeed in adding it on the next upsert.
			log.Error(err, "failed to get dnsrecord", "dnsrecord", name)
		} else if _, ok := current.Annotations[targetHostedZoneIdAnnotationKey]; !ok {
			updated := current.DeepCopy()
			if updated.Annotations == nil {
				updated.Annotations = map[string]string{}
			}
			updated.Annotations[targetHostedZoneIdAnnotationKey] = targetHostedZoneID
			if err := m.config.Client.Update(context.TODO(), updated); err != nil {
				log.Error(err, "failed to annotate dnsrecord", "dnsrecord", name)
			} else {
				log.Info("annotated dnsrecord", "dnsrecord", name, "key", targetHostedZoneIdAnnotationKey, "value", targetHostedZoneID)
			}
		}
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
// Note that by API contract, TTL cannot be specified for an AliasTarget.
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
