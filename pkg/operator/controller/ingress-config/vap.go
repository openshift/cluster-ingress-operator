package ingressconfig

import (
	"context"
	"fmt"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const gatewayAPIVAPName = "openshift-ingress-operator-gatewayapi-crd-admission"

// desiredVAP returns the ValidatingAdmissionPolicy manifest for this cluster profile.
func (r *reconciler) desiredVAP() *admissionregistrationv1.ValidatingAdmissionPolicy {
	var policy *admissionregistrationv1.ValidatingAdmissionPolicy
	if r.config.IBMCloudManaged {
		policy = manifests.GatewayAPIVAPIBM()
	} else {
		policy = manifests.GatewayAPIVAP()
	}
	policy = policy.DeepCopy()
	if len(policy.Spec.Validations) > 0 {
		policy.Spec.Validations[0].Expression = ingressOperatorServiceAccountValidation(
			r.config.OperatorNamespace,
		)
	}
	return policy
}

func ingressOperatorServiceAccountValidation(operatorNamespace string) string {
	username := fmt.Sprintf("system:serviceaccount:%s:ingress-operator", operatorNamespace)
	return fmt.Sprintf("has(request.userInfo.username) && (request.userInfo.username == %q)", username)
}

// desiredVAPBinding returns the ValidatingAdmissionPolicyBinding for Gateway API CRD protection.
func (r *reconciler) desiredVAPBinding() *admissionregistrationv1.ValidatingAdmissionPolicyBinding {
	return manifests.GatewayAPIVAPBinding()
}

// ensureGatewayAPIVAP applies the Gateway API CRD ValidatingAdmissionPolicy and binding.
func (r *reconciler) ensureGatewayAPIVAP(ctx context.Context) error {
	cache := resourceapply.NewResourceCache()
	policy := r.desiredVAP()
	if _, _, err := resourceapply.ApplyValidatingAdmissionPolicyV1(ctx, r.kclient.AdmissionregistrationV1(), r.eventRecorder, policy, cache); err != nil {
		return fmt.Errorf("failed to apply Gateway API VAP: %w", err)
	}
	binding := r.desiredVAPBinding()
	if _, _, err := resourceapply.ApplyValidatingAdmissionPolicyBindingV1(ctx, r.kclient.AdmissionregistrationV1(), r.eventRecorder, binding, cache); err != nil {
		return fmt.Errorf("failed to apply Gateway API VAP binding: %w", err)
	}
	return nil
}

// deleteGatewayAPIVAP removes the VAP binding first, then the policy.
func (r *reconciler) deleteGatewayAPIVAP(ctx context.Context) error {
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: gatewayAPIVAPName},
	}
	if _, _, err := resourceapply.DeleteValidatingAdmissionPolicyBindingV1(ctx, r.kclient.AdmissionregistrationV1(), r.eventRecorder, binding); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete Gateway API VAP binding: %w", err)
	}
	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: gatewayAPIVAPName},
	}
	if _, _, err := resourceapply.DeleteValidatingAdmissionPolicyV1(ctx, r.kclient.AdmissionregistrationV1(), r.eventRecorder, policy); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete Gateway API VAP: %w", err)
	}
	return nil
}

// gatewayAPIVAPPresent reports whether the Gateway API CRD VAP exists on the cluster.
func (r *reconciler) gatewayAPIVAPPresent(ctx context.Context) (bool, error) {
	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: gatewayAPIVAPName}, policy); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
