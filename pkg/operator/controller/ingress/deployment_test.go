package ingress

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var toleration = corev1.Toleration{
	Key:      "foo",
	Value:    "bar",
	Operator: corev1.TolerationOpExists,
	Effect:   corev1.TaintEffectNoExecute,
}

var otherToleration = corev1.Toleration{
	Key:      "xyz",
	Value:    "bar",
	Operator: corev1.TolerationOpExists,
	Effect:   corev1.TaintEffectNoExecute,
}

func checkDeploymentHasEnvVar(t *testing.T, deployment *appsv1.Deployment, name string, expectValue bool, expectedValue string) {
	t.Helper()

	var (
		actualValue string
		foundValue  bool
	)
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == name {
			foundValue = true
			actualValue = envVar.Value
			break
		}
	}
	switch {
	case !expectValue && foundValue:
		t.Errorf("router Deployment has unexpected %s setting: %q", name, actualValue)
	case expectValue && !foundValue:
		t.Errorf("router Deployment is missing %v", name)
	case expectValue && expectedValue != actualValue:
		t.Errorf("router Deployment has unexpected %s setting: expected %q, got %q", name, expectedValue, actualValue)
	}
}

func checkDeploymentDoesNotHaveEnvVar(t *testing.T, deployment *appsv1.Deployment, name string) {
	t.Helper()

	var (
		actualValue string
		foundValue  bool
	)
	for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
		if envVar.Name == name {
			foundValue = true
			actualValue = envVar.Value
			break
		}
	}
	if foundValue {
		t.Errorf("router Deployment has unexpected %s setting: %q", name, actualValue)
	}
}

func checkDeploymentHasContainer(t *testing.T, deployment *appsv1.Deployment, name string, expect bool) {
	t.Helper()

	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == name {
			if expect {
				return
			}
			t.Errorf("router Deployment has unexpected %q container", name)
		}
	}
	if expect {
		t.Errorf("router Deployment has no %q container", name)
	}
}

func checkRollingUpdateParams(t *testing.T, deployment *appsv1.Deployment, maxUnavailable intstr.IntOrString, maxSurge intstr.IntOrString) {
	t.Helper()

	params := deployment.Spec.Strategy.RollingUpdate
	if params == nil {
		t.Error("router Deployment does not specify rolling update parameters")
		return
	}

	if params.MaxSurge == nil {
		t.Errorf("router Deployment does not specify MaxSurge: %#v", params)
	} else if *params.MaxSurge != maxSurge {
		t.Errorf("router Deployment has unexpected MaxSurge value; expected %#v, got %#v", maxSurge, params.MaxSurge)
	}

	if params.MaxUnavailable == nil {
		t.Errorf("router Deployment does not specify MaxUnavailable: %#v", params)
	} else if *params.MaxUnavailable != maxUnavailable {
		t.Errorf("router Deployment has unexpected MaxUnavailable value; expected %#v, got %#v", maxUnavailable, params.MaxUnavailable)
	}
}

