package controller

import (
	configv1 "github.com/openshift/api/config/v1"
)

// copyTLSSpec creates a defensive copy of the given TLSProfileSpec to prevent
// mutations of shared/global TLS profile instances.
func copyTLSSpec(in *configv1.TLSProfileSpec) *configv1.TLSProfileSpec {
	if in == nil {
		return &configv1.TLSProfileSpec{}
	}
	out := *in
	out.Ciphers = append([]string(nil), in.Ciphers...)
	return &out
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
