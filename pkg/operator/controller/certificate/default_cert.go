package certificate

import (
	"context"
	"fmt"
	"reflect"

	"github.com/openshift/library-go/pkg/crypto"

	"github.com/openshift/api/annotations"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	routerDefaultCertificateDescription = "Serving certificate for the default ingress controller, managed by the ingress operator."
	routerCADescription                 = "CA certificate used by the ingress operator to sign default serving certificates for ingress controllers."
)

// ensureDefaultCertificateForIngress creates or deletes an operator-generated
// default certificate for a given IngressController as appropriate.  Returns true
// if it the secret exists, or false if it does not, as well as any errors.
func (r *reconciler) ensureDefaultCertificateForIngress(ctx context.Context, caSecret *corev1.Secret, namespace string, deploymentRef metav1.OwnerReference, ci *operatorv1.IngressController) (bool, error) {
	ca, err := crypto.GetCAFromBytes(caSecret.Data["tls.crt"], caSecret.Data["tls.key"])
	if err != nil {
		return false, fmt.Errorf("failed to get CA from secret %s/%s: %w", caSecret.Namespace, caSecret.Name, err)
	}
	wantCert, desired, err := desiredRouterDefaultCertificateSecret(ca, namespace, deploymentRef, ci)
	if err != nil {
		return false, err
	}
	if !wantCert {
		// If the operator generated certificate is not being used, ensure that the ingress controller's
		// Spec.DefaultCertificate secret exists before deleting the operator generated secret.
		// See https://bugzilla.redhat.com/show_bug.cgi?id=1887441
		err := r.lookupUserSpecifiedRouterDefaultCertificate(ctx, ci, namespace)
		if err != nil {
			return false, fmt.Errorf("failed to lookup user specified default certificate: %w", err)
		}
	}

	haveCert, current, err := r.currentRouterDefaultCertificate(ctx, ci, namespace)
	if err != nil {
		return false, err
	}
	switch {
	case !wantCert && !haveCert:
		// Nothing to do.
	case !wantCert && haveCert:
		if deleted, err := r.deleteRouterDefaultCertificate(ctx, current); err != nil {
			return true, fmt.Errorf("failed to delete default certificate: %w", err)
		} else if deleted {
			log.Info("Deleted default wildcard certificate secret", "namespace", current.Namespace, "name", current.Name)
			r.recorder.Eventf(ci, "Normal", "DeletedDefaultCertificate", "Deleted default wildcard certificate %q", current.Name)
			return false, nil
		}
	case wantCert && !haveCert:
		if created, err := r.createRouterDefaultCertificate(ctx, desired); err != nil {
			return false, fmt.Errorf("failed to create default certificate: %w", err)
		} else if created {
			log.Info("Created default wildcard certificate secret", "namespace", desired.Namespace, "name", desired.Name)
			r.recorder.Eventf(ci, "Normal", "CreatedDefaultCertificate", "Created default wildcard certificate %q", desired.Name)
			return true, nil
		}
	case wantCert && haveCert:
		// Reconcile the operator-managed TLS metadata annotations onto the
		// existing secret so that a secret created by a release that did not
		// set them (for example, before an upgrade) gets them.  The
		// certificate data and any unrelated annotations are preserved.
		//
		// TODO: Update the certificate if the CA certificate changed.
		if updated, err := r.updateRouterDefaultCertificateAnnotations(ctx, current, desired); err != nil {
			return true, fmt.Errorf("failed to update default certificate: %w", err)
		} else if updated {
			log.Info("Updated default wildcard certificate secret annotations", "namespace", current.Namespace, "name", current.Name)
			r.recorder.Eventf(ci, "Normal", "UpdatedDefaultCertificate", "Updated default wildcard certificate %q", current.Name)
		}
		return true, nil
	}
	return false, nil
}

