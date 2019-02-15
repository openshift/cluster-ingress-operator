package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/openshift/library-go/pkg/crypto"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// caCertSecretName is the name of the secret that holds the CA certificate
	// that the operator will use to create default certificates for
	// clusteringresses.
	caCertSecretName = "router-ca"
)

// routerCASecretName returns the namespaced name for the router CA secret.
func routerCASecretName(namespace string) types.NamespacedName {
	return types.NamespacedName{
		Namespace: namespace,
		Name:      caCertSecretName,
	}
}

// ensureRouterCACertificateSecret ensures a CA certificate secret exists that
// can be used to sign the default certificates for ClusterIngresses.
func (r *reconciler) ensureRouterCACertificateSecret() (*corev1.Secret, error) {
	current, err := r.currentRouterCASecret()
	if err != nil {
		return nil, err
	}
	if current != nil {
		return current, nil
	}
	desired, err := desiredRouterCASecret(r.Namespace)
	if err != nil {
		return nil, err
	}
	if err := r.createRouterCASecret(desired); err != nil {
		return nil, fmt.Errorf("failed to create router CA secret: %v", err)
	}
	return r.currentRouterCASecret()
}

// currentRouterCASecret returns the current router CA secret.
func (r *reconciler) currentRouterCASecret() (*corev1.Secret, error) {
	name := routerCASecretName(r.Namespace)
	secret := &corev1.Secret{}
	if err := r.Client.Get(context.TODO(), name, secret); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secret, nil
}

// desiredRouterCASecret returns the desired router CA secret.  Note: Because
// the secret has a generated certificate, this function is non-deterministic.
func desiredRouterCASecret(namespace string) (*corev1.Secret, error) {
	// TODO Use certrotationcontroller from library-go.
	signerName := fmt.Sprintf("%s@%d", "cluster-ingress-operator", time.Now().Unix())
	caConfig, err := crypto.MakeCAConfig(signerName, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to make CA config: %v", err)
	}

	certBytes, keyBytes, err := caConfig.GetPEMBytes()
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate: %v", err)
	}

	name := routerCASecretName(namespace)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
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
func (r *reconciler) createRouterCASecret(secret *corev1.Secret) error {
	if err := r.Client.Create(context.TODO(), secret); err != nil {
		return err
	}
	logrus.Infof("created secret %s/%s", secret.Namespace, secret.Name)
	return nil
}

// getRouterCA gets the CA, or creates it if it does not already exist.
func (r *reconciler) getRouterCA() (*crypto.CA, error) {
	secret, err := r.ensureRouterCACertificateSecret()
	if err != nil {
		return nil, fmt.Errorf("failed to get CA secret: %v", err)
	}

	certBytes := secret.Data["tls.crt"]
	keyBytes := secret.Data["tls.key"]

	ca, err := crypto.GetCAFromBytes(certBytes, keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to get CA from secret %s/%s: %v", secret.Namespace, secret.Name, err)
	}

	return ca, nil
}
