package ingress

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func Test_desiredLoadBalancerService(t *testing.T) {
	var (
		// platformStatus returns a PlatformStatus with the specified
		// platform type.
		platformStatus = func(platformType configv1.PlatformType) *configv1.PlatformStatus {
			return &configv1.PlatformStatus{
				Type: platformType,
			}
		}
		// lbs returns an EndpointPublishingStrategy with type
		// "LoadBalancerService" and the specified scope.
		lbs = func(scope operatorv1.LoadBalancerScope) *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.LoadBalancerServiceStrategyType,
				LoadBalancer: &operatorv1.LoadBalancerStrategy{
					Scope: scope,
				},
			}
		}
		// nps returns an EndpointPublishingStrategy with type
		// "NodePortService" and the specified protocol.
		nps = func(proto operatorv1.IngressControllerProtocol) *operatorv1.EndpointPublishingStrategy {
			return &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.NodePortServiceStrategyType,
				NodePort: &operatorv1.NodePortStrategy{
					Protocol: proto,
				},
			}
		}
		// nlb returns an EndpointPublishingStrategy with type
		// "LoadBalancerStrategy" and the specified scope and with
		// providerParameters set to specify an NLB.
		nlb = func(scope operatorv1.LoadBalancerScope) *operatorv1.EndpointPublishingStrategy {
			eps := lbs(scope)
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.AWSLoadBalancerProvider,
				AWS: &operatorv1.AWSLoadBalancerParameters{
					Type: operatorv1.AWSNetworkLoadBalancer,
				},
			}
			return eps
		}
		awsLbWithSubnets = func(lbType operatorv1.AWSLoadBalancerType, subnets *operatorv1.AWSSubnets) *operatorv1.EndpointPublishingStrategy {
			eps := lbs(operatorv1.ExternalLoadBalancer)
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.AWSLoadBalancerProvider,
				AWS: &operatorv1.AWSLoadBalancerParameters{
					Type: lbType,
				},
			}
			switch lbType {
			case operatorv1.AWSNetworkLoadBalancer:
				eps.LoadBalancer.ProviderParameters.AWS.NetworkLoadBalancerParameters = &operatorv1.AWSNetworkLoadBalancerParameters{
					Subnets: subnets,
				}
			case operatorv1.AWSClassicLoadBalancer:
				eps.LoadBalancer.ProviderParameters.AWS.ClassicLoadBalancerParameters = &operatorv1.AWSClassicLoadBalancerParameters{
					Subnets: subnets,
				}
			}
			return eps
		}

		// nlbWithEIPAllocations returns an AWS NLB with the specified EIP allocations.
		nlbWithEIPAllocations = func(scope operatorv1.LoadBalancerScope, eipAllocations []operatorv1.EIPAllocation) *operatorv1.EndpointPublishingStrategy {
			eps := lbs(scope)
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.AWSLoadBalancerProvider,
				AWS: &operatorv1.AWSLoadBalancerParameters{
					Type: operatorv1.AWSNetworkLoadBalancer,
					NetworkLoadBalancerParameters: &operatorv1.AWSNetworkLoadBalancerParameters{
						EIPAllocations: eipAllocations,
					},
				},
			}
			return eps
		}

		// gcpLB returns an EndpointPublishingStrategy with type
		// "LoadBalancerService" and the specified scope and with
		// providerParameters set with the specified GCP ClientAccess
		// setting.
		gcpLB = func(scope operatorv1.LoadBalancerScope, clientAccess operatorv1.GCPClientAccess) *operatorv1.EndpointPublishingStrategy {
			eps := lbs(scope)
			eps.LoadBalancer.ProviderParameters = &operatorv1.ProviderLoadBalancerParameters{
				Type: operatorv1.GCPLoadBalancerProvider,
				GCP: &operatorv1.GCPLoadBalancerParameters{
					ClientAccess: clientAccess,
				},
			}
			return eps
		}
	)

	type annotationExpectation struct {
		present bool
		value   string
	}
	testCases := []struct {
		description                     string
		strategySpec                    *operatorv1.EndpointPublishingStrategy
		strategyStatus                  *operatorv1.EndpointPublishingStrategy
		proxyNeeded                     bool
		expectService                   bool
		expectedServiceAnnotations      map[string]annotationExpectation
		expectedExternalTrafficPolicy   corev1.ServiceExternalTrafficPolicy
		platformStatus                  *configv1.PlatformStatus
		subnetsAWSFeatureEnabled        bool
		eipAllocationsAWSFeatureEnabled bool
	}{
		{
			description:                   "external classic load balancer with scope for aws platform",
			platformStatus:                platformStatus(configv1.AWSPlatformType),
			strategyStatus:                lbs(operatorv1.ExternalLoadBalancer),
			proxyNeeded:                   true,
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalDefault},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {true, "*"},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {false, ""},
			},
		},
		{
			description:                   "external classic load balancer with scope for aws platform and custom user tags",
			strategyStatus:                lbs(operatorv1.ExternalLoadBalancer),
			proxyNeeded:                   true,
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {true, "classic-load-balancer-key-with-value=100,classic-load-balancer-key-with-empty-value="},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalDefault},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {true, "*"},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {false, ""},
			},
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
				AWS: &configv1.AWSPlatformStatus{
					ResourceTags: []configv1.AWSResourceTag{{
						Key:   "classic-load-balancer-key-with-value",
						Value: "100",
					}, {
						Key:   "classic-load-balancer-key-with-empty-value",
						Value: "",
					}, {
						Value: "classic-load-balancer-value-without-key",
					}},
				},
			},
		},
		{
			description:                   "external classic load balancer without LoadBalancerStrategy for aws platform",
			platformStatus:                platformStatus(configv1.AWSPlatformType),
			strategyStatus:                lbs(""),
			proxyNeeded:                   true,
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalDefault},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {true, "*"},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {false, ""},
			},
		},
		{
			description:                   "internal classic load balancer for aws platform",
			platformStatus:                platformStatus(configv1.AWSPlatformType),
			strategyStatus:                lbs(operatorv1.InternalLoadBalancer),
			proxyNeeded:                   true,
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {true, "true"},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalDefault},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {true, "*"},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {false, ""},
			},
		},
		{
			description:                   "external network load balancer without scope for aws platform",
			platformStatus:                platformStatus(configv1.AWSPlatformType),
			strategyStatus:                nlb(""),
			proxyNeeded:                   false,
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalNLB},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {false, ""},
				AWSLBTypeAnnotation:                          {true, AWSNLBAnnotation},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {false, ""},
			},
		},
		{
			description:                   "external network load balancer with scope for aws platform",
			platformStatus:                platformStatus(configv1.AWSPlatformType),
			strategyStatus:                nlb(operatorv1.ExternalLoadBalancer),
			proxyNeeded:                   false,
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalNLB},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {false, ""},
				AWSLBTypeAnnotation:                          {true, AWSNLBAnnotation},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {false, ""},
			},
		},
		{
			description:    "external network load balancer with scope for aws platform and custom user tags",
			strategyStatus: nlb(operatorv1.ExternalLoadBalancer),
			proxyNeeded:    false,
			expectService:  true,
			platformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
				AWS: &configv1.AWSPlatformStatus{
					ResourceTags: []configv1.AWSResourceTag{{
						Key:   "network-load-balancer-key-with-value",
						Value: "200",
					}, {
						Key:   "network-load-balancer-key-with-empty-value",
						Value: "",
					}, {
						Value: "network-load-balancer-value-without-key",
					}},
				},
			},
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {true, "network-load-balancer-key-with-value=200,network-load-balancer-key-with-empty-value="},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalNLB},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				AWSLBTypeAnnotation:                          {true, AWSNLBAnnotation},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {false, ""},
			},
		},
		{
			description:    "network load balancer with subnets for aws platform, but feature gate is disabled",
			platformStatus: platformStatus(configv1.AWSPlatformType),
			strategySpec: awsLbWithSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-00000000000000001", "subnet-00000000000000002"},
					Names: []operatorv1.AWSSubnetName{"subnetA", "subnetB"},
				},
			),
			strategyStatus: awsLbWithSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-00000000000000001", "subnet-00000000000000002"},
					Names: []operatorv1.AWSSubnetName{"subnetA", "subnetB"},
				},
			),
			proxyNeeded:                   false,
			expectService:                 true,
			subnetsAWSFeatureEnabled:      false,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalNLB},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {false, ""},
				AWSLBTypeAnnotation:                          {true, AWSNLBAnnotation},
				localWithFallbackAnnotation:                  {true, ""},
			},
		},
		{
			description:    "network load balancer with subnets for aws platform using both IDs and names",
			platformStatus: platformStatus(configv1.AWSPlatformType),
			strategySpec: awsLbWithSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-00000000000000001", "subnet-00000000000000002"},
					Names: []operatorv1.AWSSubnetName{"subnetA", "subnetB"},
				},
			),
			strategyStatus: awsLbWithSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-00000000000000001", "subnet-00000000000000002"},
					Names: []operatorv1.AWSSubnetName{"subnetA", "subnetB"},
				},
			),
			proxyNeeded:                   false,
			expectService:                 true,
			subnetsAWSFeatureEnabled:      true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalNLB},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {false, ""},
				AWSLBTypeAnnotation:                          {true, AWSNLBAnnotation},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {true, "subnet-00000000000000001,subnet-00000000000000002,subnetA,subnetB"},
			},
		},
		{
			description:    "network load balancer with subnets for aws platform using IDs",
			platformStatus: platformStatus(configv1.AWSPlatformType),
			strategySpec: awsLbWithSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"subnet-00000000000000001", "subnet-00000000000000002"},
				},
			),
			strategyStatus: awsLbWithSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs: []operatorv1.AWSSubnetID{"subnet-00000000000000001", "subnet-00000000000000002"},
				},
			),
			proxyNeeded:                   false,
			expectService:                 true,
			subnetsAWSFeatureEnabled:      true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalNLB},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {false, ""},
				AWSLBTypeAnnotation:                          {true, AWSNLBAnnotation},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {true, "subnet-00000000000000001,subnet-00000000000000002"},
			},
		},
		{
			description:    "network load balancer with subnets for aws platform using names",
			platformStatus: platformStatus(configv1.AWSPlatformType),
			strategySpec: awsLbWithSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"subnetA", "subnetB"},
				},
			),
			strategyStatus: awsLbWithSubnets(
				operatorv1.AWSNetworkLoadBalancer,
				&operatorv1.AWSSubnets{
					Names: []operatorv1.AWSSubnetName{"subnetA", "subnetB"},
				},
			),
			proxyNeeded:                   false,
			expectService:                 true,
			subnetsAWSFeatureEnabled:      true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalNLB},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {false, ""},
				AWSLBTypeAnnotation:                          {true, AWSNLBAnnotation},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {true, "subnetA,subnetB"},
			},
		},
		{
			description:    "classic load balancer with subnets for aws platform using both IDs and names",
			platformStatus: platformStatus(configv1.AWSPlatformType),
			strategySpec: awsLbWithSubnets(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-00000000000000001", "subnet-00000000000000002"},
					Names: []operatorv1.AWSSubnetName{"subnetA", "subnetB"},
				},
			),
			strategyStatus: awsLbWithSubnets(
				operatorv1.AWSClassicLoadBalancer,
				&operatorv1.AWSSubnets{
					IDs:   []operatorv1.AWSSubnetID{"subnet-00000000000000001", "subnet-00000000000000002"},
					Names: []operatorv1.AWSSubnetName{"subnetA", "subnetB"},
				},
			),
			proxyNeeded:                   true,
			expectService:                 true,
			subnetsAWSFeatureEnabled:      true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalDefault},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {true, "*"},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {true, "subnet-00000000000000001,subnet-00000000000000002,subnetA,subnetB"},
			},
		},
		{
			description:    "network load balancer with eipAllocations for aws platform when feature gate is enabled",
			platformStatus: platformStatus(configv1.AWSPlatformType),
			strategySpec: nlbWithEIPAllocations(operatorv1.ExternalLoadBalancer,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
			),
			strategyStatus: nlbWithEIPAllocations(operatorv1.ExternalLoadBalancer,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
			),
			proxyNeeded:                     false,
			expectService:                   true,
			eipAllocationsAWSFeatureEnabled: true,
			expectedExternalTrafficPolicy:   corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalNLB},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {false, ""},
				AWSLBTypeAnnotation:                          {true, AWSNLBAnnotation},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {false, ""},
				awsEIPAllocationsAnnotation:                  {true, "eipalloc-xxxxxxxxxxxxxxxxx,eipalloc-yyyyyyyyyyyyyyyyy"},
			},
		},
		{
			description:    "network load balancer with eipAllocations for aws platform when feature gate is disabled",
			platformStatus: platformStatus(configv1.AWSPlatformType),
			strategySpec: nlbWithEIPAllocations(operatorv1.ExternalLoadBalancer,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
			),
			strategyStatus: nlbWithEIPAllocations(operatorv1.ExternalLoadBalancer,
				[]operatorv1.EIPAllocation{"eipalloc-xxxxxxxxxxxxxxxxx", "eipalloc-yyyyyyyyyyyyyyyyy"},
			),
			proxyNeeded:                     false,
			expectService:                   true,
			eipAllocationsAWSFeatureEnabled: false,
			expectedExternalTrafficPolicy:   corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalNLB},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {false, ""},
				AWSLBTypeAnnotation:                          {true, AWSNLBAnnotation},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {false, ""},
				awsEIPAllocationsAnnotation:                  {false, ""},
			},
		},
		{
			description:    "network load balancer with nil eipAllocations for aws platform when feature gate is enabled",
			platformStatus: platformStatus(configv1.AWSPlatformType),
			strategySpec: nlbWithEIPAllocations(operatorv1.ExternalLoadBalancer,
				nil,
			),
			strategyStatus: nlbWithEIPAllocations(operatorv1.ExternalLoadBalancer,
				nil,
			),
			proxyNeeded:                     false,
			expectService:                   true,
			eipAllocationsAWSFeatureEnabled: true,
			expectedExternalTrafficPolicy:   corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				awsInternalLBAnnotation:                      {false, ""},
				awsLBAdditionalResourceTags:                  {false, ""},
				awsLBHealthCheckHealthyThresholdAnnotation:   {true, awsLBHealthCheckHealthyThresholdDefault},
				awsLBHealthCheckIntervalAnnotation:           {true, awsLBHealthCheckIntervalNLB},
				awsLBHealthCheckTimeoutAnnotation:            {true, awsLBHealthCheckTimeoutDefault},
				awsLBHealthCheckUnhealthyThresholdAnnotation: {true, awsLBHealthCheckUnhealthyThresholdDefault},
				awsLBProxyProtocolAnnotation:                 {false, ""},
				AWSLBTypeAnnotation:                          {true, AWSNLBAnnotation},
				localWithFallbackAnnotation:                  {true, ""},
				awsLBSubnetsAnnotation:                       {false, ""},
				awsEIPAllocationsAnnotation:                  {false, ""},
			},
		},
		{
			description:    "nodePort service for aws platform",
			platformStatus: platformStatus(configv1.AWSPlatformType),
			strategyStatus: nps(operatorv1.TCPProtocol),
			proxyNeeded:    false,
			expectService:  false,
		},
		{
			description:                   "external load balancer for ibm platform",
			platformStatus:                platformStatus(configv1.IBMCloudPlatformType),
			strategyStatus:                lbs(operatorv1.ExternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyCluster,
			expectedServiceAnnotations: map[string]annotationExpectation{
				iksLBScopeAnnotation: {true, iksLBScopePublic},
			},
		},
		{
			description:                   "internal load balancer for ibm platform",
			platformStatus:                platformStatus(configv1.IBMCloudPlatformType),
			strategyStatus:                lbs(operatorv1.InternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyCluster,
			expectedServiceAnnotations: map[string]annotationExpectation{
				iksLBScopeAnnotation: {true, iksLBScopePrivate},
			},
		},
		{
			description:                   "external load balancer for Power VS platform",
			platformStatus:                platformStatus(configv1.PowerVSPlatformType),
			strategyStatus:                lbs(operatorv1.ExternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyCluster,
			expectedServiceAnnotations: map[string]annotationExpectation{
				iksLBScopeAnnotation: {true, iksLBScopePublic},
			},
		},
		{
			description:                   "internal load balancer for Power VS platform",
			platformStatus:                platformStatus(configv1.PowerVSPlatformType),
			strategyStatus:                lbs(operatorv1.InternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyCluster,
			expectedServiceAnnotations: map[string]annotationExpectation{
				iksLBScopeAnnotation: {true, iksLBScopePrivate},
			},
		},
		{
			description:                   "external load balancer for azure platform",
			platformStatus:                platformStatus(configv1.AzurePlatformType),
			strategyStatus:                lbs(operatorv1.ExternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				azureInternalLBAnnotation:   {false, ""},
				localWithFallbackAnnotation: {true, ""},
			},
		},
		{
			description:                   "internal load balancer for azure platform",
			platformStatus:                platformStatus(configv1.AzurePlatformType),
			strategyStatus:                lbs(operatorv1.InternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				azureInternalLBAnnotation:   {true, "true"},
				localWithFallbackAnnotation: {true, ""},
			},
		},
		{
			description:                   "external load balancer for gcp platform",
			platformStatus:                platformStatus(configv1.GCPPlatformType),
			strategyStatus:                lbs(operatorv1.ExternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				GCPGlobalAccessAnnotation:   {false, ""},
				gcpLBTypeAnnotation:         {false, ""},
				localWithFallbackAnnotation: {true, ""},
			},
		},
		{
			description:                   "internal load balancer for gcp platform",
			platformStatus:                platformStatus(configv1.GCPPlatformType),
			strategyStatus:                lbs(operatorv1.InternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				GCPGlobalAccessAnnotation:   {false, ""},
				gcpLBTypeAnnotation:         {true, "Internal"},
				localWithFallbackAnnotation: {true, ""},
			},
		},
		{
			description:                   "internal load balancer for gcp platform with global ClientAccess",
			platformStatus:                platformStatus(configv1.GCPPlatformType),
			strategyStatus:                gcpLB(operatorv1.InternalLoadBalancer, operatorv1.GCPGlobalAccess),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				GCPGlobalAccessAnnotation:   {true, "true"},
				gcpLBTypeAnnotation:         {true, "Internal"},
				localWithFallbackAnnotation: {true, ""},
			},
		},
		{
			description:                   "internal load balancer for gcp platform with local ClientAccess",
			platformStatus:                platformStatus(configv1.GCPPlatformType),
			strategyStatus:                gcpLB(operatorv1.InternalLoadBalancer, operatorv1.GCPLocalAccess),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				GCPGlobalAccessAnnotation:   {true, "false"},
				gcpLBTypeAnnotation:         {true, "Internal"},
				localWithFallbackAnnotation: {true, ""},
			},
		},
		{
			description:                   "external load balancer for openstack platform",
			platformStatus:                platformStatus(configv1.OpenStackPlatformType),
			strategyStatus:                lbs(operatorv1.ExternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				openstackInternalLBAnnotation: {false, ""},
				localWithFallbackAnnotation:   {true, ""},
			},
		},
		{
			description:                   "internal load balancer for openstack platform",
			platformStatus:                platformStatus(configv1.OpenStackPlatformType),
			strategyStatus:                lbs(operatorv1.InternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				openstackInternalLBAnnotation: {true, "true"},
				localWithFallbackAnnotation:   {true, ""},
			},
		},
		{
			description:                   "external load balancer for alibaba platform",
			platformStatus:                platformStatus(configv1.AlibabaCloudPlatformType),
			strategyStatus:                lbs(operatorv1.ExternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				alibabaCloudLBAddressTypeAnnotation: {true, alibabaCloudLBAddressTypeInternet},
				localWithFallbackAnnotation:         {true, ""},
			},
		},
		{
			description:                   "internal load balancer for alibaba platform",
			platformStatus:                platformStatus(configv1.AlibabaCloudPlatformType),
			strategyStatus:                lbs(operatorv1.InternalLoadBalancer),
			expectService:                 true,
			expectedExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			expectedServiceAnnotations: map[string]annotationExpectation{
				alibabaCloudLBAddressTypeAnnotation: {true, alibabaCloudLBAddressTypeIntranet},
				localWithFallbackAnnotation:         {true, ""},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ic := &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
				},
				Spec: operatorv1.IngressControllerSpec{
					EndpointPublishingStrategy: tc.strategySpec,
				},
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: tc.strategyStatus,
				},
			}
			trueVar := true
			deploymentRef := metav1.OwnerReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "router-default",
				UID:        "1",
				Controller: &trueVar,
			}
			infraConfig := &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					PlatformStatus: tc.platformStatus,
				},
			}

			proxyNeeded, err := IsProxyProtocolNeeded(ic, infraConfig.Status.PlatformStatus)
			switch {
			case err != nil:
				t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ic.Namespace, ic.Name, err)
			case tc.proxyNeeded && !proxyNeeded || !tc.proxyNeeded && proxyNeeded:
				t.Errorf("expected IsProxyProtocolNeeded to return %v, got %v", tc.proxyNeeded, proxyNeeded)
			}

			haveSvc, svc, err := desiredLoadBalancerService(ic, deploymentRef, infraConfig.Status.PlatformStatus, tc.subnetsAWSFeatureEnabled, tc.eipAllocationsAWSFeatureEnabled)
			switch {
			case err != nil:
				t.Error(err)
			case tc.expectService && !haveSvc:
				t.Error("expected desiredLoadBalancerService to return a service, got none")
			case !tc.expectService && haveSvc:
				t.Errorf("expected desiredLoadBalancerService to return nil, got %#v", svc)
			}

			if !tc.expectService || !haveSvc {
				return
			}

			for k, e := range tc.expectedServiceAnnotations {
				expectedValue, expect := e.value, e.present
				actualValue, present := svc.Annotations[k]
				switch {
				case !expect && present:
					t.Errorf("service has unexpected annotation: %s=%s", k, actualValue)
				case expect && !present:
					t.Errorf("service is missing annotation %s=%s", k, expectedValue)
				case expect && expectedValue != actualValue:
					t.Errorf("service has unexpected annotation: expected %[1]s=%[2]s, got %[1]s=%[3]s", k, expectedValue, actualValue)
				}
			}
			for k, v := range svc.Annotations {
				if e, ok := tc.expectedServiceAnnotations[k]; !ok || !e.present {
					t.Errorf("service has unexpected annotation: %s=%s", k, v)
				}
			}
			assert.Equal(t, "LoadBalancer", string(svc.Spec.Type))
			assert.Equal(t, tc.expectedExternalTrafficPolicy, svc.Spec.ExternalTrafficPolicy)
			assert.Equal(t, "Cluster", string(*svc.Spec.InternalTrafficPolicy))
			assert.Equal(t, []corev1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       int32(80),
				TargetPort: intstr.FromString("http"),
			}, {
				Name:       "https",
				Protocol:   "TCP",
				Port:       int32(443),
				TargetPort: intstr.FromString("https"),
			}}, svc.Spec.Ports)
			assert.Equal(t, "None", string(svc.Spec.SessionAffinity))
		})
	}
}

