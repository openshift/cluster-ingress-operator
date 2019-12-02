package certificate

import (
	"context"
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ensureDefaultIngressCertConfigMap will create or update the configmap containing the public half of the default ingress wildcard certificate
func (r *reconciler) ensureDefaultIngressCertConfigMap(caBundle string) error {
	name := controller.DefaultIngressCertConfigMapName()
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Data: map[string]string{
			"ca-bundle.crt": caBundle,
		},
	}
	return r.ensureConfigMap(name, desired)
}

// ensureRouterCAConfigMap will create, update, or delete the configmap for the
// router CA as appropriate.
func (r *reconciler) ensureRouterCAConfigMap(secret *corev1.Secret, ingresses []operatorv1.IngressController) error {
	desired, err := desiredRouterCAConfigMap(secret, ingresses)
	if err != nil {
		return err
	}
	return r.ensureConfigMap(controller.RouterCAConfigMapName(), desired)
}

// ensureConfigMap will create, update, or delete the configmap as appropriate.
func (r *reconciler) ensureConfigMap(name types.NamespacedName, desired *corev1.ConfigMap) error {
	current, err := r.currentConfigMap(name)
	if err != nil {
		return err
	}
	switch {
	case desired == nil && current == nil:
		// Nothing to do.
	case desired == nil && current != nil:
		if deleted, err := r.deleteRouterCAConfigMap(current); err != nil {
			return fmt.Errorf("failed to ensure %q in %q was unpublished: %v", name.Name, name.Namespace, err)
		} else if deleted {
			r.recorder.Eventf(current, "Normal", "UnpublishedRouterCA", "Unpublished %q in %q", name.Name, name.Namespace)
		}
	case desired != nil && current == nil:
		if created, err := r.createRouterCAConfigMap(desired); err != nil {
			return fmt.Errorf("failed to ensure %q in %q was published: %v", desired.Name, desired.Namespace, err)
		} else if created {
			new, err := r.currentConfigMap(name)
			if err != nil {
				return err
			}
			r.recorder.Eventf(new, "Normal", "PublishedRouterCA", "Published %q in %q", desired.Name, desired.Namespace)
		}
	case desired != nil && current != nil:
		if updated, err := r.updateRouterCAConfigMap(current, desired); err != nil {
			return fmt.Errorf("failed to update published %q in %q: %v", desired.Name, desired.Namespace, err)
		} else if updated {
			r.recorder.Eventf(current, "Normal", "UpdatedPublishedRouterCA", "Updated the published %q in %q", desired.Name, desired.Namespace)
		}
	}
	return nil
}

// desiredRouterCAConfigMap returns the desired router CA configmap.
func desiredRouterCAConfigMap(secret *corev1.Secret, ingresses []operatorv1.IngressController) (*corev1.ConfigMap, error) {
	if !shouldPublishRouterCA(ingresses) {
		return nil, nil
	}

	name := controller.RouterCAConfigMapName()
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

// shouldPublishRouterCA checks if some IngressController uses the default
// certificate, in which case the CA certificate needs to be published.
func shouldPublishRouterCA(ingresses []operatorv1.IngressController) bool {
	for _, ci := range ingresses {
		if ci.Spec.DefaultCertificate == nil {
			return true
		}
	}
	return false
}

// currentConfigMap returns the current state of the desired configmap namespace/name.
func (r *reconciler) currentConfigMap(name types.NamespacedName) (*corev1.ConfigMap, error) {
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
