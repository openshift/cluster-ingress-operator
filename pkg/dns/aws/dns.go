package aws

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	smithymw "github.com/aws/smithy-go/middleware"

	iov1 "github.com/openshift/api/operatoringress/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/dns"
	logf "github.com/openshift/cluster-ingress-operator/pkg/log"
	awsutil "github.com/openshift/cluster-ingress-operator/pkg/util/aws"

	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"

	configv1 "github.com/openshift/api/config/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// Service name constants are hardcoded because aws-sdk-go-v2 only exports
	// human-readable ServiceIDs (e.g. "Route 53"), not the endpoint URL prefixes
	// that the OpenShift infrastructure API uses in AWSServiceEndpoint.Name.
	Route53Service = "route53"
	ELBService     = "elasticloadbalancing"
	TaggingService = "tagging"
	// govCloudRoute53Region is the AWS GovCloud region for Route 53. See:
	// https://docs.aws.amazon.com/govcloud-us/latest/UserGuide/using-govcloud-endpoints.html
	govCloudRoute53Region = "us-gov"
	// eusCloudRegionPrefix is the prefix of regions in AWS European Sovereign Cloud.
	eusCloudRegionPrefix = "eusc-"
	// euscDeEast1RegionID is the region ID of Brandenburg (German) in AWS European Sovereign Cloud.
	euscDeEast1RegionID = "eusc-de-east-1"
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
	elb     *elasticloadbalancing.Client
	elbv2   *elbv2.Client
	route53 *route53.Client
	tags    *resourcegroupstaggingapi.Client

	// endpoint strings stored for logging and test verification, since v2
	// resolves endpoints dynamically per-request.
	elbEndpoint     string
	elbv2Endpoint   string
	route53Endpoint string
	tagsEndpoint    string

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

	// IPFamily is the cluster's IP family configuration from the
	// Infrastructure CR. When dual-stack, the provider creates both
	// Alias A and Alias AAAA Route53 records.
	IPFamily configv1.IPFamilyType
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

// partitionIDForRegion returns the AWS partition ID for the given region.
// aws-sdk-go-v2 has no public partition lookup API; the logic is internal to
// each service's endpoint resolver, so we use region prefix conventions.
func partitionIDForRegion(region string) string {
	switch {
	case strings.HasPrefix(region, "cn-"):
		return "aws-cn"
	case strings.HasPrefix(region, "us-gov-"):
		return "aws-us-gov"
	case strings.HasPrefix(region, "us-iso-"):
		return "aws-iso"
	case strings.HasPrefix(region, "us-isob-"):
		return "aws-iso-b"
	default:
		return "aws"
	}
}

// isGovCloudRegion returns true if the provided region is in a US
// GovCloud partition.
func isGovCloudRegion(region string) bool {
	return strings.HasPrefix(region, "us-gov-")
}

// resolveEndpoint returns the endpoint URL for logging and test assertions.
// In aws-sdk-go-v2, endpoints are resolved per-request internally — there is
// no client.Endpoint field — so we compute them here at construction time.
func resolveEndpoint(customEndpoint, serviceID, region string) string {
	if customEndpoint != "" {
		return customEndpoint
	}
	// For logging purposes when no custom endpoint is set, we construct
	// a best-effort endpoint string. The actual endpoint resolution in
	// v2 happens per-request inside the SDK.
	switch serviceID {
	case "Route 53":
		switch partitionIDForRegion(region) {
		case "aws-cn":
			return chinaRoute53Endpoint
		case "aws-us-gov":
			return fmt.Sprintf("https://route53.%s.amazonaws.com", govCloudRoute53Region)
		case "aws-iso":
			return "https://route53.c2s.ic.gov"
		case "aws-iso-b":
			return "https://route53.sc2s.sgov.gov"
		default:
			if strings.HasPrefix(region, eusCloudRegionPrefix) {
				return "https://route53.amazonaws.eu"
			}
			return "https://route53.amazonaws.com"
		}
	case "Elastic Load Balancing", "Elastic Load Balancing v2":
		switch partitionIDForRegion(region) {
		case "aws-cn":
			return fmt.Sprintf("https://elasticloadbalancing.%s.amazonaws.com.cn", region)
		case "aws-us-gov":
			return fmt.Sprintf("https://elasticloadbalancing.%s.amazonaws.com", region)
		case "aws-iso":
			return fmt.Sprintf("https://elasticloadbalancing.%s.c2s.ic.gov", region)
		case "aws-iso-b":
			return fmt.Sprintf("https://elasticloadbalancing.%s.sc2s.sgov.gov", region)
		default:
			if strings.HasPrefix(region, eusCloudRegionPrefix) {
				return fmt.Sprintf("https://elasticloadbalancing.%s.amazonaws.eu", region)
			}
			return fmt.Sprintf("https://elasticloadbalancing.%s.amazonaws.com", region)
		}
	case "Resource Groups Tagging API":
		switch partitionIDForRegion(region) {
		case "aws-cn":
			return fmt.Sprintf("https://tagging.%s.amazonaws.com.cn", region)
		case "aws-us-gov":
			return fmt.Sprintf("https://tagging.%s.amazonaws.com", region)
		default:
			if strings.HasPrefix(region, eusCloudRegionPrefix) {
				return fmt.Sprintf("https://tagging.%s.amazonaws.eu", region)
			}
			return fmt.Sprintf("https://tagging.%s.amazonaws.com", region)
		}
	}
	return ""
}

