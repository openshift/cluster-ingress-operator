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
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	ingressControllerImage = "quay.io/openshift/router:latest"
	routerContainerName    = "router"
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

func checkRouterContainerSecurityContext(t *testing.T, deployment *appsv1.Deployment) {
	t.Helper()
	checkDeploymentHasContainer(t, deployment, routerContainerName, true)

	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == routerContainerName {
			if v := container.SecurityContext.AllowPrivilegeEscalation; v == nil || *v != true {
				t.Errorf("%s container does not have securityContext.allowPrivilegeEscalation: true", routerContainerName)
			}
		}
	}
}

func checkDeploymentHash(t *testing.T, deployment *appsv1.Deployment) {
	t.Helper()
	expectedHash := deploymentTemplateHash(deployment)
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

// envData contains the name of an environment variable, whether it is expected to be present, and its expectedValue
type envData struct {
	name          string
	expectPresent bool
	expectedValue string
}

func checkDeploymentEnvironment(t *testing.T, deployment *appsv1.Deployment, expected []envData) error {
	t.Helper()

	var (
		errors      []error
		actualValue string
		foundValue  bool
	)

	for _, test := range expected {
		foundValue = false
		for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
			if envVar.Name == test.name {
				foundValue = true
				actualValue = envVar.Value
				break
			}
		}
		switch {
		case !test.expectPresent && foundValue:
			errors = append(errors, fmt.Errorf("router Deployment has unexpected %s setting: %q", test.name, actualValue))
		case test.expectPresent && !foundValue:
			errors = append(errors, fmt.Errorf("router Deployment is missing %v", test.name))
		case test.expectPresent && test.expectedValue != actualValue:
			errors = append(errors, fmt.Errorf("router Deployment has unexpected %s setting: expected %q, got %q", test.name, test.expectedValue, actualValue))
		}
	}
	return utilerrors.NewAggregate(errors)
}

func TestTuningOptions(t *testing.T) {
	ic, ingressConfig, infraConfig, apiConfig, networkConfig, _, clusterProxyConfig := getRouterDeploymentComponents(t)

	// Set up tuning options
	ic.Spec.TuningOptions.ClientTimeout = &metav1.Duration{Duration: 45 * time.Second}
	ic.Spec.TuningOptions.ClientFinTimeout = &metav1.Duration{Duration: 3 * time.Second}
	ic.Spec.TuningOptions.ServerTimeout = &metav1.Duration{Duration: 60 * time.Second}
	ic.Spec.TuningOptions.ServerFinTimeout = &metav1.Duration{Duration: 4 * time.Second}
	ic.Spec.TuningOptions.TunnelTimeout = &metav1.Duration{Duration: 30 * time.Minute}
	ic.Spec.TuningOptions.TLSInspectDelay = &metav1.Duration{Duration: 5 * time.Second}
	ic.Spec.TuningOptions.HealthCheckInterval = &metav1.Duration{Duration: 15 * time.Second}
	ic.Spec.TuningOptions.ReloadInterval = metav1.Duration{Duration: 30 * time.Second}

	deployment, err := desiredRouterDeployment(ic, ingressControllerImage, ingressConfig, infraConfig, apiConfig, networkConfig, false, false, nil, clusterProxyConfig)
	if err != nil {
		t.Fatalf("invalid router Deployment: %v", err)
	}

	// Verify tuning options
	tests := []envData{
		{"ROUTER_DEFAULT_CLIENT_TIMEOUT", true, "45s"},
		{"ROUTER_CLIENT_FIN_TIMEOUT", true, "3s"},
		{"ROUTER_DEFAULT_SERVER_TIMEOUT", true, "1m"},
		{"ROUTER_DEFAULT_SERVER_FIN_TIMEOUT", true, "4s"},
		{"ROUTER_DEFAULT_TUNNEL_TIMEOUT", true, "30m"},
		{"ROUTER_INSPECT_DELAY", true, "5s"},
		{RouterBackendCheckInterval, true, "15s"},
		{RouterReloadIntervalEnvName, true, "30s"},
	}

	if err := checkDeploymentEnvironment(t, deployment, tests); err != nil {
		t.Error(err)
	}

	checkDeploymentHasEnvSorted(t, deployment)
}

