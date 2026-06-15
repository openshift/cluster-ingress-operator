package gatewayclass

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/pkg/install"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	configv1 "github.com/openshift/api/config/v1"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// fakeSailInstaller implements the Istio methods for installation
// We care for now just about Apply, Uninstall and Status for the reconciliation tests
type fakeSailInstaller struct {
	notifyCh        chan struct{}
	internalOpts    install.Options
	status          install.Status
	uninstallCalled bool
	uninstallError  error
}

// Test if implementation adheres the interface
var _ SailLibraryInstaller = &fakeSailInstaller{}

func (i *fakeSailInstaller) Start(ctx context.Context) (<-chan struct{}, error) {
	i.notifyCh = make(chan struct{})
	return i.notifyCh, nil
}

func (i *fakeSailInstaller) Apply(opts install.Options) error {
	i.internalOpts = opts // Capture the options passed in

	// Simulate successful installation with CRDs in unknown state
	i.status.Installed = true
	i.status.Version = opts.Version
	i.status.CRDState = install.CRDManagementStateUnknown
	return nil
}

func (i *fakeSailInstaller) Uninstall(ctx context.Context, namespace, revision string) error {
	i.uninstallCalled = true
	return i.uninstallError
}

func (i *fakeSailInstaller) Status() install.Status {
	return i.status
}

func (i *fakeSailInstaller) Enqueue() {}

func Test_overwriteOLMManagedCRDFunc(t *testing.T) {
	crd := func(name string, labels map[string]string) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: labels,
			},
		}
	}

	subscription := func(name, namespace, label string) *operatorsv1alpha1.Subscription {
		sub := &operatorsv1alpha1.Subscription{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		}
		if label != "" {
			sub.Labels = map[string]string{label: ""}
		}
		return sub
	}

	installPlan := func(label string) *operatorsv1alpha1.InstallPlan {
		return &operatorsv1alpha1.InstallPlan{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "install-plan-1",
				Namespace: "openshift-operators",
				Labels:    map[string]string{label: ""},
			},
		}
	}

	tests := []struct {
		name            string
		crd             *apiextensionsv1.CustomResourceDefinition
		existingObjects []client.Object
		expectedResult  bool
	}{
		{
			name:           "nil CRD returns false",
			crd:            nil,
			expectedResult: false,
		},
		{
			name:           "CRD with no labels can be overwritten",
			crd:            crd("test-crd", nil),
			expectedResult: true,
		},
		{
			name: "CRD without olm.managed label can be overwritten",
			crd: crd("test-crd", map[string]string{
				"some-other-label": "value",
			}),
			expectedResult: true,
		},
		{
			name: "CRD with olm.managed=false can be overwritten",
			crd: crd("test-crd", map[string]string{
				"olm.managed": "false",
			}),
			expectedResult: true,
		},
		{
			name: "CRD with olm.managed but no subscription label can be overwritten",
			crd: crd("test-crd", map[string]string{
				"olm.managed": "true",
			}),
			expectedResult: true,
		},
		{
			name: "CRD with olm.managed and invalid subscription label can be overwritten",
			crd: crd("test-crd", map[string]string{
				"olm.managed":                        "true",
				"operators.coreos.com/invalid":       "",
				"operators.coreos.com/too.many.dots": "",
			}),
			expectedResult: true,
		},
		{
			name: "CRD with active InstallPlans cannot be overwritten",
			crd: crd("test-crd", map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/servicemeshoperator.openshift-operators": "",
			}),
			existingObjects: []client.Object{
				installPlan("operators.coreos.com/servicemeshoperator.openshift-operators"),
			},
			expectedResult: false,
		},
		{
			name: "CRD with subscription but no InstallPlans cannot be overwritten",
			crd: crd("test-crd", map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/servicemeshoperator.openshift-operators": "",
			}),
			existingObjects: []client.Object{
				subscription("servicemeshoperator", "openshift-operators", "operators.coreos.com/servicemeshoperator.openshift-operators"),
			},
			expectedResult: false,
		},
		{
			name: "CRD without subscription can be overwritten",
			crd: crd("test-crd", map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/servicemeshoperator.openshift-operators": "",
			}),
			existingObjects: []client.Object{},
			expectedResult:  true,
		},
		{
			name: "CRD with subscription name containing dots - parses correctly",
			crd: crd("test-crd", map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/my.service.operator.openshift-operators": "",
			}),
			existingObjects: []client.Object{
				subscription("my.service.operator", "openshift-operators", "operators.coreos.com/my.service.operator.openshift-operators"),
			},
			expectedResult: false,
		},
		{
			name: "CRD with subscription name containing dots - subscription deleted",
			crd: crd("test-crd", map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/my.service.operator.openshift-operators": "",
			}),
			existingObjects: []client.Object{},
			expectedResult:  true,
		},
		{
			name: "CRD with multiple subscription labels - one active subscription exists",
			crd: crd("test-crd", map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/operator-one.openshift-operators": "",
				"operators.coreos.com/operator-two.openshift-operators": "",
			}),
			existingObjects: []client.Object{
				subscription("operator-two", "openshift-operators", "operators.coreos.com/operator-two.openshift-operators"),
			},
			expectedResult: false,
		},
		{
			name: "CRD with multiple subscription labels - all subscriptions deleted",
			crd: crd("test-crd", map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/operator-one.openshift-operators": "",
				"operators.coreos.com/operator-two.openshift-operators": "",
			}),
			existingObjects: []client.Object{},
			expectedResult:  true,
		},
	}

	scheme := runtime.NewScheme()
	operatorsv1alpha1.AddToScheme(scheme)
	apiextensionsv1.AddToScheme(scheme)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tc.existingObjects...).
				Build()
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}

			reconciler := &reconciler{
				cache: cache,
			}

			result := reconciler.overwriteOLMManagedCRDFunc(context.Background(), tc.crd)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}

