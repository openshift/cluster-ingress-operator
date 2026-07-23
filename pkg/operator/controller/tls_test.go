package controller

import (
	"crypto/tls"
	"testing"

	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyTLSSpec_CopiesGroups(t *testing.T) {
	in := &configv1.TLSProfileSpec{
		Ciphers:       []string{"A", "B"},
		Groups:        []configv1.TLSGroup{configv1.TLSGroupX25519, configv1.TLSGroupSecP256r1},
		MinTLSVersion: configv1.VersionTLS12,
	}
	out := copyTLSSpec(in)

	assert.Equal(t, []configv1.TLSGroup{configv1.TLSGroupX25519, configv1.TLSGroupSecP256r1}, out.Groups, "unexpected groups: %v", out.Groups)

	// Mutating the copy must not affect the original.
	out.Groups[0] = configv1.TLSGroupSecP384r1
	assert.Equal(t, configv1.TLSGroupX25519, in.Groups[0], "mutating copy changed original")
}

func TestCopyTLSSpec_NilInput(t *testing.T) {
	out := copyTLSSpec(nil)
	assert.NotNil(t, out, "expected non-nil output for nil input")
	assert.Empty(t, out.Ciphers, "expected empty spec, got ciphers=%v groups=%v", out.Ciphers, out.Groups)
	assert.Empty(t, out.Groups, "expected empty spec, got ciphers=%v groups=%v", out.Ciphers, out.Groups)
}

func TestTLSGroupToCurveID(t *testing.T) {
	testCases := []struct {
		group    configv1.TLSGroup
		expected tls.CurveID
		ok       bool
	}{
		{configv1.TLSGroupX25519, tls.X25519, true},
		{configv1.TLSGroupSecP256r1, tls.CurveP256, true},
		{configv1.TLSGroupSecP384r1, tls.CurveP384, true},
		{configv1.TLSGroupSecP521r1, tls.CurveP521, true},
		{configv1.TLSGroupX25519MLKEM768, tls.X25519MLKEM768, true},
		{configv1.TLSGroupSecP256r1MLKEM768, 0, false},
		{configv1.TLSGroupSecP384r1MLKEM1024, 0, false},
		{configv1.TLSGroup("unknown"), 0, false},
	}

	for _, tc := range testCases {
		t.Run(string(tc.group), func(t *testing.T) {
			id, ok := TLSGroupToCurveID(tc.group)
			assert.Equal(t, tc.ok, ok, "TLSGroupToCurveID(%q): got ok=%v, want %v", tc.group, ok, tc.ok)
			if tc.ok {
				assert.Equal(t, tc.expected, id, "TLSGroupToCurveID(%q): got %v, want %v", tc.group, id, tc.expected)
			}
		})
	}
}

func TestTLSConfigFromProfile(t *testing.T) {
	log := logr.Discard()

	t.Run("nil spec uses secure defaults", func(t *testing.T) {
		cfg, err := TLSConfigFromProfile(log, nil)
		assert.NoError(t, err)
		assert.NotZero(t, cfg.MinVersion, "expected MinVersion to be set")
	})

	t.Run("intermediate profile", func(t *testing.T) {
		spec := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
		cfg, err := TLSConfigFromProfile(log, spec)
		assert.NoError(t, err)
		assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion, "expected MinVersion=VersionTLS12, got %d", cfg.MinVersion)
		assert.NotEmpty(t, cfg.CurvePreferences, "expected CurvePreferences to be set")
		// Verify X25519MLKEM768 is in the curves.
		found := false
		for _, c := range cfg.CurvePreferences {
			if c == tls.X25519MLKEM768 {
				found = true
				break
			}
		}
		assert.True(t, found, "expected X25519MLKEM768 in CurvePreferences")
		assert.NotEmpty(t, cfg.CipherSuites, "expected at least one cipher suite")
	})

	t.Run("custom profile with groups", func(t *testing.T) {
		spec := &configv1.TLSProfileSpec{
			Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
			Groups:        []configv1.TLSGroup{configv1.TLSGroupSecP256r1, configv1.TLSGroupSecP384r1},
			MinTLSVersion: configv1.VersionTLS13,
		}
		cfg, err := TLSConfigFromProfile(log, spec)
		assert.NoError(t, err)
		assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion, "expected MinVersion=VersionTLS13, got %d", cfg.MinVersion)
		assert.Equal(t, []tls.CurveID{tls.CurveP256, tls.CurveP384}, cfg.CurvePreferences, "unexpected CurvePreferences: %v", cfg.CurvePreferences)
	})

	t.Run("unsupported groups are skipped", func(t *testing.T) {
		spec := &configv1.TLSProfileSpec{
			Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
			Groups:        []configv1.TLSGroup{configv1.TLSGroupSecP256r1MLKEM768, configv1.TLSGroupSecP256r1},
			MinTLSVersion: configv1.VersionTLS12,
		}
		cfg, err := TLSConfigFromProfile(log, spec)
		assert.NoError(t, err)
		assert.Equal(t, []tls.CurveID{tls.CurveP256}, cfg.CurvePreferences, "expected 1 curve (unsupported skipped), got %v", cfg.CurvePreferences)
	})

	t.Run("invalid TLS version returns error", func(t *testing.T) {
		spec := &configv1.TLSProfileSpec{
			MinTLSVersion: "InvalidVersion",
		}
		_, err := TLSConfigFromProfile(log, spec)
		assert.Error(t, err, "expected error for invalid TLS version")
	})

	t.Run("empty groups omits CurvePreferences", func(t *testing.T) {
		spec := &configv1.TLSProfileSpec{
			Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
			MinTLSVersion: configv1.VersionTLS12,
		}
		cfg, err := TLSConfigFromProfile(log, spec)
		assert.NoError(t, err)
		assert.Nil(t, cfg.CurvePreferences, "expected nil CurvePreferences for empty groups, got %v", cfg.CurvePreferences)
	})
}

