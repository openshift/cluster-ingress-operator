package controller

import (
	"crypto/tls"
	"fmt"

	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/crypto"
)

// copyTLSSpec creates a defensive copy of the given TLSProfileSpec to prevent
// mutations of shared/global TLS profile instances.
func copyTLSSpec(in *configv1.TLSProfileSpec) *configv1.TLSProfileSpec {
	if in == nil {
		return &configv1.TLSProfileSpec{}
	}
	out := *in
	out.Ciphers = append([]string(nil), in.Ciphers...)
	out.Groups = append([]configv1.TLSGroup(nil), in.Groups...)
	return &out
}

// tlsGroupToCurveID maps a configv1.TLSGroup to a crypto/tls CurveID.
// Groups not supported by the Go runtime are returned with ok=false.
var tlsGroupToCurveID = map[configv1.TLSGroup]tls.CurveID{
	configv1.TLSGroupX25519:         tls.X25519,
	configv1.TLSGroupSecP256r1:      tls.CurveP256,
	configv1.TLSGroupSecP384r1:      tls.CurveP384,
	configv1.TLSGroupSecP521r1:      tls.CurveP521,
	configv1.TLSGroupX25519MLKEM768: tls.X25519MLKEM768,
}

// TLSGroupToCurveID converts a configv1.TLSGroup name to its crypto/tls
// CurveID. The second return value is false when the group is not supported
// by the Go runtime.
func TLSGroupToCurveID(group configv1.TLSGroup) (tls.CurveID, bool) {
	id, ok := tlsGroupToCurveID[group]
	return id, ok
}

// TLSConfigFromProfile builds a *tls.Config from the given TLSProfileSpec.
// Cipher names in the spec are expected to use OpenSSL naming (the format
// used in the configv1.TLSProfileSpec.Ciphers field).  Groups that cannot
// be mapped to a Go CurveID are skipped (with a warning on log) so that the
// operator does not fail when the API advertises groups the runtime does
// not yet support. Callers should bind resource context on log (for example
// with WithValues) so skipped-group warnings identify the related object.
func TLSConfigFromProfile(log logr.Logger, spec *configv1.TLSProfileSpec) (*tls.Config, error) {
	if spec == nil {
		return crypto.SecureTLSConfig(&tls.Config{}), nil
	}

	cfg := &tls.Config{}

	if len(spec.Ciphers) > 0 {
		ianaNames := crypto.OpenSSLToIANACipherSuites(spec.Ciphers)
		var suites []uint16
		for _, name := range ianaNames {
			id, err := crypto.CipherSuite(name)
			if err != nil {
				log.Info("skipping unsupported cipher suite", "cipher", name)
				continue
			}
			suites = append(suites, id)
		}
		cfg.CipherSuites = suites
	}

	if len(spec.MinTLSVersion) > 0 {
		v, err := crypto.TLSVersion(string(spec.MinTLSVersion))
		if err != nil {
			return nil, fmt.Errorf("invalid TLS version %q: %w", spec.MinTLSVersion, err)
		}
		cfg.MinVersion = v
	}

	if len(spec.Groups) > 0 {
		var curves []tls.CurveID
		for _, g := range spec.Groups {
			if id, ok := TLSGroupToCurveID(g); ok {
				curves = append(curves, id)
			} else {
				log.Info("skipping unsupported TLS group", "group", g)
			}
		}
		if len(curves) > 0 {
			cfg.CurvePreferences = curves
		}
	}

	return crypto.SecureTLSConfig(cfg), nil
}

// TLSProfileSpecForSecurityProfile returns a TLS profile spec based on the
// provided security profile, or the "Intermediate" profile if an unknown
// security profile type is provided or the profile is nil.  Note that the
// return value must not be mutated by the caller; the caller must make a copy
// if it needs to mutate the value.
func TLSProfileSpecForSecurityProfile(profile *configv1.TLSSecurityProfile) *configv1.TLSProfileSpec {
	if profile != nil {
		if profile.Type == configv1.TLSProfileCustomType {
			if profile.Custom != nil {
				return copyTLSSpec(&profile.Custom.TLSProfileSpec)
			}
			return &configv1.TLSProfileSpec{}
		} else if spec, ok := configv1.TLSProfiles[profile.Type]; ok {
			return copyTLSSpec(spec)
		}
	}
	return copyTLSSpec(configv1.TLSProfiles[configv1.TLSProfileIntermediateType])
}

// IsUnrecognizedTLSAdherence returns true if the given policy is not one of the
// officially recognized policies.
func IsUnrecognizedTLSAdherence(policy configv1.TLSAdherencePolicy) bool {
	switch policy {
	case configv1.TLSAdherencePolicyNoOpinion,
		configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly,
		configv1.TLSAdherencePolicyStrictAllComponents:
		return false
	default:
		return true
	}
}
