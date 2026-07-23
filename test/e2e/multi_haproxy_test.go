//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/storage/names"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

	for name, test := range testCases {
		t.Run(name, func(t *testing.T) {
			name := names.SimpleNameGenerator.GenerateName("e2e-multi-haproxy-")
			ic := &operatorv1.IngressController{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: defaultName.Namespace,
					Name:      name,
				},
				Spec: operatorv1.IngressControllerSpec{
					Domain: name + ".router.local",
					EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
						Type:    operatorv1.PrivateStrategyType,
						Private: &operatorv1.PrivateStrategy{},
					},
					HAProxyVersion: test.haproxyVersion,
				},
			}
			err = kclient.Create(ctx, ic)
			require.NoError(t, err)

			t.Cleanup(func() {
				err := kclient.Delete(ctx, ic)
				assert.NoError(t, err)
			})

			// wait for the condition in the IngressController resource
			assert.EventuallyWithT(t, func(collect *assert.CollectT) {
				icCopy := &operatorv1.IngressController{}
				err := kclient.Get(ctx, client.ObjectKeyFromObject(ic), icCopy)
				if !assert.NoError(collect, err) {
					return
				}
				idx := slices.IndexFunc(icCopy.Status.Conditions, func(c operatorv1.OperatorCondition) bool {
					return c.Type == operatorv1.OperatorStatusTypeUpgradeable
				})
				if !assert.GreaterOrEqual(collect, idx, 0, "condition %s not found", operatorv1.OperatorStatusTypeUpgradeable) {
					return
				}
				condition := icCopy.Status.Conditions[idx]
				if test.expectedStatus {
					assert.Equal(collect, operatorv1.ConditionTrue, condition.Status)
				} else {
					assert.Equal(collect, operatorv1.ConditionFalse, condition.Status)
				}
			}, 60*time.Second, 2*time.Second)

			// wait for the condition in the ClusterOperator resource
			assert.EventuallyWithT(t, func(collect *assert.CollectT) {
				co := &configv1.ClusterOperator{}
				err := kclient.Get(ctx, controller.IngressClusterOperatorName(), co)
				if !assert.NoError(collect, err) {
					return
				}
				idx := slices.IndexFunc(co.Status.Conditions, func(c configv1.ClusterOperatorStatusCondition) bool {
					return c.Type == configv1.OperatorUpgradeable
				})
				if !assert.GreaterOrEqual(collect, idx, 0, "condition %s not found", configv1.OperatorUpgradeable) {
					return
				}
				condition := co.Status.Conditions[idx]
				if test.expectedStatus {
					assert.Equal(collect, configv1.ConditionTrue, condition.Status)
				} else {
					assert.Equal(collect, configv1.ConditionFalse, condition.Status)
					assert.Contains(collect, condition.Message, fmt.Sprintf(`"%s"`, name))
				}
			}, 60*time.Second, 2*time.Second)
		})
	}

}
