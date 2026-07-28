//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/storage/names"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	operatorv1 "github.com/openshift/api/operator/v1"
)

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
			haproxyVersion: operatorv1.HAProxyVersion28,
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
