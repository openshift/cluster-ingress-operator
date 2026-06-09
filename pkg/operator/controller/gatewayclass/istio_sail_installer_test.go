package gatewayclass

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"

	testutil "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/test/util"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

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
	internalOpts    SailOptions
	status          SailStatus
	uninstallCalled bool
	uninstallError  error
}

var _ SailLibraryInstaller = &fakeSailInstaller{}

func (i *fakeSailInstaller) Start(ctx context.Context) (<-chan struct{}, error) {
	i.notifyCh = make(chan struct{})
	return i.notifyCh, nil
}

func (i *fakeSailInstaller) Apply(opts SailOptions) error {
	i.internalOpts = opts

	i.status.Installed = true
	i.status.Version = opts.Version
	i.status.CRDState = SailCRDStateUnknown
	return nil
}

func (i *fakeSailInstaller) Uninstall(ctx context.Context, namespace, revision string) error {
	i.uninstallCalled = true
	return i.uninstallError
}

func (i *fakeSailInstaller) Status() SailStatus {
	return i.status
}

func (i *fakeSailInstaller) Enqueue() {}

func Test_overwriteOLMCRDCheck(t *testing.T) {
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
		crdName         string
		crdLabels       map[string]string
		existingObjects []client.Object
		expectedResult  bool
	}{
		{
			name:           "CRD with no labels can be overwritten",
			crdName:        "test-crd",
			crdLabels:      nil,
			expectedResult: true,
		},
		{
			name:    "CRD without olm.managed label can be overwritten",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"some-other-label": "value",
			},
			expectedResult: true,
		},
		{
			name:    "CRD with olm.managed=false can be overwritten",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"olm.managed": "false",
			},
			expectedResult: true,
		},
		{
			name:    "CRD with olm.managed but no subscription label can be overwritten",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"olm.managed": "true",
			},
			expectedResult: true,
		},
		{
			name:    "CRD with olm.managed and invalid subscription label can be overwritten",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"olm.managed":                        "true",
				"operators.coreos.com/invalid":       "",
				"operators.coreos.com/too.many.dots": "",
			},
			expectedResult: true,
		},
		{
			name:    "CRD with active InstallPlans cannot be overwritten",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/servicemeshoperator.openshift-operators": "",
			},
			existingObjects: []client.Object{
				installPlan("operators.coreos.com/servicemeshoperator.openshift-operators"),
			},
			expectedResult: false,
		},
		{
			name:    "CRD with subscription but no InstallPlans cannot be overwritten",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/servicemeshoperator.openshift-operators": "",
			},
			existingObjects: []client.Object{
				subscription("servicemeshoperator", "openshift-operators", "operators.coreos.com/servicemeshoperator.openshift-operators"),
			},
			expectedResult: false,
		},
		{
			name:    "CRD without subscription can be overwritten",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/servicemeshoperator.openshift-operators": "",
			},
			existingObjects: []client.Object{},
			expectedResult:  true,
		},
		{
			name:    "CRD with subscription name containing dots - parses correctly",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/my.service.operator.openshift-operators": "",
			},
			existingObjects: []client.Object{
				subscription("my.service.operator", "openshift-operators", "operators.coreos.com/my.service.operator.openshift-operators"),
			},
			expectedResult: false,
		},
		{
			name:    "CRD with subscription name containing dots - subscription deleted",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/my.service.operator.openshift-operators": "",
			},
			existingObjects: []client.Object{},
			expectedResult:  true,
		},
		{
			name:    "CRD with multiple subscription labels - one active subscription exists",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/operator-one.openshift-operators": "",
				"operators.coreos.com/operator-two.openshift-operators": "",
			},
			existingObjects: []client.Object{
				subscription("operator-two", "openshift-operators", "operators.coreos.com/operator-two.openshift-operators"),
			},
			expectedResult: false,
		},
		{
			name:    "CRD with multiple subscription labels - all subscriptions deleted",
			crdName: "test-crd",
			crdLabels: map[string]string{
				"olm.managed": "true",
				"operators.coreos.com/operator-one.openshift-operators": "",
				"operators.coreos.com/operator-two.openshift-operators": "",
			},
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

			result := reconciler.overwriteOLMCRDCheck(context.Background(), tc.crdName, tc.crdLabels)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}

func Test_mapStatusToConditions(t *testing.T) {
	tests := []struct {
		name               string
		status             SailStatus
		generation         int64
		initialConditions  []metav1.Condition
		expectedConditions []metav1.Condition
		expectedChanged    bool
	}{
		{
			name: "Installed with CRDs ready",
			status: SailStatus{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   SailCRDStateReady,
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
					Message:            "all CRDs are ready",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Installed with CRDs ready and message",
			status: SailStatus{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   SailCRDStateReady,
				CRDMessage: "CRDs managed by cluster-ingress-operator",
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
					Message:            "CRDs managed by cluster-ingress-operator",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Installed with CRDs unknown",
			status: SailStatus{
				Installed: true,
				Version:   "v1.24.4",
				CRDState:  SailCRDStateUnknown,
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
					Reason:             "Unknown",
					Message:            "CRD management state is unknown",
					ObservedGeneration: 2,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Installed with CRDs not ready",
			status: SailStatus{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   SailCRDStateNotReady,
				CRDMessage: "not all CRDs are ready",
				CRDs: []SailCRDInfo{
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
					Message:            "not all CRDs are ready\n- gateways.gateway.networking.k8s.io: ready (not managed)\n- httproutes.gateway.networking.k8s.io: not ready",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Install failed",
			status: SailStatus{
				Installed: false,
				Error:     fmt.Errorf("failed to apply helm chart"),
				CRDState:  SailCRDStateError,
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
			status: SailStatus{
				Installed: false,
				CRDState:  SailCRDStateUnknown,
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
					Reason:             "Unknown",
					Message:            "CRD management state is unknown",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "Installed with warning",
			status: SailStatus{
				Installed:  true,
				Version:    "v1.24.4",
				Error:      fmt.Errorf("drift detected in deployment"),
				CRDState:   SailCRDStateReady,
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
					Reason:             "Ready",
					Message:            "CRDs managed",
					ObservedGeneration: 3,
				},
			},
			expectedChanged: true,
		},
		{
			name: "CRD error state",
			status: SailStatus{
				Installed:  true,
				Version:    "v1.24.4",
				CRDState:   SailCRDStateError,
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
					Reason:             "Error",
					Message:            "Cannot determine CRD ownership",
					ObservedGeneration: 1,
				},
			},
			expectedChanged: true,
		},
		{
			name: "No change when conditions already set",
			status: SailStatus{
				Installed: true,
				Version:   "v1.24.4",
				CRDState:  SailCRDStateReady,
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
					Message:            "all CRDs are ready",
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
					Message:            "all CRDs are ready",
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

// TestOLMAndSailLibraryValuesMatch was removed: with the gRPC sidecar,
// the sail library path builds values as map[string]any, so a typed
// comparison against the OLM path is no longer meaningful.
