package gatewayapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	configv1 "github.com/openshift/api/config/v1"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/status"
)

// setUnmanagedGatewayAPICRDNamesStatus sets the status of the "ingress" cluster operator
// with the names of the unmanaged Gateway CRDs.
func (r *reconciler) setUnmanagedGatewayAPICRDNamesStatus(ctx context.Context, crdNames []string) error {
	return r.setClusterOperatorStatusExtension(ctx, &status.IngressOperatorStatusExtension{
		UnmanagedGatewayAPICRDNames: strings.Join(crdNames, ","),
	})
}

// setClusterOperatorStatusExtension attempts to ensure that the "ingress" cluster operator's status
// is updated with the given extension. Returns an error if failed to update the status.
func (r *reconciler) setClusterOperatorStatusExtension(ctx context.Context, desiredExtension *status.IngressOperatorStatusExtension) error {
	have, current, err := r.currentClusterOperator(ctx, operatorcontroller.IngressClusterOperatorName())
	if err != nil {
		return err
	}
	if !have {
		return fmt.Errorf("cluster operator %q not found", operatorcontroller.IngressClusterOperatorName().Name)
	}
	if _, err := r.updateClusterOperatorStatusExtension(ctx, current, desiredExtension); err != nil {
		return err
	}
	return nil
}

// currentClusterOperator returns a boolean indicating whether a cluster operator
// with the given name exists, as well as its definition if it does exist and an error value.
func (r *reconciler) currentClusterOperator(ctx context.Context, name types.NamespacedName) (bool, *configv1.ClusterOperator, error) {
	co := &configv1.ClusterOperator{}
	if err := r.client.Get(ctx, name, co); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("failed to get cluster operator %q: %w", name.Name, err)
	}
	return true, co, nil
}

// updateClusterOperatorStatusExtension updates a cluster operator's status extension.
// Returns a boolean indicating whether the cluster operator was updated, and an error value.
// If there are no unmanaged gateway CRDs, the `unmanagedGatewayAPICRDNames` field of desiredExtension will be empty
// due to the `omitempty` tag. As a result, the `unmanagedGatewayAPICRDNames` field in the status extension of
// the given cluster operator will be reset.
func (r *reconciler) updateClusterOperatorStatusExtension(ctx context.Context, current *configv1.ClusterOperator, desiredExtension *status.IngressOperatorStatusExtension) (bool, error) {
	currentExtension := &status.IngressOperatorStatusExtension{}
	if len(current.Status.Extension.Raw) > 0 /*to avoid "unexpected end of JSON input" error*/ {
		if err := json.Unmarshal(current.Status.Extension.Raw, currentExtension); err != nil {
			return false, fmt.Errorf("failed to unmarshal status extension of cluster operator %q: %w", current.Name, err)
		}
	}
	if currentExtension.UnmanagedGatewayAPICRDNames == desiredExtension.UnmanagedGatewayAPICRDNames {
		return false, nil
	}

	updated := current.DeepCopy()
	updatedExtension := *currentExtension
	updatedExtension.UnmanagedGatewayAPICRDNames = desiredExtension.UnmanagedGatewayAPICRDNames
	rawUpdatedExtension, err := json.Marshal(updatedExtension)
	if err != nil {
		return false, fmt.Errorf("failed to marshal updated status extension of cluster operator %q: %w", updated.Name, err)
	}
	updated.Status.Extension.Raw = rawUpdatedExtension
	if err := r.client.Status().Update(ctx, updated); err != nil {
		return false, fmt.Errorf("failed to update cluster operator %q: %w", updated.Name, err)
	}
	log.Info("Updated cluster operator", "name", updated.Name, "status.extension", string(updated.Status.Extension.Raw))
	return true, nil
}
