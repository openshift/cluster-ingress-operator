package gatewayclass

import (
	"context"
	"fmt"

	"github.com/Azure/go-autorest/autorest/to"

	"github.com/istio-ecosystem/sail-operator/pkg/install"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// ensureIstio installs or updates Istio using the Sail Library.
// It returns an error if the installation fails.
func (r *reconciler) ensureIstio(ctx context.Context, gatewayclass *gatewayapiv1.GatewayClass, istioVersion string) error {
	enableInferenceExtension, err := r.inferencepoolCrdExists(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for InferencePool CRD: %w", err)
	}

	// Create owner reference for garbage collection
	ownerRef := &metav1.OwnerReference{
		APIVersion: gatewayapiv1.SchemeGroupVersion.String(),
		Kind:       "GatewayClass",
		Name:       gatewayclass.Name,
		UID:        gatewayclass.UID,
		Controller: ptr.To(true),
	}

	// Build options from current state
	opts, err := r.buildInstallerOptions(enableInferenceExtension, ownerRef, istioVersion)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if options changed - if so, stop existing drift reconciler
	if r.driftReconciler != nil && !r.optsEqual(r.currentOpts, opts) {
		log.Info("Options changed, stopping drift reconciler")
		r.stopDriftReconcilerLocked()
	}

	// If no drift reconciler running, install and start one
	if r.driftReconciler == nil {
		reconciler, err := r.istioInstaller.Install(ctx, *opts)
		if err != nil {
			return err
		}
		log.Info("installed/updated Istio via Sail Library", "namespace", r.config.OperandNamespace, "version", istioVersion)

		// Create cancellable context for drift reconciler
		driftCtx, cancel := context.WithCancel(context.Background())
		r.driftCancel = cancel
		r.driftReconciler = reconciler
		r.currentOpts = opts

		// Start drift reconciler in background
		go func() {
			if err := reconciler.Start(driftCtx); err != nil && driftCtx.Err() == nil {
				log.Error(err, "drift reconciler failed")
			}
		}()
	}

	return nil
}

func (r *reconciler) buildInstallerOptions(enableInferenceExtension bool, ownerRef *metav1.OwnerReference, istioVersion string) (*install.Options, error) {
	// Start with Gateway API defaults
	values := install.GatewayAPIDefaults()

	// Apply OpenShift-specific overrides
	openshiftOverrides := openshiftValues(enableInferenceExtension)
	values = install.MergeValues(values, openshiftOverrides)

	return &install.Options{
		Namespace:  controller.DefaultOperandNamespace,
		Revision:   controller.IstioName("").Name,
		Values:     values,
		OwnerRef:   ownerRef,
		Version:    istioVersion,
		ManageCRDs: to.BoolPtr(false),
	}, nil
}

func (r *reconciler) stopDriftReconcilerLocked() {
	if r.driftCancel != nil {
		r.driftCancel()
		r.driftCancel = nil
	}
	if r.driftReconciler != nil {
		r.driftReconciler.Stop()
		r.driftReconciler = nil
	}
	r.currentOpts = nil
}

// Shutdown stops the drift reconciler - call on controller shutdown
func (r *reconciler) Shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopDriftReconcilerLocked()
}

func (r *reconciler) optsEqual(a, b *install.Options) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Namespace == b.Namespace &&
		a.Version == b.Version &&
		a.Revision == b.Revision
	// Note: for Values comparison, consider tracking a generation/hash
}
