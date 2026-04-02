package gcp

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

func TestFilterIPv4Addresses(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "only IPv4",
			input:    []string{"192.168.1.1", "10.0.0.1"},
			expected: []string{"192.168.1.1", "10.0.0.1"},
		},
		{
			name:     "only IPv6",
			input:    []string{"2001:db8::1", "fe80::1"},
			expected: []string{},
		},
		{
			name:     "mixed IPv4 and IPv6",
			input:    []string{"192.168.1.1", "2001:db8::1", "10.0.0.1"},
			expected: []string{"192.168.1.1", "10.0.0.1"},
		},
		{
			name:     "empty",
			input:    []string{},
			expected: []string{},
		},
		{
			name:     "invalid IPs",
			input:    []string{"not-an-ip", "192.168.1.1"},
			expected: []string{"192.168.1.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterIPv4Addresses(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d IPv4 addresses, got %d", len(tt.expected), len(result))
				return
			}
			for i, addr := range result {
				if addr != tt.expected[i] {
					t.Errorf("expected address %s at index %d, got %s", tt.expected[i], i, addr)
				}
			}
		})
	}
}

func TestFilterIPv6Addresses(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "only IPv6",
			input:    []string{"2001:db8::1", "fe80::1"},
			expected: []string{"2001:db8::1", "fe80::1"},
		},
		{
			name:     "only IPv4",
			input:    []string{"192.168.1.1", "10.0.0.1"},
			expected: []string{},
		},
		{
			name:     "mixed IPv4 and IPv6",
			input:    []string{"192.168.1.1", "2001:db8::1", "10.0.0.1", "fe80::1"},
			expected: []string{"2001:db8::1", "fe80::1"},
		},
		{
			name:     "empty",
			input:    []string{},
			expected: []string{},
		},
		{
			name:     "invalid IPs",
			input:    []string{"not-an-ip", "2001:db8::1"},
			expected: []string{"2001:db8::1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterIPv6Addresses(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d IPv6 addresses, got %d", len(tt.expected), len(result))
				return
			}
			for i, addr := range result {
				if addr != tt.expected[i] {
					t.Errorf("expected address %s at index %d, got %s", tt.expected[i], i, addr)
				}
			}
		})
	}
}

func TestConfigIPFamily(t *testing.T) {
	tests := []struct {
		name     string
		ipFamily configv1.IPFamilyType
	}{
		{
			name:     "IPv4",
			ipFamily: configv1.IPv4,
		},
		{
			name:     "DualStackIPv4Primary",
			ipFamily: configv1.DualStackIPv4Primary,
		},
		{
			name:     "DualStackIPv6Primary",
			ipFamily: configv1.DualStackIPv6Primary,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := Config{
				Project:         "test-project",
				UserAgent:       "test-agent",
				CredentialsJSON: []byte("{}"),
				IPFamily:        tt.ipFamily,
			}
			if config.IPFamily != tt.ipFamily {
				t.Errorf("expected IPFamily %s, got %s", tt.ipFamily, config.IPFamily)
			}
		})
	}
}