func TestTLSProfileSpecForSecurityProfile(t *testing.T) {
	t.Run("nil profile uses intermediate", func(t *testing.T) {
		spec := TLSProfileSpecForSecurityProfile(nil)
		intermediate := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
		assert.Equal(t, intermediate.MinTLSVersion, spec.MinTLSVersion, "expected intermediate MinTLSVersion")
	})

	t.Run("custom profile preserves groups", func(t *testing.T) {
		profile := &configv1.TLSSecurityProfile{
			Type: configv1.TLSProfileCustomType,
			Custom: &configv1.CustomTLSProfile{
				TLSProfileSpec: configv1.TLSProfileSpec{
					Ciphers:       []string{"A"},
					Groups:        []configv1.TLSGroup{configv1.TLSGroupX25519MLKEM768},
					MinTLSVersion: configv1.VersionTLS12,
				},
			},
		}
		spec := TLSProfileSpecForSecurityProfile(profile)
		assert.Equal(t, []configv1.TLSGroup{configv1.TLSGroupX25519MLKEM768}, spec.Groups, "expected groups=[X25519MLKEM768], got %v", spec.Groups)
	})
}

// captureLogSink records Info messages for assertions.
type captureLogSink struct {
	msgs []string
}

func (c *captureLogSink) Init(logr.RuntimeInfo)               {}
func (c *captureLogSink) Enabled(int) bool                    { return true }
func (c *captureLogSink) Info(_ int, msg string, _ ...any)    { c.msgs = append(c.msgs, msg) }
func (c *captureLogSink) Error(_ error, msg string, _ ...any) { c.msgs = append(c.msgs, msg) }
func (c *captureLogSink) WithValues(...any) logr.LogSink      { return c }
func (c *captureLogSink) WithName(string) logr.LogSink        { return c }

