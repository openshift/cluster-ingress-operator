package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/stretchr/testify/assert"

	"github.com/aws/aws-sdk-go/service/route53"
	configv1 "github.com/openshift/api/config/v1"
)

func TestZoneMatchesTags(t *testing.T) {
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

func TestZoneIDFromResource(t *testing.T) {
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