// desiredRouterDefaultCertificateSecret returns the desired default certificate
// secret.
func desiredRouterDefaultCertificateSecret(ca *crypto.CA, namespace string, deploymentRef metav1.OwnerReference, ci *operatorv1.IngressController) (bool, *corev1.Secret, error) {
	// Without an ingress domain, we cannot generate a default certificate.
	if len(ci.Status.Domain) == 0 {
		return false, nil, nil
	}

	name := controller.RouterOperatorGeneratedDefaultCertificateSecretName(ci, namespace)

	// If the ingresscontroller specifies a default certificate secret, the
	// operator does not need to generate a certificate, unless the specified
	// secret name redundantly corresponds to the operator generated secret.
	if ci.Spec.DefaultCertificate != nil && ci.Spec.DefaultCertificate.Name != name.Name {
		return false, nil, nil
	}

	hostnames := sets.New(fmt.Sprintf("*.%s", ci.Status.Domain))
	cert, err := ca.MakeServerCert(hostnames, 0)
	if err != nil {
		return false, nil, fmt.Errorf("failed to make certificate: %v", err)
	}

	certBytes, keyBytes, err := cert.GetPEMBytes()
	if err != nil {
		return false, nil, fmt.Errorf("failed to encode certificate: %v", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
			Annotations: map[string]string{
				annotations.OpenShiftComponent:   controller.RouterTLSOwningComponent,
				annotations.OpenShiftDescription: routerDefaultCertificateDescription,
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certBytes,
			"tls.key": keyBytes,
		},
	}
	secret.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
	return true, secret, nil
}

// currentRouterDefaultCertificate returns the current router default
// certificate secret.
func (r *reconciler) currentRouterDefaultCertificate(ctx context.Context, ci *operatorv1.IngressController, namespace string) (bool, *corev1.Secret, error) {
	name := controller.RouterOperatorGeneratedDefaultCertificateSecretName(ci, namespace)
	secret := &corev1.Secret{}
	if err := r.client.Get(ctx, name, secret); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, secret, nil
}

// createRouterDefaultCertificate creates a router default certificate secret.
// Returns true if the secret was newly created, otherwise returns false.
func (r *reconciler) createRouterDefaultCertificate(ctx context.Context, secret *corev1.Secret) (bool, error) {
	if err := r.client.Create(ctx, secret); err != nil {
		return false, err
	}
	return true, nil
}

// deleteRouterDefaultCertificate deletes the router default certificate secret.
// Returns true if the secret was deleted, otherwise returns false.
func (r *reconciler) deleteRouterDefaultCertificate(ctx context.Context, secret *corev1.Secret) (bool, error) {
	if err := r.client.Delete(ctx, secret); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// lookupUserSpecifiedRouterDefaultCertificate checks to see if the given ingress controller's
// Spec.DefaultCertificate field corresponds to an existing secret. This function assumes that
// ci.Spec.DefaultCertificate is not nil.
func (r *reconciler) lookupUserSpecifiedRouterDefaultCertificate(ctx context.Context, ci *operatorv1.IngressController, namespace string) error {
	secret := &corev1.Secret{}
	name := controller.RouterEffectiveDefaultCertificateSecretName(ci, namespace)
	if err := r.client.Get(ctx, name, secret); err != nil {
		return err
	}
	return nil
}

// updateRouterDefaultCertificateAnnotations reconciles the operator-managed
// annotations from the desired default certificate secret onto the current
// secret.  Any annotation that the desired secret sets is operator-managed by
// definition, so every desired annotation is copied onto the current secret;
// unrelated annotations and the certificate data are preserved.  It returns
// true if it updated the secret.
func (r *reconciler) updateRouterDefaultCertificateAnnotations(ctx context.Context, current, desired *corev1.Secret) (bool, error) {
	updated := current.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	for key, value := range desired.Annotations {
		updated.Annotations[key] = value
	}
	if reflect.DeepEqual(updated.Annotations, current.Annotations) {
		return false, nil
	}
	if err := r.client.Update(ctx, updated); err != nil {
		return false, err
	}
	return true, nil
}
