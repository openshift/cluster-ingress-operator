package gatewayapi

import (
	"context"
	"encoding/json"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/status"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSetUnmanagedGatewayAPICRDNamesStatus_ManagementModeDisabled(t *testing.T) {
	t.Run("does not access a missing ClusterOperator during the promotion window", func(t *testing.T) {
		scheme := runtime.NewScheme()
		require.NoError(t, configv1.Install(scheme))
		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &reconciler{
			client: client,
			config: Config{ModeAccessor: operatorcontroller.NewGatewayAPIModeAccessor(false)},
		}

		// When management mode is disabled and the ClusterOperator is absent during
		// promotion, the reconciler must not attempt to write status.
		require.NoError(t, r.setUnmanagedGatewayAPICRDNamesStatus(context.Background(), []string{"foreign.gateway.networking.k8s.io"}))
	})

	t.Run("clears a stale extension from an existing ClusterOperator", func(t *testing.T) {
		scheme := runtime.NewScheme()
		require.NoError(t, configv1.Install(scheme))

		co := &configv1.ClusterOperator{
			ObjectMeta: metav1.ObjectMeta{Name: "ingress"},
			Status: configv1.ClusterOperatorStatus{
				Extension: runtime.RawExtension{Raw: []byte(`{"unmanagedGatewayAPICRDNames":"foreign.gateway.networking.k8s.io"}`)},
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(co).WithStatusSubresource(co).Build()
		statusWriter := &testutil.FakeStatusWriter{StatusWriter: fakeClient.Status()}
		r := &reconciler{
			client: &testutil.FakeClientRecorder{
				Client:       fakeClient,
				T:            t,
				StatusWriter: statusWriter,
			},
			config: Config{ModeAccessor: operatorcontroller.NewGatewayAPIModeAccessor(false)},
		}

		require.NoError(t, r.setUnmanagedGatewayAPICRDNamesStatus(context.Background(), []string{"foreign.gateway.networking.k8s.io"}))

		require.Len(t, statusWriter.Updated, 1)
		updated := statusWriter.Updated[0].(*configv1.ClusterOperator)
		extension := &status.IngressOperatorStatusExtension{}
		require.NoError(t, json.Unmarshal(updated.Status.Extension.Raw, extension))
		require.Empty(t, extension.UnmanagedGatewayAPICRDNames)
	})
}