// TestDesiredLoadBalancerServiceAWSIdleTimeout verifies that
// desiredLoadBalancerService sets the expected annotation iff
// spec.endpointPublishingStrategy.loadBalancer.providerParameters.aws.classicLoadBalancer.connectionIdleTimeout
// is specified.
func TestDesiredLoadBalancerServiceAWSIdleTimeout(t *testing.T) {
	testCases := []struct {
		name                    string
		loadBalancerStrategy    *operatorv1.LoadBalancerStrategy
		expectAnnotationPresent bool
		expectedAnnotationValue string
	}{
		{
			name:                    "nil loadBalancerStrategy",
			loadBalancerStrategy:    nil,
			expectAnnotationPresent: false,
		},
		{
			name: "nil providerParameters",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: nil,
			},
			expectAnnotationPresent: false,
		},
		{
			name: "nil providerParameters.aws",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS:  nil,
				},
			},
			expectAnnotationPresent: false,
		},
		{
			name: "nil providerParameters.aws.classicLoadBalancer",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type:                          operatorv1.AWSClassicLoadBalancer,
						ClassicLoadBalancerParameters: nil,
					},
				},
			},
			expectAnnotationPresent: false,
		},
		{
			name: "nil providerParameters.aws.classicLoadBalancerParameters.connectionIdleTimeout",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type:                          operatorv1.AWSClassicLoadBalancer,
						ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{},
					},
				},
			},
			expectAnnotationPresent: false,
		},
		{
			name: "5 seconds",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSClassicLoadBalancer,
						ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{
							ConnectionIdleTimeout: metav1.Duration{Duration: 5 * time.Second},
						},
					},
				},
			},
			expectAnnotationPresent: true,
			expectedAnnotationValue: "5",
		},
		{
			name: "1 hour",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSClassicLoadBalancer,
						ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{
							ConnectionIdleTimeout: metav1.Duration{Duration: time.Hour},
						},
					},
				},
			},
			expectAnnotationPresent: true,
			expectedAnnotationValue: "3600",
		},
		{
			name: "negative 1 second",
			loadBalancerStrategy: &operatorv1.LoadBalancerStrategy{
				ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
					Type: operatorv1.AWSLoadBalancerProvider,
					AWS: &operatorv1.AWSLoadBalancerParameters{
						Type: operatorv1.AWSClassicLoadBalancer,
						ClassicLoadBalancerParameters: &operatorv1.AWSClassicLoadBalancerParameters{
							ConnectionIdleTimeout: metav1.Duration{Duration: -1 * time.Second},
						},
					},
				},
			},
			expectAnnotationPresent: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ic := &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type:         operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: tc.loadBalancerStrategy,
					},
				},
			}
			trueVar := true
			deploymentRef := metav1.OwnerReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "router-default",
				UID:        "1",
				Controller: &trueVar,
			}
			infraConfig := &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			}
			haveSvc, svc, err := desiredLoadBalancerService(ic, deploymentRef, infraConfig.Status.PlatformStatus, true, true)
			if err != nil {
				t.Fatal(err)
			}
			if !haveSvc {
				t.Fatal("desiredLoadBalancerService didn't return a service")
			}
			const key = "service.beta.kubernetes.io/aws-load-balancer-connection-idle-timeout"
			switch v, ok := svc.Annotations[key]; {
			case !tc.expectAnnotationPresent && ok:
				t.Errorf("unexpected annotation: %s=%s", key, v)
			case tc.expectAnnotationPresent && !ok:
				t.Errorf("missing expected annotation: %s=%s", key, tc.expectedAnnotationValue)
			case tc.expectAnnotationPresent && v != tc.expectedAnnotationValue:
				t.Errorf("expected annotations %s=%s, found %s=%s", key, tc.expectedAnnotationValue, key, v)
			}
		})

	}
}