func TestDesiredRouterDeployment(t *testing.T) {
	var one int32 = 1
	ingressConfig := &configv1.Ingress{}
	ci := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: operatorv1.IngressControllerSpec{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"foo": "bar",
				},
			},
			Replicas: &one,
			RouteSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"baz": "quux",
				},
			},
		},
		Status: operatorv1.IngressControllerStatus{
			EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
				Type: operatorv1.PrivateStrategyType,
			},
		},
	}
	ingressControllerImage := "quay.io/openshift/router:latest"
	infraConfig := &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
		},
	}
	apiConfig := &configv1.APIServer{
		Spec: configv1.APIServerSpec{
			TLSSecurityProfile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers: []string{
							"foo",
							"bar",
							"baz",
						},
						MinTLSVersion: configv1.VersionTLS11,
					},
				},
			},
		},
	}
	networkConfig := &configv1.Network{
		Status: configv1.NetworkStatus{
			ClusterNetwork: []configv1.ClusterNetworkEntry{
				{CIDR: "10.0.0.1/8"},
			},
		},
	}

	proxyNeeded, err := IsProxyProtocolNeeded(ci, infraConfig.Status.PlatformStatus)
	if err != nil {
		t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ci.Namespace, ci.Name, err)
	}
	deployment, err := desiredRouterDeployment(ci, ingressControllerImage, ingressConfig, apiConfig, networkConfig, proxyNeeded)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}

	expectedHash := deploymentTemplateHash(deployment)
	actualHash, haveHashLabel := deployment.Spec.Template.Labels[controller.ControllerDeploymentHashLabel]
	if !haveHashLabel {
		t.Error("router Deployment is missing hash label")
	} else if actualHash != expectedHash {
		t.Errorf("router Deployment has wrong hash; expected: %s, got: %s", expectedHash, actualHash)
	}

	checkDeploymentHasEnvVar(t, deployment, WildcardRouteAdmissionPolicy, true, "false")

	checkDeploymentHasEnvVar(t, deployment, "NAMESPACE_LABELS", true, "foo=bar")

	checkDeploymentHasEnvVar(t, deployment, "ROUTE_LABELS", true, "baz=quux")

	if deployment.Spec.Replicas == nil {
		t.Error("router Deployment has nil replicas")
	}
	if *deployment.Spec.Replicas != 1 {
		t.Errorf("expected replicas to be 1, got %d", *deployment.Spec.Replicas)
	}

	checkRollingUpdateParams(t, deployment, intstr.FromString("50%"), intstr.FromString("25%"))

	if len(deployment.Spec.Template.Spec.NodeSelector) == 0 {
		t.Error("router Deployment has no default node selector")
	}
	if len(deployment.Spec.Template.Spec.Tolerations) != 0 {
		t.Errorf("router Deployment has unexpected toleration: %#v",
			deployment.Spec.Template.Spec.Tolerations)
	}

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_USE_PROXY_PROTOCOL", false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CANONICAL_HOSTNAME", false, "")

	if deployment.Spec.Template.Spec.Volumes[0].Secret == nil {
		t.Error("router Deployment has no secret volume")
	}

	defaultSecretName := fmt.Sprintf("router-certs-%s", ci.Name)
	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != defaultSecretName {
		t.Errorf("router Deployment expected volume with secret %s, got %s",
			defaultSecretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}

	if deployment.Spec.Template.Spec.HostNetwork != false {
		t.Error("expected host network to be false")
	}

	if len(deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty liveness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host)
	}

	checkDeploymentHasContainer(t, deployment, operatorv1.ContainerLoggingSidecarContainerName, false)
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_FACILITY", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_LEVEL", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_ADDRESS", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_FORMAT", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_REQUEST_HEADERS", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_RESPONSE_HEADERS", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_COOKIE", false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CIPHERS", true, "foo:bar:baz")

	checkDeploymentHasEnvVar(t, deployment, "SSL_MIN_VERSION", true, "TLSv1.1")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_IP_V4_V6_MODE", false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_HEADER_NAME", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_FORMAT", false, "")

	ci.Spec.Logging = &operatorv1.IngressControllerLogging{
		Access: &operatorv1.AccessLogging{
			Destination: operatorv1.LoggingDestination{
				Type:      operatorv1.ContainerLoggingDestinationType,
				Container: &operatorv1.ContainerLoggingDestinationParameters{},
			},
			HttpLogFormat: "%ci:%cp [%t] %ft %b/%s %B %bq %HM %HU %HV",
			HTTPCaptureCookies: []operatorv1.IngressControllerCaptureHTTPCookie{
				{MatchType: "Prefix", NamePrefix: "foo"},
			},
		},
	}
	ci.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		UniqueId: operatorv1.IngressControllerHTTPUniqueIdHeaderPolicy{
			Name: "unique-id",
		},
	}
	ci.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileCustomType,
		Custom: &configv1.CustomTLSProfile{
			TLSProfileSpec: configv1.TLSProfileSpec{
				Ciphers:       []string{"quux"},
				MinTLSVersion: configv1.VersionTLS13,
			},
		},
	}
	networkConfig.Status.ClusterNetwork = []configv1.ClusterNetworkEntry{
		{CIDR: "10.0.0.1/8"},
		{CIDR: "2620:0:2d0:200::7/32"},
	}
	var expectedReplicas int32 = 8
	ci.Spec.Replicas = &expectedReplicas
	ci.Status.Domain = "example.com"
	ci.Status.EndpointPublishingStrategy.Type = operatorv1.LoadBalancerServiceStrategyType
	proxyNeeded, err = IsProxyProtocolNeeded(ci, infraConfig.Status.PlatformStatus)
	if err != nil {
		t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ci.Namespace, ci.Name, err)
	}
	deployment, err = desiredRouterDeployment(ci, ingressControllerImage, ingressConfig, apiConfig, networkConfig, proxyNeeded)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}
	expectedHash = deploymentTemplateHash(deployment)
	actualHash, haveHashLabel = deployment.Spec.Template.Labels[controller.ControllerDeploymentHashLabel]
	if !haveHashLabel {
		t.Error("router Deployment is missing hash label")
	} else if actualHash != expectedHash {
		t.Errorf("router Deployment has wrong hash; expected: %s, got: %s", expectedHash, actualHash)
	}
	checkRollingUpdateParams(t, deployment, intstr.FromString("25%"), intstr.FromString("25%"))
	if deployment.Spec.Template.Spec.HostNetwork != false {
		t.Error("expected host network to be false")
	}
	if len(deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty liveness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host)
	}

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_USE_PROXY_PROTOCOL", true, "true")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CANONICAL_HOSTNAME", true, ci.Status.Domain)

	checkDeploymentHasContainer(t, deployment, operatorv1.ContainerLoggingSidecarContainerName, true)
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_FACILITY", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_LEVEL", true, "debug")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_ADDRESS", true, "/var/lib/rsyslog/rsyslog.sock")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_FORMAT", true, `"%ci:%cp [%t] %ft %b/%s %B %bq %HM %HU %HV"`)
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_COOKIE", true, "foo:256")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SET_FORWARDED_HEADERS", true, "append")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CIPHERS", true, "quux")

	// TODO: Update when haproxy is built with an openssl version that supports tls v1.3.
	checkDeploymentHasEnvVar(t, deployment, "SSL_MIN_VERSION", true, "TLSv1.2")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_IP_V4_V6_MODE", true, "v4v6")

	ci.Status.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope: operatorv1.ExternalLoadBalancer,
		ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.AWSLoadBalancerProvider,
			AWS: &operatorv1.AWSLoadBalancerParameters{
				Type: operatorv1.AWSNetworkLoadBalancer,
			},
		},
	}
	proxyNeeded, err = IsProxyProtocolNeeded(ci, infraConfig.Status.PlatformStatus)
	if err != nil {
		t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ci.Namespace, ci.Name, err)
	}
	deployment, err = desiredRouterDeployment(ci, ingressControllerImage, ingressConfig, apiConfig, networkConfig, proxyNeeded)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}
	expectedHash = deploymentTemplateHash(deployment)
	actualHash, haveHashLabel = deployment.Spec.Template.Labels[controller.ControllerDeploymentHashLabel]
	if !haveHashLabel {
		t.Error("router Deployment is missing hash label")
	} else if actualHash != expectedHash {
		t.Errorf("router Deployment has wrong hash; expected: %s, got: %s", expectedHash, actualHash)
	}
	checkRollingUpdateParams(t, deployment, intstr.FromString("25%"), intstr.FromString("25%"))
	if deployment.Spec.Template.Spec.HostNetwork != false {
		t.Error("expected host network to be false")
	}
	if len(deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty liveness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host)
	}
	checkDeploymentDoesNotHaveEnvVar(t, deployment, "ROUTER_USE_PROXY_PROTOCOL")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_HEADER_NAME", true, "unique-id")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_FORMAT", true, "%{+X}o %ci:%cp_%fi:%fp_%Ts_%rt:%pid")

	secretName := fmt.Sprintf("secret-%v", time.Now().UnixNano())
	ci.Spec.DefaultCertificate = &corev1.LocalObjectReference{
		Name: secretName,
	}
	ci.Spec.Logging = &operatorv1.IngressControllerLogging{
		Access: &operatorv1.AccessLogging{
			Destination: operatorv1.LoggingDestination{
				Type: operatorv1.SyslogLoggingDestinationType,
				Syslog: &operatorv1.SyslogLoggingDestinationParameters{
					Address:  "1.2.3.4",
					Port:     uint32(12345),
					Facility: "local2",
				},
			},
			HTTPCaptureHeaders: operatorv1.IngressControllerCaptureHTTPHeaders{
				Request: []operatorv1.IngressControllerCaptureHTTPHeader{
					{Name: "Host", MaxLength: 15},
					{Name: "Referer", MaxLength: 15},
				},
				Response: []operatorv1.IngressControllerCaptureHTTPHeader{
					{Name: "Content-length", MaxLength: 9},
					{Name: "Location", MaxLength: 15},
				},
			},
			HTTPCaptureCookies: []operatorv1.IngressControllerCaptureHTTPCookie{
				{MatchType: "Exact", Name: "foo", MaxLength: 15},
			},
		},
	}
	ci.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		ForwardedHeaderPolicy: operatorv1.NeverHTTPHeaderPolicy,
		UniqueId: operatorv1.IngressControllerHTTPUniqueIdHeaderPolicy{
			Name:   "unique-id",
			Format: "foo",
		},
	}
	ci.Spec.NodePlacement = &operatorv1.NodePlacement{
		NodeSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"xyzzy": "quux",
			},
		},
		Tolerations: []corev1.Toleration{toleration},
	}
	expectedReplicas = 3
	ci.Spec.Replicas = &expectedReplicas
	ci.Status.EndpointPublishingStrategy.Type = operatorv1.HostNetworkStrategyType
	networkConfig.Status.ClusterNetwork = []configv1.ClusterNetworkEntry{
		{CIDR: "2620:0:2d0:200::7/32"},
	}
	proxyNeeded, err = IsProxyProtocolNeeded(ci, infraConfig.Status.PlatformStatus)
	if err != nil {
		t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ci.Namespace, ci.Name, err)
	}
	deployment, err = desiredRouterDeployment(ci, ingressControllerImage, ingressConfig, apiConfig, networkConfig, proxyNeeded)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}
	actualHash, haveHashLabel = deployment.Spec.Template.Labels[controller.ControllerDeploymentHashLabel]
	if haveHashLabel {
		t.Errorf("router Deployment has unexpected hash label: %s", actualHash)
	}
	if len(deployment.Spec.Template.Spec.NodeSelector) != 1 ||
		deployment.Spec.Template.Spec.NodeSelector["xyzzy"] != "quux" {
		t.Errorf("router Deployment has unexpected node selector: %#v",
			deployment.Spec.Template.Spec.NodeSelector)
	}
	if len(deployment.Spec.Template.Spec.Tolerations) != 1 ||
		!reflect.DeepEqual(ci.Spec.NodePlacement.Tolerations, deployment.Spec.Template.Spec.Tolerations) {
		t.Errorf("router Deployment has unexpected tolerations, expected: %#v,  got: %#v",
			ci.Spec.NodePlacement.Tolerations, deployment.Spec.Template.Spec.Tolerations)
	}
	if deployment.Spec.Replicas == nil {
		t.Error("router Deployment has nil replicas")
	}
	if *deployment.Spec.Replicas != expectedReplicas {
		t.Errorf("expected replicas to be %d, got %d", expectedReplicas, *deployment.Spec.Replicas)
	}
	checkRollingUpdateParams(t, deployment, intstr.FromString("25%"), intstr.FromInt(0))
	if e, a := ingressControllerImage, deployment.Spec.Template.Spec.Containers[0].Image; e != a {
		t.Errorf("expected router Deployment image %q, got %q", e, a)
	}

	if deployment.Spec.Template.Spec.HostNetwork != true {
		t.Error("expected host network to be true")
	}
	if deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host != "localhost" {
		t.Errorf("expected liveness probe host to be \"localhost\", got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host)
	}
	if deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host != "localhost" {
		t.Errorf("expected liveness probe host to be \"localhost\", got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host)
	}

	if deployment.Spec.Template.Spec.Volumes[0].Secret == nil {
		t.Error("router Deployment has no secret volume")
	}
	if deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName != secretName {
		t.Errorf("expected router Deployment volume with secret %s, got %s",
			secretName, deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName)
	}

	checkDeploymentHasContainer(t, deployment, operatorv1.ContainerLoggingSidecarContainerName, false)
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_FACILITY", true, "local2")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_LEVEL", true, "debug")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_ADDRESS", true, "1.2.3.4:12345")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_FORMAT", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_REQUEST_HEADERS", true, "Host:15,Referer:15")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_RESPONSE_HEADERS", true, "Content-length:9,Location:15")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_COOKIE", true, "foo=:15")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SET_FORWARDED_HEADERS", true, "never")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_IP_V4_V6_MODE", true, "v6")
	checkDeploymentHasEnvVar(t, deployment, RouterDisableHTTP2EnvName, true, "true")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_HEADER_NAME", true, "unique-id")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_FORMAT", true, "foo")
}

