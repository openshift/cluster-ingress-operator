//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/openshift/api/features"

	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGatewayAPIUpgradeable(t *testing.T) {
	t.Parallel()
	if gatewayAPIEnabled, err := isFeatureGateEnabled(features.FeatureGateGatewayAPI); err != nil {
		t.Fatalf("error checking feature gate enabled status: %v", err)
	} else if gatewayAPIEnabled {
		t.Skip("Gateway API is enabled, skipping TestGatewayAPIUpgradeable")
	}
	createCRDs(t)
	testAdminGate(t, true)
	deleteExistingCRD(t, "gatewayclasses.gateway.networking.k8s.io")
	testAdminGate(t, false)

}

func createCRDs(t *testing.T) {
	t.Helper()
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gatewayclasses.gateway.networking.k8s.io",
			Annotations: map[string]string{
				"api-approved.kubernetes.io": "https://github.com/kubernetes-sigs/gateway-api/pull/2466",
			},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: gatewayapiv1.GroupName,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: "gatewayclass",
				Plural:   "gatewayclasses",
				Kind:     "GatewayClass",
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Storage: true,
					Served:  true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
						},
					},
				},
			},
		},
	}

	if err := kclient.Create(context.TODO(), crd); err != nil {
		t.Fatalf("Failed to create CRD %s: %v", crd.Name, err)
	}
}

func testAdminGate(t *testing.T, shouldExist bool) {
	t.Helper()
	adminGatesConfigMap := &corev1.ConfigMap{}
	if err := wait.PollUntilContextTimeout(context.Background(), 1*time.Second, 30*time.Second, false, func(ctx context.Context) (bool, error) {
		if err := kclient.Get(ctx, client.ObjectKey{Namespace: "openshift-config-managed", Name: "admin-gates"}, adminGatesConfigMap); err != nil {
			t.Logf("Failed to get configmap admin-gates: %v, retrying...", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Fatalf("Timed out trying to get admin-gates configmap: %v", err)
	}

	_, adminGateKeyExists := adminGatesConfigMap.Data["ack-gateway-api-management"]
	if adminGateKeyExists != shouldExist {
		t.Fatalf("Expected admin gate key existence to be %v, but got %v", shouldExist, adminGateKeyExists)
	}
}
