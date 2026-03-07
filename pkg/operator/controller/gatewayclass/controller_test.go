package gatewayclass

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	configv1 "github.com/openshift/api/config/v1"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/stretchr/testify/assert"

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

	istio := func(version string, gieEnabled bool, gatewayclasses []string, minReplicas int) *sailv1.Istio {
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
		ret.Spec.Values.GatewayClasses = []byte(fmt.Sprintf(`{%s}`, strings.Join(func() []string {
			var result []string

			for _, name := range gatewayclasses {
				result = append(result, fmt.Sprintf(`"%s":{"horizontalPodAutoscaler":{"spec":{"maxReplicas":10,"minReplicas":%d}}}`, name, minReplicas))
			}

			return result
		}(), ",")))

		return ret
	}

	tests := []struct {
		name            string
		request         reconcile.Request
		existingObjects []runtime.Object
		expectCreate    []client.Object
		expectUpdate    []client.Object
		expectDelete    []client.Object
		expectError     string
	}{
		{
			name:            "Missing cluster infrastructure config and nonexistent gatewayclass",
			request:         req("openshift-default"),
			existingObjects: []runtime.Object{},
			expectCreate:    []client.Object{},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			expectError:     `infrastructures.config.openshift.io "cluster" not found`,
		},
		{
			name:    "Nonexistent gatewayclass",
			request: req("openshift-default"),
			existingObjects: []runtime.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			expectError:  `"openshift-default" not found`,
		},
		{
			name:    "Missing cluster infrastructure config",
			request: req("openshift-default"),
			existingObjects: []runtime.Object{
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "openshift-default",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("openshift.io/gateway-controller/v1"),
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
			expectError:  `infrastructures.config.openshift.io "cluster" not found`,
		},
		{
			name:    "Minimal gatewayclass",
			request: req("openshift-default"),
			existingObjects: []runtime.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "openshift-default",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("openshift.io/gateway-controller/v1"),
					},
				},
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", false, []string{"openshift-default"}, 2),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "Minimal gatewayclass with single-node topology",
			request: req("openshift-default"),
			existingObjects: []runtime.Object{
				infraConfig(configv1.SingleReplicaTopologyMode),
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "openshift-default",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("openshift.io/gateway-controller/v1"),
					},
				},
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", false, []string{"openshift-default"}, 1),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "Minimal gatewayclass on a cluster with multiple gatewayclasses",
			request: req("openshift-default"),
			existingObjects: []runtime.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "openshift-default",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("openshift.io/gateway-controller/v1"),
					},
				},
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "openshift-internal",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("openshift.io/gateway-controller/v1"),
					},
				},
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
				istio("v1.24.4", false, []string{"openshift-default", "openshift-internal"}, 2),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "Minimal gatewayclass with experimental InferencePool CRD",
			request: req("openshift-default"),
			existingObjects: []runtime.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "openshift-default",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("openshift.io/gateway-controller/v1"),
					},
				},
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.x-k8s.io",
					},
				},
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", true, []string{"openshift-default"}, 2),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "Minimal gatewayclass with stable InferencePool CRD",
			request: req("openshift-default"),
			existingObjects: []runtime.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "openshift-default",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("openshift.io/gateway-controller/v1"),
					},
				},
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.k8s.io",
					},
				},
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", true, []string{"openshift-default"}, 2),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "Gatewayclass with Istio version override",
			request: req("openshift-default"),
			existingObjects: []runtime.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"unsupported.do-not-use.openshift.io/istio-version": "v1.24-latest",
						},
						Name: "openshift-default",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("openshift.io/gateway-controller/v1"),
					},
				},
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24-latest", false, []string{"openshift-default"}, 2),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "Gatewayclass with OSSM and Istio overrides",
			request: req("openshift-default"),
			existingObjects: []runtime.Object{
				infraConfig(configv1.HighlyAvailableTopologyMode),
				&gatewayapiv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"unsupported.do-not-use.openshift.io/ossm-catalog":  "foo",
							"unsupported.do-not-use.openshift.io/ossm-channel":  "bar",
							"unsupported.do-not-use.openshift.io/ossm-version":  "baz",
							"unsupported.do-not-use.openshift.io/istio-version": "quux",
						},
						Name: "openshift-default",
					},
					Spec: gatewayapiv1.GatewayClassSpec{
						ControllerName: gatewayapiv1.GatewayController("openshift.io/gateway-controller/v1"),
					},
				},
			},
			expectCreate: []client.Object{
				subscription("foo", "bar", "baz"),
				istio("quux", false, []string{"openshift-default"}, 2),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
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
				WithRuntimeObjects(tc.existingObjects...).
				WithIndex(&gatewayapiv1.GatewayClass{}, "spec.controllerName", client.IndexerFunc(func(o client.Object) []string {
					return []string{string(o.(*gatewayapiv1.GatewayClass).Spec.ControllerName)}
				})).
				Build()
			cl := &testutil.FakeClientRecorder{
				Client:  fakeClient,
				T:       t,
				Added:   []client.Object{},
				Updated: []client.Object{},
				Deleted: []client.Object{},
			}
			gatewayClassController = &testutil.FakeController{
				T:                     t,
				Started:               false,
				StartNotificationChan: nil,
			}
			informer := informertest.FakeInformers{Scheme: scheme}
			cache := &testutil.FakeCache{Informers: &informer, Reader: fakeClient}
			reconciler := &reconciler{
				client:       cl,
				cache:        cache,
				fieldIndexer: FakeIndexer{},
				config: Config{
					OperatorNamespace:         "openshift-ingress-operator",
					OperandNamespace:          "openshift-ingress",
					GatewayAPIOperatorCatalog: "redhat-operators",
					GatewayAPIOperatorChannel: "stable",
					GatewayAPIOperatorVersion: "servicemeshoperator3.v3.0.1",
					IstioVersion:              "v1.24.4",
				},
			}
			res, err := reconciler.Reconcile(context.Background(), tc.request)
			if tc.expectError != "" {
				assert.Contains(t, err.Error(), tc.expectError)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, reconcile.Result{}, res)
			cmpOpts := []cmp.Option{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "OwnerReferences"),
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
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
		})
	}
}

type FakeIndexer struct{}

func (indexer FakeIndexer) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	return nil
}
