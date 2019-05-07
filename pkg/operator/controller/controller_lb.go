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
	"k8s.io/apimachinery/pkg/types"
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
// Always returns the current LB service if one exists (whether it already
// existed or was created during the course of the function).
func (r *reconciler) ensureLoadBalancerService(ci *operatorv1.IngressController, deploymentRef metav1.OwnerReference, infraConfig *configv1.Infrastructure) (*corev1.Service, error) {
	desiredLBService, err := desiredLoadBalancerService(ci, deploymentRef, infraConfig)
	if err != nil {
		return nil, err
	}

	currentLBService, err := r.currentLoadBalancerService(ci)
	if err != nil {
		return nil, err
	}
	if desiredLBService != nil && currentLBService == nil {
		if err := r.client.Create(context.TODO(), desiredLBService); err != nil {
			return nil, fmt.Errorf("failed to create load balancer service %s/%s: %v", desiredLBService.Namespace, desiredLBService.Name, err)
		}
		log.Info("created load balancer service", "namespace", desiredLBService.Namespace, "name", desiredLBService.Name)
		return desiredLBService, nil
	}
	return currentLBService, nil
}

// TODO: This should take operator config into account so that the operand
// namespace isn't hard-coded.
func loadBalancerServiceName(ci *operatorv1.IngressController) types.NamespacedName {
	return types.NamespacedName{Namespace: "openshift-ingress", Name: "router-" + ci.Name}
}

// desiredLoadBalancerService returns the desired LB service for a
// ingresscontroller, or nil if an LB service isn't desired. An LB service is
// desired if the high availability type is Cloud. An LB service will declare an
// owner reference to the given deployment.
func desiredLoadBalancerService(ci *operatorv1.IngressController, deploymentRef metav1.OwnerReference, infraConfig *configv1.Infrastructure) (*corev1.Service, error) {
	if ci.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return nil, nil
	}
	service := manifests.LoadBalancerService()

	name := loadBalancerServiceName(ci)

	service.Namespace = name.Namespace
	service.Name = name.Name

	if service.Labels == nil {
		service.Labels = map[string]string{}
	}
	service.Labels["router"] = name.Name
	service.Labels[manifests.OwningIngressControllerLabel] = ci.Name

	service.Spec.Selector = IngressControllerDeploymentPodSelector(ci).MatchLabels

	if infraConfig.Status.Platform == configv1.AWSPlatformType {
		if service.Annotations == nil {
			service.Annotations = map[string]string{}
		}
		service.Annotations[awsLBProxyProtocolAnnotation] = "*"
	}
	service.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
	service.Finalizers = []string{loadBalancerServiceFinalizer}
	return service, nil
}

// currentLoadBalancerService returns any existing LB service for the
// ingresscontroller.
func (r *reconciler) currentLoadBalancerService(ci *operatorv1.IngressController) (*corev1.Service, error) {
	service := &corev1.Service{}
	if err := r.client.Get(context.TODO(), loadBalancerServiceName(ci), service); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return service, nil
}

// finalizeLoadBalancerService deletes any DNS entries associated with any
// current LB service associated with the ingresscontroller and then finalizes the
// service.
func (r *reconciler) finalizeLoadBalancerService(ci *operatorv1.IngressController, dnsConfig *configv1.DNS) error {
	service, err := r.currentLoadBalancerService(ci)
	if err != nil {
		return err
	}
	if service == nil {
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
	ingress := service.Status.LoadBalancer.Ingress
	if len(ingress) > 0 && len(ingress[0].Hostname) > 0 {
		records, err := desiredDNSAliasRecords(ci, ingress[0].Hostname, dnsConfig)
		if err != nil {
			return err
		}
		dnsErrors := []error{}
		for _, record := range records {
			if err := r.DNSManager.Delete(record); err != nil {
				dnsErrors = append(dnsErrors, fmt.Errorf("failed to delete DNS record %v for ingress %s/%s: %v", record, ci.Namespace, ci.Name, err))
			} else {
				log.Info("deleted DNS record for ingress", "namespace", ci.Namespace, "name", ci.Name, "record", record)
			}
		}
		if err := utilerrors.NewAggregate(dnsErrors); err != nil {
			return err
		}
	}
	// Mutate a copy to avoid assuming we know where the current one came from
	// (i.e. it could have been from a cache).
	updated := service.DeepCopy()
	if slice.ContainsString(updated.Finalizers, loadBalancerServiceFinalizer) {
		updated.Finalizers = slice.RemoveString(updated.Finalizers, loadBalancerServiceFinalizer)
		if err := r.client.Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to remove finalizer from service %s for ingress %s/%s: %v", service.Namespace, service.Name, ci.Name, err)
		}
	}
	return nil
}