// Test_shouldUseLocalWithFallback verifies that shouldUseLocalWithFallback
// behaves as expected.
func Test_shouldUseLocalWithFallback(t *testing.T) {
	testCases := []struct {
		description string
		local       bool
		override    string
		expect      bool
		expectError bool
	}{
		{
			description: "if using Cluster without an override",
			local:       false,
			expect:      false,
		},
		{
			description: "if using Local without an override",
			local:       true,
			expect:      true,
		},
		{
			description: "if using Local with an override",
			local:       true,
			override:    `{"localWithFallback":"false"}`,
			expect:      false,
		},
		{
			description: "if using Local with a garbage override",
			local:       true,
			override:    `{"localWithFallback":"x"}`,
			expectError: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			var override []byte
			if len(tc.override) != 0 {
				override = []byte(tc.override)
			}
			ic := &operatorv1.IngressController{
				Spec: operatorv1.IngressControllerSpec{
					UnsupportedConfigOverrides: runtime.RawExtension{
						Raw: override,
					},
				},
			}
			policy := corev1.ServiceExternalTrafficPolicyTypeCluster
			if tc.local {
				policy = corev1.ServiceExternalTrafficPolicyTypeLocal
			}
			service := corev1.Service{
				Spec: corev1.ServiceSpec{
					ExternalTrafficPolicy: policy,
				},
			}
			actual, err := shouldUseLocalWithFallback(ic, &service)
			switch {
			case !tc.expectError && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.expectError && err == nil:
				t.Error("expected error, got nil")
			case tc.expect != actual:
				t.Errorf("expected %t, got %t", tc.expect, actual)
			}
		})
	}
}

