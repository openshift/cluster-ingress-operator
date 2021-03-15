package ingress

import (
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
)

func TestTLSProfileSpecForSecurityProfile(t *testing.T) {
	invalidTLSVersion := configv1.TLSProtocolVersion("abc")
	invalidCiphers := []string{"ECDHE-ECDSA-AES256-GCM-SHA384", "invalid cipher"}
	validCiphers := []string{"ECDHE-ECDSA-AES256-GCM-SHA384"}
	tlsVersion13Ciphers := []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "TLS_CHACHA20_POLY1305_SHA256",
		"TLS_AES_128_CCM_SHA256", "TLS_AES_128_CCM_8_SHA256", "TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384",
		"TLS_CHACHA20_POLY1305_SHA256", "TLS_AES_128_CCM_SHA256", "TLS_AES_128_CCM_8_SHA256"}
	tlsVersion1213Ciphers := []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "ECDHE-ECDSA-AES256-GCM-SHA384"}
	testCases := []struct {
		description  string
		profile      *configv1.TLSSecurityProfile
		valid        bool
		expectedSpec *configv1.TLSProfileSpec
	}{

		{
			description:  "default (nil)",
			profile:      nil,
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "default (empty)",
			profile:      &configv1.TLSSecurityProfile{},
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description: "old",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileOldType,
			},
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description: "intermediate",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileIntermediateType,
			},
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		// TODO: Update test case to use Modern cipher suites when haproxy is
		//  built with an openssl version that supports tls v1.3.
		{
			description: "modern",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileModernType,
			},
			valid:        true,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description: "custom, nil profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
			},
			valid: false,
		},
		{
			description: "custom, empty profile",
			profile: &configv1.TLSSecurityProfile{
				Type:   configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{},
			},
			valid: false,
		},
		{
			description: "custom, invalid ciphers",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers:       invalidCiphers,
						MinTLSVersion: configv1.VersionTLS10,
					},
				},
			},
			valid: false,
		},
		{
			description: "custom, invalid tls v1.3 only ciphers",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers:       tlsVersion13Ciphers,
						MinTLSVersion: configv1.VersionTLS10,
					},
				},
			},
			valid: false,
		},
		{
			description: "custom, mixed tls v1.2 and v1.3 ciphers",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers:       tlsVersion1213Ciphers,
						MinTLSVersion: configv1.VersionTLS10,
					},
				},
			},
			valid: true,
		},
		{
			description: "custom, invalid minimum security protocol version",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers:       validCiphers,
						MinTLSVersion: invalidTLSVersion,
					},
				},
			},
			valid: false,
		},
		{
			description: "custom, valid minimum security protocol version",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers:       validCiphers,
						MinTLSVersion: configv1.VersionTLS10,
					},
				},
			},
			valid: true,
			expectedSpec: &configv1.TLSProfileSpec{
				Ciphers:       validCiphers,
				MinTLSVersion: configv1.VersionTLS10,
			},
		},
	}

	for _, tc := range testCases {
		ic := &operatorv1.IngressController{
			Spec: operatorv1.IngressControllerSpec{
				TLSSecurityProfile: tc.profile,
			},
		}
		tlsProfileSpec := tlsProfileSpecForSecurityProfile(ic.Spec.TLSSecurityProfile)
		err := validateTLSSecurityProfile(ic)
		if tc.valid && err != nil {
			t.Errorf("%q: unexpected error: %v\nprofile:\n%s", tc.description, err, toYaml(tlsProfileSpec))
			continue
		}
		if !tc.valid && err == nil {
			t.Errorf("%q: expected error for profile:\n%s", tc.description, toYaml(tlsProfileSpec))
			continue
		}
		if tc.expectedSpec != nil && !reflect.DeepEqual(tc.expectedSpec, tlsProfileSpec) {
			t.Errorf("%q: expected profile:\n%s\ngot profile:\n%s", tc.description, toYaml(tc.expectedSpec), toYaml(tlsProfileSpec))
			continue
		}
		t.Logf("%q: got expected values; profile:\n%s\nerror value: %v", tc.description, toYaml(tlsProfileSpec), err)
	}
}

