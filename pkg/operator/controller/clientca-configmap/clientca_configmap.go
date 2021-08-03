package clientcaconfigmap

import (
	"context"
	"fmt"
	"reflect"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	operatorv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ensureClientCAConfigMap syncs client CA configmaps for an ingresscontroller
// between the openshift-config and openshift-ingress namespaces if the user has
// configured a client CA configmap.  Returns a Boolean indicating whether the
// configmap exists, the configmap if it does exist, and an error value.
func (r *reconciler) ensureClientCAConfigMap(ctx context.Context, ic *operatorv1.IngressController) (bool, *corev1.ConfigMap, error) {
	sourceName := types.NamespacedName{
		Namespace: r.config.SourceNamespace,
		Name:      ic.Spec.ClientTLS.ClientCA.Name,
	}
	haveSource, source, err := r.currentClientCAConfigMap(ctx, sourceName)
	if err != nil {
		return false, nil, err
	}

	destName := operatorcontroller.ClientCAConfigMapName(ic)
	have, current, err := r.currentClientCAConfigMap(ctx, destName)
	if err != nil {
		return false, nil, err
	}

	want, desired, err := desiredClientCAConfigMap(ic, haveSource, source, destName)
	if err != nil {
		return have, current, err
	}

	switch {
	case !want && !have:
		return false, nil, nil
	case !want && have:
		if err := r.client.Delete(ctx, current); err != nil {
			if !errors.IsNotFound(err) {
				return true, current, fmt.Errorf("failed to delete configmap: %w", err)
			}
		} else {
			log.Info("deleted configmap", "namespace", current.Namespace, "name", current.Name)
		}
		return false, nil, nil
	case want && !have:
		if err := r.client.Create(ctx, desired); err != nil {
			return false, nil, fmt.Errorf("failed to create configmap: %w", err)
		}
		log.Info("created configmap", "namespace", desired.Namespace, "name", desired.Name)
		return r.currentClientCAConfigMap(ctx, destName)
	case want && have:
		if updated, err := r.updateClientCAConfigMap(ctx, current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update configmap: %w", err)
		} else if updated {
			log.Info("updated configmap", "namespace", desired.Namespace, "name", desired.Name)
			return r.currentClientCAConfigMap(ctx, destName)
		}
	}

	return have, current, nil
}

// desiredClientCAConfigMap returns the desired client CA configmap.  Returns a
// Boolean indicating whether a configmap is desired, as well as the configmap
// if one is desired.
func desiredClientCAConfigMap(ic *operatorv1.IngressController, haveSource bool, sourceConfigmap *corev1.ConfigMap, name types.NamespacedName) (bool, *corev1.ConfigMap, error) {
	if !haveSource {
		return false, nil, nil
	}
	if ic.DeletionTimestamp != nil {
		return false, nil, nil
	}
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
		Data: sourceConfigmap.Data,
	}
	return true, &cm, nil
}

// currentClientCAConfigMap returns the current configmap.  Returns a Boolean
// indicating whether the configmap existed, the configmap if it did exist, and
// an error value.
func (r *reconciler) currentClientCAConfigMap(ctx context.Context, name types.NamespacedName) (bool, *corev1.ConfigMap, error) {
	if len(name.Name) == 0 {
		return false, nil, nil
	}
	cm := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, name, cm); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, cm, nil
}

// updateClientCAConfigMap updates a configmap.  Returns a Boolean indicating
// whether the configmap was updated, and an error value.
func (r *reconciler) updateClientCAConfigMap(ctx context.Context, current, desired *corev1.ConfigMap) (bool, error) {
	if clientCAConfigmapsEqual(current, desired) {
		return false, nil
	}
	updated := current.DeepCopy()
	updated.Data = desired.Data
	if err := r.client.Update(ctx, updated); err != nil {
		return false, err
	}
	log.Info("updated configmap", "namespace", updated.Namespace, "name", updated.Name)
	return true, nil
}

// clientCAConfigmapsEqual compares two client CA configmaps.  Returns true if
// the configmaps should be considered equal for the purpose of determining
// whether an update is necessary, false otherwise.
func clientCAConfigmapsEqual(a, b *corev1.ConfigMap) bool {
	return reflect.DeepEqual(a.Data, b.Data)
}
