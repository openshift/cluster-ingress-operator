package controller

import (
	"context"
	"fmt"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

// ensureRouterDeployment ensures the router deployment exists for a given
// clusteringress.
func (r *reconciler) ensureRouterDeployment(ci *ingressv1alpha1.ClusterIngress) (*appsv1.Deployment, error) {
	expected, err := r.ManifestFactory.RouterDeployment(ci)
	if err != nil {
		return nil, fmt.Errorf("failed to build router deployment: %v", err)
	}
	current := expected.DeepCopy()
	err = r.Client.Get(context.TODO(), types.NamespacedName{Namespace: expected.Namespace, Name: expected.Name}, current)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get router deployment %s/%s, %v", expected.Namespace, expected.Name, err)
		}

		err = r.Client.Create(context.TODO(), current)
		if err == nil {
			log.Info("created router deployment", "namespace", current.Namespace, "name", current.Name)
		} else if !errors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create router deployment %s/%s: %v", current.Namespace, current.Name, err)
		}
	}

	if changed, updated := deploymentConfigChanged(current, expected); changed {
		err = r.Client.Update(context.TODO(), updated)
		if err == nil {
			log.Info("updated router deployment", "namespace", updated.Namespace, "name", updated.Name)
			current = updated
		} else {
			return nil, fmt.Errorf("failed to update router deployment %s/%s, %v", updated.Namespace, updated.Name, err)
		}
	}

	return current, nil
}

// ensureRouterDeleted ensures that any router resources associated with the
// clusteringress are deleted.
func (r *reconciler) ensureRouterDeleted(ci *ingressv1alpha1.ClusterIngress) error {
	deployment, err := r.ManifestFactory.RouterDeployment(ci)
	if err != nil {
		return fmt.Errorf("failed to build router deployment for deletion: %v", err)
	}

	err = r.Client.Delete(context.TODO(), deployment)
	if !errors.IsNotFound(err) {
		return err
	}
	return nil
}
