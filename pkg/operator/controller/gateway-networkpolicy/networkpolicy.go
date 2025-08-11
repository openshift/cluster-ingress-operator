package gatewaynetworkpolicy

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (r *reconciler) ensureGatewayNetworkPolicy(ctx context.Context, gateway *gatewayapiv1.Gateway) (bool, *networkingv1.NetworkPolicy, error) {
	name := operatorcontroller.GatewayNetworkPolicyName(gateway)
	have, current, err := r.currentGatewayNetworkPolicy(ctx, name)
	if err != nil {
		return false, nil, err
	}

	ownerRef := metav1.OwnerReference{
		APIVersion: gatewayapiv1.GroupVersion.String(),
		Kind:       "Gateway",
		Name:       gateway.Name,
		UID:        gateway.UID,
	}
	desired := desiredGatewayNetworkPolicy(name, ownerRef)

	switch {
	case !have:
		if err := r.client.Create(ctx, desired); err != nil {
			return false, nil, fmt.Errorf("failed to create gateway-api network policy: %v", err)
		}
		log.Info("created gateway-api network policy", "networkpolicy", desired)
		return r.currentGatewayNetworkPolicy(ctx, name)
	default:
		if updated, err := r.updateGatewayNetworkPolicy(ctx, current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update gateway-api network policy: %v", err)
		} else if updated {
			return r.currentGatewayNetworkPolicy(ctx, name)
		}
	}

	return true, current, err
}

func desiredGatewayNetworkPolicy(name types.NamespacedName, ownerRef metav1.OwnerReference) *networkingv1.NetworkPolicy {
	np := manifests.GatewayAPIAllowNetworkPolicy()

	np.Name = name.Name
	np.Namespace = name.Namespace

	if np.Labels == nil {
		np.Labels = map[string]string{}
	}
	np.Labels[manifests.OwningGatewayLabel] = ownerRef.Name

	np.OwnerReferences = []metav1.OwnerReference{ownerRef}

	np.Spec.PodSelector.MatchLabels = map[string]string{
		"gateway.networking.k8s.io/gateway-name": ownerRef.Name,
	}

	return np
}

func (r *reconciler) currentGatewayNetworkPolicy(ctx context.Context, name types.NamespacedName) (bool, *networkingv1.NetworkPolicy, error) {
	current := &networkingv1.NetworkPolicy{}
	if err := r.client.Get(ctx, name, current); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

func (r *reconciler) updateGatewayNetworkPolicy(ctx context.Context, current, desired *networkingv1.NetworkPolicy) (bool, error) {
	changed, updated := gatewayNetworkPolicyChanged(current, desired)
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

func gatewayNetworkPolicyChanged(current, desired *networkingv1.NetworkPolicy) (bool, *networkingv1.NetworkPolicy) {
	changed := false
	updated := current.DeepCopy()
	if !cmp.Equal(current.Spec, desired.Spec, cmpopts.EquateEmpty()) {
		changed = true
		updated.Spec = desired.Spec
	}
	if !cmp.Equal(current.Labels, desired.Labels, cmpopts.EquateEmpty()) {
		changed = true
		updated.Labels = desired.Labels
	}
	if !cmp.Equal(current.OwnerReferences, desired.OwnerReferences, cmpopts.EquateEmpty()) {
		changed = true
		updated.OwnerReferences = desired.OwnerReferences
	}
	return changed, updated
}
