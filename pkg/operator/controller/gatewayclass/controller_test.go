package gatewayclass

import (
	"context"
	"fmt"
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

func Test_Reconcile(t *testing.T) {
	req := func(name string) reconcile.Request {
		return reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name},
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

	istio := func(version string, gieEnabled bool) *sailv1.Istio {
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

		return ret
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

	expectedHelmInstallerOptions := func(version string, gieEnabled bool) *install.Options {
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
		name                         string
		request                      reconcile.Request
		expectedResult               reconcile.Result
		fakeHelmInstaller            SailLibraryInstaller
		existingObjects              []client.Object
		expectCreate                 []client.Object
		expectUpdate                 []client.Object
		expectPatched                []client.Object
		expectDelete                 []client.Object
		expectedStatusPatched        []client.Object
		expectError                  string
		expectedHelmInstallerOptions *install.Options
		expectHelmUninstallCalled    bool
	}{
		{
			name:            "Nonexistent gatewayclass",
			request:         req("openshift-default"),
			existingObjects: []client.Object{},
			expectCreate:    []client.Object{},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
			//expectError:     `"openshift-default" not found`, // We should not expect an error when a class is not found
		},
		{
			name:    "Minimal gatewayclass",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", false, nil, nil, false),
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", false),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "Minimal gatewayclass with experimental InferencePool CRD",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", false, nil, nil, false),
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.x-k8s.io",
					},
				},
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", true),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "Minimal gatewayclass with stable InferencePool CRD",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", false, nil, nil, false),
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "inferencepools.inference.networking.k8s.io",
					},
				},
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24.4", true),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "Gatewayclass with Istio version override",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", false, map[string]string{
					"unsupported.do-not-use.openshift.io/istio-version": "v1.24-latest",
				}, nil, false),
			},
			expectCreate: []client.Object{
				subscription("redhat-operators", "stable", "servicemeshoperator3.v3.0.1"),
				istio("v1.24-latest", false),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:    "Gatewayclass with OSSM and Istio overrides",
			request: req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", false, map[string]string{
					"unsupported.do-not-use.openshift.io/ossm-catalog":  "foo",
					"unsupported.do-not-use.openshift.io/ossm-channel":  "bar",
					"unsupported.do-not-use.openshift.io/ossm-version":  "baz",
					"unsupported.do-not-use.openshift.io/istio-version": "quux",
				}, nil, false),
			},
			expectCreate: []client.Object{
				subscription("foo", "bar", "baz"),
				istio("quux", false),
			},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			name:              "Helm installer with nonexistent GatewayClass",
			fakeHelmInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects:   []client.Object{},
			expectCreate:      []client.Object{},
			expectUpdate:      []client.Object{},
			expectDelete:      []client.Object{},
			//expectError:     `"openshift-default" not found`, // We should not expect an error when a class is not found
			expectedHelmInstallerOptions: &install.Options{},
		},
		{
			name:    "Helm installer disabled removes finalizer and status",
			request: req("openshift-default"),
			existingObjects: []client.Object{
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
			name:              "Helm installer adds finalizer to GatewayClass",
			fakeHelmInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", false, nil, nil, false),
			},
			expectPatched: []client.Object{
				gatewayClass("openshift-default", true, nil, nil, false),
			},
			expectDelete:                 []client.Object{},
			expectedHelmInstallerOptions: &install.Options{},
		},
		{
			name:              "Helm installer migrates old Istio CR instances",
			fakeHelmInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", true, nil, nil, false),
				istioCRD(),
				istio("v1.24.4", true),
			},
			expectPatched: []client.Object{},
			expectDelete: []client.Object{
				istio("v1.24.4", true),
			},
			expectedResult: reconcile.Result{
				RequeueAfter: 5 * time.Second,
			},
			expectedHelmInstallerOptions: &install.Options{},
		},
		{
			name:              "Helm installer installs Istio",
			fakeHelmInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", true, nil, nil, false),
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", true, nil, installedConditions(), false),
			},
			expectedHelmInstallerOptions: expectedHelmInstallerOptions("v1.24.4", false),
		},
		{
			name:              "Helm installer with experimental InferencePool CRD",
			fakeHelmInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
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
			expectedHelmInstallerOptions: expectedHelmInstallerOptions("v1.24.4", true),
		},
		{
			name:              "Helm installer with stable InferencePool CRD",
			fakeHelmInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
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
			expectedHelmInstallerOptions: expectedHelmInstallerOptions("v1.24.4", true),
		},
		{
			name:              "Helm installer with Istio version override",
			fakeHelmInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", true, map[string]string{
					"unsupported.do-not-use.openshift.io/istio-version": "v1.24-latest",
				}, nil, false),
			},
			expectedStatusPatched: []client.Object{
				gatewayClass("openshift-default", true, map[string]string{
					"unsupported.do-not-use.openshift.io/istio-version": "v1.24-latest",
				}, installedConditions(), false),
			},
			expectedHelmInstallerOptions: expectedHelmInstallerOptions("v1.24-latest", false),
		},
		{
			name:              "Helm installer full removal of last GatewayClass",
			fakeHelmInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", true, nil, nil, true),
			},
			expectPatched: []client.Object{
				gatewayClass("openshift-default", false, nil, nil, true),
			},
			expectedHelmInstallerOptions: &install.Options{},
			expectHelmUninstallCalled:    true,
		},
		{
			name:              "Helm installer removes finalizer when other GatewayClasses exist",
			fakeHelmInstaller: &fakeSailInstaller{},
			request:           req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", true, nil, nil, true),
				gatewayClass("openshift-secondary", true, nil, nil, false),
			},
			expectPatched: []client.Object{
				gatewayClass("openshift-default", false, nil, nil, true),
			},
			expectedHelmInstallerOptions: &install.Options{},
			expectHelmUninstallCalled:    false,
		},
		{
			name: "Helm installer returns error on Uninstall failure",
			fakeHelmInstaller: &fakeSailInstaller{
				uninstallError: fmt.Errorf("failed to cleanup resources"),
			},
			request: req("openshift-default"),
			existingObjects: []client.Object{
				gatewayClass("openshift-default", true, nil, nil, true),
			},
			expectError:                  "failed to uninstall the operator",
			expectedHelmInstallerOptions: &install.Options{},
			expectHelmUninstallCalled:    true,
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
			if tc.fakeHelmInstaller != nil {
				reconciler.config.GatewayAPIWithoutOLMEnabled = true
				reconciler.sailInstaller = tc.fakeHelmInstaller
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
			if tc.fakeHelmInstaller != nil {
				fakeInstaller := tc.fakeHelmInstaller.(*fakeSailInstaller)

				if tc.expectedHelmInstallerOptions != nil {
					optsDiff := cmp.Diff(
						tc.expectedHelmInstallerOptions,
						&fakeInstaller.internalOpts,
						cmpopts.IgnoreFields(install.Options{}, "OverwriteOLMManagedCRD"),
					)
					if optsDiff != "" {
						t.Fatalf("found diff between helm installer options:\n%s", optsDiff)
					}
				}

				if tc.expectHelmUninstallCalled != fakeInstaller.uninstallCalled {
					t.Fatalf("expected uninstallCalled=%v, got %v", tc.expectHelmUninstallCalled, fakeInstaller.uninstallCalled)
				}
			}

		})
	}
}
