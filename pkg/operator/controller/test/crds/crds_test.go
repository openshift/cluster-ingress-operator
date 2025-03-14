package test_crds

import (
	"testing"
)

func TestManifests(t *testing.T) {
	GatewayClassCRD_v0()
	GatewayCRD_v0()
	HTTPRouteCRD_v0()
	ReferenceGrantCRD_v0()

	GatewayCRD_experimental_v1()
	TCPRouteCRD_experimental_v1()
	ListenerSetCRD_experimental_v1()

	MustAsset(GatewayCRDAsset_standard_v0)
}
