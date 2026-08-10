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
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSetUnmanagedGatewayAPICRDNamesStatus_ManagementModeDisabled(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, configv1.Install(scheme))

	co := &configv1.ClusterOperator{}
	co.Name = "ingress"
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

	// Gate-off status must preserve the legacy extension consumed by the
	// status controller to calculate GatewayAPICRDsDegraded.
	require.NoError(t, r.setUnmanagedGatewayAPICRDNamesStatus(context.Background(), []string{"foreign.gateway.networking.k8s.io"}))

	require.Len(t, statusWriter.Updated, 1)
	updated := statusWriter.Updated[0].(*configv1.ClusterOperator)
	extension := &status.IngressOperatorStatusExtension{}
	require.NoError(t, json.Unmarshal(updated.Status.Extension.Raw, extension))
	require.Equal(t, "foreign.gateway.networking.k8s.io", extension.UnmanagedGatewayAPICRDNames)
}
