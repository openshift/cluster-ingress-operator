package certificatepublisher

import (
	"bytes"
	"context"
	"fmt"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ensureRouterCertsGlobalSecret will create, update, or delete the global
// certificates secret as appropriate.
func (r *reconciler) ensureRouterCertsGlobalSecret(secrets []corev1.Secret, ingresses []operatorv1.IngressController, ingressConfig *configv1.Ingress) error {
	desired, err := desiredRouterCertsGlobalSecret(secrets, ingresses, r.operandNamespace, ingressConfig.Spec.Domain)
	if err != nil {
		return err
	}
	current, err := r.currentRouterCertsGlobalSecret()
	if err != nil {
		return err
	}
	switch {
	case desired == nil && current == nil:
		// Nothing to do.
	case desired == nil && current != nil:
		if deleted, err := r.deleteRouterCertsGlobalSecret(current); err != nil {
			return fmt.Errorf("failed to ensure router certificates secret was unpublished: %v", err)
		} else if deleted {
			r.recorder.Eventf(current, "Normal", "UnpublishedRouterCertificates", "Unpublished router certificates")
		}
	case desired != nil && current == nil:
		if created, err := r.createRouterCertsGlobalSecret(desired); err != nil {
			return fmt.Errorf("failed to ensure router certificates secret was published: %v", err)
		} else if created {
			new, err := r.currentRouterCertsGlobalSecret()
			if err != nil {
				return err
			}
			r.recorder.Eventf(new, "Normal", "PublishedRouterCertificates", "Published router certificates")
		}
	case desired != nil && current != nil:
		if updated, err := r.updateRouterCertsGlobalSecret(current, desired); err != nil {
			return fmt.Errorf("failed to update published router certificates secret: %v", err)
		} else if updated {
			r.recorder.Eventf(current, "Normal", "UpdatedPublishedRouterCertificates", "Updated the published router certificates")
		}
	}
	return nil
}

// desiredRouterCertsGlobalSecret returns the desired router-certs global
// secret.
func desiredRouterCertsGlobalSecret(secrets []corev1.Secret, ingresses []operatorv1.IngressController, operandNamespace, clusterIngressDomain string) (*corev1.Secret, error) {
	for i := range ingresses {
		// The authentication operator only requires the certificate and
		// key for the ingresscontroller that handles the cluster
		// ingress domain, and publishing certificates for all
		// ingresscontrollers could cause the secret's size to exceed
		// the maximum secret size of 1 mebibyte.  See
		// <https://issues.redhat.com/browse/OCPBUGS-853>.
		if ingresses[i].Status.Domain != clusterIngressDomain {
			continue
		}

		cert := getDefaultCertificateSecretForIngressController(&ingresses[i], secrets, operandNamespace)
		if cert == nil {
			break
		}

		globalCertName := controller.RouterCertsGlobalSecretName()
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      globalCertName.Name,
				Namespace: globalCertName.Namespace,
			},
			Data: map[string][]byte{
				ingresses[i].Status.Domain: bytes.Join([][]byte{
					cert.Data["tls.crt"],
					cert.Data["tls.key"],
				}, nil),
			},
		}, nil
	}

	return nil, nil
}

// getDefaultCertificateSecretForIngressController returns the appropriate
// secret to use for the given ingresscontroller out of the provided list of
// secrets.  If the ingresscontroller does not specify a secret or specifies a
// secret that doesn't exist, the operator-generated default certificate is
// returned.
//
// Note that if ingress.Spec.DefaultCertificate is updated to point to a
// non-existent secret, the certificate controller does not delete the
// operator-generated default certificate, so it is safe to fall back to using
// it.  See <https://bugzilla.redhat.com/show_bug.cgi?id=1887441>.
//
// Note also that defaultCertName can be the same as customCertName; see
// <https://bugzilla.redhat.com/show_bug.cgi?id=1912922>.
func getDefaultCertificateSecretForIngressController(ic *operatorv1.IngressController, secrets []corev1.Secret, operandNamespace string) *corev1.Secret {
	var (
		defaultCertName         = controller.RouterOperatorGeneratedDefaultCertificateSecretName(ic, operandNamespace)
		customCertName          = ic.Spec.DefaultCertificate
		defaultCert, customCert *corev1.Secret
	)
	for i := range secrets {
		if customCertName != nil && customCertName.Name == secrets[i].Name {
			customCert = &secrets[i]
		}
		if defaultCertName.Name == secrets[i].Name {
			defaultCert = &secrets[i]
		}
	}
	// Prefer any custom certificate, and fall back to the default
	// certificate if no custom certificate was specified or the specified
	// one was not found.
	if customCert != nil {
		return customCert
	}
	return defaultCert
}

// currentRouterCertsGlobalSecret returns the current router-certs global
// secret.
func (r *reconciler) currentRouterCertsGlobalSecret() (*corev1.Secret, error) {
	name := controller.RouterCertsGlobalSecretName()
	secret := &corev1.Secret{}
	if err := r.client.Get(context.TODO(), name, secret); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secret, nil
}

// createRouterCertsGlobalSecret creates a router-certs global secret.  Returns
// true if the secret was created, false otherwise.
func (r *reconciler) createRouterCertsGlobalSecret(secret *corev1.Secret) (bool, error) {
	if err := r.client.Create(context.TODO(), secret); err != nil {
		if errors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// updateRouterCertsGlobalSecret updates the router-certs global secret.
// Returns true if the secret was updated, false otherwise.
func (r *reconciler) updateRouterCertsGlobalSecret(current, desired *corev1.Secret) (bool, error) {
	if routerCertsSecretsEqual(current, desired) {
		return false, nil
	}
	updated := current.DeepCopy()
	updated.Data = desired.Data
	if err := r.client.Update(context.TODO(), updated); err != nil {
		if errors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// deleteRouterCertsGlobalSecret deletes the router-certs global secret.
// Returns true if the secret was deleted, false otherwise.
func (r *reconciler) deleteRouterCertsGlobalSecret(secret *corev1.Secret) (bool, error) {
	if err := r.client.Delete(context.TODO(), secret); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// routerCertsSecretsEqual compares two router-certs secrets.  Returns true if
// the secrets should be considered equal for the purpose of determining whether
// an update is necessary, false otherwise
func routerCertsSecretsEqual(a, b *corev1.Secret) bool {
	if !reflect.DeepEqual(a.Data, b.Data) {
		return false
	}
	return true
}