func Test_loadBalancerServiceChanged(t *testing.T) {
	testCases := []struct {
		description string
		mutate      func(*corev1.Service)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *corev1.Service) {},
			expect:      false,
		},
		{
			description: "if .uid changes",
			mutate: func(svc *corev1.Service) {
				svc.UID = "2"
			},
			expect: false,
		},
		{
			description: "if .spec.clusterIP changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.ClusterIP = "2.3.4.5"
			},
			expect: false,
		},
		{
			description: "if .spec.externalIPs changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.ExternalIPs = []string{"3.4.5.6"}
			},
			expect: false,
		},
		{
			description: "if .spec.externalTrafficPolicy changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyTypeCluster
			},
			expect: false,
		},
		{
			description: "if the local-with-fallback annotation is added",
			mutate: func(svc *corev1.Service) {
				svc.Annotations[localWithFallbackAnnotation] = ""
			},
			expect: true,
		},
		{
			description: "if .spec.healthCheckNodePort changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.HealthCheckNodePort = int32(34566)
			},
			expect: false,
		},
		{
			description: "if .spec.ports changes",
			mutate: func(svc *corev1.Service) {
				newPort := corev1.ServicePort{
					Name:       "foo",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(8080),
					TargetPort: intstr.FromString("foo"),
				}
				svc.Spec.Ports = append(svc.Spec.Ports, newPort)
			},
			expect: false,
		},
		{
			description: "if .spec.ports[*].nodePort changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.Ports[0].NodePort = int32(33337)
				svc.Spec.Ports[1].NodePort = int32(33338)
			},
			expect: false,
		},
		{
			description: "if .spec.selector changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.Selector = nil
			},
			expect: false,
		},
		{
			description: "if .spec.sessionAffinity is defaulted",
			mutate: func(service *corev1.Service) {
				service.Spec.SessionAffinity = corev1.ServiceAffinityNone
			},
			expect: false,
		},
		{
			description: "if .spec.sessionAffinity is set to a non-default value",
			mutate: func(service *corev1.Service) {
				service.Spec.SessionAffinity = corev1.ServiceAffinityClientIP
			},
			expect: false,
		},
		{
			description: "if .spec.type changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.Type = corev1.ServiceTypeNodePort
			},
			expect: false,
		},
		{
			description: "if .spec.loadBalancerSourceRanges changes",
			mutate: func(svc *corev1.Service) {
				svc.Spec.LoadBalancerSourceRanges = []string{"10.0.0.0/8"}
			},
			expect: true,
		},
		{
			description: "if the service.beta.kubernetes.io/load-balancer-source-ranges annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/load-balancer-source-ranges"] = "10.0.0.0/8"
			},
			expect: false,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval"] = "10"
			},
			expect: true,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval annotation is deleted",
			mutate: func(svc *corev1.Service) {
				delete(svc.Annotations, "service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval")
			},
			expect: true,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags"] = "Key3=Value3,Key4=Value4"
			},
			expect: false,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-connection-idle-timeout annotation changes",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-connection-idle-timeout"] = "120"
			},
			expect: true,
		},
		{
			description: "if the service.kubernetes.io/ibm-load-balancer-cloud-provider-enable-features annotation is added",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.kubernetes.io/ibm-load-balancer-cloud-provider-enable-features"] = "proxy-protocol"
			},
			expect: true,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-subnets annotation added",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-subnets"] = "foo-subnet"
			},
			expect: false,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-subnets annotation AND service.beta.kubernetes.io/aws-load-balancer-type are added",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-subnets"] = "foo-subnet"
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-type"] = "NLB"
			},
			expect: true,
		},
		{
			description: "if the service.beta.kubernetes.io/aws-load-balancer-eip-allocations annotation added",
			mutate: func(svc *corev1.Service) {
				svc.Annotations["service.beta.kubernetes.io/aws-load-balancer-eip-allocations"] = "eipalloc-xxxxxxxxxxxxxxxxx,eipalloc-yyyyyyyyyyyyyyyyy"
			},
			expect: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			original := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"service.beta.kubernetes.io/aws-load-balancer-connection-idle-timeout":  "10",
						"service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval":     "5",
						"service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags": "Key1=Value1,Key2=Value2",
					},
					Namespace: "openshift-ingress",
					Name:      "router-original",
					UID:       "1",
				},
				Spec: corev1.ServiceSpec{
					ClusterIP:             "1.2.3.4",
					ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyTypeLocal,
					HealthCheckNodePort:   int32(33333),
					Ports: []corev1.ServicePort{
						{
							Name:       "http",
							NodePort:   int32(33334),
							Port:       int32(80),
							Protocol:   corev1.ProtocolTCP,
							TargetPort: intstr.FromString("http"),
						},
						{
							Name:       "https",
							NodePort:   int32(33335),
							Port:       int32(443),
							Protocol:   corev1.ProtocolTCP,
							TargetPort: intstr.FromString("https"),
						},
					},
					Selector: map[string]string{
						"foo": "bar",
					},
					Type: corev1.ServiceTypeLoadBalancer,
				},
			}
			mutated := original.DeepCopy()
			tc.mutate(mutated)
			if changed, updated := loadBalancerServiceChanged(&original, mutated); changed != tc.expect {
				t.Errorf("expected loadBalancerServiceChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				if updatedChanged, _ := loadBalancerServiceChanged(&original, updated); !updatedChanged {
					t.Error("loadBalancerServiceChanged reported changes but did not make any update")
				}
				if changedAgain, _ := loadBalancerServiceChanged(mutated, updated); changedAgain {
					t.Error("loadBalancerServiceChanged does not behave as a fixed point function")
				}
			}
		})
	}
}

