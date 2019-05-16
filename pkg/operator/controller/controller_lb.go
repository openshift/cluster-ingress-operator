package controller

import (
	"context"
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

const (
	// loadBalancerServiceFinalizer is applied to load balancer services to ensure
	// we can manage deletion of associated DNS records.
	loadBalancerServiceFinalizer = "ingress.openshift.io/operator"

	// awsLBProxyProtocolAnnotation is used to enable the PROXY protocol on any
	// AWS load balancer services created.
	awsLBProxyProtocolAnnotation = "service.beta.kubernetes.io/aws-load-balancer-proxy-protocol"
)

// ensureLoadBalancerService creates an LB service if one is desired but absent.
// Returns a Boolean indicating whether the service exists, the current service
// if it does exist, and an error value.  Always returns the current service if
// one exists, whether it already existed or was created during the course of
// the function.
func (r *reconciler) ensureLoadBalancerService(ic *operatorv1.IngressController, wantHealthz bool, healthzSvc *corev1.Service, deploymentRef metav1.OwnerReference, infraConfig *configv1.Infrastructure) (bool, *corev1.Service, error) {
	haveService, current, err := r.currentLoadBalancerService(ic)
	if err != nil {
		return false, nil, err
	}

	var healthzPort int32
	if wantHealthz {
		if len(healthzSvc.Spec.Ports) < 1 {
			return false, current, fmt.Errorf("healthz service has no port")
		}
		healthzPort = healthzSvc.Spec.Ports[0].NodePort
	}

	wantService, desired, err := desiredLoadBalancerService(ic, healthzPort, deploymentRef, infraConfig)
	if err != nil {
		return false, current, err
	}

	switch {
	case !wantService && !haveService:
		return false, nil, nil
	case !wantService && haveService:
		if deleted, err := r.deleteLoadBalancerService(current); err != nil {
			return true, current, fmt.Errorf("failed to delete load balancer service: %v", err)
		} else if deleted {
			log.Info("deleted load balancer service", "service", current)
		}
	case wantService && !haveService:
		if created, err := r.createLoadBalancerService(desired); err != nil {
			return false, nil, fmt.Errorf("failed to create load balancer service %s/%s: %v", desired.Namespace, desired.Name, err)
		} else if created {
			log.Info("created load balancer service service", "namespace", desired.Namespace, "name", desired.Name)
		}
	case wantService && haveService:
		if updated, err := r.updateLoadBalancerService(ic, current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update load balancer service: %v", err)
		} else if updated {
			log.Info("updated load balancer service", "service", desired)
		}
	}

	return r.currentLoadBalancerService(ic)
}

// desiredLoadBalancerService returns the desired LB service for a
// ingresscontroller, or nil if an LB service isn't desired. An LB service is
// desired if the high availability type is Cloud. An LB service will declare an
// owner reference to the given deployment.
func desiredLoadBalancerService(ic *operatorv1.IngressController, healthzPort int32, deploymentRef metav1.OwnerReference, infraConfig *configv1.Infrastructure) (bool, *corev1.Service, error) {
	if ic.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return false, nil, nil
	}
	service := manifests.LoadBalancerService()

	name := LoadBalancerServiceName(ic)

	service.Namespace = name.Namespace
	service.Name = name.Name

	if service.Labels == nil {
		service.Labels = map[string]string{}
	}
	service.Labels["router"] = name.Name
	service.Labels[manifests.OwningIngressControllerLabel] = ic.Name

	service.Spec.Selector = IngressControllerDeploymentPodSelector(ic).MatchLabels
	if healthzPort != int32(0) {
		service.Spec.HealthCheckNodePort = healthzPort
	}
	if infraConfig.Status.Platform == configv1.AWSPlatformType {
		if service.Annotations == nil {
			service.Annotations = map[string]string{}
		}
		service.Annotations[awsLBProxyProtocolAnnotation] = "*"
	}
	service.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
	service.Finalizers = []string{loadBalancerServiceFinalizer}
	return true, service, nil
}

// currentLoadBalancerService returns any existing LB service for the
// ingresscontroller.
func (r *reconciler) currentLoadBalancerService(ic *operatorv1.IngressController) (bool, *corev1.Service, error) {
	service := &corev1.Service{}
	if err := r.client.Get(context.TODO(), LoadBalancerServiceName(ic), service); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, service, nil
}

