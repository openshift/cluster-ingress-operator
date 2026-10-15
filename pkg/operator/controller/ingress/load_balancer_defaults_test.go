package ingress

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLoadBalancerPlatformDefaults(t *testing.T) {
	t.Parallel()
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.AzurePlatformType, configv1.GCPPlatformType, configv1.IBMCloudPlatformType, configv1.PowerVSPlatformType, configv1.OpenStackPlatformType, configv1.VSpherePlatformType} {
		for _, internal := range []bool{false, true} {
			t.Run(string(platform)+"/"+map[bool]string{true: "internal", false: "external"}[internal], func(t *testing.T) {
				t.Parallel()
				status := &configv1.PlatformStatus{Type: platform}
				service := LoadBalancerServiceDefaults(status, internal)
				require.Equal(t, corev1.ServiceTypeLoadBalancer, service.Spec.Type)
				policy := corev1.ServiceExternalTrafficPolicyLocal
				if platform == configv1.IBMCloudPlatformType || platform == configv1.PowerVSPlatformType {
					policy = corev1.ServiceExternalTrafficPolicyCluster
				}
				require.Equal(t, policy, service.Spec.ExternalTrafficPolicy)
				_, fallback := service.Annotations[localWithFallbackAnnotation]
				require.Equal(t, policy == corev1.ServiceExternalTrafficPolicyLocal, fallback)
				if internal {
					for key, value := range InternalLBAnnotations[platform] {
						require.Equal(t, value, service.Annotations[key])
					}
				}
				if platform == configv1.GCPPlatformType && internal {
					require.Equal(t, "false", service.Annotations[GCPGlobalAccessAnnotation])
				}
			})
		}
	}
}

func TestSharedAWSLoadBalancerDefaultsPreserveIngress(t *testing.T) {
	t.Parallel()
	for _, family := range []configv1.IPFamilyType{configv1.DualStackIPv4Primary, configv1.DualStackIPv6Primary} {
		t.Run(string(family), func(t *testing.T) {
			t.Parallel()
			platform := &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{IPFamily: family, ResourceTags: []configv1.AWSResourceTag{{Key: "cluster", Value: "test"}, {Key: "", Value: "ignored"}}}}
			profile := LoadBalancerServiceDefaults(platform, true)
			require.Equal(t, "10", profile.Annotations[awsLBHealthCheckIntervalAnnotation])
			require.Equal(t, "4", profile.Annotations[awsLBHealthCheckTimeoutAnnotation])
			require.Equal(t, "2", profile.Annotations[awsLBHealthCheckUnhealthyThresholdAnnotation])
			require.Equal(t, "2", profile.Annotations[awsLBHealthCheckHealthyThresholdAnnotation])
			require.Equal(t, "cluster=test", profile.Annotations[awsLBAdditionalResourceTags])
			require.Equal(t, awsNLBTargetGroupAttributesProxy, profile.Annotations[awsLBTargetGroupAttributesAnnotation])
			controller := &operatorv1.IngressController{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
			controller.Status.EndpointPublishingStrategy = &operatorv1.EndpointPublishingStrategy{Type: operatorv1.LoadBalancerServiceStrategyType, LoadBalancer: &operatorv1.LoadBalancerStrategy{Scope: operatorv1.InternalLoadBalancer, ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{Type: operatorv1.AWSLoadBalancerProvider, AWS: &operatorv1.AWSLoadBalancerParameters{Type: operatorv1.AWSNetworkLoadBalancer}}}}
			_, legacy, err := desiredLoadBalancerService(controller, metav1.OwnerReference{}, platform, nil, false)
			require.NoError(t, err)
			require.Equal(t, profile.Spec.IPFamilies, legacy.Spec.IPFamilies)
			require.Equal(t, profile.Spec.IPFamilyPolicy, legacy.Spec.IPFamilyPolicy)
			require.Equal(t, profile.Annotations[awsLBAdditionalResourceTags], legacy.Annotations[awsLBAdditionalResourceTags])
			require.NotEqual(t, profile.Annotations[awsLBTargetGroupAttributesAnnotation], legacy.Annotations[awsLBTargetGroupAttributesAnnotation])
		})
	}
}
