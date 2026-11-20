package gatewayclass

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestIstioImageOverrides(t *testing.T) {
	overrides := istioImageOverrides("v1.27.9")
	if !assert.NotNil(t, overrides, "expected image overrides for v1.27.9") {
		return
	}
	assert.Equal(t, istioV1279IstiodImage, derefString(overrides.Pilot.Image))
	assert.Equal(t, istioV1279ProxyImage, derefString(overrides.Global.Proxy.Image))
	assert.Equal(t, istioV1279ProxyImage, derefString(overrides.Global.ProxyInit.Image))

	for _, version := range []string{"", "v1.27.8", "v1.27-latest", "v1.28.6"} {
		t.Run(version, func(t *testing.T) {
			assert.Nil(t, istioImageOverrides(version))
		})
	}
}

// TestBuildInstallerOptionsPinsImages verifies that the v1.27.9 iteration-7
// image override survives merging with the Gateway API defaults and the
// OpenShift-specific values, and that other versions are left alone.
func TestBuildInstallerOptionsPinsImages(t *testing.T) {
	r := &reconciler{config: Config{OperandNamespace: "openshift-ingress"}}

	pinned := r.buildInstallerOptions(false, "v1.27.9", nil)
	assert.Equal(t, istioV1279IstiodImage, derefString(pinned.Values.Pilot.Image))
	assert.Equal(t, istioV1279ProxyImage, derefString(pinned.Values.Global.Proxy.Image))
	assert.Equal(t, istioV1279ProxyImage, derefString(pinned.Values.Global.ProxyInit.Image))

	unpinned := r.buildInstallerOptions(false, "v1.27.8", nil)
	assert.Empty(t, derefString(unpinned.Values.Pilot.Image))
	if unpinned.Values.Global.Proxy != nil {
		assert.Empty(t, derefString(unpinned.Values.Global.Proxy.Image))
	}
	if unpinned.Values.Global.ProxyInit != nil {
		assert.Empty(t, derefString(unpinned.Values.Global.ProxyInit.Image))
	}
}
