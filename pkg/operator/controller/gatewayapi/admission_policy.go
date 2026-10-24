package gatewayapi

import (
	"context"
	"fmt"
	"reflect"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var (
	// baseAdmissionPolicy is the SA-only ValidatingAdmissionPolicy
	// loaded from the embedded asset. At ensure time, the pod-bound
	// token validation is appended when the control plane topology
	// is not External.
	baseAdmissionPolicy           = manifests.GatewayAPICRDAdmissionPolicy()
	desiredAdmissionPolicyBinding = manifests.GatewayAPICRDAdmissionPolicyBinding()
)

// desiredAdmissionPolicyForTopology returns the desired
// ValidatingAdmissionPolicy for the given control-plane topology
// mode.
// In HyperShift, the ingress operator runs on the management cluster and
// communicates with the hosted cluster via a generated Kubeconfig
// so additional pod-bound token validation (node-name + pod-name claims) are not
// available and should be skipped from the VAP.
func desiredAdmissionPolicyForTopology(skipPodBound bool) *admissionregistrationv1.ValidatingAdmissionPolicy {
	vap := baseAdmissionPolicy.DeepCopy()
	if !skipPodBound {
		reason := metav1.StatusReasonForbidden
		vap.Spec.Validations = append(vap.Spec.Validations, admissionregistrationv1.Validation{
			Expression: `has(request.userInfo.extra) && ('authentication.kubernetes.io/node-name' in request.userInfo.extra) && ('authentication.kubernetes.io/pod-name' in request.userInfo.extra)`,
			Message:    `this user must have both "authentication.kubernetes.io/node-name" and "authentication.kubernetes.io/pod-name" claims`,
			Reason:     &reason,
		})
	}
	return vap
}

// reconcileAdmissionPolicyTransition handles VAP deletion when
// transitioning to Unmanaged mode. It MUST be called before
// reconcileIngressStatus so that the Managed=False/Unmanaged condition
// is only written to the Ingress CR status after the VAP and its
// binding have been successfully removed.
//
// The snapshot parameter carries the single authoritative Ingress CR
// read for this reconcile pass, preventing TOCTOU divergence between
// this function and reconcileIngressStatus.
//
// If the desired mode is Unmanaged and the VAP or binding still exist,
// this method deletes them and returns an error on failure so the
// reconciler retries. When the desired mode is Managed (or if the
// Ingress CR was not found), this method is a no-op.
func (r *reconciler) reconcileAdmissionPolicyTransition(ctx context.Context, snapshot ingressModeSnapshot) error {
	if snapshot.desiredMode != operatorv1.GatewayAPIManagementModeUnmanaged {
		return nil
	}

	// Step 1: Stop Istio (Sail uninstall) before removing VAP.
	// Per the EP ordering, Sail must be torn down first so that
	// no Istio workloads remain when the admission policy is removed.
	if r.config.SailUninstaller != nil {
		if err := r.config.SailUninstaller.UninstallSail(ctx); err != nil {
			return fmt.Errorf("cannot complete transition to Unmanaged, Sail uninstall failed: %w", err)
		}
	}

	// Step 2: Remove the ValidatingAdmissionPolicy and binding.
	if err := r.deleteAdmissionPolicy(ctx); err != nil {
		return fmt.Errorf("cannot complete transition to Unmanaged: %w", err)
	}
	return nil
}

// ensureAdmissionPolicy ensures the ValidatingAdmissionPolicy and its
// binding exist whenever CIO manages Gateway API CRDs, including when the
// management-mode gate is disabled. External topology and IBMCloud use the
// SA-only base; other Infrastructure configurations include the pod-bound
// token validation. Any failure to read Infrastructure fails the reconcile
// to prevent silently weakening the admission policy.
func (r *reconciler) ensureAdmissionPolicy(ctx context.Context) error {
	infra := &configv1.Infrastructure{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, infra); err != nil {
		return fmt.Errorf("failed to get Infrastructure/cluster for admission policy topology: %w", err)
	}
	skipPodBound := infra.Status.ControlPlaneTopology == configv1.ExternalTopologyMode ||
		(infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.Type == configv1.IBMCloudPlatformType)

	desired := desiredAdmissionPolicyForTopology(skipPodBound)
	if err := r.ensureValidatingAdmissionPolicy(ctx, desired); err != nil {
		return err
	}
	return r.ensureValidatingAdmissionPolicyBinding(ctx)
}

func (r *reconciler) ensureValidatingAdmissionPolicy(ctx context.Context, desired *admissionregistrationv1.ValidatingAdmissionPolicy) error {
	name := types.NamespacedName{Name: desired.Name}

	var current admissionregistrationv1.ValidatingAdmissionPolicy
	if err := r.client.Get(ctx, name, &current); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get ValidatingAdmissionPolicy %s: %w", desired.Name, err)
		}
		if err := r.client.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create ValidatingAdmissionPolicy %s: %w", desired.Name, err)
		}
		log.Info("created ValidatingAdmissionPolicy", "name", desired.Name)
		return nil
	}

	updated := current.DeepCopy()
	transitioned := removeCVOMetadata(&updated.ObjectMeta)
	if admissionPolicyUpToDate(&current, desired) && !transitioned {
		return nil
	}

	updated.Spec = desired.Spec
	if err := r.client.Update(ctx, updated); err != nil {
		return fmt.Errorf("failed to update ValidatingAdmissionPolicy %s: %w", desired.Name, err)
	}
	log.Info("updated ValidatingAdmissionPolicy", "name", desired.Name)
	if transitioned {
		log.Info("transitioned ValidatingAdmissionPolicy from CVO to CIO management", "name", name)
	}
	return nil
}

