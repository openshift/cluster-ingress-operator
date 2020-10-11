package ingress

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	oputil "github.com/openshift/cluster-ingress-operator/pkg/util"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	corev1 "k8s.io/api/core/v1"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// awsLBProxyProtocolAnnotation is used to enable the PROXY protocol on any
	// AWS load balancer services created.
	//
	// https://kubernetes.io/docs/concepts/services-networking/service/#proxy-protocol-support-on-aws
	awsLBProxyProtocolAnnotation = "service.beta.kubernetes.io/aws-load-balancer-proxy-protocol"

	// AWSLBTypeAnnotation is a Service annotation used to specify an AWS load
	// balancer type. See the following for additional details:
	//
	// https://kubernetes.io/docs/concepts/services-networking/service/#aws-nlb-support
	AWSLBTypeAnnotation = "service.beta.kubernetes.io/aws-load-balancer-type"

	// AWSNLBAnnotation is the annotation value of an AWS Network Load Balancer (NLB).
	AWSNLBAnnotation = "nlb"

	// awsInternalLBAnnotation is the annotation used on a service to specify an AWS
	// load balancer as being internal.
	awsInternalLBAnnotation = "service.beta.kubernetes.io/aws-load-balancer-internal"

	// awsLBHealthCheckIntervalAnnotation is the approximate interval, in seconds, between AWS
	// load balancer health checks of an individual AWS instance. Defaults to 5, must be between
	// 5 and 300.
	awsLBHealthCheckIntervalAnnotation = "service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval"
	awsLBHealthCheckIntervalDefault    = "5"

	// awsLBHealthCheckTimeoutAnnotation is the amount of time, in seconds, during which no response
	// means a failed AWS load balancer health check. The value must be less than the value of
	// awsLBHealthCheckIntervalAnnotation. Defaults to 4, must be between 2 and 60.
	awsLBHealthCheckTimeoutAnnotation = "service.beta.kubernetes.io/aws-load-balancer-healthcheck-timeout"
	awsLBHealthCheckTimeoutDefault    = "4"

	// awsLBHealthCheckUnhealthyThresholdAnnotation is the number of unsuccessful health checks required
	// for an AWS load balancer backend to be considered unhealthy for traffic. Defaults to 2, must be
	// between 2 and 10.
	awsLBHealthCheckUnhealthyThresholdAnnotation = "service.beta.kubernetes.io/aws-load-balancer-healthcheck-unhealthy-threshold"
	awsLBHealthCheckUnhealthyThresholdDefault    = "2"

	// awsLBHealthCheckHealthyThresholdAnnotation is the number of successive successful health checks
	// required for an AWS load balancer backend to be considered healthy for traffic. Defaults to 2,
	// must be between 2 and 10.
	awsLBHealthCheckHealthyThresholdAnnotation = "service.beta.kubernetes.io/aws-load-balancer-healthcheck-healthy-threshold"
	awsLBHealthCheckHealthyThresholdDefault    = "2"

	// iksLBScopeAnnotation is the annotation used on a service to specify an IBM
	// load balancer IP type.
	iksLBScopeAnnotation = "service.kubernetes.io/ibm-load-balancer-cloud-provider-ip-type"
	// iksLBScopePublic is the service annotation value used to specify an IBM load balancer
	// IP as public.
	iksLBScopePublic = "public"
	// iksLBScopePublic is the service annotation value used to specify an IBM load balancer
	// IP as private.
	iksLBScopePrivate = "private"

	// azureInternalLBAnnotation is the annotation used on a service to specify an Azure
	// load balancer as being internal.
	azureInternalLBAnnotation = "service.beta.kubernetes.io/azure-load-balancer-internal"

	// gcpLBTypeAnnotation is the annotation used on a service to specify a type of GCP
	// load balancer.
	gcpLBTypeAnnotation = "cloud.google.com/load-balancer-type"

	// openstackInternalLBAnnotation is the annotation used on a service to specify an
	// OpenStack load balancer as being internal.
	openstackInternalLBAnnotation = "service.beta.kubernetes.io/openstack-internal-load-balancer"
)