func applyMetricsTLSOpts(t *testing.T, opts []func(*tls.Config)) *tls.Config {
	t.Helper()
	require.Len(t, opts, 1)
	cfg := &tls.Config{}
	opts[0](cfg)
	return cfg
}

func TestMetricsTLSOptsFromAPIServer(t *testing.T) {
	log := logr.Discard()

	t.Run("nil APIServer returns nil opts", func(t *testing.T) {
		opts, err := MetricsTLSOptsFromAPIServer(log, nil)
		assert.NoError(t, err)
		assert.Nil(t, opts)
	})

	t.Run("NoOpinion returns nil opts", func(t *testing.T) {
		apiConfig := &configv1.APIServer{
			Spec: configv1.APIServerSpec{
				TLSAdherence: configv1.TLSAdherencePolicyNoOpinion,
				TLSSecurityProfile: &configv1.TLSSecurityProfile{
					Type: configv1.TLSProfileIntermediateType,
				},
			},
		}
		opts, err := MetricsTLSOptsFromAPIServer(log, apiConfig)
		assert.NoError(t, err)
		assert.Nil(t, opts)
	})

	t.Run("LegacyAdheringComponentsOnly returns nil opts", func(t *testing.T) {
		apiConfig := &configv1.APIServer{
			Spec: configv1.APIServerSpec{
				TLSAdherence: configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly,
				TLSSecurityProfile: &configv1.TLSSecurityProfile{
					Type: configv1.TLSProfileModernType,
				},
			},
		}
		opts, err := MetricsTLSOptsFromAPIServer(log, apiConfig)
		assert.NoError(t, err)
		assert.Nil(t, opts)
	})

	t.Run("StrictAllComponents applies intermediate when profile nil", func(t *testing.T) {
		apiConfig := &configv1.APIServer{
			Spec: configv1.APIServerSpec{
				TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
			},
		}
		opts, err := MetricsTLSOptsFromAPIServer(log, apiConfig)
		assert.NoError(t, err)
		cfg := applyMetricsTLSOpts(t, opts)
		assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
		assert.NotEmpty(t, cfg.CipherSuites)
		assert.NotEmpty(t, cfg.CurvePreferences)
	})

	t.Run("StrictAllComponents applies custom profile", func(t *testing.T) {
		apiConfig := &configv1.APIServer{
			Spec: configv1.APIServerSpec{
				TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
				TLSSecurityProfile: &configv1.TLSSecurityProfile{
					Type: configv1.TLSProfileCustomType,
					Custom: &configv1.CustomTLSProfile{
						TLSProfileSpec: configv1.TLSProfileSpec{
							Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
							Groups:        []configv1.TLSGroup{configv1.TLSGroupSecP256r1, configv1.TLSGroupSecP384r1},
							MinTLSVersion: configv1.VersionTLS13,
						},
					},
				},
			},
		}
		opts, err := MetricsTLSOptsFromAPIServer(log, apiConfig)
		assert.NoError(t, err)
		cfg := applyMetricsTLSOpts(t, opts)
		assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
		assert.Equal(t, []tls.CurveID{tls.CurveP256, tls.CurveP384}, cfg.CurvePreferences)
	})

	t.Run("unknown tlsAdherence treated as Strict with warning", func(t *testing.T) {
		sink := &captureLogSink{}
		captureLog := logr.New(sink)
		apiConfig := &configv1.APIServer{
			Spec: configv1.APIServerSpec{
				TLSAdherence: configv1.TLSAdherencePolicy("FutureStrictMode"),
				TLSSecurityProfile: &configv1.TLSSecurityProfile{
					Type: configv1.TLSProfileIntermediateType,
				},
			},
		}
		opts, err := MetricsTLSOptsFromAPIServer(captureLog, apiConfig)
		assert.NoError(t, err)
		cfg := applyMetricsTLSOpts(t, opts)
		assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
		assert.Contains(t, sink.msgs, "unrecognized tlsAdherence value; treating as StrictAllComponents (will apply cluster TLS profile)")
	})
}
