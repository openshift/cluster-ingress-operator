package controller

import (
	"context"
	"fmt"

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

// routerCAConfigMapName returns the namespaced name for the router CA
// configmap.
func routerCAConfigMapName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: GlobalMachineSpecifiedConfigNamespace,
		Name:      caCertConfigMapName,
	}
}

// ensureRouterCAConfigMap will create, update, or delete the configmap for the
// router CA as appropriate.
func (r *reconciler) ensureRouterCAConfigMap(secret *corev1.Secret, ingresses []ingressv1alpha1.ClusterIngress) error {
	desired, err := desiredRouterCAConfigMap(secret, ingresses)
	if err != nil {
		return err
	}
	current, err := r.currentRouterCAConfigMap()
	if err != nil {
		return err
	}
	switch {
	case desired == nil && current == nil:
		// Nothing to do.
	case desired == nil && current != nil:
		if err := r.deleteRouterCAConfigMap(current); err != nil {
			return fmt.Errorf("failed to ensure router CA was unpublished: %v", err)
		}
	case desired != nil && current == nil:
		if err := r.createRouterCAConfigMap(desired); err != nil {
			return fmt.Errorf("failed to ensure router CA was published: %v", err)
		}
	case desired != nil && current != nil:
		if err := r.updateRouterCAConfigMap(current, desired); err != nil {
			return fmt.Errorf("failed to update published router CA: %v", err)
		}
	}
	return nil
}

// desiredRouterCAConfigMap returns the desired router CA configmap.
func desiredRouterCAConfigMap(secret *corev1.Secret, ingresses []ingressv1alpha1.ClusterIngress) (*corev1.ConfigMap, error) {
	if !shouldPublishRouterCA(ingresses) {
		return nil, nil
	}

	name := routerCAConfigMapName()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Data: map[string]string{
			"ca-bundle.crt": string(secret.Data["tls.crt"]),
		},
	}
	return cm, nil
}

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

// currentRouterCAConfigMap returns the current router CA configmap.
func (r *reconciler) currentRouterCAConfigMap() (*corev1.ConfigMap, error) {
	name := routerCAConfigMapName()
	cm := &corev1.ConfigMap{}
	if err := r.Client.Get(context.TODO(), name, cm); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return cm, nil
}

// createRouterCAConfigMap creates a router CA configmap.
func (r *reconciler) createRouterCAConfigMap(cm *corev1.ConfigMap) error {
	if err := r.Client.Create(context.TODO(), cm); err != nil {
		return err
	}
	log.Info("created configmap", "namespace", cm.Namespace, "name", cm.Name)
	return nil
}

// updateRouterCAConfigMaps updates the router CA configmap.
func (r *reconciler) updateRouterCAConfigMap(current, desired *corev1.ConfigMap) error {
	if routerCAConfigMapsEqual(current, desired) {
		return nil
	}
	updated := current.DeepCopy()
	updated.Data = desired.Data
	if err := r.Client.Update(context.TODO(), updated); err != nil {
		return err
	}
	log.Info("updated configmap", "namespace", updated.Namespace, "name", updated.Name)
	return nil
}

// deleteRouterCAConfigMap deletes the router CA configmap.
func (r *reconciler) deleteRouterCAConfigMap(cm *corev1.ConfigMap) error {
	if err := r.Client.Delete(context.TODO(), cm); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	log.Info("deleted configmap", "namespace", cm.Namespace, "name", cm.Name)
	return nil
}

// routerCAConfigMapsEqual compares two router CA configmaps.
func routerCAConfigMapsEqual(a, b *corev1.ConfigMap) bool {
	if a.Data["ca-bundle.crt"] != b.Data["ca-bundle.crt"] {
		return false
	}
	return true
}
