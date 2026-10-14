//go:build e2e

package e2e

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	udproutestatuscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/udproute-status"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Temporary CI staging only; remove this file and its embedded CRD together after human review and recorded CI proof.
//
//go:embed testdata/temporary-udproute-v1.6.2-crd.yaml
var temporaryUDPRouteCRD []byte

func init() { prepareUDPRouteTest = installTemporaryUDPRouteCRD }

func installTemporaryUDPRouteCRD(t *testing.T) {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := kclient.Get(t.Context(), client.ObjectKey{Name: udproutestatuscontroller.UDPRouteCRDName}, crd)
	if errors.IsNotFound(err) {
		require.NoError(t, yaml.NewYAMLOrJSONDecoder(bytes.NewReader(temporaryUDPRouteCRD), 4096).Decode(crd))
		require.NoError(t, kclient.Create(t.Context(), crd))
	} else {
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		if err := kclient.Get(t.Context(), client.ObjectKey{Name: udproutestatuscontroller.UDPRouteCRDName}, crd); err != nil {
			return false
		}
		for _, condition := range crd.Status.Conditions {
			if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
				return true
			}
		}
		return false
	}, 2*time.Minute, 2*time.Second, "temporary UDPRoute CRD was not established")
}

func Example_temporaryUDPRouteSchemaFixture() {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(temporaryUDPRouteCRD), 4096).Decode(crd); err != nil {
		panic(err)
	}
	data, err := os.ReadFile("../../pkg/operator/controller/udproute-status/testdata/udproute-v1.6.2-status-array-schema.json")
	if err != nil {
		panic(err)
	}
	var fixture apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(data, &fixture); err != nil {
		panic(err)
	}
	for _, version := range crd.Spec.Versions {
		if version.Name != "v1" {
			continue
		}
		status := version.Schema.OpenAPIV3Schema.Properties["status"]
		parents := status.Properties["parents"]
		if status.Type != fixture.Type || !slices.Equal(status.Required, fixture.Required) || parents.Type != fixture.Properties["parents"].Type || parents.Nullable {
			panic("permanent schema regression fixture disagrees with exact temporary upstream CRD")
		}
		defaults := version.Schema.OpenAPIV3Schema.Properties["spec"].Properties["parentRefs"].Items.Schema.Properties
		parent := map[string]interface{}{"name": "gateway", "namespace": "cross-namespace", "sectionName": "udp", "port": int64(53)}
		for _, field := range []string{"group", "kind"} {
			if defaults[field].Default == nil {
				panic("upstream ParentReference default missing")
			}
			var value interface{}
			if err := json.Unmarshal(defaults[field].Default.Raw, &value); err != nil {
				panic(err)
			}
			parent[field] = value
		}
		route := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{"parentRefs": []interface{}{parent}}}}
		admitted, err := admittedUDPRouteParentRefs(route)
		if err != nil {
			panic(err)
		}
		if len(admitted) != 1 || admitted[0].Group == nil || string(*admitted[0].Group) != "gateway.networking.k8s.io" || admitted[0].Kind == nil || string(*admitted[0].Kind) != "Gateway" || admitted[0].Namespace == nil || string(*admitted[0].Namespace) != "cross-namespace" || admitted[0].SectionName == nil || string(*admitted[0].SectionName) != "udp" || admitted[0].Port == nil || *admitted[0].Port != 53 {
			panic("admitted ParentReference parser lost schema-defaulted identity")
		}
		fmt.Println("UDPRoute v1 status requires a non-null parents array")
		return
	}
	panic("temporary upstream UDPRoute CRD does not include v1")
	// Output: UDPRoute v1 status requires a non-null parents array
}
