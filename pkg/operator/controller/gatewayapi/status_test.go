package gatewayapi

import (
	"context"
	"testing"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/stretchr/testify/require"
)

func TestSetUnmanagedGatewayAPICRDNamesStatus_Gated(t *testing.T) {
	r := &reconciler{config: Config{ModeAccessor: operatorcontroller.NewGatewayAPIModeAccessor(false)}}

	// A nil client is intentional: when the management-mode gate is off, the
	// extension must not be read or written during the promotion window.
	require.NoError(t, r.setUnmanagedGatewayAPICRDNamesStatus(context.Background(), []string{"foreign.gateway.networking.k8s.io"}))
}
