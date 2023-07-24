package k8s

import (
	"testing"
)

// Test_URI verifies that URI correctly validates uri as being a valid http(s)
// uri and returns the uri scheme.
func Test_URI(t *testing.T) {
	testCases := []struct {
		description string
		uri, scheme string
		expected    bool
	}{
		{
			description: "valid http uri with IP host and no port",
			uri:         "http://1.2.3.4",
			scheme:      "http",
			expected:    true,
		},
		{
			description: "valid http uri with IP host and backslash with no port",
			uri:         "http://1.2.3.4/",
			scheme:      "http",
			expected:    true,
		},
		{
			description: "valid http uri with IP host and port",
			uri:         "http://1.2.3.4:80",
			scheme:      "http",
			expected:    true,
		},
		{
			description: "valid http uri with IP host, port and backslash",
			uri:         "http://1.2.3.4:80/",
			scheme:      "http",
			expected:    true,
		},
		{
			description: "valid http uri with hostname",
			uri:         "http://redhat",
			scheme:      "http",
			expected:    true,
		},
		{
			description: "valid http uri with underscore in hostname",
			uri:         "http://red_hat.com",
			scheme:      "http",
			expected:    true,
		},
		{
			description: "valid http uri with FQDN",
			uri:         "http://www.redhat.com",
			scheme:      "http",
			expected:    true,
		},
		{
			description: "valid http uri with capitalized FQDN",
			uri:         "http://WWW.REDHAT.COM",
			scheme:      "http",
			expected:    true,
		},
		{
			description: "valid https uri with IP host and no port",
			uri:         "https://1.2.3.4",
			scheme:      "https",
			expected:    true,
		},
		{
			description: "valid https uri with mixed capitalization, port and bckslash",
			uri:         "https://EXAMPLe.com:8080/",
			scheme:      "https",
			expected:    true,
		},
		{
			description: "http uri with invalid port number",
			uri:         "http://1.2.3.4:8080808080",
			scheme:      "http",
			expected:    false,
		},
		{
			description: "http uri with port number higher that the accepted range",
			uri:         "http://5.6.7.8:65536",
			scheme:      "http",
			expected:    false,
		},
		{
			description: "http uri with port number lower that the accepted range",
			uri:         "http://5.6.7.8:0",
			scheme:      "http",
			expected:    false,
		},
		{
			description: "missing uri scheme",
			uri:         "redhat.com",
			expected:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			scheme, err := URI(tc.uri)
			switch {
			case err != nil && tc.expected:
				t.Errorf("unexpected error: %v", err)
			case err == nil && !tc.expected:
				t.Error("expected error")
			case tc.expected && scheme != tc.scheme:
				t.Errorf("expected scheme %q, got %q", tc.scheme, scheme)
			}
		})
	}
}