func TestTLSProfileSpecForIngressController(t *testing.T) {
	testCases := []struct {
		description  string
		icProfile    *configv1.TLSSecurityProfile
		apiProfile   *configv1.TLSSecurityProfile
		expectedSpec *configv1.TLSProfileSpec
	}{

		{
			description:  "nil, nil -> intermediate",
			icProfile:    nil,
			apiProfile:   nil,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "nil, empty -> intermediate",
			icProfile:    nil,
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "nil, old -> old",
			icProfile:    nil,
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "nil, invalid -> invalid",
			icProfile:    nil,
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "empty, nil -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   nil,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "empty, empty -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "empty, old -> old",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "empty, invalid -> invalid",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "old, nil -> old",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			apiProfile:   nil,
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "old, empty -> old",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "old, modern -> old",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "old, invalid -> old",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileOldType],
		},
		{
			description:  "invalid, nil -> invalid",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "invalid, empty -> invalid",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "invalid, old -> invalid",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		{
			description:  "invalid, invalid -> invalid",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType},
			expectedSpec: &configv1.TLSProfileSpec{},
		},
		// TODO: Update test cases to use Modern cipher suites when haproxy is
		//  built with an openssl version that supports tls v1.3.
		{
			description:  "modern, nil -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "nil, modern -> intermediate",
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "modern, empty -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			apiProfile:   &configv1.TLSSecurityProfile{},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
		{
			description:  "empty, modern -> intermediate",
			icProfile:    &configv1.TLSSecurityProfile{},
			apiProfile:   &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			expectedSpec: configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		},
	}

	for _, tc := range testCases {
		ic := &operatorv1.IngressController{
			Spec: operatorv1.IngressControllerSpec{
				TLSSecurityProfile: tc.icProfile,
			},
		}
		api := &configv1.APIServer{
			Spec: configv1.APIServerSpec{
				TLSSecurityProfile: tc.apiProfile,
			},
		}
		tlsProfileSpec := tlsProfileSpecForIngressController(ic, api)
		if !reflect.DeepEqual(tc.expectedSpec, tlsProfileSpec) {
			t.Errorf("%q: expected profile:\n%v\ngot profile:\n%v", tc.description, toYaml(tc.expectedSpec), toYaml(tlsProfileSpec))
		}
	}
}

func TestValidateHTTPHeaderBufferValues(t *testing.T) {
	testCases := []struct {
		description      string
		httpHeaderBuffer operatorv1.IngressControllerHTTPHeaderBuffer
		valid            bool
	}{
		{
			description:      "no httpHeaderBuffer values",
			httpHeaderBuffer: operatorv1.IngressControllerHTTPHeaderBuffer{},
			valid:            true,
		},
		{
			description: "valid httpHeaderBuffer values",
			httpHeaderBuffer: operatorv1.IngressControllerHTTPHeaderBuffer{
				HeaderBufferBytes:           32768,
				HeaderBufferMaxRewriteBytes: 8192,
			},
			valid: true,
		},
		{
			description: "invalid httpHeaderBuffer values",
			httpHeaderBuffer: operatorv1.IngressControllerHTTPHeaderBuffer{
				HeaderBufferBytes:           8192,
				HeaderBufferMaxRewriteBytes: 32768,
			},
			valid: false,
		},
		{
			description: "invalid httpHeaderBuffer values, HeaderBufferMaxRewriteBytes not set",
			httpHeaderBuffer: operatorv1.IngressControllerHTTPHeaderBuffer{
				HeaderBufferBytes: 1,
			},
			valid: false,
		},
		{
			description: "invalid httpHeaderBuffer values, HeaderBufferBytes not set",
			httpHeaderBuffer: operatorv1.IngressControllerHTTPHeaderBuffer{
				HeaderBufferMaxRewriteBytes: 65536,
			},
			valid: false,
		},
	}

	for _, tc := range testCases {
		ic := &operatorv1.IngressController{}
		ic.Spec.HTTPHeaderBuffer = tc.httpHeaderBuffer
		err := validateHTTPHeaderBufferValues(ic)
		if tc.valid && err != nil {
			t.Errorf("%q: Expected valid HTTPHeaderBuffer to not return a validation error: %v", tc.description, err)
		}

		if !tc.valid && err == nil {
			t.Errorf("%q: Expected invalid HTTPHeaderBuffer to return a validation error", tc.description)
		}
	}
}
