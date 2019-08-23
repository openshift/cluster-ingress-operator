package certificate

import (
	"bytes"
	"context"
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ensureRouterCAConfigMap will create, update, or delete the configmap for the
// router CA as appropriate.
func (r *reconciler) ensureRouterCAConfigMap(secret *corev1.Secret, ingresses []operatorv1.IngressController) error {
	desired, err := r.desiredRouterCAConfigMap(ingresses)
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
		if deleted, err := r.deleteRouterCAConfigMap(current); err != nil {
			return fmt.Errorf("failed to ensure router CA was unpublished: %v", err)
		} else if deleted {
			r.recorder.Eventf(current, "Normal", "UnpublishedDefaultRouterCA", "Unpublished default router CA")
		}
	case desired != nil && current == nil:
		if created, err := r.createRouterCAConfigMap(desired); err != nil {
			return fmt.Errorf("failed to ensure router CA was published: %v", err)
		} else if created {
			new, err := r.currentRouterCAConfigMap()
			if err != nil {
				return err
			}
			r.recorder.Eventf(new, "Normal", "PublishedDefaultRouterCA", "Published default router CA")
		}
	case desired != nil && current != nil:
		if updated, err := r.updateRouterCAConfigMap(current, desired); err != nil {
			return fmt.Errorf("failed to update published router CA: %v", err)
		} else if updated {
			r.recorder.Eventf(current, "Normal", "UpdatedPublishedDefaultRouterCA", "Updated the published default router CA")
		}
	}
	return nil
}

// desiredRouterCAConfigMap returns a configmap containing a single 'ca-bundle.crt'
// key whose value is the concatenation of:
//
// 1. The default generated CA tls.crt data
// 2. All distinct user-provided default certificate secret tls.crt data referenced
//    by ingresscontrollers.
func (r *reconciler) desiredRouterCAConfigMap(ingresses []operatorv1.IngressController) (*corev1.ConfigMap, error) {
	caName := controller.RouterCASecretName(r.operatorNamespace)
	secrets := map[string]types.NamespacedName{
		caName.String(): caName,
	}
	for _, ingress := range ingresses {
		if cert := ingress.Spec.DefaultCertificate; cert != nil {
			name := types.NamespacedName{Namespace: "openshift-ingress", Name: cert.Name}
			secrets[name.String()] = name
		}
	}

	var certs [][]byte
	for _, name := range secrets {
		secret := &corev1.Secret{}
		if err := r.client.Get(context.TODO(), name, secret); err != nil {
			return nil, fmt.Errorf("failed to get certificate secret %q: %v", name.String(), err)
		}
		if data, ok := secret.Data["tls.crt"]; ok {
			certs = append(certs, data)
		} else {
			return nil, fmt.Errorf("certificate tls.crt key is missing from certificate secret %q", name.String())
		}
	}

	name := controller.RouterCAConfigMapName()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Data: map[string]string{
			"ca-bundle.crt": string(bytes.Join(certs, nil)),
		},
	}
	return cm, nil
}

// currentRouterCAConfigMap returns the current router CA configmap.
func (r *reconciler) currentRouterCAConfigMap() (*corev1.ConfigMap, error) {
	name := controller.RouterCAConfigMapName()
	cm := &corev1.ConfigMap{}
	if err := r.client.Get(context.TODO(), name, cm); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return cm, nil
}

// createRouterCAConfigMap creates a router CA configmap. Returns true if the
// configmap was created, false otherwise.
func (r *reconciler) createRouterCAConfigMap(cm *corev1.ConfigMap) (bool, error) {
	if err := r.client.Create(context.TODO(), cm); err != nil {
		return false, err
	}
	return true, nil
}

// updateRouterCAConfigMaps updates the router CA configmap. Returns true if the
// configmap was updated, false otherwise.
func (r *reconciler) updateRouterCAConfigMap(current, desired *corev1.ConfigMap) (bool, error) {
	if routerCAConfigMapsEqual(current, desired) {
		return false, nil
	}
	updated := current.DeepCopy()
	updated.Data = desired.Data
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	return true, nil
}

// deleteRouterCAConfigMap deletes the router CA configmap. Returns true if the
// configmap was deleted, false otherwise.
func (r *reconciler) deleteRouterCAConfigMap(cm *corev1.ConfigMap) (bool, error) {
	if err := r.client.Delete(context.TODO(), cm); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// routerCAConfigMapsEqual compares two router CA configmaps.
func routerCAConfigMapsEqual(a, b *corev1.ConfigMap) bool {
	if a.Data["ca-bundle.crt"] != b.Data["ca-bundle.crt"] {
		return false
	}
	return true
}