func (r *reconciler) ensureValidatingAdmissionPolicyBinding(ctx context.Context) error {
	desired := desiredAdmissionPolicyBinding.DeepCopy()
	name := types.NamespacedName{Name: desired.Name}

	var current admissionregistrationv1.ValidatingAdmissionPolicyBinding
	if err := r.client.Get(ctx, name, &current); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get ValidatingAdmissionPolicyBinding %s: %w", desired.Name, err)
		}
		if err := r.client.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create ValidatingAdmissionPolicyBinding %s: %w", desired.Name, err)
		}
		log.Info("created ValidatingAdmissionPolicyBinding", "name", desired.Name)
		return nil
	}

	updated := current.DeepCopy()
	transitioned := removeCVOMetadata(&updated.ObjectMeta)
	if admissionPolicyBindingUpToDate(&current, desired) && !transitioned {
		return nil
	}

	updated.Spec = desired.Spec
	if err := r.client.Update(ctx, updated); err != nil {
		return fmt.Errorf("failed to update ValidatingAdmissionPolicyBinding %s: %w", desired.Name, err)
	}
	log.Info("updated ValidatingAdmissionPolicyBinding", "name", desired.Name)
	if transitioned {
		log.Info("transitioned ValidatingAdmissionPolicyBinding from CVO to CIO management", "name", name)
	}
	return nil
}

// removeCVOMetadata clears all owner references so CIO exclusively manages the
// policy and binding, and removes their former payload-selection annotations.
// It preserves other metadata and reports whether cleanup was necessary.
func removeCVOMetadata(metadata *metav1.ObjectMeta) bool {
	changed := false
	for _, key := range []string{
		"capability.openshift.io/name",
		"include.release.openshift.io/ibm-cloud-managed",
		"include.release.openshift.io/self-managed-high-availability",
		"include.release.openshift.io/single-node-developer",
		"release.openshift.io/feature-set",
	} {
		if _, exists := metadata.Annotations[key]; exists {
			delete(metadata.Annotations, key)
			changed = true
		}
	}
	if len(metadata.OwnerReferences) > 0 {
		metadata.OwnerReferences = nil
		changed = true
	}
	return changed
}

// deleteAdmissionPolicy deletes the ValidatingAdmissionPolicy and its
// binding. Deletes the policy BEFORE the binding so that if the policy
// delete fails, the binding still references it and the transition is
// retried. (A binding referencing a missing policy is inert per the
// Kubernetes API - it does not enforce deny - so ordering only matters
// for the retry/fail-closed contract within this function.)
// Returns nil if both are already absent.
func (r *reconciler) deleteAdmissionPolicy(ctx context.Context) error {
	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	policy.Name = baseAdmissionPolicy.Name
	if err := r.client.Delete(ctx, policy); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete ValidatingAdmissionPolicy %s: %w", policy.Name, err)
	} else if err == nil {
		log.Info("deleted ValidatingAdmissionPolicy", "name", policy.Name)
	}

	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	binding.Name = desiredAdmissionPolicyBinding.Name
	if err := r.client.Delete(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete ValidatingAdmissionPolicyBinding %s: %w", binding.Name, err)
	} else if err == nil {
		log.Info("deleted ValidatingAdmissionPolicyBinding", "name", binding.Name)
	}

	return nil
}

// admissionPolicyUpToDate returns true when the current VAP spec
// matches the desired spec. Uses reflect.DeepEqual on the full Spec
// to detect drift in failurePolicy, validationActions, messages, etc.
func admissionPolicyUpToDate(current, desired *admissionregistrationv1.ValidatingAdmissionPolicy) bool {
	return reflect.DeepEqual(current.Spec, desired.Spec)
}

// admissionPolicyBindingUpToDate returns true when the current binding
// spec matches the desired spec in full.
func admissionPolicyBindingUpToDate(current, desired *admissionregistrationv1.ValidatingAdmissionPolicyBinding) bool {
	return reflect.DeepEqual(current.Spec, desired.Spec)
}
