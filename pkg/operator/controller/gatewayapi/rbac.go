package gatewayapi

import (
	"context"
	"fmt"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

var managedClusterRoles = []*rbacv1.ClusterRole{
	manifests.GatewayAPIAdminClusterRole(),
	manifests.GatewayAPIViewClusterRole(),
}

// ensureGatewayAPIRBAC ensures that all RBAC resources for Gateway API are
// deployed to the cluster, and up to date.
func (r *reconciler) ensureGatewayAPIRBAC(ctx context.Context) error {
	var errs []error

	for _, clusterRole := range managedClusterRoles {
		if err := r.ensureClusterRole(ctx, clusterRole); err != nil {
			errs = append(errs, err)
		}
	}

	return utilerrors.NewAggregate(errs)
}

// ensureClusterRole ensures that a specific ClusterRole RBAC resource is
// deployed to the cluster, and up to date.
func (r *reconciler) ensureClusterRole(ctx context.Context, desired *rbacv1.ClusterRole) error {
	clusterRoleAlreadyExists, current, err := r.currentClusterRole(ctx, desired.Name)
	if err != nil {
		return err
	}

	if clusterRoleAlreadyExists {
		if updated, err := r.updateClusterRole(ctx, current, desired); err != nil {
			return err
		} else if updated {
			if _, _, err := r.currentClusterRole(ctx, desired.Name); err != nil {
				return err
			}
		}
	} else {
		return r.createClusterRole(ctx, desired)
	}

	return nil
}

// currentClusterRole retrieves a ClusterRole from the cluster, given the name
// of that role.
func (r *reconciler) currentClusterRole(ctx context.Context, name string) (bool, *rbacv1.ClusterRole, error) {
	var existingClusterRole rbacv1.ClusterRole

	if err := r.client.Get(ctx, types.NamespacedName{Name: name}, &existingClusterRole); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("failed to get ClusterRole %s: %w", name, err)
	}

	return true, &existingClusterRole, nil
}

// createClusterRole creates the desired ClusterRole on the cluster.
func (r *reconciler) createClusterRole(ctx context.Context, desired *rbacv1.ClusterRole) error {
	desired.ResourceVersion = ""

	if err := r.client.Create(ctx, desired); err != nil {
		return fmt.Errorf("failed to create ClusterRole %s: %w", desired.Name, err)
	}

	log.Info("Created ClusterRole", "name", desired.Name)

	return nil
}

// updateClusterRole verifies whether the current ClusterRole is up to date with
// what is desired, and performs an update if not.
func (r *reconciler) updateClusterRole(ctx context.Context, current, desired *rbacv1.ClusterRole) (bool, error) {
	changed, updated := clusterRoleChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(ctx, updated); err != nil {
		return false, fmt.Errorf("failed to update ClusterRole %s: %w", updated.Name, err)
	}

	log.Info("updated ClusterRole", "name", updated.Name, "diff", diff)

	return true, nil
}

// clusterRoleChanged indicates whether there are changes between the current
// ClusterRole and what is expected, and provides the updated version of the
// ClusterRole if so.
func clusterRoleChanged(current, expected *rbacv1.ClusterRole) (bool, *rbacv1.ClusterRole) {
	if cmp.Equal(current.Rules, expected.Rules) &&
		cmp.Equal(current.ObjectMeta.Labels, expected.ObjectMeta.Labels) &&
		cmp.Equal(current.ObjectMeta.Annotations, expected.ObjectMeta.Annotations) {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.ObjectMeta.Labels = expected.ObjectMeta.Labels
	updated.ObjectMeta.Annotations = expected.ObjectMeta.Annotations
	updated.Rules = expected.Rules

	return true, updated
}
