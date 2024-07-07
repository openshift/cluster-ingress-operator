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

func (r *reconciler) ensureRouterCASecret() (*corev1.Secret, error) {
	current, err := r.currentRouterCASecret()
	if err != nil {
		return nil, err
	}
	if current != nil {
		return current, nil
	}
	desired, err := desiredRouterCASecret(r.operatorNamespace)
	if err != nil {
		return nil, err
	}
	if created, err := r.createRouterCASecret(desired); err != nil {
		return nil, fmt.Errorf("failed to create CA secret: %v", err)
	} else if created {
		new, err := r.currentRouterCASecret()
		if err != nil {
			return nil, err
		}
		r.recorder.Event(new, "Normal", "CreatedWildcardCACert", "Created a default wildcard CA certificate")
		return new, nil

	} else if !reflect.DeepEqual(current.Annotations, desired.Annotations) {
		updated := current.DeepCopy()
		updated.Data = desired.Data
		updated.Annotations = desired.Annotations
		if err := r.client.Update(context.TODO(), updated); err != nil {
			return updated, err
		}
	}
	return r.currentRouterCASecret()
}

// currentRouterCASecret returns the current router CA secret.
func (r *reconciler) currentRouterCASecret() (*corev1.Secret, error) {
	name := controller.RouterCASecretName(r.operatorNamespace)
	secret := &corev1.Secret{}
	if err := r.client.Get(context.TODO(), name, secret); err != nil {
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

// desiredRouterCASecret returns the desired router CA secret.
func desiredRouterCASecret(namespace string) (*corev1.Secret, error) {
	certBytes, keyBytes, err := generateRouterCA()
	if err != nil {
		return nil, fmt.Errorf("failed to generate certificate: %v", err)
	}

	name := controller.RouterCASecretName(namespace)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
			Annotations: map[string]string{
				annotations.OpenShiftComponent: "Networking / router",
			},
		},
		Data: map[string][]byte{
			"tls.crt": certBytes,
			"tls.key": keyBytes,
		},
		Type: corev1.SecretTypeTLS,
	}
	return secret, nil
}

// createRouterCASecret creates the router CA secret.
func (r *reconciler) createRouterCASecret(secret *corev1.Secret) (bool, error) {
	if err := r.client.Create(context.TODO(), secret); err != nil {
		if errors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
