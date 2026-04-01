package ingress

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	controller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	networkingv1 "k8s.io/api/networking/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ensureRouterNetworkPolicy ensures the per-ingresscontroller NetworkPolicy that
// allows ingress to router pods and egress to the network exists and is up to date.
func (r *reconciler) ensureRouterNetworkPolicy(ic *operatorv1.IngressController, ownerRef metav1.OwnerReference) error {
	have, current, err := r.currentRouterNetworkPolicy(ic)
	if err != nil {
		return err
	}

	desired := desiredRouterNetworkPolicy(ic, ownerRef)

	switch {
	case !have:
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return fmt.Errorf("failed to create router network policy: %v", err)
		}
		log.Info("created router network policy", "networkpolicy", desired)
		return nil
	default:
		if _, err := r.updateRouterNetworkPolicy(current, desired); err != nil {
			return fmt.Errorf("failed to update router network policy: %v", err)
		}
		return nil
	}
}

// desiredRouterNetworkPolicy returns the desired per-ingresscontroller NetworkPolicy.
func desiredRouterNetworkPolicy(ic *operatorv1.IngressController, ownerRef metav1.OwnerReference) *networkingv1.NetworkPolicy {
	np := manifests.RouterAllowNetworkPolicy()

	name := controller.RouterNetworkPolicyName(ic)
	np.Namespace = name.Namespace
	np.Name = name.Name

	if np.Labels == nil {
		np.Labels = map[string]string{}
	}
	np.Labels[manifests.OwningIngressControllerLabel] = ic.Name

	np.OwnerReferences = []metav1.OwnerReference{ownerRef}

	// Select router pods for this ingresscontroller.
	np.Spec.PodSelector = *controller.IngressControllerDeploymentPodSelector(ic)

	return np
}

// currentRouterNetworkPolicy returns the current per-ingresscontroller NetworkPolicy, if it exists.
func (r *reconciler) currentRouterNetworkPolicy(ic *operatorv1.IngressController) (bool, *networkingv1.NetworkPolicy, error) {
	current := &networkingv1.NetworkPolicy{}
	if err := r.client.Get(context.TODO(), controller.RouterNetworkPolicyName(ic), current); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

// updateRouterNetworkPolicy updates the NetworkPolicy if it differs from the desired state.
func (r *reconciler) updateRouterNetworkPolicy(current, desired *networkingv1.NetworkPolicy) (bool, error) {
	changed, updated := routerNetworkPolicyChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	log.Info("updated router network policy", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// routerNetworkPolicyChanged checks whether the current NetworkPolicy matches the expected
// state and, if not, returns an updated NetworkPolicy.
func routerNetworkPolicyChanged(current, desired *networkingv1.NetworkPolicy) (bool, *networkingv1.NetworkPolicy) {
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

// ensureRouterDenyAllNetworkPolicy ensures the router "deny all" NetworkPolicy that
// denies all unexpected network traffic in the router's namespace exists and is
// up to date.
func (r *reconciler) ensureRouterDenyAllNetworkPolicy() (bool, *networkingv1.NetworkPolicy, error) {
	have, current, err := r.currentRouterDenyAllNetworkPolicy()
	if err != nil {
		return false, nil, err
	}

	desired := desiredRouterDenyAllNetworkPolicy()

	switch {
	case !have:
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create router deny all network policy: %v", err)
		}
		log.Info("created router deny all network policy", "networkpolicy", desired)
		return r.currentRouterDenyAllNetworkPolicy()
	default:
		if updated, err := r.updateRouterDenyAllNetworkPolicy(current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update router deny all network policy: %v", err)
		} else if updated {
			return r.currentRouterDenyAllNetworkPolicy()
		}
	}

	return true, current, nil
}

// desiredRouterDenyAllNetworkPolicy returns the router deny all NetworkPolicy.
func desiredRouterDenyAllNetworkPolicy() *networkingv1.NetworkPolicy {
	return manifests.RouterDenyAllNetworkPolicy()
}

// currentRouterDenyAllNetworkPolicy returns the current router deny all
// NetworkPolicy, if it exists.
func (r *reconciler) currentRouterDenyAllNetworkPolicy() (bool, *networkingv1.NetworkPolicy, error) {
	current := manifests.RouterDenyAllNetworkPolicy()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: current.Namespace, Name: current.Name}, current); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

// updateRouterDenyAllNetworkPolicy updates the router deny all NetworkPolicy if
// it differs from the desired state.
func (r *reconciler) updateRouterDenyAllNetworkPolicy(current, desired *networkingv1.NetworkPolicy) (bool, error) {
	changed, updated := routerDenyAllNetworkPolicyChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	log.Info("updated router deny all network policy", "diff", diff)
	return true, nil
}

// routerDenyAllNetworkPolicyChanged checks whether the current deny all
// NetworkPolicy matches the expected state and, if not, returns an updated
// NetworkPolicy.
func routerDenyAllNetworkPolicyChanged(current, desired *networkingv1.NetworkPolicy) (bool, *networkingv1.NetworkPolicy) {
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
