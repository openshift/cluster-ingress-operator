//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"errors"
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type schemaReadErrorClient struct {
	client.Reader
	err error
}

type rawSchemaClient struct {
	client.Reader
	crd *apiextensionsv1.CustomResourceDefinition
}

func (reader rawSchemaClient) Get(_ context.Context, _ client.ObjectKey, object client.Object, _ ...client.GetOption) error {
	reader.crd.DeepCopyInto(object.(*apiextensionsv1.CustomResourceDefinition))
	return nil
}

func (reader schemaReadErrorClient) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return reader.err
}

func TestIngressControllerHAProxyVersionSupported(t *testing.T) {
	t.Parallel()
	stringEnum := func(values ...string) *apiextensionsv1.CustomResourceValidation {
		enum := make([]apiextensionsv1.JSON, len(values))
		for index, value := range values {
			enum[index] = apiextensionsv1.JSON{Raw: []byte(value)}
		}
		return &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"spec": {Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"haproxyVersion": {Type: "string", Enum: enum},
				}},
			},
		}}
	}
	tests := []struct {
		name          string
		mutate        func(*apiextensionsv1.CustomResourceDefinition)
		readError     error
		missingCRD    bool
		rawSchema     bool
		wantSupported bool
		wantError     string
	}{
		{name: "legacy version permitted", wantSupported: true},
		{name: "legacy version excluded", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema = stringEnum(`"3.2"`)
		}},
		{name: "other served version does not permit v1", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema = stringEnum(`"3.2"`)
			crd.Spec.Versions = append([]apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1beta1", Served: true, Schema: stringEnum(`"2.8"`),
			}}, crd.Spec.Versions...)
		}},
		{name: "missing v1", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Name = "v1beta1"
		}, wantError: "no served v1 schema"},
		{name: "v1 not served", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Served = false
		}, wantError: "no served v1 schema"},
		{name: "missing schema", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema = nil
		}, wantError: "schema is missing"},
		{name: "missing OpenAPI schema", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema.OpenAPIV3Schema = nil
		}, wantError: "schema is missing"},
		{name: "missing spec", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			delete(crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties, "spec")
		}, wantError: "spec object schema is missing or malformed"},
		{name: "malformed spec", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"] = apiextensionsv1.JSONSchemaProps{Type: "string"}
		}, wantError: "spec object schema is missing or malformed"},
		{name: "missing version field", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			delete(crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties, "haproxyVersion")
		}, wantError: "string enum is missing or malformed"},
		{name: "malformed version field", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["haproxyVersion"] = apiextensionsv1.JSONSchemaProps{Type: "integer"}
		}, wantError: "string enum is missing or malformed"},
		{name: "missing enum", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema = stringEnum()
		}, wantError: "string enum is missing or malformed"},
		{name: "malformed enum JSON", rawSchema: true, mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema = stringEnum(`"2.8`)
		}, wantError: "decode IngressController"},
		{name: "non-string enum", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema = stringEnum(`2.8`)
		}, wantError: "decode IngressController"},
		{name: "null enum", rawSchema: true, mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema = stringEnum(`null`)
		}, wantError: "enum contains null"},
		{name: "malformed enum after matching value", mutate: func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Schema = stringEnum(`"2.8"`, `true`)
		}, wantError: "decode IngressController"},
		{name: "CRD not found", missingCRD: true, wantError: "read IngressController CRD"},
		{name: "read failure", readError: errors.New("schema read failed"), wantError: "schema read failed"},
		{name: "read canceled", readError: context.Canceled, wantError: "context canceled"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			crd := &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "ingresscontrollers.operator.openshift.io"},
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name: "v1", Served: true, Schema: stringEnum(`"2.8"`, `"3.2"`),
				}}},
			}
			if testCase.mutate != nil {
				testCase.mutate(crd)
			}
			scheme := runtime.NewScheme()
			require.NoError(t, apiextensionsv1.AddToScheme(scheme))
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if !testCase.missingCRD {
				builder.WithObjects(crd)
			}
			var reader client.Reader = builder.Build()
			if testCase.rawSchema {
				reader = rawSchemaClient{Reader: reader, crd: crd}
			}
			if testCase.readError != nil {
				reader = schemaReadErrorClient{Reader: reader, err: testCase.readError}
			}
			supported, err := ingressControllerHAProxyVersionSupported(context.Background(), reader, operatorv1.HAProxyVersion("2.8"))
			if testCase.wantError != "" {
				require.ErrorContains(t, err, testCase.wantError)
				require.False(t, supported)
				if testCase.readError != nil {
					require.ErrorIs(t, err, testCase.readError)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.wantSupported, supported)
		})
	}
}
