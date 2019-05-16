package controller

import (
	"context"
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ensureHealthzService ensures that a healthz service exists for the given
// ingresscontroller.  Returns a Boolean indicating whether the service exists,
// the service if it does exist, and an error value.
func (r *reconciler) ensureHealthzService(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *corev1.Service, error) {
	haveHealthzService, current, err := r.currentHealthzService(ic)
	if err != nil {
		return false, nil, err
	}
	wantHealthzService, desired := desiredHealthzService(ic, deploymentRef)
	switch {
	case !wantHealthzService && !haveHealthzService:
		return false, nil, nil
	case !wantHealthzService && haveHealthzService:
		if deleted, err := r.deleteHealthzService(current); err != nil {
			return true, current, fmt.Errorf("failed to delete healthz service: %v", err)
		} else if deleted {
			log.Info("deleted healthz service", "service", current)
		}
	case wantHealthzService && !haveHealthzService:
		if created, err := r.createHealthzService(desired); err != nil {
			return false, nil, fmt.Errorf("failed to create healthz service: %v", err)
		} else if created {
			log.Info("created healthz service", "service", desired)
		}
	case wantHealthzService && haveHealthzService:
		return true, current, nil
	}
	return r.currentHealthzService(ic)
}

// currentHealthzService gets the current healthz service, if any, for the given
// ingresscontroller.  Returns a Boolean indicating whether the service existed,
// the service if it did exist, and an error value.
func (r *reconciler) currentHealthzService(ic *operatorv1.IngressController) (bool, *corev1.Service, error) {
	current := &corev1.Service{}
	err := r.client.Get(context.TODO(), HealthzServiceName(ic), current)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

// desiredHealthzService returns the desired healthz service, if any, for the
// given ingresscontroller.  Returns a Boolean indicating whether a service is
// desired, as well as the service if one is desired.
func desiredHealthzService(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *corev1.Service) {
	if ic.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return false, nil
	}

	name := HealthzServiceName(ic)
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
			Labels: map[string]string{
				manifests.OwningIngressControllerLabel: ic.Name,
			},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyTypeLocal,
			Selector: IngressControllerDeploymentPodSelector(ic).MatchLabels,
			Ports: []corev1.ServicePort{
				{
					Name:       "metrics",
					Port:       int32(1936),
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromInt(1936),
				},
			},
		},
	}
	service.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})

	return true, service
}

// createHealthzService creates the given healthz service.  Returns a Boolean
// indicating whether the service was created, and an error value.
func (r *reconciler) createHealthzService(s *corev1.Service) (bool, error) {
	if err := r.client.Create(context.TODO(), s); err != nil {
		return false, err
	}
	return true, nil
}

// deleteHealthzService deletes the given healthz service.  Returns a Boolean
// indicating whether the service was deleted, and an error value.
func (r *reconciler) deleteHealthzService(s *corev1.Service) (bool, error) {
	if err := r.client.Delete(context.TODO(), s); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
