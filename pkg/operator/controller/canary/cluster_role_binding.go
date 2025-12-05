package canary

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
)

// ensureCanaryClusterRoleBinding ensures the canary cluster role binding exists.
func (r *reconciler) ensureCanaryClusterRoleBinding() (bool, *rbacv1.ClusterRoleBinding, error) {
	desired := desiredCanaryClusterRoleBinding()
	haveCrb, current, err := r.currentCanaryClusterRoleBinding()

	if err != nil {
		return false, nil, err
	}

	switch {
	case !haveCrb:
		if err := r.createCanaryClusterRoleBinding(desired); err != nil {
			return false, nil, err
		}
		return r.currentCanaryClusterRoleBinding()
	case haveCrb:
		if updated, err := r.updateCanaryClusterRoleBinding(current, desired); err != nil {
			return true, current, err
		} else if updated {
			return r.currentCanaryClusterRoleBinding()
		}
	}

	return true, current, nil
}

// currentCanaryClusterRoleBinding returns the current cluster role binding.
func (r *reconciler) currentCanaryClusterRoleBinding() (bool, *rbacv1.ClusterRoleBinding, error) {
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
	if err := r.client.Get(context.TODO(), controller.CanaryClusterRoleBindingName(), clusterRoleBinding); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, clusterRoleBinding, nil
}

// createCanaryClusterRoleBinding creates the given cluster role binding resource.
func (r *reconciler) createCanaryClusterRoleBinding(clusterRoleBinding *rbacv1.ClusterRoleBinding) error {
	if err := r.client.Create(context.TODO(), clusterRoleBinding); err != nil {
		return fmt.Errorf("failed to create canary cluster role binding %s/%s: %v", clusterRoleBinding.Namespace, clusterRoleBinding.Name, err)
	}
	log.Info("created canary cluster role binding", "namespace", clusterRoleBinding.Namespace, "name", clusterRoleBinding.Name)
	return nil
}

// updateCanaryClusterBinding updates the canary clusterRoleBinding if an appropriate
// change has been detected.
func (r *reconciler) updateCanaryClusterRoleBinding(current, desired *rbacv1.ClusterRoleBinding) (bool, error) {
	changed, updated := canaryClusterRoleBindingChanged(current, desired)
	if !changed {
		return false, nil
	}

	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, fmt.Errorf("failed to update canary cluster role binding %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	log.Info("updated canary cluster role binding", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// desiredCanaryClusterRoleBinding returns the desired canary clusterRoleBinding
// read in from manifests.
func desiredCanaryClusterRoleBinding() *rbacv1.ClusterRoleBinding {
	clusterRoleBinding := manifests.CanaryClusterRoleBinding()
	name := controller.CanaryClusterRoleBindingName()
	clusterRoleBinding.Name = name.Name
	clusterRoleBinding.Namespace = name.Namespace

	if clusterRoleBinding.Labels == nil {
		clusterRoleBinding.Labels = map[string]string{}
	}

	clusterRoleBinding.Labels[manifests.OwningIngressCanaryCheckLabel] = canaryControllerName

	return clusterRoleBinding
}

// canaryClusterRoleBindingChanged returns true if current and expected differ by the
// binding's subjects.
func canaryClusterRoleBindingChanged(current, expected *rbacv1.ClusterRoleBinding) (bool, *rbacv1.ClusterRoleBinding) {
	if len(current.Subjects) == 0 && len(expected.Subjects) == 0 {
		return false, nil
	}

	if len(current.Subjects) != len(expected.Subjects) {
		return true, expected.DeepCopy()
	}
	currentSubjectsMap := make(map[string]rbacv1.Subject, len(current.Subjects))

	for _, subject := range current.Subjects {
		currentSubjectsMap[getSubjectKey(subject)] = subject
	}

	for _, subject := range expected.Subjects {
		if _, exists := currentSubjectsMap[getSubjectKey(subject)]; !exists {
			return true, expected.DeepCopy()
		}
	}

	return false, nil
}

// getSubjectKey returns a unique, complete key for a subject object.
// It is a helper function for canaryClusterRoleBindingChanged.
func getSubjectKey(s rbacv1.Subject) string {
	return fmt.Sprintf("%s/%s/%s/%s", s.Kind, s.APIGroup, s.Namespace, s.Name)
}
