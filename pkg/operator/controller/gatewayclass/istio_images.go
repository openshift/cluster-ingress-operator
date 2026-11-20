package gatewayclass

import (
	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"k8s.io/utils/ptr"
)

// Istio 1.27.9 iteration 7 (release-1.27.9-7) images. The vendored OSSM 3.3.3
// Sail library only carries the 1.27.9 GA image digests, so these are taken
// from the OSSM 3.3.8 CSV, which was built from release-1.27 after the
// release-1.27.9-7 iteration landed. The 1.27.9 charts are identical between
// OSSM 3.3.3, 3.3.8, and 3.3.9, and the iteration changes no charts or CRDs,
// so only the images need to be pinned.
const (
	istioV1279IstiodImage = "registry.redhat.io/openshift-service-mesh/istio-pilot-rhel9@sha256:ba05e86f6fee9ae173b199c843710241b7c2c0ad4203031d2f085a9c44f0f7ab"
	istioV1279ProxyImage  = "registry.redhat.io/openshift-service-mesh/istio-proxyv2-rhel9@sha256:f7795a4b73e9ffec1eb1ec71ec43812f976d77832cd56d49076f228d8f7b1220"
)

// istioImageOverrides returns image overrides for Istio versions whose
// required images are newer than the digests embedded in the vendored Sail
// library. The Sail library's ApplyDigests only sets images that are unset, so
// merging these values overrides the embedded CSV digests.
func istioImageOverrides(istioVersion string) *sailv1.Values {
	switch istioVersion {
	case "v1.27.9":
		return &sailv1.Values{
			Global: &sailv1.GlobalConfig{
				Proxy:     &sailv1.ProxyConfig{Image: ptr.To(istioV1279ProxyImage)},
				ProxyInit: &sailv1.ProxyInitConfig{Image: ptr.To(istioV1279ProxyImage)},
			},
			Pilot: &sailv1.PilotConfig{
				Image: ptr.To(istioV1279IstiodImage),
			},
		}
	default:
		return nil
	}
}
