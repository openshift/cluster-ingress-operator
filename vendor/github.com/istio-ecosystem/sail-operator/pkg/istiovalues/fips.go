// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package istiovalues

import (
	"os"
	"strings"

	"github.com/Masterminds/semver/v3"
	v1 "github.com/istio-ecosystem/sail-operator/api/v1"

	"istio.io/istio/pkg/log"
)

var (
	FipsEnabled        bool
	FipsEnableFilePath = "/proc/sys/crypto/fips_enabled"
)

// detectFipsMode checks if FIPS mode is enabled in the system.
func init() {
	detectFipsMode(FipsEnableFilePath)
}

func detectFipsMode(filepath string) {
	contents, err := os.ReadFile(filepath)
	if err != nil {
		log.Infof("FIPS detection: failed to read %s: %v; FIPS mode disabled", filepath, err)
		FipsEnabled = false
	} else {
		fipsEnabled := strings.TrimSuffix(string(contents), "\n")
		if fipsEnabled == "1" {
			FipsEnabled = true
			log.Infof("FIPS detection: %s contains %q; FIPS mode enabled", filepath, fipsEnabled)
		} else {
			log.Infof("FIPS detection: %s contains %q (expected \"1\"); FIPS mode disabled", filepath, fipsEnabled)
		}
	}
}

// ApplyFipsValues sets pilot.env.COMPLIANCE_POLICY if FIPS mode is enabled in the system.
func ApplyFipsValues(values *v1.Values, version *semver.Version) {
	if !FipsEnabled || values == nil {
		return
	}
	if values.Pilot == nil {
		values.Pilot = &v1.PilotConfig{}
	}
	if values.Pilot.Env == nil {
		values.Pilot.Env = make(map[string]string)
	}
	// For versions < 1.30, the upstream compliance policy is supported.
	// For 1.30, the Red Hat compliance policy is required because the upstream
	// fips-140-3 compliance policy is not supported.
	// TODO: When the upstream fips-140-3 compliance policy is supported,
	// migrate to using that policy.
	if version != nil && version.LessThan(istio1_30) {
		// TODO: Remove this after 1.29 is no longer supported.
		if _, found := values.Pilot.Env["COMPLIANCE_POLICY"]; !found {
			values.Pilot.Env["COMPLIANCE_POLICY"] = "fips-140-2"
		}
	} else {
		if _, found := values.Pilot.Env["COMPLIANCE_POLICY"]; !found {
			values.Pilot.Env["COMPLIANCE_POLICY"] = "fips-140-3-redhat"
		}
	}
}

// ApplyZTunnelFipsValues sets ztunnel.env.TLS12_ENABLED if FIPS mode is enabled in the system.
// For versions >= 1.30, TLS12_ENABLED is removed because ztunnel
// defaults to using only FIPS 140-3 approved ciphers.
func ApplyZTunnelFipsValues(values *v1.ZTunnelValues, version *semver.Version) {
	if !FipsEnabled || values == nil {
		return
	}

	if values.ZTunnel == nil {
		values.ZTunnel = &v1.ZTunnelConfig{}
	}
	if values.ZTunnel.Env == nil {
		values.ZTunnel.Env = make(map[string]string)
	}

	// For versions < 1.30, TLS12_ENABLED is used because only
	// fips-140-2 is supported in openshift.
	// For 1.30, we no longer need to set TLS12_ENABLED because
	// fips-140-3 is supported on openshift and ZTunnel will use
	// this by default. If the user manually specifies TLS12_ENABLED,
	// it will still be honored.
	// TODO: Remove this after 1.29 is no longer supported.
	if version != nil && version.LessThan(istio1_30) {
		if _, found := values.ZTunnel.Env["TLS12_ENABLED"]; !found {
			values.ZTunnel.Env["TLS12_ENABLED"] = "true"
		}
	}
}
