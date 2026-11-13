package dns

import "testing"

func TestNormalizeHostname(t *testing.T) {
	tests := []struct {
		description string
		name        string
		expected    string
	}{
		{description: "lowercase", name: "Example.COM", expected: "example.com"},
		{description: "root dot removed", name: "example.com.", expected: "example.com"},
		{description: "wildcard preserved", name: "*.Example.COM.", expected: "*.example.com"},
	}
	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			if actual := NormalizeHostname(test.name); actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}
