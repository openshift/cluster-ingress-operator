package certificate

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"reflect"
	"time"

	"github.com/openshift/api/annotations"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *reconciler) ensureRouterCASecret(ctx context.Context) (*corev1.Secret, error) {
	current, err := r.currentRouterCASecret(ctx)
	if err != nil {
		return nil, err
	}

	if current != nil {
		// A CA secret already exists.  Reuse its certificate and key, and
		// reconcile only the operator-managed TLS metadata annotations so
		// that a secret created by a release that predates them (for
		// example, before an upgrade) gets them.  The certificate data and
		// any unrelated annotations are preserved; the CA is never
		// regenerated for an existing secret.
		desired, err := desiredRouterCASecret(r.operatorNamespace, current.Data["tls.crt"], current.Data["tls.key"])
		if err != nil {
			return nil, err
		}
		if updated, err := r.updateRouterCASecretAnnotations(ctx, current, desired); err != nil {
			return nil, fmt.Errorf("failed to update CA secret: %w", err)
		} else if updated {
			log.Info("Updated default wildcard CA certificate secret annotations", "namespace", current.Namespace, "name", current.Name)
			r.recorder.Event(current, "Normal", "UpdatedWildcardCACert", "Updated default wildcard CA certificate annotations")
			return r.currentRouterCASecret(ctx)
		}
		return current, nil
	}

	// No CA secret exists yet, so generate a new CA.  Generating a CA is
	// expensive and non-deterministic, which is why it is done here, only when
	// there is no existing secret, rather than inside desiredRouterCASecret.
	caCert, caKey, err := generateRouterCA()
	if err != nil {
		return nil, fmt.Errorf("failed to generate CA certificate: %w", err)
	}
	desired, err := desiredRouterCASecret(r.operatorNamespace, caCert, caKey)
	if err != nil {
		return nil, err
	}
	if created, err := r.createRouterCASecret(ctx, desired); err != nil {
		return nil, fmt.Errorf("failed to create CA secret: %w", err)
	} else if created {
		new, err := r.currentRouterCASecret(ctx)
		if err != nil {
			return nil, err
		}
		log.Info("Created default wildcard CA certificate secret", "namespace", new.Namespace, "name", new.Name)
		r.recorder.Event(new, "Normal", "CreatedWildcardCACert", "Created a default wildcard CA certificate")
		return new, nil
	}
	return r.currentRouterCASecret(ctx)
}

// updateRouterCASecretAnnotations reconciles the operator-managed annotations
// from the desired router CA secret onto the current secret.  Any annotation
// that the desired secret sets is operator-managed by definition, so every
// desired annotation is copied onto the current secret; unrelated annotations
// and the certificate data are preserved.  It returns true if it updated the
// secret.
func (r *reconciler) updateRouterCASecretAnnotations(ctx context.Context, current, desired *corev1.Secret) (bool, error) {
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

// currentRouterCASecret returns the current router CA secret.
func (r *reconciler) currentRouterCASecret(ctx context.Context) (*corev1.Secret, error) {
	name := controller.RouterCASecretName(r.operatorNamespace)
	secret := &corev1.Secret{}
	if err := r.client.Get(ctx, name, secret); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secret, nil
}

// generateRouterCA generates and returns a CA certificate and key.
func generateRouterCA() ([]byte, []byte, error) {
	signerName := fmt.Sprintf("%s@%d", "ingress-operator", time.Now().Unix())

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate key: %v", err)
	}

	root := &x509.Certificate{
		Subject: pkix.Name{CommonName: signerName},

		SignatureAlgorithm: x509.SHA256WithRSA,

		NotBefore:    time.Now().Add(-1 * time.Second),
		NotAfter:     time.Now().Add(2 * 365 * 24 * time.Hour),
		SerialNumber: big.NewInt(1),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,

		IsCA: true,

		// Don't allow the CA to be used to make another CA.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, root, root, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %v", err)
	}

	certs, err := x509.ParseCertificates(derBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse certificate: %v", err)
	}

	if len(certs) != 1 {
		return nil, nil, fmt.Errorf("expected a single certificate")
	}

	certBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certs[0].Raw,
	})

	keyBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	return certBytes, keyBytes, nil
}

// desiredRouterCASecret returns the desired router CA secret for the given
// namespace using the provided CA certificate and key.
//
// This function deviates from the usual ensureFoo/desiredFoo convention in
// which desiredFoo computes the entire desired state by itself.  Generating a
// CA certificate is both expensive and non-deterministic, so the certificate
// and key are generated by the caller (ensureRouterCASecret) only when no CA
// secret exists yet and are passed in here.  Reconciling an existing secret,
// for example to add annotations, therefore never regenerates the CA.
func desiredRouterCASecret(namespace string, caCert, caKey []byte) (*corev1.Secret, error) {
	name := controller.RouterCASecretName(namespace)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
			Annotations: map[string]string{
				annotations.OpenShiftComponent:   controller.RouterTLSOwningComponent,
				annotations.OpenShiftDescription: routerCADescription,
			},
		},
		Data: map[string][]byte{
			"tls.crt": caCert,
			"tls.key": caKey,
		},
		Type: corev1.SecretTypeTLS,
	}
	return secret, nil
}

// createRouterCASecret creates the router CA secret.
func (r *reconciler) createRouterCASecret(ctx context.Context, secret *corev1.Secret) (bool, error) {
	if err := r.client.Create(ctx, secret); err != nil {
		if errors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
