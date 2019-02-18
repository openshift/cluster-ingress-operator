package controller

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/util/slice"

	appsv1 "k8s.io/api/apps/v1"
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
func (r *reconciler) ensureLoadBalancerService(ci *ingressv1alpha1.ClusterIngress, deployment *appsv1.Deployment, infraConfig *configv1.Infrastructure) (*corev1.Service, error) {
	desiredLBService, err := r.desiredLoadBalancerService(ci, deployment, infraConfig)
	if err != nil {
		return nil, err
	}

	currentLBService, err := r.currentLoadBalancerService(ci)
	if err != nil {
		return nil, err
	}
	if desiredLBService != nil && currentLBService == nil {
		err = r.Client.Create(context.TODO(), desiredLBService)
		if err == nil {
			logrus.Infof("created load balancer service %s/%s", desiredLBService.Namespace, desiredLBService.Name)
		} else if !errors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create load balancer service %s/%s: %v", desiredLBService.Namespace, desiredLBService.Name, err)
		}
		return r.currentLoadBalancerService(ci)
	}
	return currentLBService, nil
}

// TODO: This should take operator config into account so that the operand
// namespace isn't hard-coded.
func loadBalancerServiceName(ci *ingressv1alpha1.ClusterIngress) types.NamespacedName {
	return types.NamespacedName{Namespace: "openshift-ingress", Name: "router-" + ci.Name}
}

// desiredLoadBalancerService returns the desired LB service for a
// clusteringress, or nil if an LB service isn't desired. An LB service is
// desired if the high availability type is Cloud. An LB service will declare an
// owner reference to the given deployment.
func (r *reconciler) desiredLoadBalancerService(ci *ingressv1alpha1.ClusterIngress, deployment *appsv1.Deployment, infraConfig *configv1.Infrastructure) (*corev1.Service, error) {
	if ci.Spec.HighAvailability == nil || ci.Spec.HighAvailability.Type != ingressv1alpha1.CloudClusterIngressHA {
		return nil, nil
	}
	trueVar := true
	deploymentRef := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
		Controller: &trueVar,
	}
	service := manifests.LoadBalancerService()

	name := loadBalancerServiceName(ci)

	service.Namespace = name.Namespace
	service.Name = name.Name

	if service.Labels == nil {
		service.Labels = map[string]string{}
	}
	service.Labels["router"] = name.Name
	service.Labels[manifests.OwningClusterIngressLabel] = ci.Name

	if service.Spec.Selector == nil {
		service.Spec.Selector = map[string]string{}
	}
	service.Spec.Selector["router"] = name.Name

	if infraConfig.Status.Platform == configv1.AWSPlatform {
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
// clusteringress.
func (r *reconciler) currentLoadBalancerService(ci *ingressv1alpha1.ClusterIngress) (*corev1.Service, error) {
	service := &corev1.Service{}
	err := r.Client.Get(context.TODO(), loadBalancerServiceName(ci), service)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return service, nil
}

// finalizeLoadBalancerService deletes any DNS entries associated with any
// current LB service associated with the clusteringress and then finalizes the
// service.
func (r *reconciler) finalizeLoadBalancerService(ci *ingressv1alpha1.ClusterIngress, dnsConfig *configv1.DNS) error {
	service, err := r.currentLoadBalancerService(ci)
	if err != nil {
		return err
	}
	if service == nil {
		return nil
	}
	records, err := r.desiredDNSRecords(ci, service, dnsConfig)
	if err != nil {
		return err
	}
	dnsErrors := []error{}
	for _, record := range records {
		err := r.DNSManager.Delete(record)
		if err != nil {
			dnsErrors = append(dnsErrors, fmt.Errorf("failed to delete DNS record %v for ingress %s: %v", record, ci.Name, err))
		} else {
			logrus.Infof("deleted DNS record %v for ingress %s", record, ci.Name)
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
		err = r.Client.Update(context.TODO(), updated)
		if err != nil {
			return fmt.Errorf("failed to remove finalizer from service %s for ingress %s: %v", service.Name, ci.Name, err)
		}
	}
	return nil
}
