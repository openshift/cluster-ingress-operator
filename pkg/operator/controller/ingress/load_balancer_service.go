package ingress

import (
	"context"
	"fmt"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// awsLBProxyProtocolAnnotation is used to enable the PROXY protocol on any
	// AWS load balancer services created.
	//
	// https://kubernetes.io/docs/concepts/services-networking/service/#proxy-protocol-support-on-aws
	awsLBProxyProtocolAnnotation = "service.beta.kubernetes.io/aws-load-balancer-proxy-protocol"
)

var (
	// internalLBAnnotations maps platform to the annotation name and value
	// that tell the cloud provider that is associated with the platform
	// that the load balancer is internal.
	//
	// https://kubernetes.io/docs/concepts/services-networking/service/#internal-load-balancer
	InternalLBAnnotations = map[configv1.PlatformType]map[string]string{
		configv1.AWSPlatformType: {
			"service.beta.kubernetes.io/aws-load-balancer-internal": "0.0.0.0/0",
		},
		configv1.AzurePlatformType: {
			"service.beta.kubernetes.io/azure-load-balancer-internal": "true",
		},
		// There are no required annotations for this platform as of
		// 2019-06-17 (but maybe MetalLB will have something for
		// internal load-balancers in the future).
		configv1.BareMetalPlatformType: nil,
		configv1.GCPPlatformType: {
			"cloud.google.com/load-balancer-type": "Internal",
		},
		// There are no required annotations for this platform.
		configv1.LibvirtPlatformType: nil,
		configv1.OpenStackPlatformType: {
			"service.beta.kubernetes.io/openstack-internal-load-balancer": "true",
		},
		configv1.NonePlatformType: nil,
		// vSphere does not support load balancers as of 2019-06-17.
		configv1.VSpherePlatformType: nil,
	}
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

// desiredLoadBalancerService returns the desired LB service for a
// ingresscontroller, or nil if an LB service isn't desired. An LB service is
// desired if the high availability type is Cloud. An LB service will declare an
// owner reference to the given deployment.
func desiredLoadBalancerService(ci *operatorv1.IngressController, deploymentRef metav1.OwnerReference, infraConfig *configv1.Infrastructure) (*corev1.Service, error) {
	if ci.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return nil, nil
	}
	service := manifests.LoadBalancerService()

	name := controller.LoadBalancerServiceName(ci)

	service.Namespace = name.Namespace
	service.Name = name.Name

	if service.Labels == nil {
		service.Labels = map[string]string{}
	}
	service.Labels["router"] = name.Name
	service.Labels[manifests.OwningIngressControllerLabel] = ci.Name

	service.Spec.Selector = controller.IngressControllerDeploymentPodSelector(ci).MatchLabels

	isInternal := ci.Status.EndpointPublishingStrategy.LoadBalancer == nil || ci.Status.EndpointPublishingStrategy.LoadBalancer.Scope == operatorv1.InternalLoadBalancer

	if service.Annotations == nil {
		service.Annotations = map[string]string{}
	}
	if infraConfig.Status.Platform == configv1.AWSPlatformType {
		service.Annotations[awsLBProxyProtocolAnnotation] = "*"
	}
	if isInternal {
		annotation := InternalLBAnnotations[infraConfig.Status.Platform]
		for name, value := range annotation {
			service.Annotations[name] = value
		}
	}

	service.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
	service.Finalizers = []string{manifests.LoadBalancerServiceFinalizer}
	return service, nil
}

// currentLoadBalancerService returns any existing LB service for the
// ingresscontroller.
func (r *reconciler) currentLoadBalancerService(ci *operatorv1.IngressController) (*corev1.Service, error) {
	service := &corev1.Service{}
	if err := r.client.Get(context.TODO(), controller.LoadBalancerServiceName(ci), service); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return service, nil
}

// finalizeLoadBalancerService removes finalizers from any LB service. This used
// to be to help with DNS cleanup, but now that's no longer necessary, and so we
// just need to clear the finalizer which might exist on existing resources.
//
// TODO: How can we delete this code?
func (r *reconciler) finalizeLoadBalancerService(ci *operatorv1.IngressController) error {
	service, err := r.currentLoadBalancerService(ci)
	if err != nil {
		return err
	}
	if service == nil {
		return nil
	}
	// Mutate a copy to avoid assuming we know where the current one came from
	// (i.e. it could have been from a cache).
	updated := service.DeepCopy()
	if slice.ContainsString(updated.Finalizers, manifests.LoadBalancerServiceFinalizer) {
		updated.Finalizers = slice.RemoveString(updated.Finalizers, manifests.LoadBalancerServiceFinalizer)
		if err := r.client.Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to remove finalizer from service %s for ingress %s/%s: %v", service.Namespace, service.Name, ci.Name, err)
		}
	}
	return nil
}