func TestInferTLSProfileSpecFromDeployment(t *testing.T) {
	testCases := []struct {
		description string
		containers  []corev1.Container
		expected    *configv1.TLSProfileSpec
	}{
		{
			description: "no router container -> empty spec",
			containers:  []corev1.Container{{Name: "foo"}},
			expected:    &configv1.TLSProfileSpec{},
		},
		{
			description: "missing environment variables -> nil ciphers, TLSv1.2",
			containers:  []corev1.Container{{Name: "router"}},
			expected: &configv1.TLSProfileSpec{
				Ciphers:       nil,
				MinTLSVersion: configv1.VersionTLS12,
			},
		},
		{
			description: "normal deployment -> normal profile",
			containers: []corev1.Container{
				{
					Name: "router",
					Env: []corev1.EnvVar{
						{
							Name:  "ROUTER_CIPHERS",
							Value: "foo:bar:baz",
						},
						{
							Name:  "SSL_MIN_VERSION",
							Value: "TLSv1.1",
						},
					},
				},
				{
					Name: "logs",
				},
			},
			expected: &configv1.TLSProfileSpec{
				Ciphers:       []string{"foo", "bar", "baz"},
				MinTLSVersion: configv1.VersionTLS11,
			},
		},
	}
	for _, tc := range testCases {
		deployment := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: tc.containers,
					},
				},
			},
		}
		tlsProfileSpec := inferTLSProfileSpecFromDeployment(deployment)
		if !reflect.DeepEqual(tlsProfileSpec, tc.expected) {
			t.Errorf("%q: expected %#v, got %#v", tc.description, tc.expected, tlsProfileSpec)
		}
	}
}

