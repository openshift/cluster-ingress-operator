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

// ensureRouterCACertificateSecret ensures a CA certificate secret exists that
// can be used to sign the default certificates for ClusterIngresses.
func (r *reconciler) ensureRouterCACertificateSecret() (*corev1.Secret, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caCertSecretName,
			Namespace: r.Namespace,
		},
	}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, secret)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get secret %s/%s: %v", secret.Namespace, secret.Name, err)
		}

		// TODO Use certrotationcontroller from library-go.
		signerName := fmt.Sprintf("%s@%d", "cluster-ingress-operator", time.Now().Unix())
		caConfig, err := crypto.MakeCAConfig(signerName, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to make CA config: %v", err)
		}

		secret.Type = corev1.SecretTypeTLS
		certBytes, keyBytes, err := caConfig.GetPEMBytes()
		if err != nil {
			return nil, fmt.Errorf("failed to get certificate: %v", err)
		}
		secret.Data = map[string][]byte{
			"tls.crt": certBytes,
			"tls.key": keyBytes,
		}
		if err := r.Client.Create(context.TODO(), secret); err != nil {
			return nil, fmt.Errorf("failed to create secret %s/%s: %v", secret.Namespace, secret.Name, err)
		}

		logrus.Infof("created secret %s/%s", secret.Namespace, secret.Name)
	}

	return secret, nil
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