var (
	// InternalLBAnnotations maps platform to the annotation name and value
	// that tell the cloud provider that is associated with the platform
	// that the load balancer is internal.
	//
	// https://kubernetes.io/docs/concepts/services-networking/service/#internal-load-balancer
	InternalLBAnnotations = map[configv1.PlatformType]map[string]string{
		configv1.AWSPlatformType: {
			awsInternalLBAnnotation: "0.0.0.0/0",
		},
		configv1.AzurePlatformType: {
			// Azure load balancers are not customizable and are set to (2 fail @ 5s interval, 2 healthy)
			azureInternalLBAnnotation: "true",
		},
		// There are no required annotations for this platform as of
		// 2019-06-17 (but maybe MetalLB will have something for
		// internal load-balancers in the future).
		configv1.BareMetalPlatformType: nil,
		configv1.GCPPlatformType: {
			gcpLBTypeAnnotation: "Internal",
		},
		// There are no required annotations for this platform.
		configv1.LibvirtPlatformType: nil,
		configv1.OpenStackPlatformType: {
			openstackInternalLBAnnotation: "true",
		},
		configv1.NonePlatformType: nil,
		// vSphere does not support load balancers as of 2019-06-17.
		configv1.VSpherePlatformType: nil,
		configv1.IBMCloudPlatformType: {
			iksLBScopeAnnotation: iksLBScopePrivate,
		},
	}
	// externalLBAnnotations maps platform to the annotation name and value
	// that tell the cloud provider that is associated with the platform
	// that the load balancer is external.  This is the default for most
	// platforms; only platforms for which it is not the default need
	// entries in this map.
	externalLBAnnotations = map[configv1.PlatformType]map[string]string{
		configv1.IBMCloudPlatformType: {
			iksLBScopeAnnotation: iksLBScopePublic,
		},
	}
)

// ensureLoadBalancerService creates an LB service if one is desired but absent.
// Always returns the current LB service if one exists (whether it already
// existed or was created during the course of the function).
func (r *reconciler) ensureLoadBalancerService(ci *operatorv1.IngressController, deploymentRef metav1.OwnerReference, infraConfig *configv1.Infrastructure) (bool, *corev1.Service, error) {
	platform, err := oputil.GetPlatformStatus(r.client, infraConfig)
	if err != nil {
		return false, nil, fmt.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ci.Namespace, ci.Name, err)
	}
	proxyNeeded, err := IsProxyProtocolNeeded(ci, platform)
	if err != nil {
		return false, nil, fmt.Errorf("failed to determine if proxy protocol is proxyNeeded for ingresscontroller %q: %v", ci.Name, err)
	}
	wantLBS, desiredLBService, err := desiredLoadBalancerService(ci, deploymentRef, platform, proxyNeeded)
	if err != nil {
		return false, nil, err
	}

	haveLBS, currentLBService, err := r.currentLoadBalancerService(ci)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !wantLBS && !haveLBS:
		return false, nil, nil
	case !wantLBS && haveLBS:
		if deletedFinalizer, err := r.deleteLoadBalancerServiceFinalizer(currentLBService); err != nil {
			return true, currentLBService, fmt.Errorf("failed to remove finalizer from load balancer service: %v", err)
		} else if deletedFinalizer {
			haveLBS, currentLBService, err = r.currentLoadBalancerService(ci)
			if err != nil {
				return haveLBS, currentLBService, err
			}
		}
		if err := r.deleteLoadBalancerService(currentLBService, &crclient.DeleteOptions{}); err != nil {
			return true, currentLBService, err
		}
		return false, nil, nil
	case wantLBS && !haveLBS:
		if err := r.createLoadBalancerService(desiredLBService); err != nil {
			return false, nil, err
		}
		return r.currentLoadBalancerService(ci)
	case wantLBS && haveLBS:
		if deletedFinalizer, err := r.deleteLoadBalancerServiceFinalizer(currentLBService); err != nil {
			return true, currentLBService, fmt.Errorf("failed to remove finalizer from load balancer service: %v", err)
		} else if deletedFinalizer {
			haveLBS, currentLBService, err = r.currentLoadBalancerService(ci)
			if err != nil {
				return haveLBS, currentLBService, err
			}
		}
		if updated, err := r.updateLoadBalancerService(currentLBService, desiredLBService, platform); err != nil {
			return true, currentLBService, fmt.Errorf("failed to update load balancer service: %v", err)
		} else if updated {
			log.Info("updated load balancer service", "namespace", desiredLBService.Namespace, "name", desiredLBService.Name)
			return r.currentLoadBalancerService(ci)
		}
	}

	return true, currentLBService, nil
}

