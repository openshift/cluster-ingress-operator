package ingress

import (
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	awsutil "github.com/openshift/cluster-ingress-operator/pkg/util/aws"
	corev1 "k8s.io/api/core/v1"
)

// applyLoadBalancerPlatformDefaults applies platform-specific annotations and traffic policy defaults.
func applyLoadBalancerPlatformDefaults(service *corev1.Service, platform *configv1.PlatformStatus, internal bool) {
	if platform == nil {
		return
	}
	if service.Annotations == nil {
		service.Annotations = map[string]string{}
	}
	annotations := externalLBAnnotations[platform.Type]
	if internal {
		annotations = InternalLBAnnotations[platform.Type]
	}
	for name, value := range annotations {
		service.Annotations[name] = value
	}
	switch platform.Type {
	case configv1.AWSPlatformType:
		service.Annotations[awsLBHealthCheckIntervalAnnotation] = awsLBHealthCheckIntervalDefault
		service.Annotations[awsLBHealthCheckTimeoutAnnotation] = awsLBHealthCheckTimeoutDefault
		service.Annotations[awsLBHealthCheckUnhealthyThresholdAnnotation] = awsLBHealthCheckUnhealthyThresholdDefault
		service.Annotations[awsLBHealthCheckHealthyThresholdAnnotation] = awsLBHealthCheckHealthyThresholdDefault
		if platform.AWS != nil {
			var tags []string
			for _, tag := range platform.AWS.ResourceTags {
				if tag.Key != "" {
					tags = append(tags, tag.Key+"="+tag.Value)
				}
			}
			if len(tags) > 0 {
				service.Annotations[awsLBAdditionalResourceTags] = strings.Join(tags, ",")
			}
		}
	case configv1.IBMCloudPlatformType, configv1.PowerVSPlatformType:
		service.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyCluster
	}
}

// applyAWSLoadBalancerIPFamilies sets Service IP families for the load balancer type on dual-stack AWS clusters.
func applyAWSLoadBalancerIPFamilies(service *corev1.Service, platform *configv1.PlatformStatus, loadBalancerType operatorv1.AWSLoadBalancerType) {
	if platform == nil || platform.AWS == nil || !awsutil.IsDualStack(platform.AWS.IPFamily) {
		return
	}
	switch loadBalancerType {
	case operatorv1.AWSNetworkLoadBalancer:
		policy := corev1.IPFamilyPolicyRequireDualStack
		service.Spec.IPFamilyPolicy = &policy
		service.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}
		if platform.AWS.IPFamily == configv1.DualStackIPv6Primary {
			service.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol}
		}
	case operatorv1.AWSClassicLoadBalancer:
		policy := corev1.IPFamilyPolicySingleStack
		service.Spec.IPFamilyPolicy = &policy
		service.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol}
	}
}

// LoadBalancerServiceDefaults builds platform-derived Service defaults without IngressController overrides.
func LoadBalancerServiceDefaults(platform *configv1.PlatformStatus, internal bool) *corev1.Service {
	service := &corev1.Service{Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeLoadBalancer, ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
	}}
	applyLoadBalancerPlatformDefaults(service, platform, internal)
	if service.Annotations == nil {
		service.Annotations = map[string]string{}
	}
	if service.Spec.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyLocal {
		service.Annotations[localWithFallbackAnnotation] = ""
	}
	if platform != nil {
		switch platform.Type {
		case configv1.AWSPlatformType:
			service.Annotations[AWSLBTypeAnnotation] = AWSNLBAnnotation
			service.Annotations[awsLBHealthCheckIntervalAnnotation] = awsLBHealthCheckIntervalNLB
			service.Annotations[awsLBTargetGroupAttributesAnnotation] = awsNLBTargetGroupAttributesProxy
			applyAWSLoadBalancerIPFamilies(service, platform, operatorv1.AWSNetworkLoadBalancer)
		case configv1.GCPPlatformType:
			if internal {
				service.Annotations[GCPGlobalAccessAnnotation] = "false"
			}
		}
	}
	return service
}
