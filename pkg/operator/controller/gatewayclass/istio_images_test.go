package gatewayclass

import (
	"regexp"
	"testing"

	"github.com/istio-ecosystem/sail-operator/pkg/config"
	"github.com/istio-ecosystem/sail-operator/pkg/istioversion"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownstreamImageVersionsAndComponents(t *testing.T) {
	expectedVersions := []string{"v1.31.1", "v1.30.5", "v1.30.4", "v1.30.3", "v1.30.1", "v1.28.10", "v1.28.8", "v1.28.6", "v1.28.5", "v1.28.4"}
	require.Len(t, config.Config.ImageDigests, len(expectedVersions))
	for _, version := range expectedVersions {
		t.Run(version, func(t *testing.T) {
			images, present := config.Config.ImageDigests[version]
			require.True(t, present)
			for component, image := range map[string]string{
				"istio-pilot-rhel9": images.IstiodImage, "istio-proxyv2-rhel9": images.ProxyImage,
				"istio-cni-rhel9": images.CNIImage, "istio-ztunnel-rhel9": images.ZTunnelImage,
			} {
				assert.Regexp(t, "^registry\\.redhat\\.io/openshift-service-mesh/"+regexp.QuoteMeta(component)+"@sha256:[0-9a-f]{64}$", image)
			}
		})
	}
}

func TestDownstreamLatestImages(t *testing.T) {
	version, err := istioversion.Resolve("v1.31-latest")
	require.NoError(t, err)
	require.Equal(t, "v1.31.1", version)
	assert.Equal(t, config.IstioImageConfig{
		IstiodImage:  "registry.redhat.io/openshift-service-mesh/istio-pilot-rhel9@sha256:2c0f377f593f52da28cce3fdf3dfbcb65f0c445457ba383459e63d3854aaf7c4",
		ProxyImage:   "registry.redhat.io/openshift-service-mesh/istio-proxyv2-rhel9@sha256:77b4d999a06b058c3a7a5e1a24b98bfe19f9bf3d8b86fe64f1f8252e5a2bf31f",
		CNIImage:     "registry.redhat.io/openshift-service-mesh/istio-cni-rhel9@sha256:c13350c37901fc9028c1a9c43ec0a55f7614d7bf75423b709f401339f02bb42a",
		ZTunnelImage: "registry.redhat.io/openshift-service-mesh/istio-ztunnel-rhel9@sha256:0714844010b5c57cb24f453b1c35ae6f14cca678d99bd63aaa4b6f98e2a2eeb2",
	}, config.Config.ImageDigests[version])
}