// desiredLoadBalancerService returns the desired LB service for a
// ingresscontroller, or nil if an LB service isn't desired. An LB service is
// desired if the high availability type is Cloud. An LB service will declare an
// owner reference to the given deployment.
func desiredLoadBalancerService(ci *operatorv1.IngressController, deploymentRef metav1.OwnerReference, platform *configv1.PlatformStatus, proxyNeeded bool) (bool, *corev1.Service, error) {
	if ci.Status.EndpointPublishingStrategy.Type != operatorv1.LoadBalancerServiceStrategyType {
		return false, nil, nil
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

	if proxyNeeded {
		service.Annotations[awsLBProxyProtocolAnnotation] = "*"
	}

	if platform != nil {
		if isInternal {
			annotation := InternalLBAnnotations[platform.Type]
			for name, value := range annotation {
				service.Annotations[name] = value
			}
		} else {
			annotation := externalLBAnnotations[platform.Type]
			for name, value := range annotation {
				service.Annotations[name] = value
			}
		}
		switch platform.Type {
		case configv1.AWSPlatformType:
			if ci.Status.EndpointPublishingStrategy.LoadBalancer != nil &&
				ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters != nil &&
				ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.Type == operatorv1.AWSLoadBalancerProvider &&
				ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS != nil &&
				ci.Status.EndpointPublishingStrategy.LoadBalancer.ProviderParameters.AWS.Type == operatorv1.AWSNetworkLoadBalancer {
				service.Annotations[AWSLBTypeAnnotation] = AWSNLBAnnotation
			}
			// Set the load balancer for AWS to be as aggressive as Azure (2 fail @ 5s interval, 2 healthy)
			service.Annotations[awsLBHealthCheckIntervalAnnotation] = awsLBHealthCheckIntervalDefault
			service.Annotations[awsLBHealthCheckTimeoutAnnotation] = awsLBHealthCheckTimeoutDefault
			service.Annotations[awsLBHealthCheckUnhealthyThresholdAnnotation] = awsLBHealthCheckUnhealthyThresholdDefault
			service.Annotations[awsLBHealthCheckHealthyThresholdAnnotation] = awsLBHealthCheckHealthyThresholdDefault
		}
		// Azure load balancers are not customizable and are set to (2 fail @ 5s interval, 2 healthy)
		// GCP load balancers are not customizable and are set to (3 fail @ 8s interval, 1 healthy)
	}

	service.SetOwnerReferences([]metav1.OwnerReference{deploymentRef})
	return true, service, nil
}

// currentLoadBalancerService returns any existing LB service for the
// ingresscontroller.
func (r *reconciler) currentLoadBalancerService(ci *operatorv1.IngressController) (bool, *corev1.Service, error) {
	service := &corev1.Service{}
	if err := r.client.Get(context.TODO(), controller.LoadBalancerServiceName(ci), service); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, service, nil
}

// createLoadBalancerService creates a load balancer service.
func (r *reconciler) createLoadBalancerService(service *corev1.Service) error {
	if err := r.client.Create(context.TODO(), service); err != nil {
		return fmt.Errorf("failed to create load balancer service %s/%s: %v", service.Namespace, service.Name, err)
	}
	log.Info("created load balancer service", "namespace", service.Namespace, "name", service.Name)
	return nil
}

// deleteLoadBalancerService deletes a load balancer service.
func (r *reconciler) deleteLoadBalancerService(service *corev1.Service, options *crclient.DeleteOptions) error {
	if err := r.client.Delete(context.TODO(), service, options); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete load balancer service %s/%s: %v", service.Namespace, service.Name, err)
	}
	log.Info("deleted load balancer service", "namespace", service.Namespace, "name", service.Name)
	return nil
}

// updateLoadBalancerService updates a load balancer service.  Returns a Boolean
// indicating whether the service was updated, and an error value.
func (r *reconciler) updateLoadBalancerService(current, desired *corev1.Service, platform *configv1.PlatformStatus) (bool, error) {
	changed, updated, needRecreate := loadBalancerServiceChanged(current, desired, platform)
	if !changed {
		return false, nil
	}

	if needRecreate {
		log.Info("load balancer scope changed, which requires deleting and recreating the load balancer", "namespace", updated.Namespace, "name", updated.Name, "platform", platform.Type)
		foreground := metav1.DeletePropagationForeground
		deleteOptions := crclient.DeleteOptions{PropagationPolicy: &foreground}
		if err := r.deleteLoadBalancerService(current, &deleteOptions); err != nil {
			return false, err
		}
		if err := r.createLoadBalancerService(updated); err != nil {
			return false, err
		}
		return true, nil
	}

	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, err
	}
	return true, nil
}

