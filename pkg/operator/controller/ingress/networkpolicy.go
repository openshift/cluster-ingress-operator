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
)

// ensureRouterNetworkPolicy ensures the per-ingresscontroller NetworkPolicy that
// allows ingress to router pods and egress to the network exists and is up to date.
func (r *reconciler) ensureRouterNetworkPolicy(ic *operatorv1.IngressController) error {
	desired := desiredRouterNetworkPolicy(ic)

	have, current, err := r.currentRouterNetworkPolicy(ic)
	if err != nil {
		return err
	}

	switch {
	case !have:
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return fmt.Errorf("failed to create router network policy: %v", err)
		}
		log.Info("created router network policy", "networkpolicy", desired)
		return nil
	default:
		if updated, err := r.updateRouterNetworkPolicy(current, desired); err != nil {
			return fmt.Errorf("failed to update router network policy: %v", err)
		} else if updated {
			return nil
		}
	}

	return nil
}

// desiredRouterNetworkPolicy returns the desired per-ingresscontroller NetworkPolicy.
func desiredRouterNetworkPolicy(ic *operatorv1.IngressController) *networkingv1.NetworkPolicy {
	np := manifests.RouterNetworkPolicyAllow()

	name := controller.RouterNetworkPolicyName(ic)
	np.Namespace = name.Namespace
	np.Name = name.Name

	if np.Labels == nil {
		np.Labels = map[string]string{}
	}
	np.Labels[manifests.OwningIngressControllerLabel] = ic.Name

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
func routerNetworkPolicyChanged(current, expected *networkingv1.NetworkPolicy) (bool, *networkingv1.NetworkPolicy) {
	changed := false

	if !cmp.Equal(current.Spec, expected.Spec, cmpopts.EquateEmpty()) {
		changed = true
	}

	// Ensure the owning-ingresscontroller label value is as expected while preserving other labels.
	if current.Labels == nil || current.Labels[manifests.OwningIngressControllerLabel] != expected.Labels[manifests.OwningIngressControllerLabel] {
		changed = true
	}

	if !changed {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec

	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	updated.Labels[manifests.OwningIngressControllerLabel] = expected.Labels[manifests.OwningIngressControllerLabel]

	return true, updated
}
