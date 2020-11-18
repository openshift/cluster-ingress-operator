package canary

import (
	"context"
	"fmt"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ensureCanaryService ensures the ingress canary service exists
func (r *reconciler) ensureCanaryService(daemonsetRef metav1.OwnerReference) (bool, *corev1.Service, error) {
	desired := desiredCanaryService(daemonsetRef)
	haveService, current, err := r.currentCanaryService()
	if err != nil {
		return false, nil, err
	}
	if haveService {
		return true, current, nil
	}
	if err := r.createCanaryService(desired); err != nil {
		return false, nil, err
	}
	return true, desired, nil
}

// currentCanaryService gets the current ingress canary service resource
func (r *reconciler) currentCanaryService() (bool, *corev1.Service, error) {
	current := &corev1.Service{}
	err := r.client.Get(context.TODO(), controller.CanaryServiceName(), current)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

// createCanaryService creates the given service resource
func (r *reconciler) createCanaryService(service *corev1.Service) error {
	if err := r.client.Create(context.TODO(), service); err != nil {
		return fmt.Errorf("failed to create canary service %s/%s: %v", service.Namespace, service.Name, err)
	}

	log.Info("created canary service", "namespace", service.Namespace, "name", service.Name)
	return nil
}

// desiredCanaryService returns the desired canary service read in from manifests
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