func NewProvider(config Config, operatorReleaseVersion string) (*Provider, error) {
	ctx := context.TODO()

	var loadOpts []func(*awsconfig.LoadOptions) error
	loadOpts = append(loadOpts,
		awsconfig.WithSharedCredentialsFiles([]string{config.SharedCredentialFile}),
		awsconfig.WithSharedConfigFiles([]string{config.SharedCredentialFile}),
	)
	if config.CustomCABundle != "" {
		loadOpts = append(loadOpts, awsconfig.WithCustomCABundle(strings.NewReader(config.CustomCABundle)))
	}
	var region string
	if len(config.Region) > 0 {
		region = config.Region
		loadOpts = append(loadOpts, awsconfig.WithRegion(config.Region))
		log.Info("using region from operator config", "region name", region)
	}
	loadOpts = append(loadOpts, awsconfig.WithAPIOptions([]func(*smithymw.Stack) error{
		awsmiddleware.AddUserAgentKeyValue("openshift.io/ingress-operator", operatorReleaseVersion),
	}))

	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("couldn't create AWS client config: %v", err)
	}

	if len(region) == 0 {
		if cfg.Region != "" {
			region = cfg.Region
			log.Info("using region from shared config", "region name", region)
		} else {
			return nil, fmt.Errorf("region is required")
		}
	}

	// When RoleARN is provided, make a copy of the Route 53 config and configure it to use RoleARN.
	// RoleARN is intended to only provide access to another account's Route 53 service, not for ELBs.
	// Shallow copy is safe here: only Credentials is overwritten; shared reference fields
	// (HTTPClient, Logger, etc.) are not mutated after this point.
	cfgRoute53 := cfg
	if config.RoleARN != "" {
		stsClient := sts.NewFromConfig(cfg)
		cfgRoute53.Credentials = aws.NewCredentialsCache(
			stscreds.NewAssumeRoleProvider(stsClient, config.RoleARN),
		)
	}

	var r53Endpoint string
	var r53Region string
	var elbEndpointOverride string
	var tagEndpoint string
	var tagRegion string
	enableTagging := true

	// If the region is in aws china, cn-north-1 or cn-northwest-1, we should:
	// 1. hard code route53 api endpoint to https://route53.amazonaws.com.cn and region to "cn-northwest-1"
	//    as route53 is not GA in AWS China and aws sdk didn't have the endpoint.
	// 2. use the aws china region cn-northwest-1 to setup tagging api correctly instead of "us-east-1"
	switch partitionIDForRegion(region) {
	case "aws-cn":
		tagRegion = "cn-northwest-1"
		r53Region = "cn-northwest-1"
		r53Endpoint = chinaRoute53Endpoint
	case "aws-us-gov":
		// Route53 for GovCloud uses the "us-gov-west-1" region id:
		// https://docs.aws.amazon.com/govcloud-us/latest/UserGuide/using-govcloud-endpoints.html
		r53Region = "us-gov-west-1"
		// As with other AWS partitions, the GovCloud Tagging client must be
		// in the same region as the Route53 client to find the hosted zone
		// of managed records.
		tagRegion = "us-gov-west-1"
	case "aws-iso", "aws-iso-b":
		// The resourcetagging API is not available in C2S or SC2S
		enableTagging = false
		// Do not override the region in C2S or SC2S
		r53Region = region
	default:
		// AWS European Sovereign Cloud
		if strings.HasPrefix(region, eusCloudRegionPrefix) {
			// Since Route 53 is not a regionalized service, the Tagging API will
			// only return hosted zone resources when the region is "eusc-de-east-1".
			tagRegion = euscDeEast1RegionID
			// Use eusc-de-east-1 for Route 53 in AWS Regions for EU Sovereign Cloud.
			// See https://docs.aws.eu/general/latest/gr/endpoints.html for details.
			r53Region = euscDeEast1RegionID
			break
		}

		// AWS Standard Partition
		// Since Route 53 is not a regionalized service, the Tagging API will
		// only return hosted zone resources when the region is "us-east-1".
		tagRegion = "us-east-1"
		// Use us-east-1 for Route 53 in AWS Regions other than China or GovCloud Regions.
		// See https://docs.aws.amazon.com/general/latest/gr/r53.html for details.
		r53Region = "us-east-1"
	}
	if len(config.ServiceEndpoints) > 0 {
		route53Found := false
		elbFound := false
		tagFound := false
		for _, ep := range config.ServiceEndpoints {
			switch ep.Name {
			case Route53Service:
				route53Found = true
				r53Endpoint = ep.URL
				log.Info("Found route53 custom endpoint", "url", ep.URL)
			case TaggingService:
				if !enableTagging {
					log.Info(fmt.Sprintf("Found resourcegroupstaggingapi custom endpoint, which will be ignored since the %s region does not support that API", region))
					continue
				}
				tagFound = true
				url := ep.URL
				// route53 for govcloud is based out of us-gov-west-1,
				// so the tagging client must match.
				if strings.Contains(ep.URL, "us-gov-east-1") {
					url = govCloudTaggingEndpoint
				}
				tagEndpoint = url
				log.Info("Found resourcegroupstaggingapi custom endpoint", "url", url)
			case ELBService:
				elbFound = true
				elbEndpointOverride = ep.URL
				log.Info("Found elb custom endpoint", "url", ep.URL)
				// The SDK uses the same service ID "elasticloadbalancing" for elb and elbv2
				// Thus, if defined, we need to use the custom service endpoint for both.
				log.Info("Found elb v2 custom endpoint", "url", ep.URL)
			}
			// Once the three service endpoints have been found,
			// ignore any further service endpoint specifications.
			if route53Found && elbFound && tagFound {
				break
			}
		}
	}

	var tags *resourcegroupstaggingapi.Client
	var tagsEndpointStr string
	if !enableTagging {
		log.Info("No tags client configured")
	} else {
		tagOpts := func(o *resourcegroupstaggingapi.Options) {
			if tagRegion != "" {
				o.Region = tagRegion
			}
			if tagEndpoint != "" {
				o.BaseEndpoint = aws.String(tagEndpoint)
			}
		}
		tags = resourcegroupstaggingapi.NewFromConfig(cfg, tagOpts)
		tagsEndpointStr = resolveEndpoint(tagEndpoint, "Resource Groups Tagging API", tagRegion)
		log.Info("Created tags client", "endpoint", tagsEndpointStr)
	}

	elbOpts := func(o *elasticloadbalancing.Options) {
		o.Region = region
		if elbEndpointOverride != "" {
			o.BaseEndpoint = aws.String(elbEndpointOverride)
		}
	}
	elbClient := elasticloadbalancing.NewFromConfig(cfg, elbOpts)
	elbEndpointStr := resolveEndpoint(elbEndpointOverride, "Elastic Load Balancing", region)
	log.Info("Created elb client", "endpoint", elbEndpointStr)

	elbv2Opts := func(o *elbv2.Options) {
		o.Region = region
		if elbEndpointOverride != "" {
			o.BaseEndpoint = aws.String(elbEndpointOverride)
		}
	}
	elbv2Client := elbv2.NewFromConfig(cfg, elbv2Opts)
	elbv2EndpointStr := resolveEndpoint(elbEndpointOverride, "Elastic Load Balancing v2", region)
	log.Info("Created elbv2 client", "endpoint", elbv2EndpointStr)

	r53Opts := func(o *route53.Options) {
		if r53Region != "" {
			o.Region = r53Region
		}
		if r53Endpoint != "" {
			o.BaseEndpoint = aws.String(r53Endpoint)
		}
	}
	r53client := route53.NewFromConfig(cfgRoute53, r53Opts)
	r53EndpointStr := resolveEndpoint(r53Endpoint, "Route 53", r53Region)
	log.Info("Created route53 client", "endpoint", r53EndpointStr)

	p := &Provider{
		elb:             elbClient,
		elbv2:           elbv2Client,
		route53:         r53client,
		tags:            tags,
		elbEndpoint:     elbEndpointStr,
		elbv2Endpoint:   elbv2EndpointStr,
		route53Endpoint: r53EndpointStr,
		tagsEndpoint:    tagsEndpointStr,
		config:          config,
		idsToTags:       map[string]map[string]string{},
		lbZones:         map[string]string{},
	}
	if err := validateServiceEndpointsFn(p); err != nil {
		return nil, fmt.Errorf("failed to validate aws provider service endpoints: %v", err)
	}
	return p, nil
}

