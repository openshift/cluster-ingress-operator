package ingress

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	policyv1 "k8s.io/api/policy/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// ensureRouterPodDisruptionBudget ensures the pod disruption budget exists for
// a given ingresscontroller.  Returns a Boolean indicating whether the PDB
// exists, the PDB if it does exist, and an error value.
func (r *reconciler) ensureRouterPodDisruptionBudget(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *policyv1.PodDisruptionBudget, error) {
	wantPDB, desired, err := desiredRouterPodDisruptionBudget(ic, deploymentRef)
	if err != nil {
		return false, nil, fmt.Errorf("failed to build pod disruption budget: %v", err)
	}

	havePDB, current, err := r.currentRouterPodDisruptionBudget(ic)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !wantPDB && !havePDB:
		return false, nil, nil
	case !wantPDB && havePDB:
		if err := r.client.Delete(context.TODO(), current); err != nil {
			if !errors.IsNotFound(err) {
				return true, current, fmt.Errorf("failed to delete pod disruption budget: %v", err)
			}
		} else {
			log.Info("deleted pod disruption budget", "poddisruptionbudget", current)
		}
		return false, nil, nil
	case wantPDB && !havePDB:
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create pod disruption budget: %v", err)
		}
		log.Info("created pod disruption budget", "poddisruptionbudget", desired)
		return r.currentRouterPodDisruptionBudget(ic)
	case wantPDB && havePDB:
		if updated, err := r.updateRouterPodDisruptionBudget(current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update pod disruption budget: %v", err)
		} else if updated {
			return r.currentRouterPodDisruptionBudget(ic)
		}
	}

	return true, current, nil
}

// desiredRouterPodDisruptionBudget returns the desired router pod disruption
// budget.  Returns a Boolean indicating whether a PDB is desired, as well as
// the PDB if one is desired.
func desiredRouterPodDisruptionBudget(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *policyv1.PodDisruptionBudget, error) {
	if ic.Spec.Replicas != nil && *ic.Spec.Replicas < int32(2) {
		return false, nil, nil
	}

	// OCPBUGS-25739 - For 2-replica ingress controllers, use an integer MaxUnavailable
	// of 1 instead of "50%" to provide a deterministic value regardless of topology,
	// avoiding any dependency on percent-based rounding in the disruption controller.
	//
	// OCPBUGS-7546 - make sure number of available pods is always 2 when there are only 3 replicas.
	var maxUnavailable intstr.IntOrString
	replicas := ptr.Deref(ic.Spec.Replicas, 0)
	switch {
	case replicas == 2:
		maxUnavailable = intstr.FromInt(1)
	case replicas >= 3:
		maxUnavailable = intstr.FromString("25%")
	default:
		maxUnavailable = intstr.FromString("50%")
	}

	name := controller.RouterPodDisruptionBudgetName(ic)
	pdb := policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			// The disruption controller rounds MaxUnavailable up.
			// https://github.com/kubernetes/kubernetes/blob/65dc445aa2d581b4fa829258e46e4faf44e999b6/pkg/controller/disruption/disruption.go#L539
			MaxUnavailable: &maxUnavailable,
			Selector:       controller.IngressControllerDeploymentPodSelector(ic),
		},
	}
	pdb.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})

	return true, &pdb, nil
}

// currentRouterPodDisruptionBudget returns the current router pod disruption
// budget.  Returns a Boolean indicating whether the PDB existed, the PDB if it
// did exist, and an error value.
func (r *reconciler) currentRouterPodDisruptionBudget(ic *operatorv1.IngressController) (bool, *policyv1.PodDisruptionBudget, error) {
	pdb := &policyv1.PodDisruptionBudget{}
	if err := r.client.Get(context.TODO(), controller.RouterPodDisruptionBudgetName(ic), pdb); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, pdb, nil
}

// updateRouterPodDisruptionBudget updates a pod disruption budget.  Returns a
// Boolean indicating whether the PDB was updated, and an error value.
func (r *reconciler) updateRouterPodDisruptionBudget(current, desired *policyv1.PodDisruptionBudget) (bool, error) {
	changed, updated := podDisruptionBudgetChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	log.Info("updated pod disruption budget", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// podDisruptionBudgetChanged checks whether the current pod disruption budget
// spec matches the expected spec and if not returns an updated one.
func podDisruptionBudgetChanged(current, expected *policyv1.PodDisruptionBudget) (bool, *policyv1.PodDisruptionBudget) {
	if cmp.Equal(current.Spec, expected.Spec, cmpopts.EquateEmpty()) {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec
	return true, updated
}
