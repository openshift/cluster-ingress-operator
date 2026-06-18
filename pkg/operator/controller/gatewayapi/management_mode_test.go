package gatewayapi

import (
	"testing"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/gatewayapi/managementmode"

	"github.com/stretchr/testify/assert"
)

func TestShouldManageCRDs_unmanagedSkipsCRDEnsure(t *testing.T) {
	store := managementmode.NewStore(managementmode.State{
		EffectiveMode: managementmode.ModeUnmanaged,
	})
	assert.False(t, store.Current().ShouldManageCRDs(),
		"Reconcile must not call ensureGatewayAPICRDs when ShouldManageCRDs is false")
}
