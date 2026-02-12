package canary

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CanaryPorts is the list of ports exposed by the canary service
var CanaryPorts = []int32{8443, 8888}

// ensureCanaryService ensures the ingress canary service exists.
func (r *reconciler) ensureCanaryService(ctx context.Context, daemonsetRef metav1.OwnerReference) (bool, *corev1.Service, error) {
	desired := desiredCanaryService(daemonsetRef)
	haveService, current, err := r.currentCanaryService(ctx)
	if err != nil {
		return false, nil, err
	}
	switch {
	case haveService:
		if updated, err := r.updateCanaryService(ctx, current, desired); err != nil {
			return true, current, err
		} else if updated {
			return r.currentCanaryService(ctx)
		}
	case !haveService:
		if err := r.createCanaryService(ctx, desired); err != nil {
			return false, nil, err
		}
		return true, desired, nil
	}
	return true, current, nil
}

// updateCanaryService updates the canary service if an appropriate change is detected.
func (r *reconciler) updateCanaryService(ctx context.Context, current, desired *corev1.Service) (bool, error) {
	changed, updated := canaryServiceChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(ctx, updated); err != nil {
		return false, fmt.Errorf("failed to update canary service %s/%s: %w", updated.Namespace, updated.Name, err)
	}
	log.Info("updated canary service", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// canaryServiceChanged returns true if the canary service's ports or annotations have changed. If a change occurred, it
// also returns a copy of the service with the relevant changes merged.
func canaryServiceChanged(current, desired *corev1.Service) (bool, *corev1.Service) {
	changed := false
	updated := current.DeepCopy()

	if !cmp.Equal(current.Spec.Ports, desired.Spec.Ports, cmpopts.EquateEmpty()) {
		updated.Spec.Ports = desired.Spec.Ports
		changed = true
	}
	if !cmp.Equal(current.Annotations, desired.Annotations, cmpopts.EquateEmpty()) {
		updated.Annotations = desired.Annotations
		changed = true
	}

	if !changed {
		return false, nil
	}

	return true, updated
}

// currentCanaryService gets the current ingress canary service resource.
func (r *reconciler) currentCanaryService(ctx context.Context) (bool, *corev1.Service, error) {
	current := &corev1.Service{}
	err := r.client.Get(ctx, controller.CanaryServiceName(), current)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

// createCanaryService creates the given service resource.
func (r *reconciler) createCanaryService(ctx context.Context, service *corev1.Service) error {
	if err := r.client.Create(ctx, service); err != nil {
		return fmt.Errorf("failed to create canary service %s/%s: %w", service.Namespace, service.Name, err)
	}

	log.Info("created canary service", "namespace", service.Namespace, "name", service.Name)
	return nil
}

// desiredCanaryService returns the desired canary service read in from manifests.
func desiredCanaryService(daemonsetRef metav1.OwnerReference) *corev1.Service {
	s := manifests.CanaryService()

	name := controller.CanaryServiceName()
	s.Namespace = name.Namespace
	s.Name = name.Name

	s.Labels = map[string]string{
		// associate the daemonset with the ingress canary controller
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	s.Spec.Selector = controller.CanaryDaemonSetPodSelector(canaryControllerName).MatchLabels

	s.SetOwnerReferences([]metav1.OwnerReference{daemonsetRef})

	return s
}
