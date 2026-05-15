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

package install

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// DefaultVersion scans resourceFS for version directories and returns the
// highest stable (non-prerelease) semver version found.
// Returns an error if no stable version is found.
func DefaultVersion(resourceFS fs.FS) (string, error) {
	entries, err := fs.ReadDir(resourceFS, ".")
	if err != nil {
		return "", fmt.Errorf("failed to read resource directory: %w", err)
	}

	var best *semver.Version
	var bestName string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		v, err := semver.NewVersion(strings.TrimPrefix(name, "v"))
		if err != nil {
			continue // skip non-semver directories
		}
		if v.Prerelease() != "" {
			continue // skip alpha, beta, rc, etc.
		}
		if best == nil || v.GreaterThan(best) {
			best = v
			bestName = name
		}
	}
	if best == nil {
		return "", fmt.Errorf("no stable version found in resource filesystem")
	}
	return bestName, nil
}

// ValidateVersion checks that a version directory exists in the resource filesystem.
// Unlike istioversion.Resolve, this does not support aliases — only concrete
// version directory names (e.g. "v1.28.3").
func ValidateVersion(resourceFS fs.FS, version string) error {
	info, err := fs.Stat(resourceFS, version)
	if err != nil {
		return fmt.Errorf("version %q not found in resource filesystem", version)
	}
	if !info.IsDir() {
		return fmt.Errorf("version %q is not a directory", version)
	}
	return nil
}