// TestClusterProxy tests that the cluster-wide proxy settings from proxies.config.openshift.io/cluster are included in the desired router deployment.
func TestClusterProxy(t *testing.T) {
	ic, ingressConfig, infraConfig, apiConfig, networkConfig, _, clusterProxyConfig := getRouterDeploymentComponents(t)

	deployment, err := desiredRouterDeployment(ic, ingressControllerImage, ingressConfig, infraConfig, apiConfig, networkConfig, false, false, nil, clusterProxyConfig)
	if err != nil {
		t.Fatalf("invalid router Deployment: %v", err)
	}

	// Verify that with an empty cluster proxy config, none of the proxy variables are set.
	expectedEnv := []envData{
		{"HTTP_PROXY", false, ""},
		{"http_proxy", false, ""},
		{"HTTPS_PROXY", false, ""},
		{"https_proxy", false, ""},
		{"NO_PROXY", false, ""},
		{"no_proxy", false, ""},
	}

	if err := checkDeploymentEnvironment(t, deployment, expectedEnv); err != nil {
		t.Errorf("empty configv1.Proxy: %v", err)
	}

	// With values set in the cluster proxy config, verify that those values are set in the desired deployment.
	clusterProxyConfig.Status.HTTPProxy = "foo"
	clusterProxyConfig.Status.HTTPSProxy = "bar"
	clusterProxyConfig.Status.NoProxy = "baz"

	deployment, err = desiredRouterDeployment(ic, ingressControllerImage, ingressConfig, infraConfig, apiConfig, networkConfig, false, false, nil, clusterProxyConfig)
	if err != nil {
		t.Fatalf("invalid router Deployment: %v", err)
	}

	expectedEnv = []envData{
		{"HTTP_PROXY", true, clusterProxyConfig.Status.HTTPProxy},
		{"http_proxy", true, clusterProxyConfig.Status.HTTPProxy},
		{"HTTPS_PROXY", true, clusterProxyConfig.Status.HTTPSProxy},
		{"https_proxy", true, clusterProxyConfig.Status.HTTPSProxy},
		{"NO_PROXY", true, clusterProxyConfig.Status.NoProxy},
		{"no_proxy", true, clusterProxyConfig.Status.NoProxy},
	}

	if err := checkDeploymentEnvironment(t, deployment, expectedEnv); err != nil {
		t.Error(err)
	}

	checkDeploymentHasEnvSorted(t, deployment)
}

// return defaulted IngressController, Ingress config, Infrastructure config, APIServer config, Network config,
// and whether proxy is needed
func getRouterDeploymentComponents(t *testing.T) (*operatorv1.IngressController, *configv1.Ingress, *configv1.Infrastructure, *configv1.APIServer, *configv1.Network, bool, *configv1.Proxy) {
	t.Helper()

	var one int32 = 1

	ingressConfig := &configv1.Ingress{}
	ic := &operatorv1.IngressController{
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
	proxyNeeded, err := IsProxyProtocolNeeded(ic, infraConfig.Status.PlatformStatus)
	if err != nil {
		t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ic.Namespace, ic.Name, err)
	}

	clusterProxyConfig := &configv1.Proxy{}

	return ic, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, clusterProxyConfig
}

