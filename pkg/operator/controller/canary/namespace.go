package canary

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
)

// ensureCanaryNamespace ensures that the ingress-canary namespace exists
func (r *reconciler) ensureCanaryNamespace() error {
	ns := manifests.CanaryNamespace()
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, ns); err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get ingress canary namespace %q: %v", ns.Name, err)
		}
		if err := r.client.Create(context.TODO(), ns); err != nil {
			return fmt.Errorf("failed to create ingress canary namespace %q: %v", ns.Name, err)
		}
		log.Info("created ingress canary namespace", "name", ns.Name)
	}

	return nil
}
