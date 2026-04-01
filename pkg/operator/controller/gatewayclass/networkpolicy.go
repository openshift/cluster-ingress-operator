package gatewayclass

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
)

func (r *reconciler) ensureIstiodNetworkPolicy(ctx context.Context) (bool, *networkingv1.NetworkPolicy, error) {
	desired := desiredIstiodNetworkPolicy()

	have, current, err := r.currentIstiodNetworkPolicy(ctx)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !have:
		if err := r.client.Create(ctx, desired); err != nil {
			return false, nil, fmt.Errorf("failed to create istiod network policy: %w", err)
		}
		log.Info("created istiod network policy", "networkpolicy", desired)
		return r.currentIstiodNetworkPolicy(ctx)
	default:
		if updated, err := r.updateIstiodNetworkPolicy(ctx, current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update istiod network policy: %w", err)
		} else if updated {
			return r.currentIstiodNetworkPolicy(ctx)
		}
	}

	return true, current, err
}

func desiredIstiodNetworkPolicy() *networkingv1.NetworkPolicy {
	return manifests.IstiodAllowNetworkPolicy()
}

func (r *reconciler) currentIstiodNetworkPolicy(ctx context.Context) (bool, *networkingv1.NetworkPolicy, error) {
	current := &networkingv1.NetworkPolicy{}
	if err := r.client.Get(ctx, operatorcontroller.IstiodNetworkPolicyName(), current); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

func (r *reconciler) updateIstiodNetworkPolicy(ctx context.Context, current, desired *networkingv1.NetworkPolicy) (bool, error) {
	changed, updated := istiodNetworkPolicyChanged(current, desired)
	if !changed {
		return false, nil
	}
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(ctx, updated); err != nil {
		return false, err
	}
	log.Info("updated istiod network policy", "diff", diff)
	return true, nil
}

func istiodNetworkPolicyChanged(current, desired *networkingv1.NetworkPolicy) (bool, *networkingv1.NetworkPolicy) {
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
