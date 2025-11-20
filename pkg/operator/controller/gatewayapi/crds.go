package gatewayapi

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// managedCRDs is a list of CRDs that this controller manages.
var managedCRDs = []*apiextensionsv1.CustomResourceDefinition{
	manifests.GatewayClassCRD(),
	manifests.GatewayCRD(),
	manifests.GRPCRouteCRD(),
	manifests.HTTPRouteCRD(),
	manifests.ReferenceGrantCRD(),
}

// managedCRDMap is a map of CRDs that this controller manages.
var managedCRDMap = map[string]*apiextensionsv1.CustomResourceDefinition{
	manifests.GatewayClassCRD().Name:   manifests.GatewayClassCRD(),
	manifests.GatewayCRD().Name:        manifests.GatewayCRD(),
	manifests.GRPCRouteCRD().Name:      manifests.GRPCRouteCRD(),
	manifests.HTTPRouteCRD().Name:      manifests.HTTPRouteCRD(),
	manifests.ReferenceGrantCRD().Name: manifests.ReferenceGrantCRD(),
}

// ensureCRD attempts to ensure that the specified CRD exists and returns a
// Boolean indicating whether it exists, the CRD if it does exist, and an error
// value.
func (r *reconciler) ensureCRD(ctx context.Context, desired *apiextensionsv1.CustomResourceDefinition) (bool, *apiextensionsv1.CustomResourceDefinition, error) {
	name := types.NamespacedName{
		Namespace: desired.Namespace,
		Name:      desired.Name,
	}
	have, current, err := r.currentCRD(ctx, name)
	if err != nil {
		return have, current, err
	}

	switch {
	case !have:
		if err := r.createCRD(ctx, desired); err != nil {
			return false, nil, err
		}
		return r.currentCRD(ctx, name)
	case have:
		if updated, err := r.updateCRD(ctx, current, desired); err != nil {
			return have, current, err
		} else if updated {
			return r.currentCRD(ctx, name)
		}
	}
	return have, current, nil
}

// ensureGatewayAPICRDs ensures the managed Gateway API CRDs are created and
// returns an error value.  For now, the managed CRDs are the GatewayClass,
// Gateway, GRPCRoute, HTTPRoute, and ReferenceGrant CRDs.
func (r *reconciler) ensureGatewayAPICRDs(ctx context.Context) error {
	var errs []error
	for i := range managedCRDs {
		// The controller-runtime client mutates its argument, so give
		// it a copy of the CRD rather than the original.
		crd := managedCRDs[i].DeepCopy()
		_, _, err := r.ensureCRD(ctx, crd)
		errs = append(errs, err)
	}
	return utilerrors.NewAggregate(errs)
}

// listUnmanagedGatewayAPICRDs returns a list of unmanaged Gateway API CRDs
// which exist in the cluster. A Gateway API CRD has "gateway.networking.k8s.io"
// or "gateway.networking.x-k8s.io" in its "spec.group" field.
func (r *reconciler) listUnmanagedGatewayAPICRDs(ctx context.Context) ([]string, error) {
	gatewayAPICRDs := &apiextensionsv1.CustomResourceDefinitionList{}
	if err := r.cache.List(ctx, gatewayAPICRDs, client.MatchingFields{gatewayAPICRDIndexFieldName: unmanagedGatewayAPICRDIndexFieldValue}); err != nil {
		return nil, fmt.Errorf("failed to list unmanaged gateway API CRDs: %w", err)
	}

	var unmanagedCRDNames []string
	for _, crd := range gatewayAPICRDs.Items {
		unmanagedCRDNames = append(unmanagedCRDNames, crd.Name)
	}

	// Sort crd names to have a stable list.
	// This will help to avoid cluster operator
	// status flip flops during updates.
	sort.Strings(unmanagedCRDNames)

	return unmanagedCRDNames, nil
}

// currentCRD returns a Boolean indicating whether an CRD
// exists for the IngressController with the given name, as well as the
// CRD if it does exist and an error value.
func (r *reconciler) currentCRD(ctx context.Context, name types.NamespacedName) (bool, *apiextensionsv1.CustomResourceDefinition, error) {
	var crd apiextensionsv1.CustomResourceDefinition
	if err := r.client.Get(ctx, name, &crd); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("failed to get CRD %s: %w", name, err)
	}
	return true, &crd, nil
}

// createCRD attempts to create the specified CRD and returns an error value.
func (r *reconciler) createCRD(ctx context.Context, desired *apiextensionsv1.CustomResourceDefinition) error {
	if err := r.client.Create(ctx, desired); err != nil {
		return fmt.Errorf("failed to create CRD %s: %w", desired.Name, err)
	}

	log.Info("created CRD", "name", desired.Name)

	return nil
}

// updateCRD updates an CRD.  Returns a Boolean indicating
// whether the CRD was updated, and an error value.
func (r *reconciler) updateCRD(ctx context.Context, current, desired *apiextensionsv1.CustomResourceDefinition) (bool, error) {
	changed, updated := crdChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(ctx, updated); err != nil {
		return false, fmt.Errorf("failed to update CRD %s: %w", updated.Name, err)
	}
	log.Info("updated CRD", "name", updated.Name, "diff", diff)
	return true, nil
}

// crdChanged checks if the current CRD spec and annotations match
// the expected spec and annotations, and if not returns an updated one.
func crdChanged(current, expected *apiextensionsv1.CustomResourceDefinition) (bool, *apiextensionsv1.CustomResourceDefinition) {
	crdCmpOpts := []cmp.Option{
		// Ignore fields that the API may have modified.  Note: This
		// list must be kept consistent with the updated.Spec.Foo =
		// current.Spec.Foo assignments below!
		cmpopts.IgnoreFields(apiextensionsv1.CustomResourceDefinitionSpec{}, "Conversion"),
		cmpopts.EquateEmpty(),
	}

	specChanged := !cmp.Equal(current.Spec, expected.Spec, crdCmpOpts...)
	annotationsChanged := false

	// Check if any expected annotations are missing or different
	for k, v := range expected.Annotations {
		if cv, ok := current.Annotations[k]; !ok || v != cv {
			annotationsChanged = true
			break
		}
	}

	if !specChanged && !annotationsChanged {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec

	// Only update the expected annotations, preserving any additional ones
	if updated.ObjectMeta.Annotations == nil {
		updated.ObjectMeta.Annotations = make(map[string]string)
	}
	for k, v := range expected.Annotations {
		updated.ObjectMeta.Annotations[k] = v
	}

	// Preserve fields that the API, other controllers, or user may have
	// modified.  Note: This list must be kept consistent with crdCmpOpts
	// above!
	updated.Spec.Conversion = current.Spec.Conversion

	return true, updated
}
