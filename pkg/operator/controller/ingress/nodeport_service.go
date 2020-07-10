package ingress

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/util/intstr"
)

// ensureNodePortService ensures a NodePort service exists for a given
// ingresscontroller, if and only if one is desired.  Returns a Boolean
// indicating whether the NodePort service exists, the current NodePort service
// if it does exist, and an error value.
func (r *reconciler) ensureNodePortService(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *corev1.Service, error) {
	wantService, desired := desiredNodePortService(ic, deploymentRef)

	haveService, current, err := r.currentNodePortService(ic)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !wantService && !haveService:
		return false, nil, nil
	case !wantService && haveService:
		if err := r.client.Delete(context.TODO(), current); err != nil {
			if !errors.IsNotFound(err) {
				return true, current, fmt.Errorf("failed to delete NodePort service: %v", err)
			}
		} else {
			log.Info("deleted NodePort service", "service", current)
		}
		return false, nil, nil
	case wantService && !haveService:
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create NodePort service: %v", err)
		}
		log.Info("created NodePort service", "service", desired)
		return r.currentNodePortService(ic)
	case wantService && haveService:
		if updated, err := r.updateNodePortService(current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update NodePort service: %v", err)
		} else if updated {
			log.Info("updated NodePort service", "service", desired)
			return r.currentNodePortService(ic)
		}
	}

	return true, current, nil
}

// desiredNodePortService returns a Boolean indicating whether a NodePort
// service is desired, as well as the NodePort service if one is desired.
func desiredNodePortService(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *corev1.Service) {
	if ic.Status.EndpointPublishingStrategy.Type != operatorv1.NodePortServiceStrategyType {
		return false, nil
	}

	name := controller.NodePortServiceName(ic)
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: name.Namespace,
			Name:      name.Name,
			Labels: map[string]string{
				"app":                                  "router",
				"router":                               name.Name,
				manifests.OwningIngressControllerLabel: ic.Name,
			},
			OwnerReferences: []metav1.OwnerReference{deploymentRef},
		},
		Spec: corev1.ServiceSpec{
			ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyTypeLocal,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(80),
					TargetPort: intstr.FromString("http"),
				},
				{
					Name:       "https",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(443),
					TargetPort: intstr.FromString("https"),
				},
			},
			Selector: controller.IngressControllerDeploymentPodSelector(ic).MatchLabels,
			Type:     corev1.ServiceTypeNodePort,
		},
	}

	return true, service
}

// currentNodePortService returns a Boolean indicating whether a NodePort
// service exists for the given ingresscontroller, as well as the NodePort
// service if it does exist and an error value.
func (r *reconciler) currentNodePortService(ic *operatorv1.IngressController) (bool, *corev1.Service, error) {
	service := &corev1.Service{}
	if err := r.client.Get(context.TODO(), controller.NodePortServiceName(ic), service); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, service, nil
}

// updateNodePortService updates a NodePort service.  Returns a Boolean
// indicating whether the service was updated, and an error value.
func (r *reconciler) updateNodePortService(current, desired *corev1.Service) (bool, error) {
	changed, updated := nodePortServiceChanged(current, desired)
	if !changed {
		return false, nil
	}

	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	return true, nil
}

// nodePortServiceChanged checks if the current NodePort service spec matches
// the expected spec and if not returns an updated one.
func nodePortServiceChanged(current, expected *corev1.Service) (bool, *corev1.Service) {
	serviceCmpOpts := []cmp.Option{
		// Ignore fields that the API, other controllers, or user may
		// have modified.
		cmpopts.IgnoreFields(corev1.ServicePort{}, "NodePort"),
		cmpopts.IgnoreFields(corev1.ServiceSpec{}, "ClusterIP", "ExternalIPs", "HealthCheckNodePort"),
		cmp.Comparer(cmpServiceAffinity),
		cmpopts.EquateEmpty(),
	}
	if cmp.Equal(current.Spec, expected.Spec, serviceCmpOpts...) {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec

	// Preserve fields that the API, other controllers, or user may have
	// modified.
	updated.Spec.ClusterIP = current.Spec.ClusterIP
	updated.Spec.ExternalIPs = current.Spec.ExternalIPs
	updated.Spec.HealthCheckNodePort = current.Spec.HealthCheckNodePort
	for i, updatedPort := range updated.Spec.Ports {
		for _, currentPort := range current.Spec.Ports {
			if currentPort.Name == updatedPort.Name {
				updated.Spec.Ports[i].NodePort = currentPort.NodePort
			}
		}
	}

	return true, updated
}

func cmpServiceAffinity(a, b corev1.ServiceAffinity) bool {
	if len(a) == 0 {
		a = corev1.ServiceAffinityNone
	}
	if len(b) == 0 {
		b = corev1.ServiceAffinityNone
	}
	return a == b
}