func Test_mapStatusToConditions(t *testing.T) {
	tests := []struct {
		name               string
		status             install.Status
		generation         int64
		initialConditions  []metav1.Condition
		expectedConditions []metav1.Condition
		expectedChanged    bool
	}{
		{
			name: "Installed with CRDs ready",
			status: install.Status{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   install.CRDManagementStateReady,
				CRDMessage: "",
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Installed",
					Message:            "istiod v1.24.4 installed",
					ObservedGeneration: 1,
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					Message:            "all CRDs are ready and managed",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Installed with CRDs ready and all managed",
			status: install.Status{
				Installed: true,
				Version:   "v1.24.4",
				CRDState:  install.CRDManagementStateReady,
				CRDs: []install.CRDInfo{
					{Name: "wasmplugins.extensions.istio.io", Managed: true, Ready: true},
					{Name: "destinationrules.networking.istio.io", Managed: true, Ready: true},
				},
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Installed",
					Message:            "istiod v1.24.4 installed",
					ObservedGeneration: 1,
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					Message:            "all CRDs are ready and managed",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Installed with CRDs unknown",
			status: install.Status{
				Installed: true,
				Version:   "v1.24.4",
				CRDState:  install.CRDManagementStateUnknown,
			},
			generation: 2,
			expectedConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Installed",
					Message:            "istiod v1.24.4 installed",
					ObservedGeneration: 2,
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionUnknown,
					Reason:             "NoneExist",
					Message:            "CRDs not yet installed",
					ObservedGeneration: 2,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Installed with CRDs not ready",
			status: install.Status{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   install.CRDManagementStateNotReady,
				CRDMessage: "not all CRDs are ready",
				CRDs: []install.CRDInfo{
					{Name: "gateways.gateway.networking.k8s.io", Managed: false, Ready: true},
					{Name: "httproutes.gateway.networking.k8s.io", Managed: true, Ready: false},
				},
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Installed",
					Message:            "istiod v1.24.4 installed",
					ObservedGeneration: 1,
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionFalse,
					Reason:             "NotReady",
					Message:            "not all CRDs are ready\n- gateways.gateway.networking.k8s.io: NotManaged\n- httproutes.gateway.networking.k8s.io: Managed (not ready)",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "CRDs ready but some not managed",
			status: install.Status{
				Installed: true,
				Version:   "v1.24.4",
				CRDState:  install.CRDManagementStateReady,
				CRDs: []install.CRDInfo{
					{Name: "wasmplugins.extensions.istio.io", Managed: true, Ready: true},
					{Name: "destinationrules.networking.istio.io", Managed: false, Ready: true},
					{Name: "virtualservices.networking.istio.io", Managed: false, Ready: true},
				},
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Installed",
					Message:            "istiod v1.24.4 installed",
					ObservedGeneration: 1,
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionFalse,
					Reason:             "MixedOwnership",
					Message:            "CRDs have mixed ownership\n- wasmplugins.extensions.istio.io: Managed\n- destinationrules.networking.istio.io: NotManaged\n- virtualservices.networking.istio.io: NotManaged",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Install failed",
			status: install.Status{
				Installed: false,
				Error:     fmt.Errorf("failed to apply helm chart"),
				CRDState:  install.CRDManagementStateError,
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionFalse,
					Reason:             "InstallFailed",
					Message:            "failed to apply helm chart",
					ObservedGeneration: 1,
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionFalse,
					Reason:             "Error",
					Message:            "unable to determine CRD ownership",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Pending installation",
			status: install.Status{
				Installed: false,
				CRDState:  install.CRDManagementStateUnknown,
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionUnknown,
					Reason:             "Pending",
					Message:            "waiting for first reconciliation",
					ObservedGeneration: 1,
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionUnknown,
					Reason:             "NoneExist",
					Message:            "CRDs not yet installed",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Installed with warning",
			status: install.Status{
				Installed: true,
				Version:   "v1.24.4",
				Error:     fmt.Errorf("drift detected in deployment"),
				CRDState:  install.CRDManagementStateReady,
				CRDs: []install.CRDInfo{
					{Name: "wasmplugins.extensions.istio.io", Managed: true, Ready: true},
				},
			},
			generation: 3,
			expectedConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Installed",
					Message:            "istiod v1.24.4 installed (with warning: drift detected in deployment)",
					ObservedGeneration: 3,
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					Message:            "all CRDs are ready and managed",
					ObservedGeneration: 3,
				},
			},
			expectedChanged: true,
		},
		{
			name: "CRD error state",
			status: install.Status{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   install.CRDManagementStateError,
				CRDMessage: "failed to reconcile CRDs",
				CRDs: []install.CRDInfo{
					{Name: "wasmplugins.extensions.istio.io", Managed: true, Ready: true},
					{Name: "destinationrules.networking.istio.io", Managed: true, Ready: false},
				},
			},
			generation: 1,
			expectedConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Installed",
					Message:            "istiod v1.24.4 installed",
					ObservedGeneration: 1,
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionFalse,
					Reason:             "Error",
					Message:            "failed to reconcile CRDs\n- wasmplugins.extensions.istio.io: Managed\n- destinationrules.networking.istio.io: Managed (not ready)",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "No change when conditions already set",
			status: install.Status{
				Installed: true,
				Version:   "v1.24.4",
				CRDState:  install.CRDManagementStateReady,
			},
			generation: 1,
			initialConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Installed",
					Message:            "istiod v1.24.4 installed",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					Message:            "all CRDs are ready and managed",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.Now(),
				},
			},
			expectedConditions: []metav1.Condition{
				{
					Type:               ControllerInstalledConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Installed",
					Message:            "istiod v1.24.4 installed",
					ObservedGeneration: 1,
				},
				{
					Type:               CRDsReadyConditionType,
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					Message:            "all CRDs are ready and managed",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conditions := tc.initialConditions
			if conditions == nil {
				conditions = []metav1.Condition{}
			}
			changed := mapStatusToConditions(tc.status, tc.generation, &conditions)
			if changed != tc.expectedChanged {
				t.Errorf("expected changed=%v, got changed=%v", tc.expectedChanged, changed)
			}

			cmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			}
			if diff := cmp.Diff(tc.expectedConditions, conditions, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual conditions: %s", diff)
			}
		})
	}
}

// TestIstioValues verifies that both the OLM and Sail Library paths produce
// the expected Istio values. Each path is compared against a hardcoded golden
// struct so that changes in either CIO or the sail library are caught.
func TestIstioValues(t *testing.T) {
	defaultGatewayClass := []gatewayapiv1.GatewayClass{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openshift-default",
		},
	}}
	infraConfig := func(infraTopologyMode configv1.TopologyMode) *configv1.Infrastructure {
		return &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.InfrastructureStatus{
				InfrastructureTopology: infraTopologyMode,
			},
		}
	}
	singleReplicaInfraConfig := infraConfig(configv1.SingleReplicaTopologyMode)
	highlyAvailableInfraConfig := infraConfig(configv1.HighlyAvailableTopologyMode)

	gwClassConfig := func(minReplicas int) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(
			`{"openshift-default":{"deployment":{"spec":{"template":{"spec":{"containers":[{"name":"istio-proxy","terminationMessagePolicy":"FallbackToLogsOnError"}]}}}},"horizontalPodAutoscaler":{"spec":{"maxReplicas":10,"minReplicas":%d}}}}`,
			minReplicas,
		))
	}

	testCases := []struct {
		name                     string
		gatewayclasses           []gatewayapiv1.GatewayClass
		enableInferenceExtension bool
		extraconfig              *extraIstioConfig
		expectedProxyMetadata    map[string]string
		expectedGatewayClasses   json.RawMessage
	}{
		{
			name:                     "without inference extension",
			gatewayclasses:           defaultGatewayClass,
			enableInferenceExtension: false,
			extraconfig:              &extraIstioConfig{infraConfig: highlyAvailableInfraConfig},
			expectedGatewayClasses:   gwClassConfig(2),
		},
		{
			name:                     "with inference extension",
			gatewayclasses:           defaultGatewayClass,
			enableInferenceExtension: true,
			extraconfig:              &extraIstioConfig{infraConfig: highlyAvailableInfraConfig},
			expectedGatewayClasses:   gwClassConfig(2),
		},
		{
			name:                     "with single-node topology",
			gatewayclasses:           defaultGatewayClass,
			enableInferenceExtension: true,
			extraconfig:              &extraIstioConfig{infraConfig: singleReplicaInfraConfig},
			expectedGatewayClasses:   gwClassConfig(1),
		},
		{
			name:           "with system proxy config enabled",
			gatewayclasses: defaultGatewayClass,
			extraconfig: &extraIstioConfig{
				proxyConfig: &configv1.Proxy{
					Status: configv1.ProxyStatus{
						HTTPProxy:  "http://some.proxy.tld:8080",
						HTTPSProxy: "http://some.proxytls.tld:8080",
						NoProxy:    ".cluster.local,.ec2.internal,.svc,10.0.0.0/16,10.128.0.0/14",
					},
				},
				infraConfig: highlyAvailableInfraConfig,
			},
			expectedProxyMetadata: map[string]string{
				"HTTP_PROXY":  "http://some.proxy.tld:8080",
				"HTTPS_PROXY": "http://some.proxytls.tld:8080",
				"NO_PROXY":    ".cluster.local,.ec2.internal,.svc,10.0.0.0/16,10.128.0.0/14",
				"http_proxy":  "http://some.proxy.tld:8080",
				"https_proxy": "http://some.proxytls.tld:8080",
				"no_proxy":    ".cluster.local,.ec2.internal,.svc,10.0.0.0/16,10.128.0.0/14",
			},
			expectedGatewayClasses: gwClassConfig(2),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pilotEnv := map[string]string{
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
			}
			if tc.enableInferenceExtension {
				pilotEnv["ENABLE_GATEWAY_API_INFERENCE_EXTENSION"] = "true"
			}

			expected := &sailv1.Values{
				Global: &sailv1.GlobalConfig{
					DefaultPodDisruptionBudget: &sailv1.DefaultPodDisruptionBudgetConfig{
						Enabled: ptr.To(false),
					},
					IstioNamespace:    ptr.To("openshift-ingress"),
					PriorityClassName: ptr.To("system-cluster-critical"),
					TrustBundleName:   ptr.To("openshift-gw-ca-root-cert"),
				},
				Pilot: &sailv1.PilotConfig{
					Env: pilotEnv,
					PodAnnotations: map[string]string{
						"target.workload.openshift.io/management": `{"effect": "PreferredDuringScheduling"}`,
					},
				},
				MeshConfig: &sailv1.MeshConfig{
					AccessLogFile:         ptr.To("/dev/stdout"),
					IngressControllerMode: sailv1.MeshConfigIngressControllerModeOff,
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
						ProxyMetadata: tc.expectedProxyMetadata,
					},
				},
				GatewayClasses: tc.expectedGatewayClasses,
			}

			// Sail Library path: only tests the values CIO explicitly sets
			// (GatewayAPIDefaults + openshiftValues), not the full effective
			// values after OSSM applies profiles and Istio defaults via
			// ComputeValues, which is difficult to invoke in a unit test.
			t.Run("sail library", func(t *testing.T) {
				sailValues := install.GatewayAPIDefaults("openshift-ingress")
				openshiftOverrides, err := openshiftValues(tc.enableInferenceExtension, "openshift-ingress", tc.gatewayclasses, tc.extraconfig)
				require.NoError(t, err)
				sailValues, err = install.MergeValues(sailValues, openshiftOverrides)
				require.NoError(t, err)

				cmpOpts := []cmp.Option{
					cmpopts.EquateEmpty(),
				}
				if diff := cmp.Diff(expected, sailValues, cmpOpts...); diff != "" {
					t.Errorf("Sail Library values differ from expected:\n%s", diff)
				}
			})

			// OLM path
			t.Run("OLM", func(t *testing.T) {
				olmIstio, err := desiredIstio(
					types.NamespacedName{Name: "test", Namespace: "test-ns"},
					metav1.OwnerReference{},
					"v1.24.4",
					tc.enableInferenceExtension,
					tc.gatewayclasses,
					tc.extraconfig,
				)
				require.NoError(t, err)

				cmpOpts := []cmp.Option{
					cmpopts.EquateEmpty(),
					// OLM explicitly sets these Istio defaults but the sail
					// library path does not. Including them in the expected
					// struct would fail the sail library sub-test.
					cmpopts.IgnoreFields(sailv1.PilotConfig{}, "Enabled", "Cni", "ExtraContainerArgs"),
					cmpopts.IgnoreFields(sailv1.Values{}, "SidecarInjectorWebhook"),
				}
				if diff := cmp.Diff(expected, olmIstio.Spec.Values, cmpOpts...); diff != "" {
					t.Errorf("OLM values differ from expected:\n%s", diff)
				}
			})
		})
	}
}
