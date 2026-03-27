package gatewayclass

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/install"
	configv1 "github.com/openshift/api/config/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
)

var (
	expectedProxyConfiguration = map[string]string{
		"HTTP_PROXY":  "http://some.proxy.tld:8080",
		"HTTPS_PROXY": "https://another.proxy.tld",
		"NO_PROXY":    ".cluster.local,.ec2.internal,.svc,10.0.0.0/16,10.128.0.0/14",
		"http_proxy":  "http://some.proxy.tld:8080",
		"https_proxy": "https://another.proxy.tld",
		"no_proxy":    ".cluster.local,.ec2.internal,.svc,10.0.0.0/16,10.128.0.0/14",
	}
)

func Test_Reconcile(t *testing.T) {
	req := func(name string) reconcile.Request {
		return reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name},
		}
	}

	infraConfig := func(infraTopologyMode configv1.TopologyMode) *configv1.Infrastructure {
		return &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.InfrastructureStatus{
				InfrastructureTopology: infraTopologyMode,
			},
		}
	}

	subscription := func(catalog, channel, version string) *operatorsv1alpha1.Subscription {
		return &operatorsv1alpha1.Subscription{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "servicemeshoperator3",
				Namespace:   "openshift-operators",
				Annotations: map[string]string{"ingress.operator.openshift.io/owned": ""},
			},
			Spec: &operatorsv1alpha1.SubscriptionSpec{
				CatalogSource:          catalog,
				CatalogSourceNamespace: "openshift-marketplace",
				Package:                "servicemeshoperator3",
				Channel:                channel,
				StartingCSV:            version,
				InstallPlanApproval:    "Manual",
			},
		}
	}

	istioCRD := func() *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "istios.sailoperator.io",
			},
		}
	}

	istioRevisionCRD := func() *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "istiorevisions.sailoperator.io",
			},
		}
	}

	proxyConfig := func(http, https, noproxy string) *configv1.Proxy {
		return &configv1.Proxy{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.ProxyStatus{
				HTTPProxy:  http,
				HTTPSProxy: https,
				NoProxy:    noproxy,
			},
		}
	}

	istio := func(version string, gieEnabled bool, proxyconfig map[string]string, gatewayclassesConfig json.RawMessage) *sailv1.Istio {
		ret := &sailv1.Istio{
			ObjectMeta: metav1.ObjectMeta{
				Name: "openshift-gateway",
			},
			Spec: sailv1.IstioSpec{
				UpdateStrategy: &sailv1.IstioUpdateStrategy{
					Type: "InPlace",
				},
				Namespace: "openshift-ingress",
				Values: &sailv1.Values{
					Global: &sailv1.GlobalConfig{
						DefaultPodDisruptionBudget: &sailv1.DefaultPodDisruptionBudgetConfig{
							Enabled: ptr.To(false),
						},
						IstioNamespace:    ptr.To("openshift-ingress"),
						PriorityClassName: ptr.To("system-cluster-critical"),
						TrustBundleName:   ptr.To("openshift-gw-ca-root-cert"),
					},
					Pilot: &sailv1.PilotConfig{
						Cni: &sailv1.CNIUsageConfig{
							Enabled: ptr.To(false),
						},
						Enabled: ptr.To(true),
						Env: map[string]string{
							"PILOT_ENABLE_GATEWAY_API":                         "true",
							"PILOT_ENABLE_ALPHA_GATEWAY_API":                   "false",
							"PILOT_ENABLE_GATEWAY_API_STATUS":                  "true",
							"PILOT_ENABLE_GATEWAY_API_DEPLOYMENT_CONTROLLER":   "true",
							"PILOT_ENABLE_GATEWAY_API_GATEWAYCLASS_CONTROLLER": "false",
							"PILOT_GATEWAY_API_DEFAULT_GATEWAYCLASS_NAME":      "openshift-default",
							"PILOT_GATEWAY_API_CONTROLLER_NAME":                "openshift.io/gateway-controller/v1",
							"PILOT_MULTI_NETWORK_DISCOVER_GATEWAY_API":         "false",
							"ENABLE_GATEWAY_API_MANUAL_DEPLOYMENT":             "false",
							"PILOT_ENABLE_GATEWAY_API_CA_CERT_ONLY":            "true",
							"PILOT_ENABLE_GATEWAY_API_COPY_LABELS_ANNOTATIONS": "false",
						},
						ExtraContainerArgs: []string{},
						PodAnnotations: map[string]string{
							"target.workload.openshift.io/management": `{"effect": "PreferredDuringScheduling"}`,
						},
					},
					SidecarInjectorWebhook: &sailv1.SidecarInjectorConfig{
						EnableNamespacesByDefault: ptr.To(false),
					},
					MeshConfig: &sailv1.MeshConfig{
						AccessLogFile: ptr.To("/dev/stdout"),
						DefaultConfig: &sailv1.MeshConfigProxyConfig{
							ProxyHeaders: &sailv1.ProxyConfigProxyHeaders{
								Server: &sailv1.ProxyConfigProxyHeadersServer{
									Disabled: ptr.To(true),
								},
								EnvoyDebugHeaders: &sailv1.ProxyConfigProxyHeadersEnvoyDebugHeaders{
									Disabled: ptr.To(true),
								},
								MetadataExchangeHeaders: &sailv1.ProxyConfigProxyHeadersMetadataExchangeHeaders{
									Mode: sailv1.ProxyConfigProxyHeadersMetadataExchangeModeInMesh,
								},
							},
							ProxyMetadata: proxyconfig,
						},
						IngressControllerMode: sailv1.MeshConfigIngressControllerModeOff,
					},
				},
				Version: version,
			},
		}

		if gieEnabled {
			ret.Spec.Values.Pilot.Env["ENABLE_GATEWAY_API_INFERENCE_EXTENSION"] = "true"
		}
		ret.Spec.Values.GatewayClasses = gatewayclassesConfig

		return ret
	}

	gatewayclassesConfig := func(config string, gatewayclasses ...string) json.RawMessage {
		return json.RawMessage(fmt.Appendf(nil, `{%s}`, strings.Join(func() []string {
			var result []string

			for _, name := range gatewayclasses {
				result = append(result, fmt.Sprintf(`"%s":%s`, name, config))
			}

			return result
		}(), ",")))
	}
	hpaConfig := func(minReplicas int) string {
		return fmt.Sprintf(`{"horizontalPodAutoscaler":{"spec":{"maxReplicas":10,"minReplicas":%d}}}`, minReplicas)
	}

	istioRevision := func() *sailv1.IstioRevision {
		return &sailv1.IstioRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name: "openshift-gateway",
			},
		}
	}

	gatewayClass := func(name string, finalizer bool, annotations map[string]string, conditions []metav1.Condition, deletionTimestamp bool) *gatewayapiv1.GatewayClass {
		gc := &gatewayapiv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: gatewayapiv1.GatewayClassSpec{
				ControllerName: gatewayapiv1.GatewayController("openshift.io/gateway-controller/v1"),
			},
		}

		if finalizer {
			gc.ObjectMeta.Finalizers = []string{"openshift.io/ingress-operator-sail-finalizer"}
		}

		if annotations != nil {
			gc.ObjectMeta.Annotations = annotations
		}

		if conditions != nil {
			gc.Status = gatewayapiv1.GatewayClassStatus{
				Conditions: conditions,
			}
		}

		if deletionTimestamp {
			now := metav1.Now()
			gc.ObjectMeta.DeletionTimestamp = &now
		}

		return gc
	}

	installedConditions := func() []metav1.Condition {
		return []metav1.Condition{
			{
				Type:   "ControllerInstalled",
				Status: metav1.ConditionTrue,
				Reason: "Installed",
			},
			{
				Type:   "CRDsReady",
				Status: metav1.ConditionUnknown,
				Reason: "NoneExist",
			},
		}
	}

	expectedSailLibraryOptions := func(version string, gieEnabled bool, proxyConfig map[string]string, gatewayclassesConfig json.RawMessage) *install.Options {
		// Start with sail-operator's Gateway API defaults aka trust upstream defaults
		values := install.GatewayAPIDefaults()

		// Apply our OpenShift-specific overrides
		pilotEnv := map[string]string{
			"PILOT_GATEWAY_API_DEFAULT_GATEWAYCLASS_NAME": "openshift-default",
			"PILOT_GATEWAY_API_CONTROLLER_NAME":           "openshift.io/gateway-controller/v1",
		}
		if gieEnabled {
			pilotEnv["ENABLE_GATEWAY_API_INFERENCE_EXTENSION"] = "true"
		}

		openshiftOverrides := &sailv1.Values{
			Global: &sailv1.GlobalConfig{
				IstioNamespace:  ptr.To("openshift-ingress"),
				TrustBundleName: ptr.To("openshift-gw-ca-root-cert"),
			},
			Pilot: &sailv1.PilotConfig{
				Env: pilotEnv,
				PodAnnotations: map[string]string{
					"target.workload.openshift.io/management": `{"effect": "PreferredDuringScheduling"}`,
				},
			},
			MeshConfig: &sailv1.MeshConfig{
				DefaultConfig: &sailv1.MeshConfigProxyConfig{
					ProxyMetadata: proxyConfig,
				},
			},
			GatewayClasses: gatewayclassesConfig,
		}
		values = install.MergeValues(values, openshiftOverrides)

		return &install.Options{
			Namespace:      "openshift-ingress",
			Version:        version,
			Revision:       "openshift-gateway",
			Values:         values,
			ManageCRDs:     ptr.To(true),
			IncludeAllCRDs: ptr.To(true),
		}
	}

	tests := []struct {
		name                             string
		request                          reconcile.Request
		expectedResult                   reconcile.Result
		fakeSailInstaller                SailLibraryInstaller
		existingObjects                  []client.Object
		expectCreate                     []client.Object
		expectUpdate                     []client.Object
		expectPatched                    []client.Object
		expectDelete                     []client.Object
		expectedStatusPatched            []client.Object
		expectError                      string
		expectedSailLibraryOptions       *install.Options
		expectSailLibraryUninstallCalled bool
	}{
		{
			name:            "OLM mode: Missing cluster infrastructure config and nonexistent gatewayclass",
			request:         req("openshift-default"),
			existingObjects: []client.Object{},
			expectCreate:    []client.Object{},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectError:     `infrastructures.config.openshift.io "cluster" not found`,
		},
		{
			name:    "OLM mode: Nonexistent gatewayclass",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			//expectError:     `"openshift-default" not found`, // We should not expect an error when a class is not found
		},
		{
			name:    "OLM mode: Missing cluster infrastructure config",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", false, nil, nil, false),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			expectError:  `infrastructures.config.openshift.io "cluster" not found`,
		},
		{
			name:    "OLM mode: Minimal gatewayclass",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", false, nil, nil, false),
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", false, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "OLM mode: Minimal gatewayclass with single-node topology",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.SingleReplicaTopologyMode),
				gatewayClass("openshift-default", false, nil, nil, false),
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", false, nil, gatewayclassesConfig(hpaConfig(1), "openshift-default")),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "OLM mode: Minimal gatewayclass on a cluster with multiple gatewayclasses",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", false, nil, nil, false),
				gatewayClass("openshift-internal", false, nil, nil, false),
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "istio",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("istio"),
					},
				},
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", false, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default", "openshift-internal")),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "OLM mode: Minimal gatewayclass and system proxy",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", false, nil, nil, false),
				proxyConfig("http://some.proxy.tld:8080", "https://another.proxy.tld", ".cluster.local,.ec2.internal,.svc,10.0.0.0/16,10.128.0.0/14"),
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", false, expectedProxyConfiguration, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "OLM mode: Minimal gatewayclass with experimental InferencePool CRD",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", false, nil, nil, false),
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.x-k8s.io",
					},
				},
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", true, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "OLM mode: Minimal gatewayclass with stable InferencePool CRD",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", false, nil, nil, false),
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.k8s.io",
					},
				},
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", true, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "OLM mode: Gatewayclass with Istio version override",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", false, map[string]string{
					"unsupported.do-not-use.openshift.io/istio-version": "v1.24-latest",
				}, nil, false),
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24-latest", false, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "OLM mode: Gatewayclass with OSSM and Istio overrides",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", false, map[string]string{
					"unsupported.do-not-use.openshift.io/ossm-catalog":  "foo",
					"unsupported.do-not-use.openshift.io/ossm-channel":  "bar",
					"unsupported.do-not-use.openshift.io/ossm-version":  "baz",
					"unsupported.do-not-use.openshift.io/istio-version": "quux",
				}, nil, false),
			},
			expectCreate: []client.Object{
				subscription("foo", "bar", "baz"),
				istio("quux", false, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:                       "Sail Library: Missing cluster infrastructure config and nonexistent gatewayclass",
			fakeSailInstaller:          &fakeSailInstaller{},
			request:                    req("openshift-default"),
			existingObjects:            []client.Object{},
			expectCreate:               []client.Object{},
			expectUpdate:               []client.Object{},
			expectDelete:               []client.Object{},
			expectError:                `infrastructures.config.openshift.io "cluster" not found`,
			expectedSailLibraryOptions: &install.Options{},
		},
		{
			name:              "Sail Library: nonexistent GatewayClass",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			//expectError:     `"openshift-default" not found`, // We should not expect an error when a class is not found
			expectedSailLibraryOptions: &install.Options{},
		},
		{
			name:              "Sail Library: Missing cluster infrastructure config",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", false, nil, nil, false),
			},
			expectCreate:               []client.Object{},
			expectUpdate:               []client.Object{},
			expectDelete:               []client.Object{},
			expectError:                `infrastructures.config.openshift.io "cluster" not found`,
			expectedSailLibraryOptions: &install.Options{},
		},
		{
			name:    "Sail Library: disabled - removes finalizer and status",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, []metav1.Condition{
					{
						Type:   ControllerInstalledConditionType,
						Reason: "Something",
						Status: metav1.ConditionUnknown,
					},
					{
						Type:   CRDsReadyConditionType,
						Reason: "Otherthing",
						Status: metav1.ConditionTrue,
					},
				}, false),
			},
			expectPatched: []client.Object{
				gatewayClass("openshift-default", false, nil, []metav1.Condition{}, false),
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", false, nil, []metav1.Condition{}, false),
			},
		},
		{
			name:              "Sail Library: adds finalizer to GatewayClass",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", false, nil, nil, false),
			},
			expectPatched: []client.Object{
				gatewayClass("openshift-default", true, nil, nil, false),
			},
			expectDelete:               []client.Object{},
			expectedSailLibraryOptions: &install.Options{},
		},
		{
			name:              "Sail Library: migrates old Istio CR instances",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, false),
				istioCRD(),
				istio("v1.24.4", true, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
			},
			expectPatched: []client.Object{},
			expectDelete: []client.Object{
				istio("v1.24.4", true, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
			},
			expectedResult: reconcile.Result{
				RequeueAfter: 5 * time.Second,
			},
			expectedSailLibraryOptions: &install.Options{},
		},
		{
			name:              "Sail Library: migration waiting for IstioRevision cleanup",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, false),
				istioCRD(),
				istioRevisionCRD(),
				istioRevision(),
			},
			expectPatched: []client.Object{},
			expectDelete:  []client.Object{},
			expectedResult: reconcile.Result{
				RequeueAfter: 5 * time.Second,
			},
			expectedSailLibraryOptions: &install.Options{},
		},
		{
			name:              "Sail Library: migration complete, proceed with install",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, false),
				istioCRD(),
				istioRevisionCRD(),
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", true, nil, installedConditions(), false),
			},
			expectedSailLibraryOptions: expectedSailLibraryOptions("v1.24.4", false, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
		},
		{
			name:              "Sail Library: installs Istio (single replica)",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.SingleReplicaTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, false),
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", true, nil, installedConditions(), false),
			},
			expectedSailLibraryOptions: expectedSailLibraryOptions("v1.24.4", false, nil, gatewayclassesConfig(hpaConfig(1), "openshift-default")),
		},
		{
			name:              "Sail Library: installs Istio (highly available)",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, false),
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", true, nil, installedConditions(), false),
			},
			expectedSailLibraryOptions: expectedSailLibraryOptions("v1.24.4", false, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
		},
		{
			name:              "Sail Library: installs Istio with system proxy configuration",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, false),
				proxyConfig("http://some.proxy.tld:8080", "https://another.proxy.tld", ".cluster.local,.ec2.internal,.svc,10.0.0.0/16,10.128.0.0/14"),
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", true, nil, installedConditions(), false),
			},
			expectedSailLibraryOptions: expectedSailLibraryOptions("v1.24.4", false, expectedProxyConfiguration, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
		},
		{
			name:              "Sail Library: installs Istio for multiple gatewayclasses in single-topology mode with system proxy configuration",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.SingleReplicaTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, false),
				gatewayClass("openshift-internal", true, nil, nil, false),
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "istio",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("istio"),
					},
				},
				gatewayClass("openshift-custom", true, nil, nil, false),
				proxyConfig("http://some.proxy.tld:8080", "https://another.proxy.tld", ".cluster.local,.ec2.internal,.svc,10.0.0.0/16,10.128.0.0/14"),
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", true, nil, installedConditions(), false),
			},
			expectedSailLibraryOptions: expectedSailLibraryOptions("v1.24.4", false, expectedProxyConfiguration, gatewayclassesConfig(hpaConfig(1), "openshift-default", "openshift-internal", "openshift-custom")),
		},
		{
			name:              "Sail Library: experimental InferencePool CRD",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, false),
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.x-k8s.io",
					},
				},
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", true, nil, installedConditions(), false),
			},
			expectedSailLibraryOptions: expectedSailLibraryOptions("v1.24.4", true, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
		},
		{
			name:              "Sail Library: stable InferencePool CRD",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, false),
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.k8s.io",
					},
				},
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", true, nil, installedConditions(), false),
			},
			expectedSailLibraryOptions: expectedSailLibraryOptions("v1.24.4", true, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
		},
		{
			name:              "Sail Library: Istio version override",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, map[string]string{
					"unsupported.do-not-use.openshift.io/istio-version": "v1.24-latest",
				}, nil, false),
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", true, map[string]string{
					"unsupported.do-not-use.openshift.io/istio-version": "v1.24-latest",
				}, installedConditions(), false),
			},
			expectedSailLibraryOptions: expectedSailLibraryOptions("v1.24-latest", false, nil, gatewayclassesConfig(hpaConfig(2), "openshift-default")),
		},
		{
			name:              "Sail Library: full removal of last GatewayClass",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, true),
			},
			expectPatched: []client.Object{
				gatewayClass("openshift-default", false, nil, nil, true),
			},
			expectedSailLibraryOptions:       &install.Options{},
			expectSailLibraryUninstallCalled: true,
		},
		{
			name:              "Sail Library: removes finalizer when other GatewayClasses exist",
			fakeSailInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, true),
				gatewayClass("openshift-secondary", true, nil, nil, false),
			},
			expectPatched: []client.Object{
				gatewayClass("openshift-default", false, nil, nil, true),
			},
			expectedSailLibraryOptions:       &install.Options{},
			expectSailLibraryUninstallCalled: false,
		},
		{
			name: "Sail Library: error on Uninstall failure",
			fakeSailInstaller: &fakeSailInstaller{
				uninstallError: fmt.Errorf("failed to cleanup resources"),
			},
			request: req("openshift-default"),
			existingObjects: []client.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				gatewayClass("openshift-default", true, nil, nil, true),
			},
			expectError:                      "failed to uninstall Istio: failed to cleanup resources",
			expectedSailLibraryOptions:       &install.Options{},
			expectSailLibraryUninstallCalled: true,
		},
	}

	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	gatewayapiv1.Install(scheme)
	operatorsv1alpha1.AddToScheme(scheme)
	sailv1.AddToScheme(scheme)
	apiextensionsv1.AddToScheme(scheme)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(tc.existingObjects...).
				WithObjects(tc.existingObjects...).
				WithIndex(&gatewayapiv1.GatewayClass{}, operatorcontroller.GatewayClassIndexFieldName, func(o client.Object) []string {
					gc := o.(*gatewayapiv1.GatewayClass)
					return []string{string(gc.Spec.ControllerName)}
				}).
				Build()
			cl := &testutil.FakeClientRecorder{
				Client: fakeClient,
				StatusWriter: &testutil.FakeStatusWriter{
					StatusWriter: fakeClient.Status(),
				},
				T:       t,
				Added:   []client.Object{},
				Updated: []client.Object{},
				Deleted: []client.Object{},
				Patched: []client.Object{},
			}
			gatewayClassController = &testutil.FakeController{
				T:                     t,
				Started:               false,
				StartNotificationChan: nil,
			}
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
			reconciler := &reconciler{
				client: cl,
				cache:  cache,
				config: Config{
					OperatorNamespace:         "openshift-ingress-operator",
					OperandNamespace:          "openshift-ingress",
					GatewayAPIOperatorCatalog: "redhat-operators",
					GatewayAPIOperatorChannel: "stable",
					GatewayAPIOperatorVersion: "servicemeshoperator3.v3.0.1",
					IstioVersion:              "v1.24.4",
				},
			}
			if tc.fakeSailInstaller != nil {
				reconciler.config.GatewayAPIWithoutOLMEnabled = true
				reconciler.sailInstaller = tc.fakeSailInstaller
			}
			res, err := reconciler.Reconcile(context.Background(), tc.request)
			if tc.expectError != "" {
				require.NotNil(t, err)
				assert.Contains(t, err.Error(), tc.expectError)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tc.expectedResult, res)
			cmpOpts := []cmp.Option{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "OwnerReferences", "DeletionTimestamp"),
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message"),
				cmpopts.IgnoreFields(operatorsv1alpha1.SubscriptionSpec{}, "Config"),
				cmpopts.IgnoreFields(apiextensionsv1.CustomResourceDefinition{}, "Spec"),
			}
			if diff := cmp.Diff(tc.expectCreate, cl.Added, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual creates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectUpdate, cl.Updated, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual updates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectDelete, cl.Deleted, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual deletes: %s", diff)
			}
			if diff := cmp.Diff(tc.expectPatched, cl.Patched, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual patches: %s", diff)
			}
			if diff := cmp.Diff(tc.expectedStatusPatched, cl.StatusWriter.Patched, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual patches: %s", diff)
			}
			if tc.fakeSailInstaller != nil {
				fakeInstaller := tc.fakeSailInstaller.(*fakeSailInstaller)

				if tc.expectedSailLibraryOptions != nil {
					optsDiff := cmp.Diff(
						tc.expectedSailLibraryOptions,
						&fakeInstaller.internalOpts,
						cmpopts.IgnoreFields(install.Options{}, "OverwriteOLMManagedCRD"),
					)
					if optsDiff != "" {
						t.Fatalf("found diff between Sail Library options:\n%s", optsDiff)
					}
				}

				if tc.expectSailLibraryUninstallCalled != fakeInstaller.uninstallCalled {
					t.Fatalf("expected uninstallCalled=%v, got %v", tc.expectSailLibraryUninstallCalled, fakeInstaller.uninstallCalled)
				}
			}

		})
	}
}
