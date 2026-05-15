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

	"github.com/istio-ecosystem/sail-operator/pkg/config"
)

const (
	// downstream registry and image names for OpenShift
	defaultRegistry     = "registry.redhat.io/openshift-service-mesh"
	defaultIstiodImage  = "istio-pilot-rhel9"
	defaultProxyImage   = "istio-proxyv2-rhel9"
	defaultCNIImage     = "istio-cni-rhel9"
	defaultZTunnelImage = "istio-ztunnel-rhel9"
)

// ImageNames defines the image name for each component (without registry or tag).
type ImageNames struct {
	Istiod  string
	Proxy   string
	CNI     string
	ZTunnel string
}

// SetImageDefaults populates config.Config.ImageDigests by scanning resourceFS
// for version directories and constructing image refs from the given registry
// and image names.
//
// This is a no-op if config.Config.ImageDigests is already populated (e.g. by
// config.Read() in the operator path).
//
// Example:
//
//	install.SetImageDefaults(resourceFS, "registry.redhat.io/openshift-service-mesh", install.ImageNames{
//	    Istiod:  "istio-pilot-rhel9",
//	    Proxy:   "istio-proxyv2-rhel9",
//	    CNI:     "istio-cni-rhel9",
//	    ZTunnel: "istio-ztunnel-rhel9",
//	})
func SetImageDefaults(resourceFS fs.FS, registry string, images ImageNames) error {
	if config.Config.ImageDigests != nil {
		return nil
	}
	entries, err := fs.ReadDir(resourceFS, ".")
	if err != nil {
		return fmt.Errorf("failed to read resource directory: %w", err)
	}
	config.Config.ImageDigests = make(map[string]config.IstioImageConfig)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		v := e.Name()
		config.Config.ImageDigests[v] = config.IstioImageConfig{
			IstiodImage:  registry + "/" + images.Istiod + ":" + v,
			ProxyImage:   registry + "/" + images.Proxy + ":" + v,
			CNIImage:     registry + "/" + images.CNI + ":" + v,
			ZTunnelImage: registry + "/" + images.ZTunnel + ":" + v,
		}
	}
	return nil
}
