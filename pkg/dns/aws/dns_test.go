package aws

import (
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"testing"
)

// TestURLContainsValidRegion validates uri as being a valid for the provided region.
func TestURLContainsValidRegion(t *testing.T) {
	testCases := []struct {
		description string
		uri, region string
		expected    bool
	}{
		{
			description: "regionalized standard route53 uri for region us-east-1",
			uri:         "https://route53.us-east-1.amazonaws.com",
			region:      endpoints.UsEast1RegionID,
			expected:    true,
		},
		{
			description: "non-regionalized standard route53 uri for region us-west-1",
			uri:         standardRoute53Endpoint,
			region:      endpoints.UsWest1RegionID,
			expected:    true,
		},
		{
			description: "invalid non-regionalized standard route53 uri for region us-west-2",
			uri:         "https://route53.us-west-2.amazonaws.com",
			region:      endpoints.UsWest2RegionID,
			expected:    false,
		},
		{
			description: "route53 China uri for region cn-north-1",
			uri:         chinaRoute53Endpoint,
			region:      endpoints.CnNorth1RegionID,
			expected:    true,
		},
		{
			description: "route53 China uri for region cn-northwest-1",
			uri:         chinaRoute53Endpoint,
			region:      endpoints.CnNorthwest1RegionID,
			expected:    true,
		},
		{
			description: "invalid route53 China uri for region cn-northwest-1",
			uri:         "https://route53.cn-northwest-1.amazonaws.com.cn",
			region:      endpoints.CnNorthwest1RegionID,
			expected:    false,
		},
		{
			description: "route53 GovCloud uri for region us-gov-east-1",
			uri:         govCloudRoute53Endpoint,
			region:      endpoints.UsGovEast1RegionID,
			expected:    true,
		},
		{
			description: "route53 GovCloud uri for region us-gov-west-1",
			uri:         govCloudRoute53Endpoint,
			region:      endpoints.UsGovWest1RegionID,
			expected:    true,
		},
		{
			description: "route53 GovCloud uri for region us-west-2",
			uri:         govCloudRoute53Endpoint,
			region:      endpoints.UsWest2RegionID,
			expected:    false,
		},
		{
			description: "tagging uri for region us-west-2",
			uri:         "https://tagging.us-east-1.amazonaws.com",
			region:      endpoints.UsWest2RegionID,
			expected:    true,
		},
		{
			description: "invalid standard tagging uri for region us-west-2",
			uri:         "https://tagging.us-west-2.amazonaws.com",
			region:      endpoints.UsWest2RegionID,
			expected:    false,
		},
		{
			description: "tagging GovCloud uri for region us-gov-west-1",
			uri:         govCloudTaggingEndpoint,
			region:      endpoints.UsGovWest1RegionID,
			expected:    true,
		},
		{
			description: "tagging GovCloud uri for region us-gov-east-1",
			uri:         govCloudTaggingEndpoint,
			region:      endpoints.UsGovEast1RegionID,
			expected:    true,
		},
		{
			description: "tagging China uri for region cn-north-1",
			uri:         "https://tagging.cn-northwest-1.amazonaws.com.cn",
			region:      endpoints.CnNorth1RegionID,
			expected:    true,
		},
		{
			description: "tagging China uri for region cn-northwest-1",
			uri:         "https://tagging.cn-northwest-1.amazonaws.com.cn",
			region:      endpoints.CnNorthwest1RegionID,
			expected:    true,
		},
		{
			description: "invalid tagging China uri for region cn-north-1",
			uri:         "https://tagging.cn-north-1.amazonaws.com.cn",
			region:      endpoints.CnNorth1RegionID,
			expected:    false,
		},
		{
			description: "elb China uri for region cn-north-1",
			uri:         "https://elasticloadbalancing.cn-north-1.amazonaws.com.cn",
			region:      endpoints.CnNorth1RegionID,
			expected:    true,
		},
		{
			description: "elb GovCloud uri for region us-gov-west-1",
			uri:         "https://elasticloadbalancing.us-gov-west-1.amazonaws.com",
			region:      endpoints.UsGovWest1RegionID,
			expected:    true,
		},
		{
			description: "elb standard uri for region us-west-2",
			uri:         "https://elasticloadbalancing.us-west-2.amazonaws.com",
			region:      endpoints.UsWest2RegionID,
			expected:    true,
		},
	}

	for _, tc := range testCases {
		valid := urlContainsValidRegion(tc.region, tc.uri)
		switch {
		case !valid && tc.expected:
			t.Errorf("test %s failed, invalid url %s", tc.description, tc.uri)
		case valid && !tc.expected:
			t.Errorf("test %s expected to fail, but passed for url: %s", tc.description, tc.uri)
		}
	}
}
