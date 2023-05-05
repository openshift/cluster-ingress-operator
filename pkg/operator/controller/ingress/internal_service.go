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
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	// Annotation used to inform the certificate generation service to
	// generate a cluster-signed certificate and populate the secret.
	ServingCertSecretAnnotation = "service.alpha.openshift.io/serving-cert-secret-name"
)

// ensureInternalRouterServiceForIngress ensures that an internal service exists
// for a given IngressController.  Returns a Boolean indicating whether the
// service exists, the current service if it does exist, and an error value.
func (r *reconciler) ensureInternalIngressControllerService(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) (bool, *corev1.Service, error) {
	desired := desiredInternalIngressControllerService(ic, deploymentRef)
	have, current, err := r.currentInternalIngressControllerService(ic)
	if err != nil {
		return false, nil, err
	}
	switch {
	case !have:
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create internal ingresscontroller service: %w", err)
		}
		log.Info("created internal ingresscontroller service", "service", desired)
		return r.currentInternalIngressControllerService(ic)
	case have:
		if updated, err := r.updateInternalService(current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update internal service: %v", err)
		} else if updated {
			return r.currentInternalIngressControllerService(ic)
		}
	}

	return true, current, nil
}

func (r *reconciler) currentInternalIngressControllerService(ic *operatorv1.IngressController) (bool, *corev1.Service, error) {
	current := &corev1.Service{}
	err := r.client.Get(context.TODO(), controller.InternalIngressControllerServiceName(ic), current)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, current, nil
}

func desiredInternalIngressControllerService(ic *operatorv1.IngressController, deploymentRef metav1.OwnerReference) *corev1.Service {
	s := manifests.InternalIngressControllerService()

	name := controller.InternalIngressControllerServiceName(ic)

	s.Namespace = name.Namespace
	s.Name = name.Name

	s.Labels = map[string]string{
		manifests.OwningIngressControllerLabel: ic.Name,
	}

	s.Annotations = map[string]string{
		// TODO: remove hard-coded name
		ServingCertSecretAnnotation: fmt.Sprintf("router-metrics-certs-%s", ic.Name),
	}

	s.Spec.Selector = controller.IngressControllerDeploymentPodSelector(ic).MatchLabels

	s.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})

	return s
}

// updateInternalService updates a ClusterIP service.  Returns a Boolean
// indicating whether the service was updated, and an error value.
func (r *reconciler) updateInternalService(current, desired *corev1.Service) (bool, error) {
	changed, updated := internalServiceChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	log.Info("updated internal service", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// managedInternalServiceAnnotations is a set of annotation keys for annotations
// that the operator manages for its internal service.
var managedInternalServiceAnnotations = sets.NewString(
	ServingCertSecretAnnotation,
)

// internalServiceChanged checks if the current internal service annotations and
// spec match the expected annotations and spec and if not returns an updated
// service.
func internalServiceChanged(current, expected *corev1.Service) (bool, *corev1.Service) {
	changed := false

	serviceCmpOpts := []cmp.Option{
		// Ignore fields that the API, other controllers, or user may
		// have modified.
		cmpopts.IgnoreFields(corev1.ServicePort{}, "NodePort"),
		cmpopts.IgnoreFields(corev1.ServiceSpec{},
			"ClusterIP", "ClusterIPs",
			"ExternalIPs",
			"HealthCheckNodePort",
			"IPFamilies", "IPFamilyPolicy",
		),
		cmp.Comparer(cmpServiceAffinity),
		cmpopts.EquateEmpty(),
	}
	if !cmp.Equal(current.Spec, expected.Spec, serviceCmpOpts...) {
		changed = true
	}

	annotationCmpOpts := []cmp.Option{
		cmpopts.IgnoreMapEntries(func(k, _ string) bool {
			return !managedInternalServiceAnnotations.Has(k)
		}),
	}
	if !cmp.Equal(current.Annotations, expected.Annotations, annotationCmpOpts...) {
		changed = true
	}

	if !changed {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec

	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	for annotation := range managedInternalServiceAnnotations {
		currentVal, have := current.Annotations[annotation]
		expectedVal, want := expected.Annotations[annotation]
		if want && (!have || currentVal != expectedVal) {
			updated.Annotations[annotation] = expected.Annotations[annotation]
		} else if have && !want {
			delete(updated.Annotations, annotation)
		}
	}
	// Preserve fields that the API, other controllers, or user may have
	// modified.
	updated.Spec.ClusterIP = current.Spec.ClusterIP
	updated.Spec.ExternalIPs = current.Spec.ExternalIPs
	updated.Spec.HealthCheckNodePort = current.Spec.HealthCheckNodePort
	for i, updatedPort := range updated.Spec.Ports {
		for _, currentPort := range current.Spec.Ports {
			if currentPort.Name == updatedPort.Name {
				updated.Spec.Ports[i].TargetPort = currentPort.TargetPort
			}
		}
	}

	return true, updated
}
