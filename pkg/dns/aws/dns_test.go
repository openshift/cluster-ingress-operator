package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/stretchr/testify/assert"

	"github.com/aws/aws-sdk-go/service/route53"
	configv1 "github.com/openshift/api/config/v1"
)

func Test_zoneMatchesTags(t *testing.T) {
	cases := []struct {
		name        string
		tagsForZone map[string]string
		expected    bool
	}{
		{
			name:     "no tags for zone",
			expected: false,
		},
		{
			name: "matches exactly",
			tagsForZone: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
			expected: true,
		},
		{
			name: "matches with extra",
			tagsForZone: map[string]string{
				"key0": "value0",
				"key1": "value1",
				"key2": "value2",
			},
			expected: true,
		},
		{
			name: "missing first key",
			tagsForZone: map[string]string{
				"key2": "value2",
			},
			expected: false,
		},
		{
			name: "missing second key",
			tagsForZone: map[string]string{
				"key1": "value1",
			},
			expected: false,
		},
		{
			name: "mismatched first value",
			tagsForZone: map[string]string{
				"key1": "other",
				"key2": "value2",
			},
			expected: false,
		},
		{
			name: "mismatched second value",
			tagsForZone: map[string]string{
				"key1": "value1",
				"key2": "other",
			},
			expected: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tagsForZone []*route53.Tag
			for k, v := range tc.tagsForZone {
				tag := &route53.Tag{
					Key:   aws.String(k),
					Value: aws.String(v),
				}
				tagsForZone = append(tagsForZone, tag)
			}
			zoneConfig := configv1.DNSZone{
				Tags: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
			}
			actual := zoneMatchesTags(tagsForZone, zoneConfig)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func Test_zoneIDFromResource(t *testing.T) {
	cases := []struct {
		resource       string
		expectedZoneID string
		expectError    bool
	}{
		{
			resource:       "/hostedzone/test-zone-id",
			expectedZoneID: "test-zone-id",
		},
		{
			resource:       "hostedzone/test-zone-id",
			expectedZoneID: "test-zone-id",
		},
		{
			resource:    "/other-type/test-zone-id",
			expectError: true,
		},
		{
			resource:    "/hostedzone/",
			expectError: true,
		},
		{
			resource:    "no-slash",
			expectError: true,
		},
		{
			resource:    "hostedzone/test-zone-id/extra-slash",
			expectError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.resource, func(t *testing.T) {
			actualZoneID, err := zoneIDFromResource(tc.resource)
			if tc.expectError {
				assert.Error(t, err, "expected error")
			} else {
				assert.NoError(t, err, "unexpected error")
				assert.Equal(t, tc.expectedZoneID, actualZoneID, "unexpected zone ID")
			}
		})
	}
}

// Test_NewProvider verifies that NewProvider creates clients with the expected
// service endpoints.
func Test_NewProvider(t *testing.T) {
	cases := []struct {
		name                                 string
		config                               Config
		expectedTaggingServiceEndpoint       string
		expectedElbServiceEndpointEndpoint   string
		expectedElbv2ServiceEndpointEndpoint string
		expectedRoute53ServiceEndpoint       string
	}{{
		name: "default service endpoints, us-east-1",
		config: Config{
			Region: "us-east-1",
		},
		expectedTaggingServiceEndpoint:       "https://tagging.us-east-1.amazonaws.com",
		expectedElbServiceEndpointEndpoint:   "https://elasticloadbalancing.us-east-1.amazonaws.com",
		expectedElbv2ServiceEndpointEndpoint: "https://elasticloadbalancing.us-east-1.amazonaws.com",
		expectedRoute53ServiceEndpoint:       "https://route53.amazonaws.com",
	}, {
		name: "custom service endpoints",
		config: Config{
			Region: "us-east-1",
			ServiceEndpoints: []ServiceEndpoint{
				// Service endpoints with unrecognized names
				// are ignored.
				{Name: "bogus", URL: "http://ignored"},
				// When the same service endpoint is specified
				// more than once, the last one is used, until
				// all service endpoints have been set.
				{Name: "tagging", URL: "http://overridden1"},
				{Name: "tagging", URL: "http://overridden2"},
				{Name: "tagging", URL: "http://x"},
				{Name: "elasticloadbalancing", URL: "http://y"},
				{Name: "route53", URL: "http://z"},
				// Once all service endpoints have been set,
				// any further entries are ignored.
				{Name: "tagging", URL: "http://ignored"},
				{Name: "elasticloadbalancing", URL: "http://ignored"},
				{Name: "route53", URL: "http://ignored"},
			},
		},
		expectedTaggingServiceEndpoint:       "http://x",
		expectedElbServiceEndpointEndpoint:   "http://y",
		expectedElbv2ServiceEndpointEndpoint: "http://y",
		expectedRoute53ServiceEndpoint:       "http://z",
	}, {
		name: "default service endpoints, GovCloud East",
		config: Config{
			Region: "us-gov-east-1",
		},
		expectedTaggingServiceEndpoint:       "https://tagging.us-gov-west-1.amazonaws.com",
		expectedElbServiceEndpointEndpoint:   "https://elasticloadbalancing.us-gov-east-1.amazonaws.com",
		expectedElbv2ServiceEndpointEndpoint: "https://elasticloadbalancing.us-gov-east-1.amazonaws.com",
		expectedRoute53ServiceEndpoint:       "https://route53.us-gov.amazonaws.com",
	}, {
		name: "default service endpoints, C2S",
		config: Config{
			Region: "us-iso-east-1",
		},
		expectedTaggingServiceEndpoint:       "",
		expectedElbServiceEndpointEndpoint:   "https://elasticloadbalancing.us-iso-east-1.c2s.ic.gov",
		expectedElbv2ServiceEndpointEndpoint: "https://elasticloadbalancing.us-iso-east-1.c2s.ic.gov",
		expectedRoute53ServiceEndpoint:       "https://route53.c2s.ic.gov",
	}, {
		name: "default service endpoints, SC2S",
		config: Config{
			Region: "us-isob-east-1",
		},
		expectedTaggingServiceEndpoint:       "",
		expectedElbServiceEndpointEndpoint:   "https://elasticloadbalancing.us-isob-east-1.sc2s.sgov.gov",
		expectedElbv2ServiceEndpointEndpoint: "https://elasticloadbalancing.us-isob-east-1.sc2s.sgov.gov",
		expectedRoute53ServiceEndpoint:       "https://route53.sc2s.sgov.gov",
	}, {
		name: "default service endpoints, China North",
		config: Config{
			Region: "cn-north-1",
		},
		expectedTaggingServiceEndpoint:       "https://tagging.cn-northwest-1.amazonaws.com.cn",
		expectedElbServiceEndpointEndpoint:   "https://elasticloadbalancing.cn-north-1.amazonaws.com.cn",
		expectedElbv2ServiceEndpointEndpoint: "https://elasticloadbalancing.cn-north-1.amazonaws.com.cn",
		expectedRoute53ServiceEndpoint:       "https://route53.amazonaws.com.cn",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validateServiceEndpointsFn = func(provider *Provider) error {
				return nil
			}

			provider, err := NewProvider(tc.config, "0.0.0-0")
			if !assert.NoError(t, err) {
				return
			}

			assert.NotNil(t, provider.elb)
			assert.Equal(t, tc.expectedElbServiceEndpointEndpoint, provider.elb.Client.Endpoint)

			assert.NotNil(t, provider.elbv2)
			assert.Equal(t, tc.expectedElbv2ServiceEndpointEndpoint, provider.elbv2.Client.Endpoint)

			assert.NotNil(t, provider.route53)
			assert.Equal(t, tc.expectedRoute53ServiceEndpoint, provider.route53.Client.Endpoint)

			if tc.expectedTaggingServiceEndpoint == "" {
				assert.Nil(t, provider.tags)
			} else {
				assert.NotNil(t, provider.tags)
				assert.Equal(t, tc.expectedTaggingServiceEndpoint, provider.tags.Client.Endpoint)
			}
		})
	}
}
