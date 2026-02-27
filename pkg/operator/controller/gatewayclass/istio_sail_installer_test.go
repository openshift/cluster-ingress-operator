package gatewayclass

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/istio-ecosystem/sail-operator/pkg/install"
	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/stretchr/testify/assert"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

func (i *fakeSailInstaller) Start(ctx context.Context) <-chan struct{} {
	i.notifyCh = make(chan struct{})
	return i.notifyCh
}

func (i *fakeSailInstaller) Apply(opts install.Options) {
	i.internalOpts = opts // Capture the options passed in

	// Simulate successful installation with CRDs not yet installed
	i.status.Installed = true
	i.status.Version = opts.Version
	i.status.CRDState = install.CRDNoneExist
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
		expectedConditions []metav1.Condition
	}{
		{
			name: "Installed with CRDs managed by CIO",
			status: install.Status{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   install.CRDManagedByCIO,
				CRDMessage: "CRDs installed by cluster-ingress-operator",
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
					Reason:             "ManagedByCIO",
					Message:            "CRDs installed by cluster-ingress-operator",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name: "Installed with CRDs managed by OLM",
			status: install.Status{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   install.CRDManagedByOLM,
				CRDMessage: "CRDs managed by OLM",
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
					Reason:             "ManagedByOLM",
					Message:            "CRDs managed by OLM",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name: "Installed with no CRDs",
			status: install.Status{
				Installed: true,
				Version:   "v1.24.4",
				CRDState:  install.CRDNoneExist,
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
		},
		{
			name: "Installed with mixed CRD ownership",
			status: install.Status{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   install.CRDMixedOwnership,
				CRDMessage: "Mixed ownership detected",
				CRDs: []install.CRDInfo{
					{Name: "gateways.gateway.networking.k8s.io", State: install.CRDManagedByOLM},
					{Name: "httproutes.gateway.networking.k8s.io", State: install.CRDManagedByCIO},
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
					Message:            "Mixed ownership detected\n- gateways.gateway.networking.k8s.io: ManagedByOLM\n- httproutes.gateway.networking.k8s.io: ManagedByCIO",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name: "Install failed",
			status: install.Status{
				Installed: false,
				Error:     fmt.Errorf("failed to apply helm chart"),
				CRDState:  install.CRDUnknownManagement,
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
					Reason:             "UnknownManagement",
					ObservedGeneration: 1,
				},
			},
		},
		{
			name: "Pending installation",
			status: install.Status{
				Installed: false,
				CRDState:  install.CRDNoneExist,
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
		},
		{
			name: "Installed with warning",
			status: install.Status{
				Installed:  true,
				Version:    "v1.24.4",
				Error:      fmt.Errorf("drift detected in deployment"),
				CRDState:   install.CRDManagedByCIO,
				CRDMessage: "CRDs managed",
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
					Reason:             "ManagedByCIO",
					Message:            "CRDs managed",
					ObservedGeneration: 3,
				},
			},
		},
		{
			name: "Unknown CRD management",
			status: install.Status{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   install.CRDUnknownManagement,
				CRDMessage: "Cannot determine CRD ownership",
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
					Reason:             "UnknownManagement",
					Message:            "Cannot determine CRD ownership",
					ObservedGeneration: 1,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var conditions []metav1.Condition
			mapStatusToConditions(tc.status, tc.generation, &conditions)

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
