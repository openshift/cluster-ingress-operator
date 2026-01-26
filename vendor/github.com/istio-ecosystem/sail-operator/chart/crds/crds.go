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

// Package crds provides embedded CRD YAML files for Istio and Sail resources.
package crds

import "embed"

// FS contains all CRD YAML files from the chart/crds directory.
// This allows programmatic access to CRDs for installation.
//
// CRD files follow the naming convention: {group}_{plural}.yaml
// Example: extensions.istio.io_wasmplugins.yaml
//
//go:embed *.yaml
var FS embed.FS