func TestDesiredRouterDeployment(t *testing.T) {
	ic, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, clusterProxyConfig := getRouterDeploymentComponents(t)

	deployment, err := desiredRouterDeployment(ic, ingressControllerImage, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, false, nil, clusterProxyConfig)
	if err != nil {
		t.Fatalf("invalid router Deployment: %v", err)
	}

	checkRouterContainerSecurityContext(t, deployment)
	checkDeploymentHash(t, deployment)

	if deployment.Spec.Replicas == nil {
		t.Error("router Deployment has nil replicas")
	} else if *deployment.Spec.Replicas != 1 {
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
	tests := []envData{
		{"NAMESPACE_LABELS", true, "foo=bar"},
		{RouterReloadIntervalEnvName, true, "5s"},
		{"ROUTE_LABELS", true, "baz=quux"},
		{RouterBackendCheckInterval, false, ""},
		{RouterCompressionMIMETypes, false, ""},
		{RouterEnableCompression, false, ""},
		{RouterDontLogNull, false, ""},
		{RouterHAProxyThreadsEnvName, true, strconv.Itoa(RouterHAProxyThreadsDefaultValue)},
		{RouterHTTPIgnoreProbes, false, ""},
		{"ROUTER_BUF_SIZE", false, ""},
		{"ROUTER_CANONICAL_HOSTNAME", false, ""},
		{"ROUTER_CAPTURE_HTTP_COOKIE", false, ""},
		{"ROUTER_CAPTURE_HTTP_REQUEST_HEADERS", false, ""},
		{"ROUTER_CAPTURE_HTTP_RESPONSE_HEADERS", false, ""},
		{"ROUTER_CIPHERS", true, "foo:bar:baz"},
		{"ROUTER_CIPHERSUITES", false, ""},
		{"ROUTER_CLIENT_FIN_TIMEOUT", false, ""},
		{"ROUTER_DEFAULT_CLIENT_TIMEOUT", false, ""},
		{"ROUTER_DEFAULT_SERVER_FIN_TIMEOUT", false, ""},
		{"ROUTER_DEFAULT_SERVER_TIMEOUT", false, ""},
		{"ROUTER_DEFAULT_TUNNEL_TIMEOUT", false, ""},
		{"ROUTER_ERRORFILE_503", false, ""},
		{"ROUTER_ERRORFILE_404", false, ""},
		{"ROUTER_HAPROXY_CONFIG_MANAGER", false, ""},
		{"ROUTER_H1_CASE_ADJUST", false, ""},
		{"ROUTER_INSPECT_DELAY", false, ""},
		{"ROUTER_IP_V4_V6_MODE", false, ""},
		{"ROUTER_LOAD_BALANCE_ALGORITHM", true, "random"},
		{"ROUTER_LOG_FACILITY", false, ""},
		{"ROUTER_LOG_LEVEL", false, ""},
		{"ROUTER_LOG_MAX_LENGTH", false, ""},
		{"ROUTER_MAX_CONNECTIONS", false, ""},
		{"ROUTER_MAX_REWRITE_SIZE", false, ""},
		{"ROUTER_SYSLOG_ADDRESS", false, ""},
		{"ROUTER_SYSLOG_FORMAT", false, ""},
		{"ROUTER_TCP_BALANCE_SCHEME", true, "source"},
		{"ROUTER_UNIQUE_ID_FORMAT", false, ""},
		{"ROUTER_UNIQUE_ID_HEADER_NAME", false, ""},
		{"ROUTER_USE_PROXY_PROTOCOL", false, ""},
		{"STATS_USERNAME_FILE", true, "/var/lib/haproxy/conf/metrics-auth/statsUsername"},
		{"STATS_PASSWORD_FILE", true, "/var/lib/haproxy/conf/metrics-auth/statsPassword"},
		{"SSL_MIN_VERSION", true, "TLSv1.1"},
		{WildcardRouteAdmissionPolicy, true, "false"},
		{"ROUTER_DOMAIN", false, ""},
	}
	if err := checkDeploymentEnvironment(t, deployment, tests); err != nil {
		t.Error(err)
	}

	checkDeploymentHasEnvSorted(t, deployment)
}

func TestDesiredRouterDeploymentSpecTemplate(t *testing.T) {
	ic, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, clusterProxyConfig := getRouterDeploymentComponents(t)

	deployment, err := desiredRouterDeployment(ic, ingressControllerImage, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, false, nil, clusterProxyConfig)
	if err != nil {
		t.Fatalf("invalid router Deployment: %v", err)
	}

	expectedVolumeSecretPairs := map[string]string{
		"default-certificate": fmt.Sprintf("router-certs-%s", ic.Name),
		"metrics-certs":       fmt.Sprintf("router-metrics-certs-%s", ic.Name),
		"stats-auth":          fmt.Sprintf("router-stats-%s", ic.Name),
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

	if expected, got := 1, len(deployment.Spec.Template.Annotations); expected != got {
		t.Errorf("expected len(annotations)=%v, got %v", expected, got)
	}

	if val, ok := deployment.Spec.Template.Annotations[LivenessGracePeriodSecondsAnnotation]; ok {
		t.Errorf("expected annotation %[1]q not to be set, got %[1]s=%[2]s", LivenessGracePeriodSecondsAnnotation, val)
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
	if deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TerminationGracePeriodSeconds == nil {
		t.Error("expected liveness probe's termination grace period to be set")
	}
	if len(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty readiness probe host, got %q", deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Host)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Host) != 0 {
		t.Errorf("expected empty startup probe host, got %q", deployment.Spec.Template.Spec.Containers[0].StartupProbe.ProbeHandler.HTTPGet.Host)
	}

	checkDeploymentHasContainer(t, deployment, operatorv1.ContainerLoggingSidecarContainerName, false)

	checkDeploymentHasEnvSorted(t, deployment)
}

func TestDesiredRouterDeploymentSpecAndNetwork(t *testing.T) {
	ic, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, clusterProxyConfig := getRouterDeploymentComponents(t)

	ic.Spec.Logging = &operatorv1.IngressControllerLogging{
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
	ic.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		UniqueId: operatorv1.IngressControllerHTTPUniqueIdHeaderPolicy{
			Name: "unique-id",
		},
		HeaderNameCaseAdjustments: []operatorv1.IngressControllerHTTPHeaderNameCaseAdjustment{
			"Host",
			"Cache-Control",
		},
	}
	ic.Spec.TuningOptions = operatorv1.IngressControllerTuningOptions{
		HeaderBufferBytes:           16384,
		HeaderBufferMaxRewriteBytes: 4096,
		ThreadCount:                 RouterHAProxyThreadsDefaultValue * 2,
		MaxConnections:              -1,
	}
	ic.Spec.HTTPEmptyRequestsPolicy = "Ignore"

	ic.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
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
	ic.Spec.Replicas = &expectedReplicas
	ic.Spec.UnsupportedConfigOverrides = runtime.RawExtension{
		Raw: []byte(`{"loadBalancingAlgorithm":"leastconn","dynamicConfigManager":"false"}`),
	}
	ic.Spec.HttpErrorCodePages = configv1.ConfigMapNameReference{
		Name: "my-custom-error-code-pages",
	}
	ic.Spec.HTTPCompression.MimeTypes = []operatorv1.CompressionMIMEType{"text/html", "application/*"}
	ic.Status.Domain = "example.com"
	ic.Status.EndpointPublishingStrategy.Type = operatorv1.LoadBalancerServiceStrategyType

	proxyNeeded, err := IsProxyProtocolNeeded(ic, infraConfig.Status.PlatformStatus)
	if err != nil {
		t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ic.Namespace, ic.Name, err)
	}
	deployment, err := desiredRouterDeployment(ic, ingressControllerImage, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, false, nil, clusterProxyConfig)
	if err != nil {
		t.Fatalf("invalid router Deployment: %v", err)
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

	if len(deployment.Spec.Template.Spec.Containers[0].VolumeMounts) <= 4 || deployment.Spec.Template.Spec.Containers[0].VolumeMounts[4].Name != "error-pages" {
		t.Errorf("deployment.Spec.Template.Spec.Containers[0].VolumeMounts[4].Name %v", deployment.Spec.Template.Spec.Containers[0].VolumeMounts)
		//log.Info(fmt.Sprintf("deployment.Spec.Template.Spec.Containers[0].VolumeMounts[4].Name %v", deployment.Spec.Template.Spec.Containers[0]))
		t.Error("router Deployment is missing error code pages volume mount")
	}

	checkDeploymentHasContainer(t, deployment, operatorv1.ContainerLoggingSidecarContainerName, true)
	tests := []envData{
		{"ROUTER_HAPROXY_CONFIG_MANAGER", false, ""},
		{"ROUTER_LOAD_BALANCE_ALGORITHM", true, "leastconn"},
		{"ROUTER_TCP_BALANCE_SCHEME", true, "source"},
		{"ROUTER_MAX_CONNECTIONS", true, "auto"},
		{RouterReloadIntervalEnvName, true, "5s"},
		{"ROUTER_USE_PROXY_PROTOCOL", true, "true"},
		{"ROUTER_UNIQUE_ID_HEADER_NAME", true, "unique-id"},
		{"ROUTER_UNIQUE_ID_FORMAT", true, `"%{+X}o %ci:%cp_%fi:%fp_%Ts_%rt:%pid"`},
		{"ROUTER_H1_CASE_ADJUST", true, "Host,Cache-Control"},
		{"ROUTER_CANONICAL_HOSTNAME", true, "router-" + ic.Name + "." + ic.Status.Domain},
		{"ROUTER_LOG_FACILITY", false, ""},
		{"ROUTER_LOG_MAX_LENGTH", false, ""},
		{"ROUTER_LOG_LEVEL", true, "info"},
		{"ROUTER_SYSLOG_ADDRESS", true, "/var/lib/rsyslog/rsyslog.sock"},
		{"ROUTER_SYSLOG_FORMAT", true, `"%ci:%cp [%t] %ft %b/%s %B %bq %HM %HU %HV"`},
		{"ROUTER_CAPTURE_HTTP_COOKIE", true, "foo:256"},
		{RouterDontLogNull, true, "true"},
		{"ROUTER_BUF_SIZE", true, "16384"},
		{"ROUTER_MAX_REWRITE_SIZE", true, "4096"},
		{RouterHAProxyThreadsEnvName, true, strconv.Itoa(RouterHAProxyThreadsDefaultValue * 2)},
		{"ROUTER_SET_FORWARDED_HEADERS", true, "append"},
		{RouterHTTPIgnoreProbes, true, "true"},
		{"ROUTER_CIPHERS", true, "quux"},
		{"ROUTER_CIPHERSUITES", true, "TLS_AES_256_GCM_SHA384:TLS_CHACHA20_POLY1305_SHA256"},
		{"SSL_MIN_VERSION", true, "TLSv1.2"},
		{"ROUTER_IP_V4_V6_MODE", true, "v4v6"},
		{RouterEnableCompression, true, "true"},
		{RouterCompressionMIMETypes, true, "text/html application/*"},
		{"ROUTER_DOMAIN", true, ic.Status.Domain},
	}
	if err := checkDeploymentEnvironment(t, deployment, tests); err != nil {
		t.Error(err)
	}

	checkDeploymentHasEnvSorted(t, deployment)

	// Any value for loadBalancingAlgorithm other than "leastconn" should be
	// ignored.
	ic.Spec.UnsupportedConfigOverrides = runtime.RawExtension{
		Raw: []byte(`{"loadBalancingAlgorithm":"source","dynamicConfigManager":"true"}`),
	}
	ic.Spec.TuningOptions.MaxConnections = 40000
	ic.Status.EndpointPublishingStrategy.LoadBalancer = &operatorv1.LoadBalancerStrategy{
		Scope: operatorv1.ExternalLoadBalancer,
		ProviderParameters: &operatorv1.ProviderLoadBalancerParameters{
			Type: operatorv1.AWSLoadBalancerProvider,
			AWS: &operatorv1.AWSLoadBalancerParameters{
				Type: operatorv1.AWSNetworkLoadBalancer,
			},
		},
	}
	proxyNeeded, err = IsProxyProtocolNeeded(ic, infraConfig.Status.PlatformStatus)
	if err != nil {
		t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ic.Namespace, ic.Name, err)
	}
	deployment, err = desiredRouterDeployment(ic, ingressControllerImage, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, false, nil, clusterProxyConfig)
	if err != nil {
		t.Fatalf("invalid router Deployment: %v", err)
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

	tests = []envData{
		{"ROUTER_HAPROXY_CONFIG_MANAGER", true, "true"},
		{"ROUTER_LOAD_BALANCE_ALGORITHM", true, "random"},
		{"ROUTER_TCP_BALANCE_SCHEME", true, "source"},
		{"ROUTER_MAX_CONNECTIONS", true, "40000"},
		{RouterReloadIntervalEnvName, true, "5s"},
		{"ROUTER_USE_PROXY_PROTOCOL", false, ""},
	}
	if err := checkDeploymentEnvironment(t, deployment, tests); err != nil {
		t.Error(err)
	}

	checkDeploymentHasEnvSorted(t, deployment)
}

func TestDesiredRouterDeploymentVariety(t *testing.T) {
	ic, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, clusterProxyConfig := getRouterDeploymentComponents(t)

	secretName := fmt.Sprintf("secret-%v", time.Now().UnixNano())
	ic.Spec.DefaultCertificate = &corev1.LocalObjectReference{
		Name: secretName,
	}
	ic.Spec.Logging = &operatorv1.IngressControllerLogging{
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
	ic.Spec.HTTPHeaders = &operatorv1.IngressControllerHTTPHeaders{
		ForwardedHeaderPolicy: operatorv1.NeverHTTPHeaderPolicy,
		UniqueId: operatorv1.IngressControllerHTTPUniqueIdHeaderPolicy{
			Name:   "unique-id",
			Format: "foo",
		},
	}
	ic.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
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
	ic.Spec.NodePlacement = &operatorv1.NodePlacement{
		NodeSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"xyzzy": "quux",
			},
		},
		Tolerations: []corev1.Toleration{toleration},
	}
	expectedReplicas := int32(3)
	ic.Spec.Replicas = &expectedReplicas
	ic.Status.EndpointPublishingStrategy.Type = operatorv1.HostNetworkStrategyType
	ic.Status.EndpointPublishingStrategy.HostNetwork = &operatorv1.HostNetworkStrategy{
		Protocol:  operatorv1.TCPProtocol,
		HTTPPort:  8080,
		HTTPSPort: 8443,
		StatsPort: 9146,
	}
	networkConfig.Status.ClusterNetwork = []configv1.ClusterNetworkEntry{
		{CIDR: "2620:0:2d0:200::7/32"},
	}
	proxyNeeded, err := IsProxyProtocolNeeded(ic, infraConfig.Status.PlatformStatus)
	if err != nil {
		t.Errorf("failed to determine infrastructure platform status for ingresscontroller %s/%s: %v", ic.Namespace, ic.Name, err)
	}
	deployment, err := desiredRouterDeployment(ic, ingressControllerImage, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, false, nil, clusterProxyConfig)
	if err != nil {
		t.Fatalf("invalid router Deployment: %v", err)
	}
	checkDeploymentHash(t, deployment)
	if len(deployment.Spec.Template.Spec.NodeSelector) != 1 ||
		deployment.Spec.Template.Spec.NodeSelector["xyzzy"] != "quux" {
		t.Errorf("router Deployment has unexpected node selector: %#v",
			deployment.Spec.Template.Spec.NodeSelector)
	}
	if len(deployment.Spec.Template.Spec.Tolerations) != 1 ||
		!reflect.DeepEqual(ic.Spec.NodePlacement.Tolerations, deployment.Spec.Template.Spec.Tolerations) {
		t.Errorf("router Deployment has unexpected tolerations, expected: %#v,  got: %#v",
			ic.Spec.NodePlacement.Tolerations, deployment.Spec.Template.Spec.Tolerations)
	}
	if deployment.Spec.Replicas == nil {
		t.Error("router Deployment has nil replicas")
	} else if *deployment.Spec.Replicas != expectedReplicas {
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

	expectedVolumeSecretPairs := map[string]string{
		"default-certificate": secretName,
		"metrics-certs":       fmt.Sprintf("router-metrics-certs-%s", ic.Name),
		"stats-auth":          fmt.Sprintf("router-stats-%s", ic.Name),
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
	tests := []envData{
		{"ROUTER_LOG_FACILITY", true, "local2"},
		{"ROUTER_LOG_MAX_LENGTH", true, "4096"},
		{"ROUTER_LOG_LEVEL", true, "info"},
		{"ROUTER_SYSLOG_ADDRESS", true, "1.2.3.4:12345"},
		{"ROUTER_SYSLOG_FORMAT", false, ""},
		{"ROUTER_CAPTURE_HTTP_REQUEST_HEADERS", true, "Host:15,Referer:15"},
		{"ROUTER_CAPTURE_HTTP_RESPONSE_HEADERS", true, "Content-length:9,Location:15"},
		{"ROUTER_CAPTURE_HTTP_COOKIE", true, "foo=:15"},

		{"ROUTER_SET_FORWARDED_HEADERS", true, "never"},

		{"ROUTER_CIPHERS", true, "quux"},
		{"ROUTER_CIPHERSUITES", true, "TLS_AES_256_GCM_SHA384:TLS_CHACHA20_POLY1305_SHA256"},

		{"SSL_MIN_VERSION", true, "TLSv1.3"},

		{"ROUTER_IP_V4_V6_MODE", true, "v6"},
		{RouterDisableHTTP2EnvName, true, "true"},

		{"ROUTER_UNIQUE_ID_HEADER_NAME", true, "unique-id"},
		{"ROUTER_UNIQUE_ID_FORMAT", true, `"foo"`},

		{"ROUTER_H1_CASE_ADJUST", false, ""},
		{RouterHardStopAfterEnvName, false, ""},
		{"STATS_PORT", true, "9146"},
		{"ROUTER_SERVICE_HTTP_PORT", true, "8080"},
		{"ROUTER_SERVICE_HTTPS_PORT", true, "8443"},
	}
	if err := checkDeploymentEnvironment(t, deployment, tests); err != nil {
		t.Error(err)
	}

	checkDeploymentHasEnvSorted(t, deployment)

	checkContainerPort(t, deployment, "http", 8080)
	checkContainerPort(t, deployment, "https", 8443)
	checkContainerPort(t, deployment, "metrics", 9146)
}

// TestDesiredRouterDeploymentHostNetworkNil verifies that
// desiredRouterDeployment behaves correctly when
// status.endpointPublishingStrategy.type is "HostNetwork" but
// status.endpointPublishingStrategy.hostNetwork is nil, which can happen on a
// cluster that was upgraded from a version of OpenShift that did not define any
// subfields for spec.endpointPublishingStrategy.hostNetwork.
// See <https://bugzilla.redhat.com/show_bug.cgi?id=2095229>.
func TestDesiredRouterDeploymentHostNetworkNil(t *testing.T) {
	ic, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, clusterProxyConfig := getRouterDeploymentComponents(t)
	ic.Status.EndpointPublishingStrategy.Type = operatorv1.HostNetworkStrategyType
	proxyNeeded, err := IsProxyProtocolNeeded(ic, infraConfig.Status.PlatformStatus)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := desiredRouterDeployment(ic, ingressControllerImage, ingressConfig, infraConfig, apiConfig, networkConfig, proxyNeeded, false, nil, clusterProxyConfig)
	if err != nil {
		t.Fatal(err)
	}
	env := []envData{
		{"STATS_PORT", true, "1936"},
		{"ROUTER_SERVICE_HTTP_PORT", true, "80"},
		{"ROUTER_SERVICE_HTTPS_PORT", true, "443"},
	}
	if err := checkDeploymentEnvironment(t, deployment, env); err != nil {
		t.Error(err)
	}
	checkDeploymentHasEnvSorted(t, deployment)
	checkContainerPort(t, deployment, "http", 80)
	checkContainerPort(t, deployment, "https", 443)
	checkContainerPort(t, deployment, "metrics", 1936)
}

func TestDesiredRouterDeploymentSingleReplica(t *testing.T) {
	ic, ingressConfig, _, apiConfig, networkConfig, _, clusterProxyConfig := getRouterDeploymentComponents(t)

	infraConfig := &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.IBMCloudPlatformType,
			},
			ControlPlaneTopology:   configv1.ExternalTopologyMode,
			InfrastructureTopology: configv1.SingleReplicaTopologyMode,
		},
	}

	deployment, err := desiredRouterDeployment(ic, ingressControllerImage, ingressConfig, infraConfig, apiConfig, networkConfig, false, false, nil, clusterProxyConfig)
	if err != nil {
		t.Fatalf("invalid router Deployment: %v", err)
	}

	if *deployment.Spec.Replicas != 1 {
		t.Errorf("expected replicas to be 1, got %d", *deployment.Spec.Replicas)
	}

	if deployment.Spec.Template.Spec.Affinity.PodAffinity != nil {
		t.Errorf("expected no pod affinity, got %+v", *deployment.Spec.Template.Spec.Affinity)
	}

	if deployment.Spec.Strategy.Type != "" || deployment.Spec.Strategy.RollingUpdate != nil {
		t.Errorf("expected default deployment strategy, got %s", deployment.Spec.Strategy.Type)
	}
}

func checkContainerPort(t *testing.T, d *appsv1.Deployment, portName string, port int32) {
	t.Helper()
	for _, p := range d.Spec.Template.Spec.Containers[0].Ports {
		if p.Name == portName && p.ContainerPort == port {
			return
		}
	}
	t.Errorf("deployment %s container does not have port with name %s and number %d", d.Name, portName, port)
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
		{
			description: "if ports are changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort = int32(8080)
				deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort = int32(8443)
				deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort = int32(8936)
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
						Containers: []corev1.Container{
							{
								Ports: []corev1.ContainerPort{
									{ContainerPort: 80},
									{ContainerPort: 443},
									{ContainerPort: 1936},
								},
							},
						},
					},
				},
				Replicas: &two,
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
			description: "if the deployment template node affinity is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values = []string{"xyz"}
			},
			expect: true,
		},
		{
			description: "if the deployment template -affinity node selector expressions change ordering",
			mutate: func(deployment *appsv1.Deployment) {
				exprs := deployment.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchExpressions
				exprs[0], exprs[1] = exprs[1], exprs[0]
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
			description: "if the liveness probe's terminationGracePeriodSeconds is changed",
			mutate: func(deployment *appsv1.Deployment) {
				v := int64(123)
				deployment.Spec.Template.Spec.Containers[0].LivenessProbe.TerminationGracePeriodSeconds = &v
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
		{
			description: "if the router container security context changes",
			mutate: func(deployment *appsv1.Deployment) {
				v := true
				sc := &corev1.SecurityContext{
					AllowPrivilegeEscalation: &v,
				}
				deployment.Spec.Template.Spec.Containers[0].SecurityContext = sc
			},
			expect: true,
		},
		{
			description: "if container HTTP port is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort = 8080
			},
			expect: true,
		},
		{
			description: "if container HTTPS port is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].Ports[1].ContainerPort = 8443
			},
			expect: true,
		},
		{
			description: "if container Stats port is changed",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].Ports[2].ContainerPort = 8936
			},
			expect: true,
		},
		{
			description: "if the protocol for container ports is set to the default value",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].Ports[0].Protocol = corev1.ProtocolTCP
				deployment.Spec.Template.Spec.Containers[0].Ports[1].Protocol = corev1.ProtocolTCP
				deployment.Spec.Template.Spec.Containers[0].Ports[2].Protocol = corev1.ProtocolTCP
			},
			expect: false,
		},
		{
			description: "if the protocol for container ports is set to a non-default value",
			mutate: func(deployment *appsv1.Deployment) {
				deployment.Spec.Template.Spec.Containers[0].Ports[0].Protocol = corev1.ProtocolUDP
				deployment.Spec.Template.Spec.Containers[0].Ports[1].Protocol = corev1.ProtocolUDP
				deployment.Spec.Template.Spec.Containers[0].Ports[2].Protocol = corev1.ProtocolUDP
			},
			expect: true,
		},
		{
			// This test case can be removed after OpenShift 4.13.
			// See <https://issues.redhat.com/browse/OCPBUGS-4703>.
			description: "if the unsupported.do-not-use.openshift.io/override-liveness-grace-period-seconds annotation is removed",
			mutate: func(deployment *appsv1.Deployment) {
				delete(deployment.Spec.Template.Annotations, LivenessGracePeriodSecondsAnnotation)
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
							LivenessGracePeriodSecondsAnnotation: "10",
							WorkloadPartitioningManagement:       "{\"effect\": \"PreferredDuringScheduling\"}",
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
								Ports: []corev1.ContainerPort{
									{
										Name:          "http",
										ContainerPort: int32(80),
										Protocol:      corev1.ProtocolTCP,
									},
									{
										Name:          "https",
										ContainerPort: 443,
										Protocol:      corev1.ProtocolTCP,
									},
									{
										Name:          "metrics",
										ContainerPort: 1936,
										Protocol:      corev1.ProtocolTCP,
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
							NodeAffinity: &corev1.NodeAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
									NodeSelectorTerms: []corev1.NodeSelectorTerm{{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      controller.RemoteWorkerLabel,
												Operator: corev1.NodeSelectorOpNotIn,
												Values:   []string{""},
											},
											// this match expression was added only for ordering change test case
											{
												Key:      controller.ControllerDeploymentHashLabel,
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"1"},
											},
										},
									}},
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
		if changed, updated := deploymentConfigChanged(&original, mutated); changed != tc.expect {
			t.Errorf("%s, expect deploymentConfigChanged to be %t, got %t", tc.description, tc.expect, changed)
		} else if changed {
			if changedAgain, _ := deploymentConfigChanged(mutated, updated); changedAgain {
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
		// A value below the minimum 0s returns 0s
		{-1, "0s"},

		// Values above the maximum 2147483647ms return 2147483647ms
		{2147489999 * time.Millisecond, "2147483647ms"},
		{2147483647009 * time.Microsecond, "2147483647ms"},
		{720 * time.Hour, "2147483647ms"},

		// A value less than a millisecond should return microseconds
		{10 * time.Microsecond, "10us"},

		// A value that would return fractional milliseconds should return microseconds
		{500 * time.Microsecond, "500us"},
		{1500 * time.Microsecond, "1500us"},

		// A value that would return fractional seconds should return milliseconds
		{time.Duration(1.5 * float64(time.Second)), "1500ms"},

		// Values that can be converted to the next higher time unit should be converted
		{2000 * time.Microsecond, "2ms"},
		{1000 * time.Millisecond, "1s"},
		{60 * time.Second, "1m"},
		{120 * time.Minute, "2h"},

		{0, "0s"},
		{100 * time.Millisecond, "100ms"},
		{1500 * time.Millisecond, "1500ms"},
		{10 * time.Second, "10s"},
		{90 * time.Second, "90s"},
		{2 * time.Minute, "2m"},
		{75 * time.Minute, "75m"},
		{48 * time.Hour, "48h"},
	}
	for _, tc := range testCases {
		output := durationToHAProxyTimespec(tc.inputDuration)
		if output != tc.expectedOutput {
			t.Errorf("Expected %q, got %q", tc.expectedOutput, output)
		}
	}
}

func TestCapReloadIntervalValue(t *testing.T) {
	testCases := []struct {
		inputDuration  time.Duration
		expectedOutput time.Duration
	}{
		// Values below the minimum 1s returns 1s.
		{20 * time.Nanosecond, 1 * time.Second},
		{5 * time.Microsecond, 1 * time.Second},
		{1 * time.Millisecond, 1 * time.Second},
		{-1, 1 * time.Second},

		// Values above the maximum 120s returns 120s.
		{6 * time.Minute, 120 * time.Second},
		{1 * time.Hour, 120 * time.Second},
		{365 * time.Second, 120 * time.Second},

		// Values in the allowed range returns itself (i.e. between 1s and 120s).
		{1 * time.Minute, 1 * time.Minute},
		{2 * time.Minute, 2 * time.Minute},
		{1 * time.Second, 1 * time.Second},
		{20 * time.Second, 20 * time.Second},

		// Value of 0 returns default of 5s.
		{0, 5 * time.Second},
	}
	for _, tc := range testCases {
		output := capReloadIntervalValue(tc.inputDuration)
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

func TestDesiredRouterDeploymentDefaultPlacement(t *testing.T) {
	var (
		workerNodeSelector = map[string]string{
			"kubernetes.io/os":               "linux",
			"node-role.kubernetes.io/worker": "",
		}
		controlPlaneNodeSelector = map[string]string{
			"kubernetes.io/os":               "linux",
			"node-role.kubernetes.io/master": "",
		}
		highlyAvailableTopologyReplicas = int32(2)
		singleReplicaTopologyReplicas   = int32(1)
	)

	testCases := []struct {
		name                 string
		ingressConfig        *configv1.Ingress
		infraConfig          *configv1.Infrastructure
		expectedNodeSelector map[string]string
		expectedReplicas     int32
	}{
		{
			name:          "empty ingress/infra config",
			ingressConfig: &configv1.Ingress{},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: workerNodeSelector,
			expectedReplicas:     highlyAvailableTopologyReplicas,
		},
		{
			name:          "empty ingress, single/single topologies",
			ingressConfig: &configv1.Ingress{},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: workerNodeSelector,
			expectedReplicas:     singleReplicaTopologyReplicas,
		},
		{
			name:          "empty ingress, high/single topologies",
			ingressConfig: &configv1.Ingress{},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
					ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: workerNodeSelector,
			expectedReplicas:     highlyAvailableTopologyReplicas,
		},
		{
			name:          "empty ingress, single/high topologies",
			ingressConfig: &configv1.Ingress{},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: workerNodeSelector,
			expectedReplicas:     singleReplicaTopologyReplicas,
		},
		{
			name:          "empty ingress, high/high topologies",
			ingressConfig: &configv1.Ingress{},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
					ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: workerNodeSelector,
			expectedReplicas:     highlyAvailableTopologyReplicas,
		},
		{
			name: "worker default ingress, single/single topologies",
			ingressConfig: &configv1.Ingress{
				Status: configv1.IngressStatus{
					DefaultPlacement: configv1.DefaultPlacementWorkers,
				},
			},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: workerNodeSelector,
			expectedReplicas:     singleReplicaTopologyReplicas,
		},
		{
			name: "worker default ingress, high/single topologies",
			ingressConfig: &configv1.Ingress{
				Status: configv1.IngressStatus{
					DefaultPlacement: configv1.DefaultPlacementWorkers,
				},
			},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
					ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: workerNodeSelector,
			expectedReplicas:     highlyAvailableTopologyReplicas,
		},
		{
			name: "worker default ingress, single/high topologies",
			ingressConfig: &configv1.Ingress{
				Status: configv1.IngressStatus{
					DefaultPlacement: configv1.DefaultPlacementWorkers,
				},
			},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: workerNodeSelector,
			expectedReplicas:     singleReplicaTopologyReplicas,
		},
		{
			name: "worker default ingress, high/high topologies",
			ingressConfig: &configv1.Ingress{
				Status: configv1.IngressStatus{
					DefaultPlacement: configv1.DefaultPlacementWorkers,
				},
			},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
					ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: workerNodeSelector,
			expectedReplicas:     highlyAvailableTopologyReplicas,
		},
		{
			name: "control-plane default ingress, single/single topologies",
			ingressConfig: &configv1.Ingress{
				Status: configv1.IngressStatus{
					DefaultPlacement: configv1.DefaultPlacementControlPlane,
				},
			},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: controlPlaneNodeSelector,
			expectedReplicas:     singleReplicaTopologyReplicas,
		},
		{
			name: "control-plane default ingress, high/single topologies",
			ingressConfig: &configv1.Ingress{
				Status: configv1.IngressStatus{
					DefaultPlacement: configv1.DefaultPlacementControlPlane,
				},
			},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
					ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: controlPlaneNodeSelector,
			expectedReplicas:     singleReplicaTopologyReplicas,
		},
		{
			name: "control-plane default ingress, single/high topologies",
			ingressConfig: &configv1.Ingress{
				Status: configv1.IngressStatus{
					DefaultPlacement: configv1.DefaultPlacementControlPlane,
				},
			},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: controlPlaneNodeSelector,
			expectedReplicas:     highlyAvailableTopologyReplicas,
		},
		{
			name: "control-plane default ingress, high/high topologies",
			ingressConfig: &configv1.Ingress{
				Status: configv1.IngressStatus{
					DefaultPlacement: configv1.DefaultPlacementControlPlane,
				},
			},
			infraConfig: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
					ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.AWSPlatformType,
					},
				},
			},
			expectedNodeSelector: controlPlaneNodeSelector,
			expectedReplicas:     highlyAvailableTopologyReplicas,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				ic = &operatorv1.IngressController{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
					},
					Spec: operatorv1.IngressControllerSpec{},
					Status: operatorv1.IngressControllerStatus{
						EndpointPublishingStrategy: &operatorv1.EndpointPublishingStrategy{
							Type: operatorv1.PrivateStrategyType,
						},
					},
				}
				ingressControllerImage = "quay.io/openshift/router:latest"
				apiConfig              = &configv1.APIServer{}
				networkConfig          = &configv1.Network{}
			)

			// This value does not matter in the context of this test, just use a dummy value
			dummyProxyNeeded := true

			deployment, err := desiredRouterDeployment(ic, ingressControllerImage, tc.ingressConfig, tc.infraConfig, apiConfig, networkConfig, dummyProxyNeeded, false, nil, &configv1.Proxy{})
			if err != nil {
				t.Error(err)
			}

			expectedReplicas := tc.expectedReplicas
			if !reflect.DeepEqual(deployment.Spec.Replicas, &expectedReplicas) {
				t.Errorf("expected replicas to be %v, got %v", expectedReplicas, *deployment.Spec.Replicas)
			}

			if !reflect.DeepEqual(deployment.Spec.Template.Spec.NodeSelector, tc.expectedNodeSelector) {
				t.Errorf("expected node selector to be %v, got %v", tc.expectedNodeSelector, deployment.Spec.Template.Spec.NodeSelector)
			}
		})
	}

}