// validateServiceEndpoints validates that provider clients can communicate with
// associated API endpoints by having each client make a list/describe/get call.
func validateServiceEndpoints(provider *Provider) error {
	ctx := context.TODO()
	var errs []error
	zoneInput := route53.ListHostedZonesInput{MaxItems: aws.Int32(1)}
	if _, err := provider.route53.ListHostedZones(ctx, &zoneInput); err != nil {
		errs = append(errs, fmt.Errorf("failed to list route53 hosted zones: %v", err))
	}
	elbInput := elasticloadbalancing.DescribeLoadBalancersInput{PageSize: aws.Int32(1)}
	if _, err := provider.elb.DescribeLoadBalancers(ctx, &elbInput); err != nil {
		errs = append(errs, fmt.Errorf("failed to describe elb load balancers: %v", err))
	}
	elbv2Input := elbv2.DescribeLoadBalancersInput{PageSize: aws.Int32(1)}
	if _, err := provider.elbv2.DescribeLoadBalancers(ctx, &elbv2Input); err != nil {
		errs = append(errs, fmt.Errorf("failed to describe elbv2 load balancers: %v", err))
	}
	if provider.tags != nil {
		tagInput := resourcegroupstaggingapi.GetResourcesInput{TagsPerPage: aws.Int32(100)}
		if _, err := provider.tags.GetResources(ctx, &tagInput); err != nil {
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
	ctx := context.TODO()
	tagFilters := []tagtypes.TagFilter{}
	for k, v := range zoneConfig.Tags {
		tagFilters = append(tagFilters, tagtypes.TagFilter{
			Key:    aws.String(k),
			Values: []string{v},
		})
	}
	// Even though we use filters when getting resources, the resources are still
	// paginated as though no filter were applied.  If the desired resource is not
	// on the first page, then GetResources will not return it.  We need to use
	// the paginator and possibly go through one or more empty pages of
	// resources till we find a resource that gets through the filters.
	paginator := resourcegroupstaggingapi.NewGetResourcesPaginator(m.tags, &resourcegroupstaggingapi.GetResourcesInput{
		ResourceTypeFilters: []string{"route53:hostedzone"},
		TagFilters:          tagFilters,
	})
	for paginator.HasMorePages() {
		resp, err := paginator.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get tagged resources: %v", err)
		}
		for _, zone := range resp.ResourceTagMappingList {
			zoneARN, err := arn.Parse(aws.ToString(zone.ResourceARN))
			if err != nil {
				return "", fmt.Errorf("failed to parse hostedzone ARN %q: %v", aws.ToString(zone.ResourceARN), err)
			}
			return zoneIDFromResource(zoneARN.Resource)
		}
	}
	return "", fmt.Errorf("no matching hosted zone found")
}

func (m *Provider) lookupZoneIDWithoutResourceTagging(zoneConfig configv1.DNSZone) (string, error) {
	ctx := context.TODO()
	// the maximum page size is limited to 10 because the call to ListTagsForResources only supports 10 resources in a single call.
	paginator := route53.NewListHostedZonesPaginator(m.route53, &route53.ListHostedZonesInput{
		MaxItems: aws.Int32(10),
	})
	for paginator.HasMorePages() {
		resp, err := paginator.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to list hosted zones: %v", err)
		}
		resourceIds := make([]string, len(resp.HostedZones))
		for i, zone := range resp.HostedZones {
			zoneID, err := zoneIDFromResource(aws.ToString(zone.Id))
			if err != nil {
				return "", err
			}
			resourceIds[i] = zoneID
		}
		output, err := m.route53.ListTagsForResources(ctx, &route53.ListTagsForResourcesInput{
			ResourceIds:  resourceIds,
			ResourceType: r53types.TagResourceTypeHostedzone,
		})
		if err != nil {
			return "", err
		}
		for _, tagSet := range output.ResourceTagSets {
			if zoneMatchesTags(tagSet.Tags, zoneConfig) {
				return aws.ToString(tagSet.ResourceId), nil
			}
		}
	}
	return "", fmt.Errorf("no matching hosted zone found")
}

