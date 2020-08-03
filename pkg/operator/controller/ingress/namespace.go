package ingress

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
)

// ensureRouterNamespace ensures all the necessary scaffolding exists for
// routers generally, including a namespace and all RBAC setup.
func (r *reconciler) ensureRouterNamespace() error {
	ns := manifests.RouterNamespace()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router namespace %q: %v", ns.Name, err)
		}
		if err := r.client.Create(context.TODO(), ns); err != nil {
			return fmt.Errorf("failed to create router namespace %s: %v", ns.Name, err)
		}
		log.Info("created router namespace", "name", ns.Name)
	}

	sa := manifests.RouterServiceAccount()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: sa.Namespace, Name: sa.Name}, sa); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
		if err := r.client.Create(context.TODO(), sa); err != nil {
			return fmt.Errorf("failed to create router service account %s/%s: %v", sa.Namespace, sa.Name, err)
		}
		log.Info("created router service account", "namespace", sa.Namespace, "name", sa.Name)
	}

	crb := manifests.RouterClusterRoleBinding()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: crb.Name}, crb); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get router cluster role binding %s: %v", crb.Name, err)
		}
		if err := r.client.Create(context.TODO(), crb); err != nil {
			return fmt.Errorf("failed to create router cluster role binding %s: %v", crb.Name, err)
		}
		log.Info("created router cluster role binding", "name", crb.Name)
	}

	return nil
}