// finalizeLoadBalancerService deletes any DNS entries associated with any
// current LB service associated with the ingresscontroller and then finalizes the
// service.
func (r *reconciler) finalizeLoadBalancerService(ic *operatorv1.IngressController, dnsConfig *configv1.DNS) error {
	haveLBService, service, err := r.currentLoadBalancerService(ic)
	if err != nil {
		return err
	}
	if !haveLBService {
		return nil
	}
	// We cannot published DNS records for a load balancer till it has been
	// provisioned.  Thus if the service's status does not _currently_
	// indicate that a load balancer has been provisioned, that means we
	// _probably_ have not published any DNS records (and if we have, then
	// we have lost track of them).
	//
	// TODO: Instead of trying to infer whether DNS records exist by looking
	// at the service, we should be maintaining state with any DNS records
	// that we have created for the ingresscontroller, for example by using
	// an annotation on the ingresscontroller.
	records := desiredDNSRecords(ic, dnsConfig, service)
	dnsErrors := []error{}
	for _, record := range records {
		if err := r.DNSManager.Delete(record); err != nil {
			dnsErrors = append(dnsErrors, fmt.Errorf("failed to delete DNS record %v for ingress %s/%s: %v", record, ic.Namespace, ic.Name, err))
		} else {
			log.Info("deleted DNS record for ingress", "namespace", ic.Namespace, "name", ic.Name, "record", record)
		}
	}
	if err := utilerrors.NewAggregate(dnsErrors); err != nil {
		return err
	}
	// Mutate a copy to avoid assuming we know where the current one came from
	// (i.e. it could have been from a cache).
	updated := service.DeepCopy()
	if slice.ContainsString(updated.Finalizers, loadBalancerServiceFinalizer) {
		updated.Finalizers = slice.RemoveString(updated.Finalizers, loadBalancerServiceFinalizer)
		if err := r.client.Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to remove finalizer from service %s/%s: %v", service.Namespace, service.Name, err)
		}
	}
	return nil
}

// createLoadBalancerService creates the given LoadBalancer service.  Returns a
// Boolean indicating whether the service was created, and an error value.
func (r *reconciler) createLoadBalancerService(s *corev1.Service) (bool, error) {
	if err := r.client.Create(context.TODO(), s); err != nil {
		return false, err
	}
	return true, nil
}

// deleteLoadBalancerService deletes the given LoadBalancer service.  Returns a
// Boolean indicating whether the service was deleted, and an error value.
func (r *reconciler) deleteLoadBalancerService(s *corev1.Service) (bool, error) {
	if err := r.client.Delete(context.TODO(), s); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// updateLoadBalancerService updates the given LoadBalancer service if it
// differs from the given desired service.  Returns a Boolean indicating whether
// the service was updated, and an error value.
func (r *reconciler) updateLoadBalancerService(ic *operatorv1.IngressController, current, desired *corev1.Service) (bool, error) {
	if loadBalancerServicesEqual(current, desired) {
		return false, nil
	}

	// The only field we care about is .spec.healthCheckNodePort, which is
	// immutable, so we must delete and recreate in order to update the
	// service.
	//
	if _, err := r.deleteLoadBalancerService(current); err != nil {
		return false, err
	}
	// The ingress domain is immutable, and the new LoadBalancer service's
	// DNS records will overwrite the old one's, so it is safe to remove the
	// finalizer without calling finalizeLoadBalancerService.
	if haveCurrent, current, err := r.currentLoadBalancerService(ic); err != nil {
		return false, err
	} else if haveCurrent {
		current.Finalizers = slice.RemoveString(current.Finalizers, loadBalancerServiceFinalizer)
		if err := r.client.Update(context.TODO(), current); err != nil {
			return false, fmt.Errorf("failed to remove finalizer from service %s/%s: %v", current.Namespace, current.Name, err)
		}
	}

	if _, err := r.createLoadBalancerService(desired); err != nil {
		return false, err
	}

	return true, nil
}

// loadBalancerServicesEqual compares two LoadBalancer services.  Returns a
// Boolean indicating whether the two services are equal for the purpose of
// determine whether an update is necessary.
func loadBalancerServicesEqual(a, b *corev1.Service) bool {
	if a.Spec.HealthCheckNodePort != b.Spec.HealthCheckNodePort {
		return false
	}

	return true
}
