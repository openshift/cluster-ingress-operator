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

// ensureCurrentCanaryServiceAccount ensures the canary service account exists.
func (r *reconciler) ensureCanaryServiceAccount() (bool, *corev1.ServiceAccount, error) {
	desired := desiredCanaryServiceAccount()
	haveSa, current, err := r.currentCanaryServiceAccount()

	if err != nil {
		return false, nil, err
	}

	switch {
	case !haveSa:
		if err := r.createCanaryServiceAccount(desired); err != nil {
			return false, nil, err
		}
		return r.currentCanaryServiceAccount()
	case haveSa:
		if updated, err := r.updateCanaryServiceAccount(current, desired); err != nil {
			return true, current, err
		} else if updated {
			return r.currentCanaryServiceAccount()
		}
	}
	return true, current, nil
}

// currentCanaryServiceAccount returns the current service account.
func (r *reconciler) currentCanaryServiceAccount() (bool, *corev1.ServiceAccount, error) {
	serviceAccount := &corev1.ServiceAccount{}
	if err := r.client.Get(context.TODO(), controller.CanaryServiceAccountName(), serviceAccount); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, serviceAccount, nil
}

// createCanaryServiceAccount creates the given service account resource.
func (r *reconciler) createCanaryServiceAccount(serviceAccount *corev1.ServiceAccount) error {
	if err := r.client.Create(context.TODO(), serviceAccount); err != nil {
		return fmt.Errorf("failed to create canary service account %s/%s: %w", serviceAccount.Namespace, serviceAccount.Name, err)
	}
	log.Info("created canary service account", "namespace", serviceAccount.Namespace, "name", serviceAccount.Name)
	return nil
}

// updateCanaryServiceaccount updates the canary ServiceAccount if an appropriate
// change has been detected.
func (r *reconciler) updateCanaryServiceAccount(current, desired *corev1.ServiceAccount) (bool, error) {
	changed, updated := canaryServiceAccountChanged(current, desired)
	if !changed {
		return false, nil
	}

	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, fmt.Errorf("failed to update canary service account %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	log.Info("updated canary service account", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)

	return true, nil
}

// desiredServiceAccount returns the desired canary ServiceAccount
// read in from manifests.
func desiredCanaryServiceAccount() *corev1.ServiceAccount {
	serviceAccount := manifests.CanaryServiceAccount()
	name := controller.CanaryServiceAccountName()
	serviceAccount.Name = name.Name
	serviceAccount.Namespace = name.Namespace

	serviceAccount.Labels = map[string]string{
		// associate the service account with the ingress canary controller
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	return serviceAccount
}

// canaryServiceAccountChanged returns true if current and expected differ by the
// service account's secrets, image pull secrets, and/or
// AutomountServiceAccountToken.
func canaryServiceAccountChanged(current, expected *corev1.ServiceAccount) (bool, *corev1.ServiceAccount) {
	currentSpec := SASpecComparison{
		Secrets:                      current.Secrets,
		ImagePullSecrets:             current.ImagePullSecrets,
		AutomountServiceAccountToken: current.AutomountServiceAccountToken,
	}

	expectedSpec := SASpecComparison{
		Secrets:                      expected.Secrets,
		ImagePullSecrets:             expected.ImagePullSecrets,
		AutomountServiceAccountToken: expected.AutomountServiceAccountToken,
	}

	if reflect.DeepEqual(currentSpec, expectedSpec) {
		return false, nil
	}

	return true, expected.DeepCopy()
}

// SASpecComparison is a helper struct for comparing two service account objects.
type SASpecComparison struct {
	Secrets                      []corev1.ObjectReference
	ImagePullSecrets             []corev1.LocalObjectReference
	AutomountServiceAccountToken *bool
}
