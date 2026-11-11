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

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
)

func TestMultiHAProxyUpgradeableCondition(t *testing.T) {
	enabled, err := isFeatureGateEnabled(features.FeatureGateIngressControllerMultipleHAProxyVersions)
	require.NoError(t, err)
	if !enabled {
		t.Skip("Skipping TestMultiHAProxyUpgradeableCondition as FeatureGateIngressControllerMultipleHAProxyVersions is disabled")
	}

	ctx := context.Background()
	getHAProxyImage := func(t *testing.T, ic *operatorv1.IngressController) string {
		t.Helper()
		deployment, err := getDeployment(t, kclient, controller.RouterDeploymentName(ic), time.Minute)
		require.NoError(t, err)
		for _, container := range deployment.Spec.Template.Spec.InitContainers {
			if container.Name == "haproxy" {
				require.NotEmpty(t, container.Image)
				return container.Image
			}
		}
		t.Fatal("HAProxy init container not found")
		return ""
	}
	err = waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...)
	require.NoError(t, err)
	defaultIC, err := getIngressController(t, kclient, defaultName, time.Minute)
	require.NoError(t, err)
	defaultImage := getHAProxyImage(t, defaultIC)
	operatorDeployment, err := getDeployment(t, kclient, types.NamespacedName{Namespace: controller.DefaultOperatorNamespace, Name: "ingress-operator"}, time.Minute)
	require.NoError(t, err)
	configuredImages := map[string]string{}
	for _, container := range operatorDeployment.Spec.Template.Spec.Containers {
		if container.Name == "ingress-operator" {
			for _, env := range container.Env {
				configuredImages[env.Name] = env.Value
			}
		}
	}

	testCases := map[string]struct {
		haproxyVersion operatorv1.HAProxyVersion
		imageEnv       string
		expectedStatus bool
		skip           string
	}{
		"should block upgrade on deprecated version": {
			haproxyVersion: "new version here",
			expectedStatus: false,
			skip:           "waiting for the next deprecated HAProxy version",
		},
		"should allow upgrade on supported version": {
			haproxyVersion: operatorv1.HAProxyVersion32,
			imageEnv:       "HAPROXY_32_IMAGE",
			expectedStatus: true,
		},
		"should allow upgrade on supported version 3.4": {
			haproxyVersion: operatorv1.HAProxyVersion34,
			imageEnv:       "HAPROXY_34_IMAGE",
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
			if test.skip != "" {
				t.Skip(test.skip)
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

			if test.haproxyVersion != "" {
				observed, err := getIngressController(t, kclient, name, time.Minute)
				require.NoError(t, err)
				assert.Equal(t, test.haproxyVersion, observed.Status.EffectiveHAProxyVersion)
				require.NotEmpty(t, configuredImages[test.imageEnv], "operator must configure %s", test.imageEnv)
				assert.Equal(t, configuredImages[test.imageEnv], getHAProxyImage(t, observed))
			}
			err = waitForIngressControllerCondition(t, kclient, 5*time.Minute, defaultName, defaultAvailableConditions...)
			require.NoError(t, err)
			observedDefault, err := getIngressController(t, kclient, defaultName, time.Minute)
			require.NoError(t, err)
			assert.Equal(t, defaultIC.Status.EffectiveHAProxyVersion, observedDefault.Status.EffectiveHAProxyVersion)
			assert.Equal(t, defaultImage, getHAProxyImage(t, observedDefault))

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
