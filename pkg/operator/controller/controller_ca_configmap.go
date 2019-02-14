package controller

import (
	"bytes"
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// GlobalMachineSpecifiedConfigNamespace is the location for global
	// config.  In particular, the operator will put the configmap with the
	// CA certificate in this namespace.
	GlobalMachineSpecifiedConfigNamespace = "openshift-config-managed"

	// caCertConfigMapName is the name of the config map with the public key
	// for the CA certificate, which the operator publishes for other
	// operators to use.
	caCertConfigMapName = "router-ca"
)

// shouldPublishRouterCA checks if some ClusterIngress uses the default
// certificate, in which case the CA certificate needs to be published.
func shouldPublishRouterCA(ingresses []ingressv1alpha1.ClusterIngress) bool {
	for _, ci := range ingresses {
		if ci.Spec.DefaultCertificateSecret == nil {
			return true
		}
	}
	return false
}

// ensureRouterCAIsPublished ensures a configmap exists with the CA certificate
// in a well known namespace.
func (r *reconciler) ensureRouterCAIsPublished() error {
	secret, err := r.ensureRouterCACertificateSecret()
	if err != nil {
		return fmt.Errorf("failed to get CA secret: %v", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caCertConfigMapName,
			Namespace: GlobalMachineSpecifiedConfigNamespace,
		},
	}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, cm)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get configmap %s/%s: %v", cm.Namespace, cm.Name, err)
		}

		cm.Data = map[string]string{"ca-bundle.crt": string(secret.Data["tls.crt"])}
		if err := r.Client.Create(context.TODO(), cm); err != nil {
			if errors.IsAlreadyExists(err) {
				return nil
			}

			return fmt.Errorf("failed to create configmap %s/%s: %v", cm.Namespace, cm.Name, err)
		}

		logrus.Infof("created configmap %s/%s", cm.Namespace, cm.Name)

		return nil
	}

	if !bytes.Equal(secret.Data["tls.crt"], []byte(cm.Data["ca-bundle.crt"])) {
		cm.Data["ca-bundle.crt"] = string(secret.Data["tls.crt"])
		if err := r.Client.Update(context.TODO(), cm); err != nil {
			if errors.IsAlreadyExists(err) {
				return nil
			}

			return fmt.Errorf("failed to update configmap %s/%s: %v", cm.Namespace, cm.Name, err)
		}

		logrus.Infof("updated configmap %s/%s", cm.Namespace, cm.Name)

		return nil
	}

	return nil
}

// ensureRouterCAIsUnpublished ensures the configmap with the CA certificate is
// deleted.
func (r *reconciler) ensureRouterCAIsUnpublished() error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caCertConfigMapName,
			Namespace: GlobalMachineSpecifiedConfigNamespace,
		},
	}
	if err := r.Client.Delete(context.TODO(), cm); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete configmap %s/%s: %v", cm.Namespace, cm.Name, err)
	}

	logrus.Infof("deleted configmap %s/%s", cm.Namespace, cm.Name)

	return nil
}
