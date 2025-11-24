package canary

import (
	"context"
	"fmt"
	"reflect"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
)

// ensureCanaryServiceAccount ensures the canary service account exists.
func (r *reconciler) ensureCanaryServiceAccount(ctx context.Context) (bool, *corev1.ServiceAccount, error) {
	desired := desiredCanaryServiceAccount()
	haveSa, current, err := r.currentCanaryServiceAccount(ctx)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !haveSa:
		if err := r.createCanaryServiceAccount(ctx, desired); err != nil {
			return false, nil, err
		}
		return r.currentCanaryServiceAccount(ctx)
	case haveSa:
		if updated, err := r.updateCanaryServiceAccount(ctx, current, desired); err != nil {
			return true, current, err
		} else if updated {
			return r.currentCanaryServiceAccount(ctx)
		}
	}
	return true, current, nil
}

// currentCanaryServiceAccount returns the current service account.
func (r *reconciler) currentCanaryServiceAccount(ctx context.Context) (bool, *corev1.ServiceAccount, error) {
	serviceAccount := &corev1.ServiceAccount{}
	if err := r.client.Get(ctx, controller.CanaryServiceAccountName(), serviceAccount); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, serviceAccount, nil
}

// createCanaryServiceAccount creates the given service account resource.
func (r *reconciler) createCanaryServiceAccount(ctx context.Context, serviceAccount *corev1.ServiceAccount) error {
	if err := r.client.Create(ctx, serviceAccount); err != nil {
		return fmt.Errorf("failed to create canary service account %s/%s: %w", serviceAccount.Namespace, serviceAccount.Name, err)
	}
	log.Info("created canary service account", "namespace", serviceAccount.Namespace, "name", serviceAccount.Name)
	return nil
}

// updateCanaryServiceAccount updates the canary ServiceAccount if an appropriate
// change has been detected.
func (r *reconciler) updateCanaryServiceAccount(ctx context.Context, current, desired *corev1.ServiceAccount) (bool, error) {
	changed, updated := canaryServiceAccountChanged(current, desired)
	if !changed {
		return false, nil
	}

	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(ctx, updated); err != nil {
		return false, fmt.Errorf("failed to update canary service account %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	log.Info("updated canary service account", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)

	return true, nil
}

// desiredCanaryServiceAccount returns the desired canary ServiceAccount
// read in from manifests.
func desiredCanaryServiceAccount() *corev1.ServiceAccount {
	serviceAccount := manifests.CanaryServiceAccount()
	name := controller.CanaryServiceAccountName()
	serviceAccount.Name = name.Name
	serviceAccount.Namespace = name.Namespace

	serviceAccount.Labels = map[string]string{
		// Associate the service account with the ingress canary controller.
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	return serviceAccount
}

// canaryServiceAccountChanged returns true if current and expected differ by the
// service account's AutomountServiceAccountToken.
func canaryServiceAccountChanged(current, expected *corev1.ServiceAccount) (bool, *corev1.ServiceAccount) {
	if reflect.DeepEqual(current.AutomountServiceAccountToken, expected.AutomountServiceAccountToken) {
		return false, nil
	}

	// Bring over changed field (AutomountServiceAccountToken)
	// from expected to current.
	// This way we do not bring over any unwanted changes.
	currentCopy := current.DeepCopy()

	currentCopy.AutomountServiceAccountToken = expected.AutomountServiceAccountToken

	return true, currentCopy.DeepCopy()
}