// Test_loadBalancerServiceAnnotationsChanged verifies that
// loadBalancerServiceAnnotationsChanged behaves correctly.
func Test_loadBalancerServiceAnnotationsChanged(t *testing.T) {
	testCases := []struct {
		description              string
		mutate                   func(*corev1.Service)
		currentAnnotations       map[string]string
		expectedAnnotations      map[string]string
		managedAnnotations       []string
		expect                   bool
		expectChangedAnnotations []string
	}{
		{
			description:         "if current and expected annotations are both empty",
			currentAnnotations:  map[string]string{},
			expectedAnnotations: map[string]string{},
			managedAnnotations:  []string{"foo"},
			expect:              false,
		},
		{
			description:         "if current annotations is nil and expected annotations is empty",
			currentAnnotations:  nil,
			expectedAnnotations: map[string]string{},
			managedAnnotations:  []string{"foo"},
			expect:              false,
		},
		{
			description:         "if current annotations is empty and expected annotations is nil",
			currentAnnotations:  map[string]string{},
			expectedAnnotations: nil,
			managedAnnotations:  []string{"foo"},
			expect:              false,
		},
		{
			description: "if an unmanaged annotation is updated",
			currentAnnotations: map[string]string{
				"foo": "bar",
			},
			expectedAnnotations: map[string]string{
				"foo": "bar",
				"baz": "quux",
			},
			managedAnnotations: []string{"foo"},
			expect:             false,
		},
		{
			description:        "if a managed annotation is set",
			currentAnnotations: map[string]string{},
			expectedAnnotations: map[string]string{
				"foo": "bar",
			},
			managedAnnotations:       []string{"foo"},
			expect:                   true,
			expectChangedAnnotations: []string{"foo"},
		},
		{
			description: "if a managed annotation is updated",
			currentAnnotations: map[string]string{
				"foo": "bar",
			},
			expectedAnnotations: map[string]string{
				"foo": "baz",
			},
			managedAnnotations:       []string{"foo"},
			expect:                   true,
			expectChangedAnnotations: []string{"foo"},
		},
		{
			description: "if a managed annotation is deleted",
			currentAnnotations: map[string]string{
				"foo": "bar",
			},
			expectedAnnotations:      map[string]string{},
			managedAnnotations:       []string{"foo"},
			expect:                   true,
			expectChangedAnnotations: []string{"foo"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			current := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tc.currentAnnotations,
				},
			}
			expected := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tc.expectedAnnotations,
				},
			}
			if changed, changedAnnotations, updated := loadBalancerServiceAnnotationsChanged(&current, &expected, tc.managedAnnotations); changed != tc.expect {
				t.Errorf("expected loadBalancerServiceAnnotationsChanged to be %t, got %t", tc.expect, changed)
			} else if changed {
				assert.Equal(t, tc.expectChangedAnnotations, changedAnnotations)
				if updatedChanged, _, _ := loadBalancerServiceAnnotationsChanged(&current, updated, tc.managedAnnotations); !updatedChanged {
					t.Error("loadBalancerServiceAnnotationsChanged reported changes but did not make any update")
				}
				if changedAgain, _, _ := loadBalancerServiceAnnotationsChanged(&expected, updated, tc.managedAnnotations); changedAgain {
					t.Error("loadBalancerServiceAnnotationsChanged does not behave as a fixed point function")
				}
			}
		})
	}
}

