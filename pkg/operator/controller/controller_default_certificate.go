package controller

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

// ensureDefaultCertificateForIngress ensures that a default certificate exists
// for a given ClusterIngress.
func (r *reconciler) ensureDefaultCertificateForIngress(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("router-certs-%s", ci.Name),
			Namespace: deployment.Namespace,
		},
	}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, secret)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get secret %s/%s: %v", secret.Namespace, secret.Name, err)
		}

		ca, err := r.getRouterCA()
		if err != nil {
			return fmt.Errorf("failed to get CA certificate: %v", err)
		}
		hostnames := sets.NewString(fmt.Sprintf("*.%s", *ci.Spec.IngressDomain))
		cert, err := ca.MakeServerCert(hostnames, 0)
		if err != nil {
			return fmt.Errorf("failed to make CA: %v", err)
		}

		secret.Type = corev1.SecretTypeTLS
		certBytes, keyBytes, err := cert.GetPEMBytes()
		if err != nil {
			return fmt.Errorf("failed to get certificate from secret %s/%s: %v", secret.Namespace, secret.Name, err)
		}
		secret.Data = map[string][]byte{
			"tls.crt": certBytes,
			"tls.key": keyBytes,
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
		if err := r.Client.Create(context.TODO(), secret); err != nil {
			if !errors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create secret %s/%s: %v", secret.Namespace, secret.Name, err)
			}

			return nil
		}
		logrus.Infof("created secret %s/%s", secret.Namespace, secret.Name)
	}

	return nil
}

// ensureDefaultCertificateDeleted ensures any operator-generated default
// certificate for a given ClusterIngress is deleted.
func (r *reconciler) ensureDefaultCertificateDeleted(deployment *appsv1.Deployment, ci *ingressv1alpha1.ClusterIngress) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("router-certs-%s", ci.Name),
			Namespace: deployment.Namespace,
		},
	}
	err := r.Client.Delete(context.TODO(), secret)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete secret %s/%s: %v", secret.Namespace, secret.Name, err)
	}

	logrus.Infof("deleted secret %s/%s", secret.Namespace, secret.Name)

	return nil
}
