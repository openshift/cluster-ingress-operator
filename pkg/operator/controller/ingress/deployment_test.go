package ingress

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

func checkDeploymentHasEnvSorted(t *testing.T, deployment *appsv1.Deployment) {
	t.Helper()

	env := deployment.Spec.Template.Spec.Containers[0].Env
	isSorted := sort.SliceIsSorted(env, func(i, j int) bool {
		return env[i].Name < env[j].Name
	})
	if !isSorted {
		t.Errorf("router Deployment environment variables are unsorted")
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

func checkDeploymentHash(t *testing.T, deployment *appsv1.Deployment) {
	t.Helper()
	expectedHash := deploymentTemplateHash(deployment, false)
	actualHash, haveHashLabel := deployment.Spec.Template.Labels[controller.ControllerDeploymentHashLabel]
	if !haveHashLabel {
		t.Error("router Deployment is missing hash label")
	} else if actualHash != expectedHash {
		t.Errorf("router Deployment has wrong hash; expected: %s, got: %s", expectedHash, actualHash)
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
	deployment, err := desiredRouterDeployment(ci, ingressControllerImage, ingressConfig, apiConfig, networkConfig, proxyNeeded, false, nil, false)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}

	checkDeploymentHash(t, deployment)

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

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_HAPROXY_CONFIG_MANAGER", false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOAD_BALANCE_ALGORITHM", true, "leastconn")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_TCP_BALANCE_SCHEME", true, "source")
	checkDeploymentDoesNotHaveEnvVar(t, deployment, "ROUTER_ERRORFILE_503")
	checkDeploymentDoesNotHaveEnvVar(t, deployment, "ROUTER_ERRORFILE_404")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_MAX_CONNECTIONS", false, "")

	checkDeploymentHasEnvVar(t, deployment, "RELOAD_INTERVAL", true, "5s")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_USE_PROXY_PROTOCOL", false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CANONICAL_HOSTNAME", false, "")

	checkDeploymentHasEnvVar(t, deployment, "STATS_USERNAME_FILE", true, "/var/lib/haproxy/conf/metrics-auth/statsUsername")
	checkDeploymentHasEnvVar(t, deployment, "STATS_PASSWORD_FILE", true, "/var/lib/haproxy/conf/metrics-auth/statsPassword")

	expectedVolumeSecretPairs := map[string]string{
		"default-certificate": fmt.Sprintf("router-certs-%s", ci.Name),
		"metrics-certs":       fmt.Sprintf("router-metrics-certs-%s", ci.Name),
		"stats-auth":          fmt.Sprintf("router-stats-%s", ci.Name),
	}

	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if secretName, ok := expectedVolumeSecretPairs[volume.Name]; ok {
			if volume.Secret.SecretName != secretName {
				t.Errorf("router Deployment expected volume %s to have secret %s, got %s", volume.Name, secretName, volume.Secret.SecretName)
			}
		} else if volume.Name != "service-ca-bundle" {
			t.Errorf("router deployment has unexpected volume %s", volume.Name)
		}
	}

	if expected, got := 2, len(deployment.Spec.Template.Annotations); expected != got {
		t.Errorf("expected len(annotations)=%v, got %v", expected, got)
	}

	if val, ok := deployment.Spec.Template.Annotations[LivenessGracePeriodSecondsAnnotation]; !ok {
		t.Errorf("missing annotation %q", LivenessGracePeriodSecondsAnnotation)
	} else if expected := "10"; expected != val {
		t.Errorf("expected annotation %q to be %q, got %q", LivenessGracePeriodSecondsAnnotation, expected, val)
	}

	if val, ok := deployment.Spec.Template.Annotations[WorkloadPartitioningManagement]; !ok {
		t.Errorf("missing annotation %q", WorkloadPartitioningManagement)
	} else if expected := "{\"effect\": \"PreferredDuringScheduling\"}"; expected != val {
		t.Errorf("expected annotation %q to be %q, got %q", WorkloadPartitioningManagement, expected, val)
	}

	if deployment.Spec.Template.Spec.HostNetwork != false {
		t.Error("expected host network to be false")
	}

	if deployment.Spec.Template.Spec.DNSPolicy != corev1.DNSClusterFirst {
		t.Errorf("expected dnsPolicy to be %s, got %s", corev1.DNSClusterFirst, deployment.Spec.Template.Spec.DNSPolicy)
	}

	if len(deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty liveness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty startup probe host, got %q", deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Host)
	}

	checkDeploymentHasContainer(t, deployment, operatorv1.ContainerLoggingSidecarContainerName, false)
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_FACILITY", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_MAX_LENGTH", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_LEVEL", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_ADDRESS", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_FORMAT", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_REQUEST_HEADERS", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_RESPONSE_HEADERS", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_COOKIE", false, "")
	checkDeploymentHasEnvVar(t, deployment, RouterDontLogNull, false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_BUF_SIZE", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_MAX_REWRITE_SIZE", false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CIPHERS", true, "foo:bar:baz")
	checkDeploymentDoesNotHaveEnvVar(t, deployment, "ROUTER_CIPHERSUITES")

	checkDeploymentHasEnvVar(t, deployment, "SSL_MIN_VERSION", true, "TLSv1.1")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_IP_V4_V6_MODE", false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_HEADER_NAME", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_FORMAT", false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_H1_CASE_ADJUST", false, "")

	checkDeploymentHasEnvVar(t, deployment, RouterHAProxyThreadsEnvName, true, strconv.Itoa(RouterHAProxyThreadsDefaultValue))

	checkDeploymentHasEnvVar(t, deployment, RouterHTTPIgnoreProbes, false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_DEFAULT_CLIENT_TIMEOUT", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CLIENT_FIN_TIMEOUT", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_DEFAULT_SERVER_TIMEOUT", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_DEFAULT_SERVER_FIN_TIMEOUT", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_DEFAULT_TUNNEL_TIMEOUT", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_INSPECT_DELAY", false, "")

	checkDeploymentHasEnvVar(t, deployment, RouterEnableCompression, false, "")
	checkDeploymentHasEnvVar(t, deployment, RouterCompressionMIMETypes, false, "")

	checkDeploymentHasEnvSorted(t, deployment)

	ci.Spec.Logging = &operatorv1.IngressControllerLogging{
		Access: &operatorv1.AccessLogging{
			Destination: operatorv1.LoggingDestination{
				Type:      operatorv1.ContainerLoggingDestinationType,
				Container: &operatorv1.ContainerLoggingDestinationParameters{},
			},
			HttpLogFormat: "%ci:%cp [%t] %ft %b/%s %B %bq %HM %HU %HV",
			HTTPCaptureCookies: []operatorv1.IngressControllerCaptureHTTPCookie{{
				IngressControllerCaptureHTTPCookieUnion: operatorv1.IngressControllerCaptureHTTPCookieUnion{
					MatchType:  "Prefix",
					NamePrefix: "foo",
				},
			}},
			LogEmptyRequests: "Ignore",
		},
	}
	ci.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		UniqueId: operatorv1.IngressControllerHTTPUniqueIdHeaderPolicy{
			Name: "unique-id",
		},
		HeaderNameCaseAdjustments: []operatorv1.IngressControllerHTTPHeaderNameCaseAdjustment{
			"Host",
			"Cache-Control",
		},
	}
	ci.Spec.TuningOptions = operatorv1.IngressControllerTuningOptions{
		HeaderBufferBytes:           16384,
		HeaderBufferMaxRewriteBytes: 4096,
		ThreadCount:                 RouterHAProxyThreadsDefaultValue * 2,
	}
	ci.Spec.HTTPEmptyRequestsPolicy = "Ignore"
	ci.Spec.TuningOptions.ClientTimeout = &metav1.Duration{45 * time.Second}
	ci.Spec.TuningOptions.ClientFinTimeout = &metav1.Duration{3 * time.Second}
	ci.Spec.TuningOptions.ServerTimeout = &metav1.Duration{60 * time.Second}
	ci.Spec.TuningOptions.ServerFinTimeout = &metav1.Duration{4 * time.Second}
	ci.Spec.TuningOptions.TunnelTimeout = &metav1.Duration{30 * time.Minute}
	ci.Spec.TuningOptions.TLSInspectDelay = &metav1.Duration{5 * time.Second}
	ci.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileCustomType,
		Custom: &configv1.CustomTLSProfile{
			TLSProfileSpec: configv1.TLSProfileSpec{
				Ciphers: []string{
					"quux",
					"TLS_AES_256_GCM_SHA384",
					"TLS_CHACHA20_POLY1305_SHA256",
				},
				MinTLSVersion: configv1.VersionTLS12,
			},
		},
	}
	networkConfig.Status.ClusterNetwork = []configv1.ClusterNetworkEntry{
		{CIDR: "10.0.0.1/8"},
		{CIDR: "2620:0:2d0:200::7/32"},
	}
	var expectedReplicas int32 = 8
	ci.Spec.Replicas = &expectedReplicas
	ci.Spec.UnsupportedConfigOverrides = runtime.RawExtension{
		Raw: []byte(`{"loadBalancingAlgorithm":"random","dynamicConfigManager":"false","maxConnections":-1,"reloadInterval":15}`),
	}
	ci.Spec.HttpErrorCodePages = configv1.ConfigMapNameReference{
		Name: "my-custom-error-code-pages",
	}
	ci.Spec.HTTPCompression.MimeTypes = []operatorv1.CompressionMIMEType{"text/html", "application/*"}
	ci.Status.Domain = "example.com"
	ci.Status.EndpointPublishingStrategy.Type = operatorv1.LoadBalancerServiceStrategyType
	proxyNeeded, err = IsProxyProtocolNeeded(ci, infraConfig.Status.PlatformStatus)
	if err != nil {
		t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ci.Namespace, ci.Name, err)
	}
	deployment, err = desiredRouterDeployment(ci, ingressControllerImage, ingressConfig, apiConfig, networkConfig, proxyNeeded, false, nil, false)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}
	checkDeploymentHash(t, deployment)
	checkRollingUpdateParams(t, deployment, intstr.FromString("25%"), intstr.FromString("25%"))
	if deployment.Spec.Template.Spec.HostNetwork != false {
		t.Error("expected host network to be false")
	}
	if deployment.Spec.Template.Spec.DNSPolicy != corev1.DNSClusterFirst {
		t.Errorf("expected dnsPolicy to be %s, got %s", corev1.DNSClusterFirst, deployment.Spec.Template.Spec.DNSPolicy)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty liveness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty startup probe host, got %q", deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Host)
	}

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_HAPROXY_CONFIG_MANAGER", false, "")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOAD_BALANCE_ALGORITHM", true, "random")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_TCP_BALANCE_SCHEME", true, "source")
	if len(deployment.Spec.Template.Spec.Containers[0].VolumeMounts) <= 4 || deployment.Spec.Template.Spec.Containers[0].VolumeMounts[4].Name != "error-pages" {
		t.Errorf("hi")
		t.Errorf("deployment.Spec.Template.Spec.Containers[0].VolumeMounts[4].Name %v", deployment.Spec.Template.Spec.Containers[0].VolumeMounts)
		//log.Info(fmt.Sprintf("deployment.Spec.Template.Spec.Containers[0].VolumeMounts[4].Name %v", deployment.Spec.Template.Spec.Containers[0]))
		t.Error("router Deployment is missing error code pages volume mount")
	}

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_MAX_CONNECTIONS", true, "auto")

	checkDeploymentHasEnvVar(t, deployment, "RELOAD_INTERVAL", true, "15s")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_USE_PROXY_PROTOCOL", true, "true")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_HEADER_NAME", true, "unique-id")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_FORMAT", true, `"%{+X}o %ci:%cp_%fi:%fp_%Ts_%rt:%pid"`)

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_H1_CASE_ADJUST", true, "Host,Cache-Control")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CANONICAL_HOSTNAME", true, "router-"+ci.Name+"."+ci.Status.Domain)

	checkDeploymentHasContainer(t, deployment, operatorv1.ContainerLoggingSidecarContainerName, true)
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_FACILITY", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_MAX_LENGTH", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_LEVEL", true, "info")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_ADDRESS", true, "/var/lib/rsyslog/rsyslog.sock")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_FORMAT", true, `"%ci:%cp [%t] %ft %b/%s %B %bq %HM %HU %HV"`)
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_COOKIE", true, "foo:256")
	checkDeploymentHasEnvVar(t, deployment, RouterDontLogNull, true, "true")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_BUF_SIZE", true, "16384")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_MAX_REWRITE_SIZE", true, "4096")

	checkDeploymentHasEnvVar(t, deployment, RouterHAProxyThreadsEnvName, true, strconv.Itoa(RouterHAProxyThreadsDefaultValue*2))

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SET_FORWARDED_HEADERS", true, "append")

	checkDeploymentHasEnvVar(t, deployment, RouterHTTPIgnoreProbes, true, "true")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CIPHERS", true, "quux")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CIPHERSUITES", true, "TLS_AES_256_GCM_SHA384:TLS_CHACHA20_POLY1305_SHA256")

	checkDeploymentHasEnvVar(t, deployment, "SSL_MIN_VERSION", true, "TLSv1.2")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_IP_V4_V6_MODE", true, "v4v6")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_DEFAULT_CLIENT_TIMEOUT", true, "45s")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CLIENT_FIN_TIMEOUT", true, "3s")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_DEFAULT_SERVER_TIMEOUT", true, "1m")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_DEFAULT_SERVER_FIN_TIMEOUT", true, "4s")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_DEFAULT_TUNNEL_TIMEOUT", true, "30m")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_INSPECT_DELAY", true, "5s")

	checkDeploymentHasEnvVar(t, deployment, RouterEnableCompression, true, "true")
	checkDeploymentHasEnvVar(t, deployment, RouterCompressionMIMETypes, true, "text/html application/*")

	checkDeploymentHasEnvSorted(t, deployment)

	// Any value for loadBalancingAlgorithm other than "random" should be
	// ignored.
	ci.Spec.UnsupportedConfigOverrides = runtime.RawExtension{
		Raw: []byte(`{"loadBalancingAlgorithm":"source","dynamicConfigManager":"true","maxConnections":40000}`),
	}
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
	deployment, err = desiredRouterDeployment(ci, ingressControllerImage, ingressConfig, apiConfig, networkConfig, proxyNeeded, false, nil, false)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}
	checkDeploymentHash(t, deployment)
	checkRollingUpdateParams(t, deployment, intstr.FromString("25%"), intstr.FromString("25%"))
	if deployment.Spec.Template.Spec.HostNetwork != false {
		t.Error("expected host network to be false")
	}
	if len(deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty liveness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty startup probe host, got %q", deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Host)
	}

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_HAPROXY_CONFIG_MANAGER", true, "true")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOAD_BALANCE_ALGORITHM", true, "leastconn")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_TCP_BALANCE_SCHEME", true, "source")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_MAX_CONNECTIONS", true, "40000")

	checkDeploymentHasEnvVar(t, deployment, "RELOAD_INTERVAL", true, "5s")

	checkDeploymentDoesNotHaveEnvVar(t, deployment, "ROUTER_USE_PROXY_PROTOCOL")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_HEADER_NAME", true, "unique-id")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_FORMAT", true, `"%{+X}o %ci:%cp_%fi:%fp_%Ts_%rt:%pid"`)

	checkDeploymentHasEnvSorted(t, deployment)

	secretName := fmt.Sprintf("secret-%v", time.Now().UnixNano())
	ci.Spec.DefaultCertificate = &corev1.LocalObjectReference{
		Name: secretName,
	}
	ci.Spec.Logging = &operatorv1.IngressControllerLogging{
		Access: &operatorv1.AccessLogging{
			Destination: operatorv1.LoggingDestination{
				Type: operatorv1.SyslogLoggingDestinationType,
				Syslog: &operatorv1.SyslogLoggingDestinationParameters{
					Address:   "1.2.3.4",
					Port:      uint32(12345),
					Facility:  "local2",
					MaxLength: uint32(4096),
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
			HTTPCaptureCookies: []operatorv1.IngressControllerCaptureHTTPCookie{{
				IngressControllerCaptureHTTPCookieUnion: operatorv1.IngressControllerCaptureHTTPCookieUnion{
					MatchType: "Exact",
					Name:      "foo",
				},
				MaxLength: 15,
			}},
		},
	}
	ci.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		ForwardedHeaderPolicy: operatorv1.NeverHTTPHeaderPolicy,
		UniqueId: operatorv1.IngressControllerHTTPUniqueIdHeaderPolicy{
			Name:   "unique-id",
			Format: "foo",
		},
	}
	ci.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileCustomType,
		Custom: &configv1.CustomTLSProfile{
			TLSProfileSpec: configv1.TLSProfileSpec{
				Ciphers: []string{
					"quux",
					"TLS_AES_256_GCM_SHA384",
					"TLS_CHACHA20_POLY1305_SHA256",
				},
				MinTLSVersion: configv1.VersionTLS13,
			},
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
	deployment, err = desiredRouterDeployment(ci, ingressControllerImage, ingressConfig, apiConfig, networkConfig, proxyNeeded, false, nil, false)
	if err != nil {
		t.Errorf("invalid router Deployment: %v", err)
	}
	checkDeploymentHash(t, deployment)
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

	if deployment.Spec.Template.Spec.DNSPolicy != corev1.DNSClusterFirstWithHostNet {
		t.Errorf("expected dnsPolicy to be %s, got %s", corev1.DNSClusterFirstWithHostNet, deployment.Spec.Template.Spec.DNSPolicy)
	}

	if deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Host != "localhost" {
		t.Errorf("expected liveness probe host to be \"localhost\", got %q", deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Host)
	}
	if deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Host != "localhost" {
		t.Errorf("expected readiness probe host to be \"localhost\", got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Host)
	}
	if deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Host != "localhost" {
		t.Errorf("expected startup probe host to be \"localhost\", got %q", deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Host)
	}

	expectedVolumeSecretPairs = map[string]string{
		"default-certificate": secretName,
		"metrics-certs":       fmt.Sprintf("router-metrics-certs-%s", ci.Name),
		"stats-auth":          fmt.Sprintf("router-stats-%s", ci.Name),
	}

	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if secretName, ok := expectedVolumeSecretPairs[volume.Name]; ok {
			if volume.Secret.SecretName != secretName {
				t.Errorf("router Deployment expected volume %s to have secret %s, got %s", volume.Name, secretName, volume.Secret.SecretName)
			}
		} else if volume.Name != "service-ca-bundle" && volume.Name != "error-pages" {
			t.Errorf("router deployment has unexpected volume %s", volume.Name)
		}
	}

	checkDeploymentHasContainer(t, deployment, operatorv1.ContainerLoggingSidecarContainerName, false)
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_FACILITY", true, "local2")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_MAX_LENGTH", true, "4096")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_LOG_LEVEL", true, "info")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_ADDRESS", true, "1.2.3.4:12345")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SYSLOG_FORMAT", false, "")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_REQUEST_HEADERS", true, "Host:15,Referer:15")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_RESPONSE_HEADERS", true, "Content-length:9,Location:15")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CAPTURE_HTTP_COOKIE", true, "foo=:15")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_SET_FORWARDED_HEADERS", true, "never")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CIPHERS", true, "quux")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_CIPHERSUITES", true, "TLS_AES_256_GCM_SHA384:TLS_CHACHA20_POLY1305_SHA256")

	checkDeploymentHasEnvVar(t, deployment, "SSL_MIN_VERSION", true, "TLSv1.3")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_IP_V4_V6_MODE", true, "v6")
	checkDeploymentHasEnvVar(t, deployment, RouterDisableHTTP2EnvName, true, "true")

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_HEADER_NAME", true, "unique-id")
	checkDeploymentHasEnvVar(t, deployment, "ROUTER_UNIQUE_ID_FORMAT", true, `"foo"`)

	checkDeploymentHasEnvVar(t, deployment, "ROUTER_H1_CASE_ADJUST", false, "")
	checkDeploymentHasEnvVar(t, deployment, RouterHardStopAfterEnvName, false, "")

	checkDeploymentHasEnvSorted(t, deployment)
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
		{
			description: "min TLS version 1.2",
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
							Value: "TLSv1.2",
						},
					},
				},
				{
					Name: "logs",
				},
			},
			expected: &configv1.TLSProfileSpec{
				Ciphers:       []string{"foo", "bar", "baz"},
				MinTLSVersion: configv1.VersionTLS12,
			},
		},
		{
			description: "min TLS version 1.3",
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
							Value: "TLSv1.3",
						},
					},
				},
				{
					Name: "logs",
				},
			},
			expected: &configv1.TLSProfileSpec{
				Ciphers:       []string{"foo", "bar", "baz"},
				MinTLSVersion: configv1.VersionTLS13,
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
		two := int32(2)
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
				Replicas: &two,
			},
		}
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		deploymentHashChanged := deploymentHash(original, false) != deploymentHash(mutated, false)
		templateHashChanged := deploymentTemplateHash(original, false) != deploymentTemplateHash(mutated, false)
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
		description              string
		mutate                   func(*appsv1.Deployment)
		defaultIngressController bool
		expect                   bool
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
			description: "if .spec.template.spec.topologySpreadConstraints.maxSkew changes",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.TopologySpreadConstraints[0].MaxSkew = int32(2)
			},
			expect: true,
		},
		{
			description: "if .spec.template.spec.topologySpreadConstraints is zeroed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{}
			},
			expect: true,
		},
		{
			description: "if the hash in .spec.template.spec.topologySpreadConstraints changes",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.TopologySpreadConstraints[0].LabelSelector.MatchExpressions[0].Values = []string{"xyz"}
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
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Scheme = "HTTP"
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = int32(1)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.PeriodSeconds = int32(10)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.SuccessThreshold = int32(1)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.FailureThreshold = int32(3)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Scheme = "HTTP"
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.TimeoutSeconds = int32(1)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.PeriodSeconds = int32(10)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.SuccessThreshold = int32(1)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.FailureThreshold = int32(3)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Scheme = "HTTP"
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.TimeoutSeconds = int32(1)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.PeriodSeconds = int32(10)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.SuccessThreshold = int32(1)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.FailureThreshold = int32(3)
			},
			expect: false,
		},
		{
			description: "if probe timeoutSecond values are set to non-default values",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.TimeoutSeconds = int32(2)
			},
			expect: false,
		},
		{
			description: "if liveness probe values are set to non-default values",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Scheme = "HTTPS"
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.PeriodSeconds = int32(20)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.SuccessThreshold = int32(2)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.FailureThreshold = int32(30)
			},
			expect: true,
		},
		{
			description: "if readiness probe values are set to non-default values",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Scheme = "HTTPS"
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.PeriodSeconds = int32(20)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.SuccessThreshold = int32(2)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.FailureThreshold = int32(30)
			},
			expect: true,
		},
		{
			description: "if startup probe values are set to non-default values",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Scheme = "HTTPS"
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.PeriodSeconds = int32(20)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.SuccessThreshold = int32(2)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.FailureThreshold = int32(30)
			},
			expect: true,
		},
		{
			description: "if probe values are set to default values for the default IngressController",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Scheme = "HTTP"
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = int32(1)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.PeriodSeconds = int32(10)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.SuccessThreshold = int32(1)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.FailureThreshold = int32(3)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Scheme = "HTTP"
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.TimeoutSeconds = int32(1)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.PeriodSeconds = int32(10)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.SuccessThreshold = int32(1)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.FailureThreshold = int32(3)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Scheme = "HTTP"
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.TimeoutSeconds = int32(1)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.PeriodSeconds = int32(10)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.SuccessThreshold = int32(1)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.FailureThreshold = int32(3)
			},
			defaultIngressController: true,
			expect:                   false,
		},
		{
			description: "if probe timeoutSecond values are set to non-default values on the default IngressController",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.TimeoutSeconds = int32(2)
			},
			defaultIngressController: true,
			expect:                   true,
		},
		{
			description: "if liveness probe values are set to non-default values on the default IngressController",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Scheme = "HTTPS"
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.PeriodSeconds = int32(20)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.SuccessThreshold = int32(2)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.FailureThreshold = int32(30)
			},
			defaultIngressController: true,
			expect:                   true,
		},
		{
			description: "if readiness probe values are set to non-default values on the default IngressController",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Scheme = "HTTPS"
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.PeriodSeconds = int32(20)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.SuccessThreshold = int32(2)
				deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.FailureThreshold = int32(30)
			},
			defaultIngressController: true,
			expect:                   true,
		},
		{
			description: "if startup probe values are set to non-default values on the default IngressController",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Scheme = "HTTPS"
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.TimeoutSeconds = int32(2)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.PeriodSeconds = int32(20)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.SuccessThreshold = int32(2)
				deployment.Spec.Template.Spec.Containers[0].StartupProbe.FailureThreshold = int32(30)
			},
			defaultIngressController: true,
			expect:                   true,
		},
		{
			description: "if dnsPolicy is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.DNSPolicy = corev1.DNSClusterFirstWithHostNet
			},
			expect: true,
		},
		{
			description: "if management workload partitioning annotation is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Annotations[WorkloadPartitioningManagement] = "someNewValue"
			},
			expect: true,
		},
		{
			description: "if .spec.minReadySeconds changes to non-zero",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.MinReadySeconds = 10
			},
			expect: true,
		},
		{
			description: "if .spec.minReadySeconds changes to none",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.MinReadySeconds = 0
			},
			expect: true,
		},
		{
			description: "if .spec.HTTPCompressionPolicy changes",
			mutate: func(deployment *appsv1.Deployment) {
				enableEV := corev1.EnvVar{Name: RouterEnableCompression, Value: "true"}
				mimes := "text/html application/*"
				mimesEV := corev1.EnvVar{Name: RouterCompressionMIMETypes, Value: mimes}
				deployment.Spec.Template.Spec.Containers[0].Env = append(deployment.Spec.Template.Spec.Containers[0].Env, enableEV)
				deployment.Spec.Template.Spec.Containers[0].Env = append(deployment.Spec.Template.Spec.Containers[0].Env, mimesEV)
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
				MinReadySeconds: 30,
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
						Annotations: map[string]string{
							WorkloadPartitioningManagement: "{\"effect\": \"PreferredDuringScheduling\"}",
						},
					},
					Spec: corev1.PodSpec{
						DNSPolicy: corev1.DNSClusterFirst,
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
							{
								Name: "stats-auth",
								VolumeSource: corev1.VolumeSource{
									Secret: &corev1.SecretVolumeSource{
										SecretName: "router-stats-default",
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
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/healthz",
											Port: intstr.IntOrString{
												IntVal: int32(1936),
											},
										},
									},
								},
								ReadinessProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/healthz/ready",
											Port: intstr.IntOrString{
												IntVal: int32(1936),
											},
										},
									},
								},
								StartupProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
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
						TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
							MaxSkew:           int32(1),
							TopologyKey:       "topology.kubernetes.io/zone",
							WhenUnsatisfiable: corev1.ScheduleAnyway,
							LabelSelector: &metav1.LabelSelector{
								MatchExpressions: []metav1.LabelSelectorRequirement{
									{
										Key:      controller.ControllerDeploymentHashLabel,
										Operator: metav1.LabelSelectorOpIn,
										Values:   []string{"default"},
									},
								},
							},
						}},
					},
				},
				Replicas: &nineteen,
			},
		}
		mutated := original.DeepCopy()
		tc.mutate(mutated)
		if changed, updated := deploymentConfigChanged(&original, mutated, tc.defaultIngressController); changed != tc.expect {
			t.Errorf("%s, expect deploymentConfigChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := deploymentConfigChanged(mutated, updated, tc.defaultIngressController); changedAgain {
				t.Errorf("%s, deploymentConfigChanged does not behave as a fixed point function", tc.description)
			}
		}
	}
}

