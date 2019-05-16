package controller

import (
	"context"
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// Annotation used to inform the certificate generation service to
	// generate a cluster-signed certificate and populate the secret.
	ServingCertSecretAnnotation = "service.alpha.openshift.io/serving-cert-secret-name"
)

// ensureInternalIngressControllerService ensures that a ClusterIP internal
// service exists for the given ingresscontroller.  Returns a Boolean indicating
// whether the service exists, the service if it does exist, and an error value.
func (r *reconciler) ensureInternalIngressControllerService(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *corev1.Service, error) {
	wantInternalService, desired := desiredInternalService(ic, deploymentRef)
	haveInternalService, current, err := r.currentInternalService(ic)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !wantInternalService && !haveInternalService:
		return false, nil, nil
	case !wantInternalService && haveInternalService:
		if deleted, err := r.deleteInternalService(current); err != nil {
			return true, current, fmt.Errorf("failed to delete internal service: %v", err)
		} else if deleted {
			log.Info("deleted internal service", "service", current)
		}
	case wantInternalService && !haveInternalService:
		if created, err := r.createInternalService(desired); err != nil {
			return false, nil, fmt.Errorf("failed to create internal service: %v", err)
		} else if created {
			log.Info("created internal service", "service", desired)
		}
	case wantInternalService && haveInternalService:
		return true, current, nil
	}

	return r.currentInternalService(ic)
}

func (r *reconciler) currentInternalService(ic *operatorv1.IngressController) (bool, *corev1.Service, error) {
	current := &corev1.Service{}
	err := r.client.Get(context.TODO(), InternalIngressControllerServiceName(ic), current)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

func desiredInternalService(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *corev1.Service) {
	s := manifests.InternalIngressControllerService()

	name := InternalIngressControllerServiceName(ic)

	s.Namespace = name.Namespace
	s.Name = name.Name

	s.Labels = map[string]string{
		manifests.OwningIngressControllerLabel: ic.Name,
	}

	s.Annotations = map[string]string{
		// TODO: remove hard-coded name
		ServingCertSecretAnnotation: fmt.Sprintf("router-metrics-certs-%s", ic.Name),
	}

	s.Spec.Selector = IngressControllerDeploymentPodSelector(ic).MatchLabels

	s.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})

	return true, s
}

// createInternalService creates the given internal service.  Returns a Boolean
// indicating whether the service was created, and an error value.
func (r *reconciler) createInternalService(s *corev1.Service) (bool, error) {
	if err := r.client.Create(context.TODO(), s); err != nil {
		return false, err
	}
	return true, nil
}

// deleteInternalService deletes the given internal service.  Returns a Boolean
// indicating whether the service was deleted, and an error value.
func (r *reconciler) deleteInternalService(s *corev1.Service) (bool, error) {
	if err := r.client.Delete(context.TODO(), s); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