// TestDeploymentHash verifies that the hash values that deploymentHash and
// deploymentTemplateHash return change exactly when expected with respect to
// mutations to a deployment.
func TestDeploymentHash(t *testing.T) {
	three := int32(3)
	testCases := []struct {
		description                 string
		mutate                      func(*appsv1.Deployment)
		expectDeploymentHashChanged bool
		expectTemplateHashChanged   bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *appsv1.Deployment) {},
		},
		{
			description: "if .uid changes",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.UID = "2"
			},
		},
		{
			description: "if .name changes",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Name = "foo"
			},
			expectDeploymentHashChanged: true,
			expectTemplateHashChanged:   true,
		},
		{
			description: "if .spec.replicas changes",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Replicas = &three
			},
			expectDeploymentHashChanged: true,
		},
		{
			description: "if .spec.template.spec.tolerations change",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Tolerations = []corev1.Toleration{toleration}
			},
			expectDeploymentHashChanged: true,
			expectTemplateHashChanged:   true,
		},
	}

	for _, tc := range testCases {
		defaultReplicas := int32(3)
		original := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "router-original",
				Namespace: "openshift-ingress",
				UID:       "1",
			},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Tolerations: []corev1.Toleration{toleration, otherToleration},
					},
				},
				Replicas: &defaultReplicas,
			},
		}
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		deploymentHashChanged := deploymentHash(original) != deploymentHash(mutated)
		templateHashChanged := deploymentTemplateHash(original) != deploymentTemplateHash(mutated)
		if templateHashChanged && !deploymentHashChanged {
			t.Errorf("%q: deployment hash changed but the template hash did not", tc.description)
		}
		if deploymentHashChanged != tc.expectDeploymentHashChanged {
			t.Errorf("%q: expected deployment hash changed to be %t, got %t", tc.description, tc.expectDeploymentHashChanged, deploymentHashChanged)
		}
		if templateHashChanged != tc.expectTemplateHashChanged {
			t.Errorf("%q: expected template hash changed to be %t, got %t", tc.description, tc.expectTemplateHashChanged, templateHashChanged)
		}
	}
}

