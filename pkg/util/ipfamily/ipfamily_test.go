package ipfamily

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

func Test_ForPlatform(t *testing.T) {
	tests := []struct {
		name     string
		status   *configv1.PlatformStatus
		expected configv1.IPFamilyType
	}{
		{
			name:     "nil platform status",
			status:   nil,
			expected: "",
		},
		{
			name:     "azure with no platform-specific status",
			status:   &configv1.PlatformStatus{Type: configv1.AzurePlatformType},
			expected: "",
		},
		{
			name: "azure dual-stack",
			status: &configv1.PlatformStatus{
				Type:  configv1.AzurePlatformType,
				Azure: &configv1.AzurePlatformStatus{IPFamily: configv1.DualStackIPv4Primary},
			},
			expected: configv1.DualStackIPv4Primary,
		},
		{
			name: "aws dual-stack",
			status: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
				AWS:  &configv1.AWSPlatformStatus{IPFamily: configv1.DualStackIPv6Primary},
			},
			expected: configv1.DualStackIPv6Primary,
		},
		{
			name: "azure single-stack",
			status: &configv1.PlatformStatus{
				Type:  configv1.AzurePlatformType,
				Azure: &configv1.AzurePlatformStatus{IPFamily: configv1.IPv4},
			},
			expected: configv1.IPv4,
		},
		{
			// GCP has no IPFamily field, so dual-stack handling must
			// never engage for it.
			name:     "gcp reports no ip family",
			status:   &configv1.PlatformStatus{Type: configv1.GCPPlatformType},
			expected: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := ForPlatform(test.status); actual != test.expected {
				t.Errorf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

func Test_IsDualStack(t *testing.T) {
	tests := []struct {
		ipFamily configv1.IPFamilyType
		expected bool
	}{
		{configv1.DualStackIPv4Primary, true},
		{configv1.DualStackIPv6Primary, true},
		{configv1.IPv4, false},
		{"", false},
	}

	for _, test := range tests {
		t.Run(string(test.ipFamily), func(t *testing.T) {
			if actual := IsDualStack(test.ipFamily); actual != test.expected {
				t.Errorf("IsDualStack(%q): expected %v, got %v", test.ipFamily, test.expected, actual)
			}
		})
	}
}

func Test_PrimaryIsIPv6(t *testing.T) {
	tests := []struct {
		ipFamily configv1.IPFamilyType
		expected bool
	}{
		{configv1.DualStackIPv6Primary, true},
		{configv1.DualStackIPv4Primary, false},
		{configv1.IPv4, false},
		{"", false},
	}

	for _, test := range tests {
		t.Run(string(test.ipFamily), func(t *testing.T) {
			if actual := PrimaryIsIPv6(test.ipFamily); actual != test.expected {
				t.Errorf("PrimaryIsIPv6(%q): expected %v, got %v", test.ipFamily, test.expected, actual)
			}
		})
	}
}
