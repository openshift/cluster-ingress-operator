//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/storage/names"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	operatorv1 "github.com/openshift/api/operator/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const legacyHAProxyVersion28 operatorv1.HAProxyVersion = "2.8"

func ingressControllerHAProxyVersionSupported(ctx context.Context, reader client.Reader, version operatorv1.HAProxyVersion) (bool, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := reader.Get(ctx, types.NamespacedName{Name: "ingresscontrollers.operator.openshift.io"}, crd); err != nil {
		return false, fmt.Errorf("read IngressController CRD: %w", err)
	}
	for _, servedVersion := range crd.Spec.Versions {
		if servedVersion.Name != operatorv1.GroupVersion.Version || !servedVersion.Served {
			continue
		}
		if servedVersion.Schema == nil || servedVersion.Schema.OpenAPIV3Schema == nil {
			return false, fmt.Errorf("IngressController %s schema is missing", servedVersion.Name)
		}
		spec, found := servedVersion.Schema.OpenAPIV3Schema.Properties["spec"]
		if !found || spec.Type != "object" {
			return false, fmt.Errorf("IngressController %s spec object schema is missing or malformed", servedVersion.Name)
		}
		haproxyVersion, found := spec.Properties["haproxyVersion"]
		if !found || haproxyVersion.Type != "string" || len(haproxyVersion.Enum) == 0 {
			return false, fmt.Errorf("IngressController %s spec.haproxyVersion string enum is missing or malformed", servedVersion.Name)
		}
		supported := false
		for _, enumValue := range haproxyVersion.Enum {
			var enumVersion *string
			if err := json.Unmarshal(enumValue.Raw, &enumVersion); err != nil {
				return false, fmt.Errorf("decode IngressController %s spec.haproxyVersion enum: %w", servedVersion.Name, err)
			}
			if enumVersion == nil {
				return false, fmt.Errorf("IngressController %s spec.haproxyVersion enum contains null", servedVersion.Name)
			}
			if *enumVersion == string(version) {
				supported = true
			}
		}
		return supported, nil
	}
	return false, fmt.Errorf("IngressController CRD has no served %s schema", operatorv1.GroupVersion.Version)
}

func TestMultiHAProxyUpgradeableCondition(t *testing.T) {
	enabled, err := isFeatureGateEnabled(features.FeatureGateIngressControllerMultipleHAProxyVersions)
	require.NoError(t, err)
	if !enabled {
		t.Skip("Skipping TestMultiHAProxyUpgradeableCondition as FeatureGateIngressControllerMultipleHAProxyVersions is disabled")
	}

	ctx := context.Background()

	testCases := map[string]struct {
		haproxyVersion operatorv1.HAProxyVersion
		expectedStatus bool
	}{
		"should block upgrade on deprecated version": {
			haproxyVersion: legacyHAProxyVersion28,
			expectedStatus: false,
		},
		"should allow upgrade on supported version": {
			haproxyVersion: operatorv1.HAProxyVersion32,
			expectedStatus: true,
		},
		"should allow upgrade on default version": {
			haproxyVersion: "",
			expectedStatus: true,
		},
	}

	icStatus := map[bool]operatorv1.ConditionStatus{false: operatorv1.ConditionFalse, true: operatorv1.ConditionTrue}
	coStatus := map[bool]configv1.ConditionStatus{false: configv1.ConditionFalse, true: configv1.ConditionTrue}

	for name, test := range testCases {
		t.Run(name, func(t *testing.T) {
			if test.haproxyVersion == legacyHAProxyVersion28 {
				supported, err := ingressControllerHAProxyVersionSupported(ctx, kclient, test.haproxyVersion)
				require.NoError(t, err, "failed to inspect served IngressController schema")
				if !supported {
					t.Skipf("HAProxy %s is unsupported by the served IngressController %s schema", test.haproxyVersion, operatorv1.GroupVersion.Version)
				}
			}
			name := types.NamespacedName{
				Namespace: defaultName.Namespace,
				Name:      names.SimpleNameGenerator.GenerateName("e2e-multi-haproxy-"),
			}
			ic := newPrivateController(name, name.Name+".router.local")
			ic.Spec.HAProxyVersion = test.haproxyVersion

			err := createWithRetryOnError(t, ctx, ic, DefaultRetryTimeout)
			require.NoError(t, err, "error creating ingress controller")

			// icCleanup deletes ingress controller and waits for the cluster operator to report Upgradeable as True
			icCleanup := func() {
				assertIngressControllerDeleted(t, kclient, ic)
				err := waitForClusterOperatorConditions(t, kclient, configv1.ClusterOperatorStatusCondition{
					Type:   configv1.OperatorUpgradeable,
					Status: configv1.ConditionTrue,
				})
				assert.NoError(t, err)
			}
			t.Cleanup(icCleanup)

			// wait for the ingress controller to be ready
			err = waitForIngressControllerCondition(t, kclient, 5*time.Minute, name, availableConditionsForPrivateIngressController...)
			require.NoError(t, err)

			// wait for the ingress controller to report Upgradeable in the expected status
			err = waitForIngressControllerCondition(t, kclient, time.Minute, name, operatorv1.OperatorCondition{
				Type:   operatorv1.OperatorStatusTypeUpgradeable,
				Status: icStatus[test.expectedStatus],
			})
			assert.NoError(t, err)

			// wait for the cluster operator to report Upgradeable in the expected status
			err = waitForClusterOperatorConditions(t, kclient, configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorUpgradeable,
				Status: coStatus[test.expectedStatus],
			})
			assert.NoError(t, err)

			// clean up ingress controller before finishing the test
			icCleanup()
		})
	}

}