func TestDeploymentConfigChanged(t *testing.T) {
	pointerTo := func(ios intstr.IntOrString) *intstr.IntOrString { return &ios }
	testCases := []struct {
		description string
		mutate      func(*appsv1.Deployment)
		expect      bool
	}{
		{
			description: "if nothing changes",
			mutate:      func(_ *appsv1.Deployment) {},
			expect:      false,
		},
		{
			description: "if .uid changes",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.UID = "2"
			},
			expect: false,
		},
		{
			description: "if the deployment hash changes",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Labels[controller.ControllerDeploymentHashLabel] = "2"
			},
			expect: false,
		},
		{
			description: "if .spec.template.spec.volumes is set to empty",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Volumes = []corev1.Volume{}
			},
			expect: true,
		},
		{
			description: "if .spec.template.spec.volumes is set to nil",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Volumes = nil
			},
			expect: true,
		},
		{
			description: "if the default-certificates default mode value changes",
			mutate: func(deployment *appsv1.Deployment) {
				newVal := int32(0)
				deployment.Spec.Template.Spec.Volumes[0].Secret.DefaultMode = &newVal
			},
			expect: true,
		},
		{
			description: "if the default-certificates default mode value is omitted",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Volumes[0].Secret.DefaultMode = nil
			},
			expect: false,
		},
		{
			description: "if .spec.template.spec.nodeSelector changes",
			mutate: func(deployment *appsv1.Deployment) {
				ns := map[string]string{"xyzzy": "quux"}
				deployment.Spec.Template.Spec.NodeSelector = ns
			},
			expect: true,
		},
		{
			description: "if .spec.template.spec.tolerations change",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Tolerations = []corev1.Toleration{toleration}
			},
			expect: true,
		},
		{
			description: "if the tolerations change ordering",
			mutate: func(deployment *appsv1.Deployment) {
				tolerations := deployment.Spec.Template.Spec.Tolerations
				tolerations[1], tolerations[0] = tolerations[0], tolerations[1]
			},
			expect: false,
		},
		{
			description: "if ROUTER_CANONICAL_HOSTNAME changes",
			mutate: func(deployment *appsv1.Deployment) {
				envs := deployment.Spec.Template.Spec.Containers[0].Env
				for i, env := range envs {
					if env.Name == "ROUTER_CANONICAL_HOSTNAME" {
						envs[i].Value = "mutated.example.com"
					}
				}
				deployment.Spec.Template.Spec.Containers[0].Env = envs
			},
			expect: true,
		},
		{
			description: "if ROUTER_USE_PROXY_PROTOCOL changes",
			mutate: func(deployment *appsv1.Deployment) {
				envs := deployment.Spec.Template.Spec.Containers[0].Env
				for i, env := range envs {
					if env.Name == "ROUTER_USE_PROXY_PROTOCOL" {
						envs[i].Value = "true"
					}
				}
				deployment.Spec.Template.Spec.Containers[0].Env = envs
			},
			expect: true,
		},
		{
			description: "if ROUTER_ALLOW_WILDCARD_ROUTES changes",
			mutate: func(deployment *appsv1.Deployment) {
				envs := deployment.Spec.Template.Spec.Containers[0].Env
				for i, env := range envs {
					if env.Name == WildcardRouteAdmissionPolicy {
						envs[i].Value = "true"
					}
				}
				deployment.Spec.Template.Spec.Containers[0].Env = envs
			},
			expect: true,
		},
		{
			description: "if NAMESPACE_LABELS is added",
			mutate: func(deployment *appsv1.Deployment) {
				envs := deployment.Spec.Template.Spec.Containers[0].Env
				env := corev1.EnvVar{
					Name:  "NAMESPACE_LABELS",
					Value: "x=y",
				}
				envs = append(envs, env)
				deployment.Spec.Template.Spec.Containers[0].Env = envs
			},
			expect: true,
		},
		{
			description: "if ROUTE_LABELS is deleted",
			mutate: func(deployment *appsv1.Deployment) {
				oldEnvs := deployment.Spec.Template.Spec.Containers[0].Env
				newEnvs := []corev1.EnvVar{}
				for _, env := range oldEnvs {
					if env.Name != "ROUTE_LABELS" {
						newEnvs = append(newEnvs, env)
					}
				}
				deployment.Spec.Template.Spec.Containers[0].Env = newEnvs
			},
			expect: true,
		},
		{
			description: "if the container image is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].Image = "openshift/origin-cluster-ingress-operator:latest"
			},
			expect: true,
		},
		{
			description: "if the volumes change ordering",
			mutate: func(deployment *appsv1.Deployment) {
				vols := deployment.Spec.Template.Spec.Volumes
				vols[1], vols[0] = vols[0], vols[1]
			},
			expect: false,
		},
		{
			description: "if the rolling update strategy's max surge parameter is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Strategy.RollingUpdate.MaxSurge = pointerTo(intstr.FromString("25%"))
			},
			expect: true,
		},
		{
			description: "if the rolling update strategy's max unavailable parameter is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Strategy.RollingUpdate.MaxUnavailable = pointerTo(intstr.FromString("50%"))
			},
			expect: true,
		},
		{
			description: "if the deployment template affinity is deleted",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Affinity = nil
			},
			expect: true,
		},
		{
			description: "if the deployment template anti-affinity is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchExpressions[1].Values = []string{"xyz"}
			},
			expect: true,
		},
		{
			description: "if the deployment template affinity label selector expressions change ordering",
			mutate: func(deployment *appsv1.Deployment) {
				exprs := deployment.Spec.Template.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.LabelSelector.MatchExpressions
				exprs[0], exprs[1] = exprs[1], exprs[0]
			},
			expect: false,
		},
		{
			description: "if the deployment template anti-affinity label selector expressions change ordering",
			mutate: func(deployment *appsv1.Deployment) {
				exprs := deployment.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchExpressions
				exprs[0], exprs[1] = exprs[1], exprs[0]
			},
			expect: false,
		},
		{
			description: "if the hash in the deployment template affinity is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.LabelSelector.MatchExpressions[0].Values = []string{"2"}
			},
			expect: false,
		},
		{
			description: "if probe values are set to default values",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Scheme = "HTTP"
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = int32(1)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.PeriodSeconds = int32(10)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.SuccessThreshold = int32(1)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.FailureThreshold = int32(3)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Scheme = "HTTP"
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.TimeoutSeconds = int32(1)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.PeriodSeconds = int32(10)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.SuccessThreshold = int32(1)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.FailureThreshold = int32(3)
			},
			expect: false,
		},
		{
			description: "if probe values are set to non-default values",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Scheme = "HTTPS"
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.PeriodSeconds = int32(20)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.SuccessThreshold = int32(2)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.FailureThreshold = int32(30)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Scheme = "HTTPS"
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.PeriodSeconds = int32(20)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.SuccessThreshold = int32(2)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.FailureThreshold = int32(30)
			},
			expect: true,
		},
	}

	for _, tc := range testCases {
		nineteen := int32(19)
		fourTwenty := int32(420) // = 0644 octal.
		original := appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "router-original",
				Namespace: "openshift-ingress",
				UID:       "1",
			},
			Spec: appsv1.DeploymentSpec{
				Strategy: appsv1.DeploymentStrategy{
					Type: appsv1.RollingUpdateDeploymentStrategyType,
					RollingUpdate: &appsv1.RollingUpdateDeployment{
						MaxUnavailable: pointerTo(intstr.FromString("25%")),
						MaxSurge:       pointerTo(intstr.FromInt(0)),
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							controller.ControllerDeploymentHashLabel: "1",
						},
					},
					Spec: corev1.PodSpec{
						Volumes: []corev1.Volume{
							{
								Name: "default-certificate",
								VolumeSource: corev1.VolumeSource{
									Secret: &corev1.SecretVolumeSource{
										SecretName:  "secrets-volume",
										DefaultMode: &fourTwenty,
									},
								},
							},
							{
								Name: "metrics-certs",
								VolumeSource: corev1.VolumeSource{
									Secret: &corev1.SecretVolumeSource{
										SecretName: "router-metrics-certs-default",
									},
								},
							},
						},
						Containers: []corev1.Container{
							{
								Env: []corev1.EnvVar{
									{
										Name:  "ROUTER_CANONICAL_HOSTNAME",
										Value: "example.com",
									},
									{
										Name:  "ROUTER_USE_PROXY_PROTOCOL",
										Value: "false",
									},
									{
										Name:  "ROUTER_ALLOW_WILDCARD_ROUTES",
										Value: "false",
									},
									{
										Name:  "ROUTE_LABELS",
										Value: "foo=bar",
									},
								},
								Image: "openshift/origin-cluster-ingress-operator:v4.0",
								LivenessProbe: &corev1.Probe{
									Handler: corev1.Handler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/healthz",
											Port: intstr.IntOrString{
												IntVal: int32(1936),
											},
										},
									},
								},
								ReadinessProbe: &corev1.Probe{
									Handler: corev1.Handler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/healthz/ready",
											Port: intstr.IntOrString{
												IntVal: int32(1936),
											},
										},
									},
								},
							},
						},
						Affinity: &corev1.Affinity{
							PodAffinity: &corev1.PodAffinity{
								PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
									{
										Weight: int32(100),
										PodAffinityTerm: corev1.PodAffinityTerm{
											TopologyKey: "kubernetes.io/hostname",
											LabelSelector: &metav1.LabelSelector{
												MatchExpressions: []metav1.LabelSelectorRequirement{
													{
														Key:      controller.ControllerDeploymentHashLabel,
														Operator: metav1.LabelSelectorOpNotIn,
														Values:   []string{"1"},
													},
													{
														Key:      controller.ControllerDeploymentLabel,
														Operator: metav1.LabelSelectorOpIn,
														Values:   []string{"default"},
													},
												},
											},
										},
									},
								},
							},
							PodAntiAffinity: &corev1.PodAntiAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
									{
										TopologyKey: "kubernetes.io/hostname",
										LabelSelector: &metav1.LabelSelector{
											MatchExpressions: []metav1.LabelSelectorRequirement{
												{
													Key:      controller.ControllerDeploymentHashLabel,
													Operator: metav1.LabelSelectorOpIn,
													Values:   []string{"1"},
												},
												{
													Key:      controller.ControllerDeploymentLabel,
													Operator: metav1.LabelSelectorOpIn,
													Values:   []string{"default"},
												},
											},
										},
									},
								},
							},
						},
						Tolerations: []corev1.Toleration{toleration, otherToleration},
					},
				},
				Replicas: &nineteen,
			},
		}
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		if changed, updated := deploymentConfigChanged(&original, mutated); changed != tc.expect {
			t.Errorf("%s, expect deploymentConfigChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := deploymentConfigChanged(mutated, updated); changedAgain {
				t.Errorf("%s, deploymentConfigChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}
