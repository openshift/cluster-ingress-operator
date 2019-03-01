package certificate

import (
	"context"
	"fmt"

	"github.com/openshift/library-go/pkg/crypto"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// ensureDefaultCertificateForIngress creates or deletes an operator-generated
// default certificate for a given ClusterIngress as appropriate.  Returns true
// if it creates, updates, or deletes the secret for the certificate, false
// otherwise.
func (r *reconciler) ensureDefaultCertificateForIngress(caSecret *corev1.Secret, namespace string, deploymentRef metav1.OwnerReference, ci *ingressv1alpha1.ClusterIngress) (bool, error) {
	ca, err := crypto.GetCAFromBytes(caSecret.Data["tls.crt"], caSecret.Data["tls.key"])
	if err != nil {
		return false, fmt.Errorf("failed to get CA from secret %s/%s: %v", caSecret.Namespace, caSecret.Name, err)
	}
	desired, err := desiredRouterDefaultCertificateSecret(ca, namespace, deploymentRef, ci)
	if err != nil {
		return false, err
	}
	current, err := r.currentRouterDefaultCertificate(ci, namespace)
	if err != nil {
		return false, err
	}
	switch {
	case desired == nil && current == nil:
		// Nothing to do.
	case desired == nil && current != nil:
		if deleted, err := r.deleteRouterDefaultCertificate(current); err != nil {
			return false, fmt.Errorf("failed to delete default certificate: %v", err)
		} else if deleted {
			r.recorder.Eventf(ci, "Normal", "DeletedDefaultCertificate", "Deleted default wildcard certificate %q", current.Name)
			return true, nil
		}
	case desired != nil && current == nil:
		if created, err := r.createRouterDefaultCertificate(desired); err != nil {
			return false, fmt.Errorf("failed to create default certificate: %v", err)
		} else if created {
			r.recorder.Eventf(ci, "Normal", "CreatedDefaultCertificate", "Created default wildcard certificate %q", desired.Name)
			return true, nil
		}
	case desired != nil && current != nil:
		// TODO Update if CA certificate changed.
	}
	return false, nil
}

// desiredRouterDefaultCertificateSecret returns the desired default certificate
// secret.
func desiredRouterDefaultCertificateSecret(ca *crypto.CA, namespace string, deploymentRef metav1.OwnerReference, ci *ingressv1alpha1.ClusterIngress) (*corev1.Secret, error) {
	// Without an ingress domain, we cannot generate a default certificate.
	if len(ci.Status.IngressDomain) == 0 {
		return nil, nil
	}

	// If the clusteringress specifies a default certificate secret, the
	// operator does not need to generate a certificate.
	if ci.Spec.DefaultCertificateSecret != nil {
		return nil, nil
	}

	hostnames := sets.NewString(fmt.Sprintf("*.%s", ci.Status.IngressDomain))
	cert, err := ca.MakeServerCert(hostnames, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to make certificate: %v", err)
	}

	certBytes, keyBytes, err := cert.GetPEMBytes()
	if err != nil {
		return nil, fmt.Errorf("failed to encode certificate: %v", err)
	}

	name := controller.RouterDefaultCertificateSecretName(ci, namespace)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certBytes,
			"tls.key": keyBytes,
		},
	}
	secret.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
	return secret, nil
}

// currentRouterDefaultCertificate returns the current router default
// certificate secret.
func (r *reconciler) currentRouterDefaultCertificate(ci *ingressv1alpha1.ClusterIngress, namespace string) (*corev1.Secret, error) {
	name := controller.RouterDefaultCertificateSecretName(ci, namespace)
	secret := &corev1.Secret{}
	if err := r.client.Get(context.TODO(), name, secret); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secret, nil
}

// createRouterDefaultCertificate creates a router default certificate secret.
// Returns true if the secret was newly created, otherwise returns false.
func (r *reconciler) createRouterDefaultCertificate(secret *corev1.Secret) (bool, error) {
	if err := r.client.Create(context.TODO(), secret); err != nil {
		if errors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// deleteRouterDefaultCertificate deletes the router default certificate secret.
// Returns true if the secret was deleted, otherwise returns false.
func (r *reconciler) deleteRouterDefaultCertificate(secret *corev1.Secret) (bool, error) {
	if err := r.client.Delete(context.TODO(), secret); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
