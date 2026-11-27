package aws

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	iov1 "github.com/openshift/api/operatoringress/v1"
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
			var tagsForZone []r53types.Tag
			for k, v := range tc.tagsForZone {
				tag := r53types.Tag{
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

func Test_partitionIDForRegion(t *testing.T) {
	cases := []struct {
		region    string
		partition string
	}{
		{"us-east-1", "aws"},
		{"eu-west-1", "aws"},
		{"ap-southeast-1", "aws"},
		{"cn-north-1", "aws-cn"},
		{"cn-northwest-1", "aws-cn"},
		{"us-gov-east-1", "aws-us-gov"},
		{"us-gov-west-1", "aws-us-gov"},
		{"us-iso-east-1", "aws-iso"},
		{"us-isob-east-1", "aws-iso-b"},
		{"eusc-de-east-1", "aws"},
	}
	for _, tc := range cases {
		t.Run(tc.region, func(t *testing.T) {
			assert.Equal(t, tc.partition, partitionIDForRegion(tc.region))
		})
	}
}

func Test_isGovCloudRegion(t *testing.T) {
	cases := []struct {
		region   string
		expected bool
	}{
		{"us-gov-west-1", true},
		{"us-gov-east-1", true},
		{"us-east-1", false},
		{"cn-north-1", false},
		{"us-iso-east-1", false},
		{"eu-west-1", false},
	}
	for _, tc := range cases {
		t.Run(tc.region, func(t *testing.T) {
			assert.Equal(t, tc.expected, isGovCloudRegion(tc.region))
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
		name: "default service endpoints, EUSC Brandenburg",
		config: Config{
			Region: "eusc-de-east-1",
		},
		expectedTaggingServiceEndpoint:       "https://tagging.eusc-de-east-1.amazonaws.eu",
		expectedElbServiceEndpointEndpoint:   "https://elasticloadbalancing.eusc-de-east-1.amazonaws.eu",
		expectedElbv2ServiceEndpointEndpoint: "https://elasticloadbalancing.eusc-de-east-1.amazonaws.eu",
		expectedRoute53ServiceEndpoint:       "https://route53.amazonaws.eu",
	}, {
		name: "custom service endpoints, EUSC Brandenburg",
		config: Config{
			Region: "eusc-de-east-1",
			ServiceEndpoints: []ServiceEndpoint{
				{Name: "tagging", URL: "https://tagging.eusc-de-east-1.amazonaws.eu"},
				{Name: "elasticloadbalancing", URL: "https://elasticloadbalancing.eusc-de-east-1.amazonaws.eu"},
				{Name: "route53", URL: "https://route53.amazonaws.eu"},
			},
		},
		expectedTaggingServiceEndpoint:       "https://tagging.eusc-de-east-1.amazonaws.eu",
		expectedElbServiceEndpointEndpoint:   "https://elasticloadbalancing.eusc-de-east-1.amazonaws.eu",
		expectedElbv2ServiceEndpointEndpoint: "https://elasticloadbalancing.eusc-de-east-1.amazonaws.eu",
		expectedRoute53ServiceEndpoint:       "https://route53.amazonaws.eu",
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
		name: "default service endpoints, GovCloud West",
		config: Config{
			Region: "us-gov-west-1",
		},
		expectedTaggingServiceEndpoint:       "https://tagging.us-gov-west-1.amazonaws.com",
		expectedElbServiceEndpointEndpoint:   "https://elasticloadbalancing.us-gov-west-1.amazonaws.com",
		expectedElbv2ServiceEndpointEndpoint: "https://elasticloadbalancing.us-gov-west-1.amazonaws.com",
		expectedRoute53ServiceEndpoint:       "https://route53.us-gov.amazonaws.com",
	}, {
		name: "custom service endpoints, GovCloud East ignores custom tagging",
		config: Config{
			Region: "us-gov-east-1",
			ServiceEndpoints: []ServiceEndpoint{
				{Name: "tagging", URL: "https://tagging.us-gov-east-1.amazonaws.com"},
				{Name: "elasticloadbalancing", URL: "http://custom-elb"},
				{Name: "route53", URL: "http://custom-r53"},
			},
		},
		// Custom tagging endpoint is ignored for us-gov-east-1;
		// SDK resolves from tagRegion=us-gov-west-1.
		expectedTaggingServiceEndpoint:       "https://tagging.us-gov-west-1.amazonaws.com",
		expectedElbServiceEndpointEndpoint:   "http://custom-elb",
		expectedElbv2ServiceEndpointEndpoint: "http://custom-elb",
		expectedRoute53ServiceEndpoint:       "http://custom-r53",
	}, {
		name: "custom service endpoints, GovCloud West uses custom tagging",
		config: Config{
			Region: "us-gov-west-1",
			ServiceEndpoints: []ServiceEndpoint{
				{Name: "tagging", URL: "http://custom-tagging"},
				{Name: "elasticloadbalancing", URL: "http://custom-elb"},
				{Name: "route53", URL: "http://custom-r53"},
			},
		},
		expectedTaggingServiceEndpoint:       "http://custom-tagging",
		expectedElbServiceEndpointEndpoint:   "http://custom-elb",
		expectedElbv2ServiceEndpointEndpoint: "http://custom-elb",
		expectedRoute53ServiceEndpoint:       "http://custom-r53",
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
			assert.Equal(t, tc.expectedElbServiceEndpointEndpoint, provider.elbEndpoint)

			assert.NotNil(t, provider.elbv2)
			assert.Equal(t, tc.expectedElbv2ServiceEndpointEndpoint, provider.elbv2Endpoint)

			assert.NotNil(t, provider.route53)
			assert.Equal(t, tc.expectedRoute53ServiceEndpoint, provider.route53Endpoint)

			if tc.expectedTaggingServiceEndpoint == "" {
				assert.Nil(t, provider.tags)
			} else {
				assert.NotNil(t, provider.tags)
				assert.Equal(t, tc.expectedTaggingServiceEndpoint, provider.tagsEndpoint)
			}
		})
	}
}

func Test_change_EmptyTargets(t *testing.T) {
	record := &iov1.DNSRecord{
		Spec: iov1.DNSRecordSpec{
			RecordType: iov1.CNAMERecordType,
			DNSName:    "test.example.com",
			Targets:    []string{},
		},
	}
	p := &Provider{
		config: Config{Region: "us-east-1"},
	}
	err := p.change(record, configv1.DNSZone{ID: "zone"}, upsertAction)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no targets specified")
}

func Test_updateRecord_DualStackDeleteRetry(t *testing.T) {
	// Simulate a Route53 server that rejects the first batched delete
	// (because AAAA doesn't exist) but accepts individual A deletes.
	type changeRequest struct {
		XMLName xml.Name `xml:"ChangeResourceRecordSetsRequest"`
		Changes struct {
			Items []struct {
				Action          string `xml:"Action"`
				ResourceRecords struct {
					Name string `xml:"Name"`
					Type string `xml:"Type"`
				} `xml:"ResourceRecordSet"`
			} `xml:"Change"`
		} `xml:"ChangeBatch>Changes"`
	}
	var (
		callCount atomic.Int32
		mu        sync.Mutex
		captured  []changeRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		var req changeRequest
		if xmlErr := xml.Unmarshal(body, &req); xmlErr == nil {
			mu.Lock()
			captured = append(captured, req)
			mu.Unlock()
		}
		n := callCount.Add(1)
		if n == 1 {
			// First call: batched delete containing both A and AAAA.
			// Return an error indicating the AAAA record was not found.
			w.WriteHeader(http.StatusBadRequest)
			type errResponse struct {
				XMLName xml.Name `xml:"ErrorResponse"`
				Error   struct {
					Type    string
					Code    string
					Message string
				}
			}
			resp := errResponse{}
			resp.Error.Type = "Sender"
			resp.Error.Code = "InvalidChangeBatch"
			resp.Error.Message = "[RRSet of type AAAA with DNS name test.example.com. is not found in zone Z123.]"
			if err := xml.NewEncoder(w).Encode(resp); err != nil {
				t.Errorf("failed to encode error response: %v", err)
			}
			return
		}
		// Subsequent calls: individual record deletes succeed.
		type changeInfo struct {
			XMLName xml.Name `xml:"ChangeResourceRecordSetsResponse"`
			Info    struct {
				Id     string
				Status string
			} `xml:"ChangeInfo"`
		}
		ci := changeInfo{}
		ci.Info.Id = "/change/C1"
		ci.Info.Status = "PENDING"
		w.WriteHeader(http.StatusOK)
		if err := xml.NewEncoder(w).Encode(ci); err != nil {
			t.Errorf("failed to encode success response: %v", err)
		}
	}))
	defer server.Close()

	cfg, err := awsconfig.LoadDefaultConfig(context.TODO(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("AKID", "SECRET", "TOKEN")),
	)
	assert.NoError(t, err)

	r53client := route53.NewFromConfig(cfg, func(o *route53.Options) {
		o.BaseEndpoint = aws.String(server.URL)
	})

	p := &Provider{
		route53: r53client,
		config: Config{
			Region:   "us-east-1",
			IPFamily: configv1.DualStackIPv4Primary,
		},
	}

	// Call updateRecord with DELETE action — dual-stack should produce
	// A + AAAA, first batch fails, then individual retries succeed.
	err = p.updateRecord("test.example.com", "Z123", "elb.amazonaws.com", "Z456", string(deleteAction), 60)
	assert.NoError(t, err, "dual-stack delete should succeed via individual retries")

	// The mock should have received at least 3 calls:
	// 1 batched (failed) + 2 individual retries (A and AAAA).
	assert.GreaterOrEqual(t, int(callCount.Load()), 3,
		"expected at least 3 Route53 API calls (batch + 2 retries)")

	// Verify that individual A and AAAA delete retries were made.
	mu.Lock()
	defer mu.Unlock()
	var retryTypes []string
	for i, req := range captured {
		if i == 0 {
			continue // skip the initial batched request
		}
		for _, ch := range req.Changes.Items {
			retryTypes = append(retryTypes, ch.ResourceRecords.Type)
		}
	}
	assert.Contains(t, retryTypes, "A",
		"expected an individual A record delete retry")
	assert.Contains(t, retryTypes, "AAAA",
		"expected an individual AAAA record delete retry")
}