func TestDurationToHAProxyTimespec(t *testing.T) {
	testCases := []struct {
		inputDuration  time.Duration
		expectedOutput string
	}{
		{0, "0s"},
		{100 * time.Millisecond, "100ms"},
		{1500 * time.Millisecond, "1500ms"},
		{10 * time.Second, "10s"},
		{60 * time.Second, "1m"},
		{90 * time.Second, "90s"},
		{2 * time.Minute, "2m"},
		{75 * time.Minute, "75m"},
		{120 * time.Minute, "2h"},
		{48 * time.Hour, "48h"},
		{720 * time.Hour, "2147483647ms"},
	}
	for _, tc := range testCases {
		output := durationToHAProxyTimespec(tc.inputDuration)
		if output != tc.expectedOutput {
			t.Errorf("Expected %q, got %q", tc.expectedOutput, output)
		}
	}
}

func TestGetMIMETypes(t *testing.T) {
	testCases := []struct {
		mimeArrayInput []operatorv1.CompressionMIMEType
		expectedOutput string
	}{
		{nil, ""},
		{[]operatorv1.CompressionMIMEType{"text/html", "application/json"}, "text/html application/json"},
		{[]operatorv1.CompressionMIMEType{"text/html", "image/svg+xml"}, "text/html image/svg+xml"},
		{[]operatorv1.CompressionMIMEType{"text/html", "image/\x7F"}, "text/html image/\x7F"},
		// the input quotes a string with an embedded space
		{[]operatorv1.CompressionMIMEType{"text/html", "text/css; charset=utf-8"}, "text/html \"text/css; charset=utf-8\""},
		// the input escapes a doublequote with \ for an usual string like text/xx"xx.  We expect " to be \" in the output
		{[]operatorv1.CompressionMIMEType{"text/html", `text/xx"xx`}, `text/html "text/xx\"xx"`},
		// the input escapes a literal \ with a \.  We expect this to be \ before each literal \, and \ before a ' or " in the output
		{[]operatorv1.CompressionMIMEType{"text/html", ` '<>@,\;:"[]?.`}, `text/html " \'<>@,\\;:\"[]?."`},
		// the input doesn't escape special characters other than a space, \, ', or "
		{[]operatorv1.CompressionMIMEType{"text/html", "<>@,;:[]?."}, "text/html <>@,;:[]?."},
	}
	for _, tc := range testCases {
		output := GetMIMETypes(tc.mimeArrayInput)
		if strings.Join(output, " ") != tc.expectedOutput {
			t.Errorf("Expected %s, got %s", tc.expectedOutput, output)
		}
	}
}