func zoneMatchesTags(tags []r53types.Tag, zoneConfig configv1.DNSZone) bool {
	for k, v := range zoneConfig.Tags {
		tagMatches := false
		for _, tag := range tags {
			if k != aws.ToString(tag.Key) {
				continue
			}
			if v != aws.ToString(tag.Value) {
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

	ctx := context.TODO()
	var id string

	elbPaginator := elasticloadbalancing.NewDescribeLoadBalancersPaginator(m.elb, &elasticloadbalancing.DescribeLoadBalancersInput{})
	for elbPaginator.HasMorePages() {
		resp, err := elbPaginator.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to describe classic load balancers: %v", err)
		}
		for _, lb := range resp.LoadBalancerDescriptions {
			dnsName := aws.ToString(lb.DNSName)
			zoneID := aws.ToString(lb.CanonicalHostedZoneNameID)
			log.V(2).Info("found classic load balancer", "name", aws.ToString(lb.LoadBalancerName), "dns name", dnsName, "hosted zone ID", zoneID)
			if dnsName == name {
				id = zoneID
				break
			}
		}
		if len(id) > 0 {
			break
		}
	}
	if len(id) == 0 {
		elbv2Paginator := elbv2.NewDescribeLoadBalancersPaginator(m.elbv2, &elbv2.DescribeLoadBalancersInput{})
		for elbv2Paginator.HasMorePages() {
			resp, err := elbv2Paginator.NextPage(ctx)
			if err != nil {
				return "", fmt.Errorf("failed to describe network load balancers: %v", err)
			}
			for _, lb := range resp.LoadBalancers {
				dnsName := aws.ToString(lb.DNSName)
				zoneID := aws.ToString(lb.CanonicalHostedZoneId)
				log.V(2).Info("found network load balancer", "name", aws.ToString(lb.LoadBalancerName), "dns name", dnsName, "hosted zone ID", zoneID)
				if dnsName == name {
					id = zoneID
					break
				}
			}
			if len(id) > 0 {
				break
			}
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
	if isGovCloudRegion(m.route53.Options().Region) {
		record := r53types.ResourceRecord{Value: aws.String(target)}
		input.ChangeBatch = &r53types.ChangeBatch{
			Changes: []r53types.Change{
				{
					Action: r53types.ChangeAction(action),
					ResourceRecordSet: &r53types.ResourceRecordSet{
						Name:            aws.String(domain),
						Type:            r53types.RRTypeCname,
						TTL:             aws.Int64(ttl),
						ResourceRecords: []r53types.ResourceRecord{record},
					},
				},
			},
		}
	} else {
		aliasTarget := &r53types.AliasTarget{
			HostedZoneId:         aws.String(targetHostedZoneID),
			DNSName:              aws.String(target),
			EvaluateTargetHealth: false,
		}
		changes := []r53types.Change{
			{
				Action: r53types.ChangeAction(action),
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name:        aws.String(domain),
					Type:        r53types.RRTypeA,
					AliasTarget: aliasTarget,
				},
			},
		}
		if awsutil.IsDualStack(m.config.IPFamily) {
			changes = append(changes, r53types.Change{
				Action: r53types.ChangeAction(action),
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name:        aws.String(domain),
					Type:        r53types.RRTypeAaaa,
					AliasTarget: aliasTarget,
				},
			})
		}
		input.ChangeBatch = &r53types.ChangeBatch{
			Changes: changes,
		}
	}
	resp, err := m.route53.ChangeResourceRecordSets(context.TODO(), &input)
	if err != nil {
		if action == string(deleteAction) {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				if strings.Contains(apiErr.ErrorMessage(), "not found") {
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
