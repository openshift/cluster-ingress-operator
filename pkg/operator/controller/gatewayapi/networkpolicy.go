package gatewayapi

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	networkingv1 "k8s.io/api/networking/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

// ensureGatewayAPINetworkPolicy ensures the allow NetworkPolicy for Gateway API namespace is present and correct.
func (r *reconciler) ensureGatewayAPINetworkPolicy(ctx context.Context) (bool, *networkingv1.NetworkPolicy, error) {
	desired := desiredGatewayAPINetworkPolicy()

	have, current, err := r.currentGatewayAPINetworkPolicy(ctx)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !have:
		if err := r.client.Create(ctx, desired); err != nil {
			return false, nil, fmt.Errorf("failed to create gateway-api network policy: %v", err)
		}
		log.Info("created gateway-api network policy", "networkpolicy", desired)
		return r.currentGatewayAPINetworkPolicy(ctx)
	default:
		if updated, err := r.updateGatewayAPINetworkPolicy(ctx, current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update gateway-api network policy: %v", err)
		} else if updated {
			return r.currentGatewayAPINetworkPolicy(ctx)
		}
	}

	return true, current, nil
}

func desiredGatewayAPINetworkPolicy() *networkingv1.NetworkPolicy {
	np := manifests.GatewayAPIAllowNetworkPolicy()

	// Set name/namespace, labels for the Gateway API controller assets.
	// The manifest may already contain namespace/name; ensure it's correct.
	// We use the same namespace as the openshift-ingress (operand) namespace, per asset design.
	// Label for selection by owning controller if needed in the future.
	if np.Labels == nil {
		np.Labels = map[string]string{}
	}
	np.Labels[operatorcontroller.ControllerDeploymentLabel] = operatorcontroller.OpenShiftDefaultGatewayClassName

	return np
}

func (r *reconciler) currentGatewayAPINetworkPolicy(ctx context.Context) (bool, *networkingv1.NetworkPolicy, error) {
	current := &networkingv1.NetworkPolicy{}
	// The asset already defines the name/namespace. Use it for lookup.
	desired := manifests.GatewayAPIAllowNetworkPolicy()
	key := types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}
	if err := r.client.Get(ctx, key, current); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

func (r *reconciler) updateGatewayAPINetworkPolicy(ctx context.Context, current, desired *networkingv1.NetworkPolicy) (bool, error) {
	changed, updated := gatewayAPINetworkPolicyChanged(current, desired)
	if !changed {
		return false, nil
	}
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(ctx, updated); err != nil {
		return false, err
	}
	log.Info("updated gateway-api network policy", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

func gatewayAPINetworkPolicyChanged(current, expected *networkingv1.NetworkPolicy) (bool, *networkingv1.NetworkPolicy) {
	if cmp.Equal(current.Spec, expected.Spec, cmpopts.EquateEmpty()) && cmp.Equal(current.Labels, expected.Labels, cmpopts.EquateEmpty()) {
		return false, nil
	}
	updated := current.DeepCopy()
	updated.Spec = expected.Spec
	// Merge/overwrite labels from expected; preserve any unrelated labels.
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	for k, v := range expected.Labels {
		updated.Labels[k] = v
	}
	return true, updated
}