func Test_isServiceOwnedByIngressController(t *testing.T) {
	testCases := []struct {
		description string
		service     *corev1.Service
		ingressName string
		expect      bool
	}{
		{
			description: "if owner is set correctly",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						manifests.OwningIngressControllerLabel: "foo",
					},
				},
			},
			ingressName: "foo",
			expect:      true,
		},
		{
			description: "if owner is not set correctly",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						manifests.OwningIngressControllerLabel: "foo",
					},
				},
			},
			ingressName: "bar",
			expect:      false,
		},
		{
			description: "if owner label is not set at all",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{},
			},
			ingressName: "bar",
			expect:      false,
		},
		{
			description: "if service is nil",
			service:     nil,
			ingressName: "bar",
			expect:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ic := &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Name: tc.ingressName,
				},
				Spec:   operatorv1.IngressControllerSpec{},
				Status: operatorv1.IngressControllerStatus{},
			}

			if actual := isServiceOwnedByIngressController(tc.service, ic); actual != tc.expect {
				t.Errorf("expected ownership %t got %t", tc.expect, actual)
			}
		})
	}

}

func TestUpdateLoadBalancerServiceSourceRanges(t *testing.T) {
	// Test all cases in the table presented in <https://github.com/openshift/enhancements/pull/1177>.
	testCases := []struct {
		name                             string
		allowedSourceRanges              []operatorv1.CIDR
		currentAnnotation                string
		currentLoadBalancerSourceRanges  []string
		expectedLoadBalancerSourceRanges []string
		expectAnnotationToBeCleared      bool
		expectChanged                    bool
	}{
		{
			name:                             "only loadBalancerSourceRanges is set",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"foo"},
			expectChanged:                    false,
		},
		{
			name:                             "loadBalancerSourceRanges is different from allowedSourceRanges and annotation is not set",
			allowedSourceRanges:              []operatorv1.CIDR{"bar"},
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"bar"},
			expectChanged:                    true,
		},
		{
			name:                             "only allowedSourceRanges is set",
			allowedSourceRanges:              []operatorv1.CIDR{"bar"},
			expectedLoadBalancerSourceRanges: []string{"bar"},
			expectChanged:                    true,
		},
		{
			name:                             "annotation and loadBalancerSourceRanges are set and allowedSourceRanges is not set",
			currentAnnotation:                "cow",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"foo"},
			expectAnnotationToBeCleared:      false,
			expectChanged:                    false,
		},
		{
			name:                             "annotation, loadBalancerSourceRanges, and allowedSourceRanges are set",
			allowedSourceRanges:              []operatorv1.CIDR{"bar"},
			currentAnnotation:                "cow",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"bar"},
			expectAnnotationToBeCleared:      true,
			expectChanged:                    true,
		},
		{
			name:                             "annotation and loadBalancerSourceRanges are set and identical, allowedSourceRanges is not set",
			currentAnnotation:                "foo",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"foo"},
			expectAnnotationToBeCleared:      false,
			expectChanged:                    false,
		},
		{
			name:                             "annotation and loadBalancerSourceRanges are set and identical, allowedSourceRanges is also set",
			allowedSourceRanges:              []operatorv1.CIDR{"bar"},
			currentAnnotation:                "foo",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"bar"},
			expectAnnotationToBeCleared:      true,
			expectChanged:                    true,
		},
		{
			name:                        "only annotation is set",
			currentAnnotation:           "foo",
			expectAnnotationToBeCleared: false,
			expectChanged:               false,
		},
		{
			name:                             "annotation and allowedSourceRanges are set, loadBalancerSourceRanges is also set",
			allowedSourceRanges:              []operatorv1.CIDR{"bar"},
			currentAnnotation:                "foo",
			expectedLoadBalancerSourceRanges: []string{"bar"},
			expectAnnotationToBeCleared:      true,
			expectChanged:                    true,
		},
		{
			name:                             "allowedSourceRanges and loadBalancerSourceRanges are set and identical, annotation is also set",
			allowedSourceRanges:              []operatorv1.CIDR{"foo"},
			currentAnnotation:                "cow",
			currentLoadBalancerSourceRanges:  []string{"foo"},
			expectedLoadBalancerSourceRanges: []string{"foo"},
			expectAnnotationToBeCleared:      true,
			expectChanged:                    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ic := &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Spec: operatorv1.IngressControllerSpec{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						LoadBalancer: &operatorv1.LoadBalancerStrategy{
							AllowedSourceRanges: tc.allowedSourceRanges,
						},
					},
				},
				Status: operatorv1.IngressControllerStatus{
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type:         operatorv1.LoadBalancerServiceStrategyType,
						LoadBalancer: &operatorv1.LoadBalancerStrategy{},
					},
				},
			}
			trueVar := true
			deploymentRef := metav1.OwnerReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "router-default",
				UID:        "1",
				Controller: &trueVar,
			}
			infraConfig := &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			}
			wantSvc, desired, err := desiredLoadBalancerService(ic, deploymentRef, infraConfig.Status.PlatformStatus, true, true)
			if err != nil {
				t.Fatal(err)
			}
			if !wantSvc {
				t.Fatal("desiredLoadBalancerService didn't return a service")
			}

			current := desired.DeepCopy()
			if len(tc.currentAnnotation) > 0 {
				current.Annotations[corev1.AnnotationLoadBalancerSourceRangesKey] = tc.currentAnnotation
			}
			current.Spec.LoadBalancerSourceRanges = tc.currentLoadBalancerSourceRanges

			changed, svc := loadBalancerServiceChanged(current, desired)
			if changed != tc.expectChanged {
				t.Errorf("expected changed to be %t, got %t", tc.expectChanged, changed)
			}

			if changed {
				if actual := svc.Spec.LoadBalancerSourceRanges; !reflect.DeepEqual(actual, tc.expectedLoadBalancerSourceRanges) {
					t.Errorf("expected LoadBalancerSourceRanges %v, got %v", tc.expectedLoadBalancerSourceRanges, actual)
				}

				if len(tc.currentAnnotation) > 0 {
					if a, exists := svc.Annotations[corev1.AnnotationLoadBalancerSourceRangesKey]; (!exists || len(a) == 0) && !tc.expectAnnotationToBeCleared {
						t.Error("expected service.beta.kubernetes.io/load-balancer-source-ranges annotation not to be cleared")
					} else if exists && len(a) > 0 && tc.expectAnnotationToBeCleared {
						t.Error("expected service.beta.kubernetes.io/load-balancer-source-ranges annotation to be cleared")
					}
				}
			}
		})
	}
}

// TestLoadBalancerServiceChangedEmptyAnnotations verifies that a service with null
// .metadata.annotations and a service with empty .metadata.annotations are
// considered equal.
func TestLoadBalancerServiceChangedEmptyAnnotations(t *testing.T) {
	svc1 := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: nil,
		},
	}
	svc2 := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{},
		},
	}
	testCases := []struct {
		description      string
		current, desired *corev1.Service
	}{
		{"null to empty", &svc1, &svc2},
		{"empty to null", &svc2, &svc1},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			changed, _ := loadBalancerServiceChanged(tc.current, tc.desired)
			assert.False(t, changed)
		})
	}
}
