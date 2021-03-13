package canary

import (
	"context"
	"fmt"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	projectv1 "github.com/openshift/api/project/v1"
)

// ensureCanaryNamespace ensures that the ingress-canary namespace exists
func (r *reconciler) ensureCanaryNamespace() (bool, *corev1.Namespace, error) {
	desired := manifests.CanaryNamespace()

	haveNamespace, current, err := r.currentCanaryNamespace()
	if err != nil {
		return false, nil, err
	}

	switch {
	case !haveNamespace:
		if err := r.createCanaryNamespace(desired); err != nil {
			return false, nil, err
		}
		return r.currentCanaryNamespace()
	case haveNamespace:
		if updated, err := r.updateCanaryNamespace(current, desired); err != nil {
			return true, current, err
		} else if updated {
			return r.currentCanaryNamespace()
		}
	}

	return true, current, nil
}

// currentCanaryNamespace gets the current canary namespace resource
func (r *reconciler) currentCanaryNamespace() (bool, *corev1.Namespace, error) {
	ns := &corev1.Namespace{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: controller.DefaultCanaryNamespace}, ns); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, ns, nil
}

// createCanaryNamespace creates the given namespace
func (r *reconciler) createCanaryNamespace(ns *corev1.Namespace) error {
	if err := r.client.Create(context.TODO(), ns); err != nil {
		return fmt.Errorf("failed to create canary namespace %s: %v", ns.Name, err)
	}

	log.Info("created canary namespace", "namespace", ns.Name)
	return nil
}

// updateCanaryNamespace updates the canary namespace if an appropriate change
// has been detected
func (r *reconciler) updateCanaryNamespace(current, desired *corev1.Namespace) (bool, error) {
	changed, updated := canaryNamespaceChanged(current, desired)
	if !changed {
		return false, nil
	}

	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, fmt.Errorf("failed to update canary namespace %s: %v", updated.Name, err)
	}
	log.Info("updated canary namespace", "namespace", updated.Name)
	return true, nil
}

// canaryNamespaceChanged returns true if current and expected differ by the openshift
// namespace node-selector annotation
func canaryNamespaceChanged(current, expected *corev1.Namespace) (bool, *corev1.Namespace) {
	updated := current.DeepCopy()

	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}

	if current.Annotations[projectv1.ProjectNodeSelector] == expected.Annotations[projectv1.ProjectNodeSelector] {
		return false, nil
	}

	updated.Annotations[projectv1.ProjectNodeSelector] = expected.Annotations[projectv1.ProjectNodeSelector]

	return true, updated
}
