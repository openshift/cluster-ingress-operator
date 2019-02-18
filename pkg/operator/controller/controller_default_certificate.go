package controller

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/openshift/library-go/pkg/crypto"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

// routerDefaultCertificateSecretName returns the namespaced name for the router
// default certificate secret.
func routerDefaultCertificateSecretName(ci *ingressv1alpha1.ClusterIngress, namespace string) types.NamespacedName {
	return types.NamespacedName{
		Namespace: namespace,
		Name:      fmt.Sprintf("router-certs-%s", ci.Name),
	}
}

// ensureDefaultCertificateForIngress creates or deletes an operator-generated
// default certificate for a given ClusterIngress as appropriate.
func (r *reconciler) ensureDefaultCertificateForIngress(caSecret *corev1.Secret, deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) error {
	ca, err := crypto.GetCAFromBytes(caSecret.Data["tls.crt"], caSecret.Data["tls.key"])
	if err != nil {
		return fmt.Errorf("failed to get CA from secret %s/%s: %v", caSecret.Namespace, caSecret.Name, err)
	}
	desired, err := desiredRouterDefaultCertificateSecret(ca, deployment, ci)
	if err != nil {
		return err
	}
	current, err := r.currentRouterDefaultCertificate(ci, deployment)
	if err != nil {
		return err
	}
	switch {
	case desired == nil && current == nil:
		// Nothing to do.
	case desired == nil && current != nil:
		if err := r.deleteRouterDefaultCertificate(current); err != nil {
			return fmt.Errorf("failed to delete default certificate: %v", err)
		}
	case desired != nil && current == nil:
		if err := r.createRouterDefaultCertificate(desired); err != nil {
			return fmt.Errorf("failed to create default certificate: %v", err)
		}
	case desired != nil && current != nil:
		// TODO Update if CA certificate changed.
	}
	return nil
}

// desiredRouterDefaultCertificateSecret returns the desired default certificate
// secret.
func desiredRouterDefaultCertificateSecret(ca *crypto.CA, deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) (*corev1.Secret, error) {
	if ci.Spec.DefaultCertificateSecret != nil {
		return nil, nil
	}

	hostnames := sets.NewString(fmt.Sprintf("*.%s", *ci.Spec.IngressDomain))
	cert, err := ca.MakeServerCert(hostnames, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to make certificate: %v", err)
	}

	certBytes, keyBytes, err := cert.GetPEMBytes()
	if err != nil {
		return nil, fmt.Errorf("failed to encode certificate: %v", err)
	}

	name := routerDefaultCertificateSecretName(ci, deployment.Namespace)
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
	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
		Controller: &trueVar,
	}
	secret.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
	return secret, nil
}

// currentRouterDefaultCertificate returns the current router default
// certificate secret.
func (r *reconciler) currentRouterDefaultCertificate(ci *ingressv1alpha1.ClusterIngress, deployment *appsv1.Deployment) (*corev1.Secret, error) {
	name := routerDefaultCertificateSecretName(ci, deployment.Namespace)
	secret := &corev1.Secret{}
	if err := r.Client.Get(context.TODO(), name, secret); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secret, nil
}

// createRouterDefaultCertificate creates a router default certificate secret.
func (r *reconciler) createRouterDefaultCertificate(secret *corev1.Secret) error {
	if err := r.Client.Create(context.TODO(), secret); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	logrus.Infof("created secret %s/%s", secret.Namespace, secret.Name)
	return nil
}

// deleteRouterDefaultCertificate deletes the router default certificate secret.
func (r *reconciler) deleteRouterDefaultCertificate(secret *corev1.Secret) error {
	if err := r.Client.Delete(context.TODO(), secret); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	logrus.Infof("deleted secret %s/%s", secret.Namespace, secret.Name)
	return nil
}
