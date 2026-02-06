package gatewayclass

import (
	"context"
	"fmt"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/apimachinery/pkg/api/errors"
)

// ensureOSSMtoSailLibraryMigration handles the upgrade migration from OLM-based
// Istio installation (4.21) to Helm-based installation via Sail Library (4.22).
//
// Steps:
//  1. Check if old Istio CR exists and delete it if present
//  2. Wait for Sail Operator to clean up IstioRevision and Helm resources
//  3. Return nil when cleanup is complete to proceed with Helm-based installation
//
// Returns a bool whether the migration is complete and an error.
//
// This migration logic can be removed in 4.23+ when 4.21 is no longer supported.
func (r *reconciler) ensureOSSMtoSailLibraryMigration(ctx context.Context) (bool, error) {
	// Check if migration is needed.
	needsMigration, istio, err := r.needsOSSMtoSailLibraryMigration(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to check migration status: %w", err)
	}
	if !needsMigration {
		// No Istio CR found, check if we're waiting for IstioRevision cleanup.
		istioRevisionExists, err := r.istioRevisionExists(ctx)
		if err != nil {
			return false, fmt.Errorf("failed to check for IstioRevision: %w", err)
		}
		if istioRevisionExists {
			log.Info("IstioRevision still exists, requeueing to wait for cleanup", "name", controller.IstioName("").Name)
			return false, nil
		}
		return true, nil
	}

	// Istio CR exists - delete it to trigger Sail Operator cleanup.
	log.Info("Migrating from OSSM to Sail Library: deleting Istio CR", "name", controller.IstioName(""))

	if err := r.client.Delete(ctx, istio); err != nil {
		if errors.IsNotFound(err) {
			// Already deleted between Get and Delete, that's fine.
			log.Info("Istio CR already deleted during migration", "name", controller.IstioName(""))
			return false, nil
		}
		return false, fmt.Errorf("failed to delete Istio CR: %w", err)
	}

	// Waiting for istio revision cleanup.
	return false, nil
}

func (r *reconciler) needsOSSMtoSailLibraryMigration(ctx context.Context) (bool, *sailv1.Istio, error) {
	// First check if the Istio CRD exists.
	istioCrdExists, err := r.crdExists(ctx, "istios.sailoperator.io")
	if err != nil {
		return false, nil, fmt.Errorf("failed to check for Istio CRD: %w", err)
	}
	if !istioCrdExists {
		return false, nil, nil
	}

	// Check if the specific Istio CR "openshift-gateway" exists.
	name := controller.IstioName(r.config.OperandNamespace)
	exists, istio, err := r.currentIstio(ctx, name)
	if err != nil {
		return false, nil, fmt.Errorf("failed to check for Istio CR: %w", err)
	}
	if !exists {
		return false, nil, nil
	}

	return true, istio, nil
}

// istioRevisionExists checks if an IstioRevision resource exists with the expected name.
func (r *reconciler) istioRevisionExists(ctx context.Context) (bool, error) {
	istioRevisionCrdExists, err := r.crdExists(ctx, "istiorevisions.sailoperator.io")
	if err != nil {
		return false, fmt.Errorf("failed to check for IstioRevision CRD: %w", err)
	}
	if !istioRevisionCrdExists {
		return false, nil
	}

	// Check if the IstioRevision resource exists.
	istioRevisionName := controller.IstioName("")
	istioRevision := &sailv1.IstioRevision{}
	err = r.client.Get(ctx, istioRevisionName, istioRevision)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get IstioRevision: %w", err)
	}
	return true, nil
}
