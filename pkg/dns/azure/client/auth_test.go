package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEndpointToScope(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		expected string
	}{
		{
			name:     "Default scope is suffixed",
			endpoint: "https://graph.microsoft.com",
			expected: "https://graph.microsoft.com/.default",
		},
		{
			name:     "No-OP for a valid scope",
			endpoint: "https://graph.microsoft.com/.default",
			expected: "https://graph.microsoft.com/.default",
		},
		{
			name:     "Existing slash is preserved",
			endpoint: "https://graph.microsoft.com/",
			expected: "https://graph.microsoft.com//.default",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope := endpointToScope(tc.endpoint)
			assert.Equal(t, scope, tc.expected)
		})
	}
}
