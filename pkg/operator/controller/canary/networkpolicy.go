package canary

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	controller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	networkingv1 "k8s.io/api/networking/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

// ensureCanaryNetworkPolicy ensures the canary NetworkPolicy that allows ingress to
// the canary pods exists and is up to date.
func (r *reconciler) ensureCanaryNetworkPolicy() (bool, *networkingv1.NetworkPolicy, error) {
	have, current, err := r.currentCanaryNetworkPolicy()
	if err != nil {
		return false, nil, err
	}

	desired := desiredCanaryNetworkPolicy()

	switch {
	case !have:
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create canary network policy: %v", err)
		}
		log.Info("created canary network policy", "networkpolicy", desired)
		return r.currentCanaryNetworkPolicy()
	default:
		if updated, err := r.updateCanaryNetworkPolicy(current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update canary network policy: %v", err)
		} else if updated {
			return r.currentCanaryNetworkPolicy()
		}
	}

	return true, current, nil
}

// desiredCanaryNetworkPolicy returns the desired canary NetworkPolicy.
func desiredCanaryNetworkPolicy() *networkingv1.NetworkPolicy {
	np := manifests.CanaryAllowNetworkPolicy()

	name := controller.CanaryNetworkPolicyName()
	np.Namespace = name.Namespace
	np.Name = name.Name

	if np.Labels == nil {
		np.Labels = map[string]string{}
	}
	np.Labels[manifests.OwningIngressCanaryCheckLabel] = canaryControllerName

	// Select canary pods managed by the canary daemonset.
	np.Spec.PodSelector = *controller.CanaryDaemonSetPodSelector(canaryControllerName)

	return np
}

// currentCanaryNetworkPolicy returns the current canary NetworkPolicy, if it exists.
func (r *reconciler) currentCanaryNetworkPolicy() (bool, *networkingv1.NetworkPolicy, error) {
	current := &networkingv1.NetworkPolicy{}
	if err := r.client.Get(context.TODO(), controller.CanaryNetworkPolicyName(), current); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

// updateCanaryNetworkPolicy updates the canary NetworkPolicy if it differs from the desired state.
func (r *reconciler) updateCanaryNetworkPolicy(current, desired *networkingv1.NetworkPolicy) (bool, error) {
	changed, updated := canaryNetworkPolicyChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	log.Info("updated canary network policy", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// canaryNetworkPolicyChanged checks whether the current NetworkPolicy matches the expected
// state and, if not, returns an updated NetworkPolicy.
func canaryNetworkPolicyChanged(current, desired *networkingv1.NetworkPolicy) (bool, *networkingv1.NetworkPolicy) {
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
	return changed, updated
}

// ensureCanaryDenyAllNetworkPolicy ensures the canary "deny all" NetworkPolicy that
// denies all unexpected network traffic in the canary namespace exists and is
// up to date.
func (r *reconciler) ensureCanaryDenyAllNetworkPolicy() (bool, *networkingv1.NetworkPolicy, error) {
	have, current, err := r.currentCanaryDenyAllNetworkPolicy()
	if err != nil {
		return false, nil, err
	}

	desired := desiredCanaryDenyAllNetworkPolicy()

	switch {
	case !have:
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create canary deny all network policy: %v", err)
		}
		log.Info("created canary deny all network policy", "networkpolicy", desired)
		return r.currentCanaryDenyAllNetworkPolicy()
	default:
		if updated, err := r.updateCanaryDenyAllNetworkPolicy(current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update canary deny all network policy: %v", err)
		} else if updated {
			return r.currentCanaryDenyAllNetworkPolicy()
		}
	}

	return true, current, nil
}

// desiredCanaryDenyAllNetworkPolicy returns the desired canary deny all
// NetworkPolicy.
func desiredCanaryDenyAllNetworkPolicy() *networkingv1.NetworkPolicy {
	return manifests.CanaryDenyAllNetworkPolicy()
}

// currentCanaryDenyAllNetworkPolicy returns the current canary deny all
// NetworkPolicy, if it exists.
func (r *reconciler) currentCanaryDenyAllNetworkPolicy() (bool, *networkingv1.NetworkPolicy, error) {
	current := manifests.CanaryDenyAllNetworkPolicy()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: current.Namespace, Name: current.Name}, current); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

// updateCanaryDenyAllNetworkPolicy updates the canary deny all NetworkPolicy if
// it differs from the desired state.
func (r *reconciler) updateCanaryDenyAllNetworkPolicy(current, desired *networkingv1.NetworkPolicy) (bool, error) {
	changed, updated := canaryDenyAllNetworkPolicyChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	log.Info("updated canary deny all network policy", "diff", diff)
	return true, nil
}

// canaryDenyAllNetworkPolicyChanged checks whether the current deny all
// NetworkPolicy matches the expected state and, if not, returns an updated
// NetworkPolicy.
func canaryDenyAllNetworkPolicyChanged(current, desired *networkingv1.NetworkPolicy) (bool, *networkingv1.NetworkPolicy) {
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
	return changed, updated
}
