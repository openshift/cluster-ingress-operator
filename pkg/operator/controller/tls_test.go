package controller

import (
	"crypto/tls"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

func TestCopyTLSSpec_CopiesGroups(t *testing.T) {
	in := &configv1.TLSProfileSpec{
		Ciphers:       []string{"A", "B"},
		Groups:        []configv1.TLSGroup{configv1.TLSGroupX25519, configv1.TLSGroupSecP256r1},
		MinTLSVersion: configv1.VersionTLS12,
	}
	out := copyTLSSpec(in)

	if len(out.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(out.Groups))
	}
	if out.Groups[0] != configv1.TLSGroupX25519 || out.Groups[1] != configv1.TLSGroupSecP256r1 {
		t.Fatalf("unexpected groups: %v", out.Groups)
	}

	// Mutating the copy must not affect the original.
	out.Groups[0] = configv1.TLSGroupSecP384r1
	if in.Groups[0] != configv1.TLSGroupX25519 {
		t.Fatalf("mutating copy changed original")
	}
}

func TestCopyTLSSpec_NilInput(t *testing.T) {
	out := copyTLSSpec(nil)
	if out == nil {
		t.Fatal("expected non-nil output for nil input")
	}
	if len(out.Ciphers) != 0 || len(out.Groups) != 0 {
		t.Fatalf("expected empty spec, got ciphers=%v groups=%v", out.Ciphers, out.Groups)
	}
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
			if ok != tc.ok {
				t.Fatalf("TLSGroupToCurveID(%q): got ok=%v, want %v", tc.group, ok, tc.ok)
			}
			if ok && id != tc.expected {
				t.Fatalf("TLSGroupToCurveID(%q): got %v, want %v", tc.group, id, tc.expected)
			}
		})
	}
}

func TestTLSConfigFromProfile(t *testing.T) {
	t.Run("nil spec uses secure defaults", func(t *testing.T) {
		cfg, err := TLSConfigFromProfile(nil)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MinVersion == 0 {
			t.Error("expected MinVersion to be set")
		}
	})

	t.Run("intermediate profile", func(t *testing.T) {
		spec := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
		cfg, err := TLSConfigFromProfile(spec)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MinVersion != tls.VersionTLS12 {
			t.Errorf("expected MinVersion=VersionTLS12, got %d", cfg.MinVersion)
		}
		if len(cfg.CurvePreferences) == 0 {
			t.Error("expected CurvePreferences to be set")
		}
		// Verify X25519MLKEM768 is in the curves.
		found := false
		for _, c := range cfg.CurvePreferences {
			if c == tls.X25519MLKEM768 {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected X25519MLKEM768 in CurvePreferences")
		}
		if len(cfg.CipherSuites) == 0 {
			t.Error("expected at least one cipher suite")
		}
	})

	t.Run("custom profile with groups", func(t *testing.T) {
		spec := &configv1.TLSProfileSpec{
			Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
			Groups:        []configv1.TLSGroup{configv1.TLSGroupSecP256r1, configv1.TLSGroupSecP384r1},
			MinTLSVersion: configv1.VersionTLS13,
		}
		cfg, err := TLSConfigFromProfile(spec)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MinVersion != tls.VersionTLS13 {
			t.Errorf("expected MinVersion=VersionTLS13, got %d", cfg.MinVersion)
		}
		if len(cfg.CurvePreferences) != 2 {
			t.Fatalf("expected 2 curves, got %d", len(cfg.CurvePreferences))
		}
		if cfg.CurvePreferences[0] != tls.CurveP256 || cfg.CurvePreferences[1] != tls.CurveP384 {
			t.Errorf("unexpected CurvePreferences: %v", cfg.CurvePreferences)
		}
	})

	t.Run("unsupported groups are skipped", func(t *testing.T) {
		spec := &configv1.TLSProfileSpec{
			Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
			Groups:        []configv1.TLSGroup{configv1.TLSGroupSecP256r1MLKEM768, configv1.TLSGroupSecP256r1},
			MinTLSVersion: configv1.VersionTLS12,
		}
		cfg, err := TLSConfigFromProfile(spec)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.CurvePreferences) != 1 {
			t.Fatalf("expected 1 curve (unsupported skipped), got %d", len(cfg.CurvePreferences))
		}
		if cfg.CurvePreferences[0] != tls.CurveP256 {
			t.Errorf("expected CurveP256, got %v", cfg.CurvePreferences[0])
		}
	})

	t.Run("invalid TLS version returns error", func(t *testing.T) {
		spec := &configv1.TLSProfileSpec{
			MinTLSVersion: "InvalidVersion",
		}
		_, err := TLSConfigFromProfile(spec)
		if err == nil {
			t.Error("expected error for invalid TLS version")
		}
	})

	t.Run("empty groups omits CurvePreferences", func(t *testing.T) {
		spec := &configv1.TLSProfileSpec{
			Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
			MinTLSVersion: configv1.VersionTLS12,
		}
		cfg, err := TLSConfigFromProfile(spec)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.CurvePreferences != nil {
			t.Errorf("expected nil CurvePreferences for empty groups, got %v", cfg.CurvePreferences)
		}
	})
}

func TestTLSProfileSpecForSecurityProfile(t *testing.T) {
	t.Run("nil profile uses intermediate", func(t *testing.T) {
		spec := TLSProfileSpecForSecurityProfile(nil)
		intermediate := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
		if spec.MinTLSVersion != intermediate.MinTLSVersion {
			t.Errorf("expected intermediate MinTLSVersion")
		}
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
		if len(spec.Groups) != 1 || spec.Groups[0] != configv1.TLSGroupX25519MLKEM768 {
			t.Errorf("expected groups=[X25519MLKEM768], got %v", spec.Groups)
		}
	})
}
