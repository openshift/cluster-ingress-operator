package managementmode

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = apiextensionsv1.AddToScheme(scheme)
	return scheme
}

func testFakeClient(objs ...runtime.Object) *fake.ClientBuilder {
	return fake.NewClientBuilder().WithScheme(testScheme()).WithRuntimeObjects(objs...)
}

func compliantCRD(name string) *apiextensionsv1.CustomResourceDefinition {
	profile := SupportedProfile()
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				AnnotationBundleVersion: profile.BundleVersion,
				AnnotationChannel:       profile.Channel,
			},
		},
	}
}

func crdListObjects(overrides ...*apiextensionsv1.CustomResourceDefinition) []runtime.Object {
	profile := SupportedProfile()
	overrideMap := map[string]*apiextensionsv1.CustomResourceDefinition{}
	for _, o := range overrides {
		overrideMap[o.Name] = o
	}
	objs := make([]runtime.Object, 0, len(profile.CRDNames)+len(overrideMap))
	for _, name := range profile.CRDNames {
		if o, ok := overrideMap[name]; ok {
			objs = append(objs, o)
			delete(overrideMap, name)
			continue
		}
		objs = append(objs, compliantCRD(name))
	}
	for _, o := range overrideMap {
		objs = append(objs, o)
	}
	return objs
}

func TestAssessGatewayAPICRDCompliance_allPresentAndMatch(t *testing.T) {
	c := testFakeClient(crdListObjects()...).Build()
	result, err := AssessGatewayAPICRDCompliance(context.Background(), c)
	require.NoError(t, err)
	assert.True(t, result.Compliant)
	assert.Empty(t, result.MissingCRDs)
}

func TestAssessGatewayAPICRDCompliance_allAbsent(t *testing.T) {
	c := testFakeClient().Build()
	result, err := AssessGatewayAPICRDCompliance(context.Background(), c)
	require.NoError(t, err)
	assert.False(t, result.Compliant)
	assert.Len(t, result.MissingCRDs, 6)
}

func TestAssessGatewayAPICRDCompliance_partialSet(t *testing.T) {
	c := testFakeClient(compliantCRD("gatewayclasses.gateway.networking.k8s.io")).Build()
	result, err := AssessGatewayAPICRDCompliance(context.Background(), c)
	require.NoError(t, err)
	assert.False(t, result.Compliant)
	assert.True(t, result.PartialSet)
}

func TestAssessGatewayAPICRDCompliance_versionMismatch(t *testing.T) {
	crd := compliantCRD("gatewayclasses.gateway.networking.k8s.io")
	crd.Annotations[AnnotationBundleVersion] = "v1.0.0"
	c := testFakeClient(crdListObjects(crd)...).Build()
	result, err := AssessGatewayAPICRDCompliance(context.Background(), c)
	require.NoError(t, err)
	assert.False(t, result.Compliant)
	assert.Contains(t, result.VersionMismatch, "gatewayclasses.gateway.networking.k8s.io")
}

func TestAssessGatewayAPICRDCompliance_channelMismatch_experimentalRejected(t *testing.T) {
	crd := compliantCRD("gatewayclasses.gateway.networking.k8s.io")
	crd.Annotations[AnnotationChannel] = "experimental"
	c := testFakeClient(crdListObjects(crd)...).Build()
	result, err := AssessGatewayAPICRDCompliance(context.Background(), c)
	require.NoError(t, err)
	assert.False(t, result.Compliant)
	assert.Contains(t, result.ChannelMismatch, "gatewayclasses.gateway.networking.k8s.io")
}

func TestAssessGatewayAPICRDCompliance_missingBundleVersionAnnotation(t *testing.T) {
	crd := compliantCRD("gatewayclasses.gateway.networking.k8s.io")
	delete(crd.Annotations, AnnotationBundleVersion)
	c := testFakeClient(crdListObjects(crd)...).Build()
	result, err := AssessGatewayAPICRDCompliance(context.Background(), c)
	require.NoError(t, err)
	assert.False(t, result.Compliant)
}

func TestAssessGatewayAPICRDCompliance_missingChannelAnnotation(t *testing.T) {
	crd := compliantCRD("gatewayclasses.gateway.networking.k8s.io")
	delete(crd.Annotations, AnnotationChannel)
	c := testFakeClient(crdListObjects(crd)...).Build()
	result, err := AssessGatewayAPICRDCompliance(context.Background(), c)
	require.NoError(t, err)
	assert.False(t, result.Compliant)
}

func TestAssessGatewayAPICRDCompliance_thirdPartyVersionBlocksManagedReturn(t *testing.T) {
	profile := SupportedProfile()
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: profile.CRDNames[0],
			Annotations: map[string]string{
				AnnotationBundleVersion: "v1.2.0",
				AnnotationChannel:       profile.Channel,
			},
		},
	}
	c := testFakeClient(crdListObjects(crd)...).Build()
	result, err := AssessGatewayAPICRDCompliance(context.Background(), c)
	require.NoError(t, err)
	assert.False(t, result.Compliant)
}

func TestFormatComplianceConditionMessage_includesExpectedProfileAndRemediation(t *testing.T) {
	result := ComplianceResult{
		Profile: SupportedProfile(),
		MissingCRDs: []string{
			"gatewayclasses.gateway.networking.k8s.io",
		},
	}
	msg := FormatComplianceConditionMessage(result)
	assert.Contains(t, msg, "v1.4.1")
	assert.Contains(t, msg, "standard")
	assert.Contains(t, msg, "gateway-api")
}

func TestAssessGatewayAPICRDCompliance_extraThirdPartyCRD_doesNotAloneFailCompliance(t *testing.T) {
	extra := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "tcproutes.gateway.networking.k8s.io"},
		Spec:       apiextensionsv1.CustomResourceDefinitionSpec{Group: "gateway.networking.k8s.io"},
	}
	objs := crdListObjects()
	objs = append(objs, extra)
	c := testFakeClient(objs...).Build()
	result, err := AssessGatewayAPICRDCompliance(context.Background(), c)
	require.NoError(t, err)
	assert.True(t, result.Compliant)
}
