package test_crds

import (
	"bytes"
	"embed"
	"io"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	GatewayClassCRDAsset_standard_v0   = "gateway-api/standard/v0/gateway.networking.k8s.io_gatewayclasses.yaml"
	GatewayCRDAsset_standard_v0        = "gateway-api/standard/v0/gateway.networking.k8s.io_gateways.yaml"
	HTTPRouteCRDAsset_standard_v0      = "gateway-api/standard/v0/gateway.networking.k8s.io_httproutes.yaml"
	ReferenceGrantCRDAsset_standard_v0 = "gateway-api/standard/v0/gateway.networking.k8s.io_referencegrants.yaml"

	GatewayCRDAsset_experimental_v1     = "gateway-api/experimental/v1/gateway.networking.k8s.io_gateways.yaml"
	TCPRouteCRDAsset_experimental_v1    = "gateway-api/experimental/v1/gateway.networking.k8s.io_tcproutes.yaml"
	ListenerSetCRDAsset_experimental_v1 = "gateway-api/experimental/v1/gateway.networking.x-k8s.io_listenersets.yaml"
)

//go:embed gateway-api
var content embed.FS

// MustAsset returns the bytes for the named asset.
func MustAsset(asset string) []byte {
	b, err := content.ReadFile(asset)
	if err != nil {
		panic(err)
	}
	return b
}

func MustAssetReader(asset string) io.Reader {
	return bytes.NewReader(MustAsset(asset))
}

func GatewayClassCRD_v0() *apiextensionsv1.CustomResourceDefinition {
	crd, err := NewCustomResourceDefinition(MustAssetReader(GatewayClassCRDAsset_standard_v0))
	if err != nil {
		panic(err)
	}
	return crd
}

func GatewayCRD_v0() *apiextensionsv1.CustomResourceDefinition {
	crd, err := NewCustomResourceDefinition(MustAssetReader(GatewayCRDAsset_standard_v0))
	if err != nil {
		panic(err)
	}
	return crd
}

func HTTPRouteCRD_v0() *apiextensionsv1.CustomResourceDefinition {
	crd, err := NewCustomResourceDefinition(MustAssetReader(HTTPRouteCRDAsset_standard_v0))
	if err != nil {
		panic(err)
	}
	return crd
}

func ReferenceGrantCRD_v0() *apiextensionsv1.CustomResourceDefinition {
	crd, err := NewCustomResourceDefinition(MustAssetReader(ReferenceGrantCRDAsset_standard_v0))
	if err != nil {
		panic(err)
	}
	return crd
}

func GatewayCRD_experimental_v1() *apiextensionsv1.CustomResourceDefinition {
	crd, err := NewCustomResourceDefinition(MustAssetReader(GatewayCRDAsset_experimental_v1))
	if err != nil {
		panic(err)
	}
	return crd
}

func TCPRouteCRD_experimental_v1() *apiextensionsv1.CustomResourceDefinition {
	crd, err := NewCustomResourceDefinition(MustAssetReader(TCPRouteCRDAsset_experimental_v1))
	if err != nil {
		panic(err)
	}
	return crd
}

func ListenerSetCRD_experimental_v1() *apiextensionsv1.CustomResourceDefinition {
	crd, err := NewCustomResourceDefinition(MustAssetReader(ListenerSetCRDAsset_experimental_v1))
	if err != nil {
		panic(err)
	}
	return crd
}

func NewCustomResourceDefinition(manifest io.Reader) (*apiextensionsv1.CustomResourceDefinition, error) {
	o := apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&o); err != nil {
		return nil, err
	}

	return &o, nil
}