// loadBalancerServiceScopeChanged checks if the load balancer scope changed.
func loadBalancerServiceScopeChanged(current, expected *corev1.Service, platform *configv1.PlatformStatus) bool {
	currentAnnotations := current.Annotations
	if currentAnnotations == nil {
		currentAnnotations = map[string]string{}
	}
	expectedAnnotations := expected.Annotations
	if expectedAnnotations == nil {
		expectedAnnotations = map[string]string{}
	}
	for name := range InternalLBAnnotations[platform.Type] {
		if currentAnnotations[name] != expectedAnnotations[name] {
			return true
		}
	}
	return false
}

// loadBalancerServiceChanged checks if the current load balancer service spec
// matches the expected spec and if not returns an updated one.
func loadBalancerServiceChanged(current, expected *corev1.Service, platform *configv1.PlatformStatus) (bool, *corev1.Service, bool) {
	scopeChanged := loadBalancerServiceScopeChanged(current, expected, platform)

	serviceCmpOpts := []cmp.Option{
		// Ignore fields that the API, other controllers, or user may
		// have modified.
		cmpopts.IgnoreFields(corev1.ServicePort{}, "NodePort"),
		cmpopts.IgnoreFields(corev1.ServiceSpec{}, "ClusterIP", "ExternalIPs", "HealthCheckNodePort"),
		cmp.Comparer(cmpServiceAffinity),
		cmpopts.EquateEmpty(),
	}
	if !scopeChanged && cmp.Equal(current.Spec, expected.Spec, serviceCmpOpts...) {
		return false, nil, false
	}

	updated := current.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	for name := range InternalLBAnnotations[platform.Type] {
		if v, ok := expected.Annotations[name]; ok {
			updated.Annotations[name] = v
		} else {
			delete(updated.Annotations, name)
		}
	}

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

	// When switching between internal scope and external scope, it may be
	// necessary to delete and recreate the service, depending on the cloud
	// provider implementation:
	//
	// * Azure and GCE can handle changing scope, so updating the annotation
	//   on the service suffices.
	//
	// * AWS cannot handle changing scope, so the service needs to be
	//   recreated.
	//
	// * IBM Cloud may or may not handle scope change, so recreate the
	//   service to be safe.
	needsRecreate := false
	if scopeChanged {
		switch platform.Type {
		case configv1.AWSPlatformType, configv1.IBMCloudPlatformType:
			needsRecreate = true
		}
	}

	return true, updated, needsRecreate
}

// deleteLoadBalancerServiceFinalizer removes the
// "ingress.openshift.io/operator" finalizer from the provided load balancer
// service.  This finalizer used to be needed for helping with DNS cleanup, but
// that's no longer necessary.  We just need to clear the finalizer which might
// exist on existing resources.
//
// TODO: Delete this method and all calls to it after 4.7.
func (r *reconciler) deleteLoadBalancerServiceFinalizer(service *corev1.Service) (bool, error) {
	if !slice.ContainsString(service.Finalizers, manifests.LoadBalancerServiceFinalizer) {
		return false, nil
	}

	// Mutate a copy to avoid assuming we know where the current one came from
	// (i.e. it could have been from a cache).
	updated := service.DeepCopy()
	updated.Finalizers = slice.RemoveString(updated.Finalizers, manifests.LoadBalancerServiceFinalizer)
	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, fmt.Errorf("failed to remove finalizer from service %s/%s: %v", service.Namespace, service.Name, err)
	}

	log.Info("removed finalizer from load balancer service", "namespace", service.Namespace, "name", service.Name)

	return true, nil
}

// finalizeLoadBalancerService removes the "ingress.openshift.io/operator" finalizer
// from the load balancer service of ci. This was for helping with DNS cleanup, but
// that's no longer necessary. We just need to clear the finalizer which might exist
// on existing resources.
func (r *reconciler) finalizeLoadBalancerService(ci *operatorv1.IngressController) (bool, error) {
	haveLBS, service, err := r.currentLoadBalancerService(ci)
	if err != nil {
		return false, err
	}
	if !haveLBS {
		return false, nil
	}
	_, err = r.deleteLoadBalancerServiceFinalizer(service)
	return true, err
}
