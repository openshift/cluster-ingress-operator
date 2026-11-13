package ipfamily

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

func TestIsDualStack(t *testing.T) {
	tests := []struct {
		name     string
		ipFamily configv1.IPFamilyType
		expected bool
	}{
		{
			name:     "IPv4 is not dual-stack",
			ipFamily: configv1.IPv4,
			expected: false,
		},
		{
			name:     "DualStackIPv4Primary is dual-stack",
			ipFamily: configv1.DualStackIPv4Primary,
			expected: true,
		},
		{
			name:     "DualStackIPv6Primary is dual-stack",
			ipFamily: configv1.DualStackIPv6Primary,
			expected: true,
		},
		{
			name:     "Empty string is not dual-stack",
			ipFamily: "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsDualStack(tt.ipFamily)
			if result != tt.expected {
				t.Errorf("IsDualStack(%q) = %v, expected %v", tt.ipFamily, result, tt.expected)
			}
		})
	}
}
