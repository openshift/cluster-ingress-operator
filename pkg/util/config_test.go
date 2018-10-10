package util

import (
	"testing"
)

func TestIngressDomain(t *testing.T) {
	for _, tc := range ConfigTestScenarios() {
		domain, err := IngressDomain(tc.ConfigMap)
		t.Logf("test case: %s", tc.Name)
		if tc.ErrorExpectation {
			t.Logf("    expected error: %v", err)
			if err == nil {
				t.Errorf("test case %s expected an error, got none", tc.Name)
			}
		} else {
			t.Logf("    ingress domain: %s", domain)
			if err != nil {
				t.Errorf("test case %s did not expect an error, got %v", tc.Name, err)
			}
		}
	}
}
