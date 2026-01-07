package status

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/ingress"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_computeOperatorProgressingCondition(t *testing.T) {
	type versions struct {
		operator, operand1, operand2 string
	}

	testCases := []struct {
		description            string
		noNamespace            bool
		allIngressesAvailable  bool
		someIngressProgressing bool
		reportedVersions       versions
		oldVersions            versions
		curVersions            versions
		expectProgressing      configv1.ConditionStatus
	}{
		{
			description:           "all ingress controllers are available",
			allIngressesAvailable: true,
			expectProgressing:     configv1.ConditionFalse,
		},
		{
			description:            "some ingress controller is progressing",
			allIngressesAvailable:  true,
			someIngressProgressing: true,
			expectProgressing:      configv1.ConditionTrue,
		},
		{
			description:       "all ingress controllers are not available",
			expectProgressing: configv1.ConditionTrue,
		},
		{
			description:           "versions match",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v1", "ic-v1", "c-v1"},
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v1", "ic-v1", "c-v1"},
			expectProgressing:     configv1.ConditionFalse,
		},
		{
			description:           "operator upgrade in progress",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v1", "ic-v1", "c-v1"},
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v2", "ic-v1", "c-v1"},
			expectProgressing:     configv1.ConditionTrue,
		},
		{
			description:           "operand upgrade in progress",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v1", "ic-v1", "c-v1"},
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v1", "ic-v2", "c-v2"},
			expectProgressing:     configv1.ConditionTrue,
		},
		{
			description:           "operator and operand upgrade in progress",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v1", "ic-v1", "c-v1"},
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v2", "ic-v2", "c-v2"},
			expectProgressing:     configv1.ConditionTrue,
		},
		{
			description:           "operator upgrade done",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v2", "ic-v1", "c-v1"},
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v2", "ic-v1", "c-v1"},
			expectProgressing:     configv1.ConditionFalse,
		},
		{
			description:           "operand upgrade done",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v1", "ic-v2", "c-v2"},
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v1", "ic-v2", "c-v2"},
			expectProgressing:     configv1.ConditionFalse,
		},
		{
			description:           "operator and operand upgrade done",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v2", "ic-v2", "c-v2"},
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v2", "ic-v2", "c-v2"},
			expectProgressing:     configv1.ConditionFalse,
		},
		{
			description:           "operator upgrade in progress, operand upgrade done",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v2", "ic-v1", "c-v1"},
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v2", "ic-v2", "c-v2"},
			expectProgressing:     configv1.ConditionTrue,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			oldVersions := []configv1.OperandVersion{
				{
					Name:    OperatorVersionName,
					Version: tc.oldVersions.operator,
				},
				{
					Name:    IngressControllerVersionName,
					Version: tc.oldVersions.operand1,
				},
				{
					Name:    CanaryImageVersionName,
					Version: tc.oldVersions.operand2,
				},
			}
			reportedVersions := []configv1.OperandVersion{
				{
					Name:    OperatorVersionName,
					Version: tc.reportedVersions.operator,
				},
				{
					Name:    IngressControllerVersionName,
					Version: tc.reportedVersions.operand1,
				},
				{
					Name:    CanaryImageVersionName,
					Version: tc.reportedVersions.operand2,
				},
			}

			expected := configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorProgressing,
				Status: tc.expectProgressing,
			}

			var ingresscontrollers []operatorv1.IngressController
			ic := operatorv1.IngressController{
				Status: operatorv1.IngressControllerStatus{
					Conditions: []operatorv1.OperatorCondition{{
						Type:   operatorv1.OperatorStatusTypeProgressing,
						Status: operatorv1.ConditionFalse,
					}},
				},
			}
			ingresscontrollers = append(ingresscontrollers, ic)
			if tc.someIngressProgressing {
				ingresscontrollers[0].Status.Conditions[0].Status = operatorv1.ConditionTrue
			}

			actual := computeOperatorProgressingCondition(ingresscontrollers, tc.allIngressesAvailable, oldVersions, reportedVersions, tc.curVersions.operator, tc.curVersions.operand1, tc.curVersions.operand2)
			conditionsCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(configv1.ClusterOperatorStatusCondition{}, "LastTransitionTime", "Reason", "Message"),
			}
			if !cmp.Equal(actual, expected, conditionsCmpOpts...) {
				t.Fatalf("expected %#v, got %#v", expected, actual)
			}
		})
	}
}

func Test_operatorStatusesEqual(t *testing.T) {
	testCases := []struct {
		description string
		expected    bool
		a, b        configv1.ClusterOperatorStatus
	}{
		{
			description: "zero-valued ClusterOperatorStatus should be equal",
			expected:    true,
		},
		{
			description: "nil and non-nil slices are equal",
			expected:    true,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{},
			},
		},
		{
			description: "empty slices should be equal",
			expected:    true,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{},
			},
			b: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{},
			},
		},
		{
			description: "check no change in versions",
			expected:    true,
			a: configv1.ClusterOperatorStatus{
				Versions: []configv1.OperandVersion{
					{
						Name:    "operator",
						Version: "v1",
					},
					{
						Name:    "router",
						Version: "v2",
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				Versions: []configv1.OperandVersion{
					{
						Name:    "operator",
						Version: "v1",
					},
					{
						Name:    "router",
						Version: "v2",
					},
				},
			},
		},
		{
			description: "condition LastTransitionTime should not be ignored",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:               configv1.OperatorAvailable,
						Status:             configv1.ConditionTrue,
						LastTransitionTime: metav1.Unix(0, 0),
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:               configv1.OperatorAvailable,
						Status:             configv1.ConditionTrue,
						LastTransitionTime: metav1.Unix(1, 0),
					},
				},
			},
		},
		{
			description: "order of versions should not matter",
			expected:    true,
			a: configv1.ClusterOperatorStatus{
				Versions: []configv1.OperandVersion{
					{
						Name:    "operator",
						Version: "v1",
					},
					{
						Name:    "router",
						Version: "v2",
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				Versions: []configv1.OperandVersion{
					{
						Name:    "router",
						Version: "v2",
					},
					{
						Name:    "operator",
						Version: "v1",
					},
				},
			},
		},
		{
			description: "check missing related objects",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				RelatedObjects: []configv1.ObjectReference{
					{
						Name: "openshift-ingress",
					},
					{
						Name: "default",
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				RelatedObjects: []configv1.ObjectReference{
					{
						Name: "default",
					},
				},
			},
		},
		{
			description: "check extra related objects",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				RelatedObjects: []configv1.ObjectReference{
					{
						Name: "default",
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				RelatedObjects: []configv1.ObjectReference{
					{
						Name: "openshift-ingress",
					},
					{
						Name: "default",
					},
				},
			},
		},
		{
			description: "check condition reason differs",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:   configv1.OperatorAvailable,
						Status: configv1.ConditionFalse,
						Reason: "foo",
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:   configv1.OperatorAvailable,
						Status: configv1.ConditionFalse,
						Reason: "bar",
					},
				},
			},
		},
		{
			description: "check duplicate with single condition",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:    configv1.OperatorAvailable,
						Message: "foo",
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type:    configv1.OperatorAvailable,
						Message: "foo",
					},
					{
						Type:    configv1.OperatorAvailable,
						Message: "foo",
					},
				},
			},
		},
		{
			description: "check duplicate with multiple conditions",
			expected:    false,
			a: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type: configv1.OperatorAvailable,
					},
					{
						Type: configv1.OperatorProgressing,
					},
					{
						Type: configv1.OperatorAvailable,
					},
				},
			},
			b: configv1.ClusterOperatorStatus{
				Conditions: []configv1.ClusterOperatorStatusCondition{
					{
						Type: configv1.OperatorProgressing,
					},
					{
						Type: configv1.OperatorAvailable,
					},
					{
						Type: configv1.OperatorProgressing,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			if actual := operatorStatusesEqual(tc.a, tc.b); actual != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, actual)
			}
		})
	}
}

func Test_computeOperatorStatusVersions(t *testing.T) {
	type versions struct {
		operator string
		operand1 string
		operand2 string
	}

	testCases := []struct {
		description           string
		oldVersions           versions
		curVersions           versions
		allIngressesAvailable bool
		expectedVersions      versions
	}{
		{
			description:           "initialize versions, operator is available",
			oldVersions:           versions{UnknownVersionValue, UnknownVersionValue, UnknownVersionValue},
			curVersions:           versions{"v1", "ic-v1", "c-v1"},
			allIngressesAvailable: true,
			expectedVersions:      versions{"v1", "ic-v1", "c-v1"},
		},
		{
			description:      "initialize versions, operator is not available",
			oldVersions:      versions{UnknownVersionValue, UnknownVersionValue, UnknownVersionValue},
			curVersions:      versions{"v1", "ic-v1", "c-v1"},
			expectedVersions: versions{UnknownVersionValue, UnknownVersionValue, UnknownVersionValue},
		},
		{
			description:           "update with no change",
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v1", "ic-v1", "c-v1"},
			allIngressesAvailable: true,
			expectedVersions:      versions{"v1", "ic-v1", "c-v1"},
		},
		{
			description:      "update operator version, operator is not available",
			oldVersions:      versions{"v1", "ic-v1", "c-v1"},
			curVersions:      versions{"v2", "ic-v2", "c-v2"},
			expectedVersions: versions{"v1", "ic-v1", "c-v1"},
		},
		{
			description:           "update operator version, operator is available",
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v2", "ic-v2", "c-v2"},
			allIngressesAvailable: true,
			expectedVersions:      versions{"v2", "ic-v2", "c-v2"},
		},
		{
			description:      "update ingress controller and canary images, operator is not available",
			oldVersions:      versions{"v1", "ic-v1", "c-v1"},
			curVersions:      versions{"v1", "ic-v2", "c-v2"},
			expectedVersions: versions{"v1", "ic-v1", "c-v1"},
		},
		{
			description:           "update ingress controller and canary images, operator is available",
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v1", "ic-v2", "c-v2"},
			allIngressesAvailable: true,
			expectedVersions:      versions{"v1", "ic-v2", "c-v2"},
		},
		{
			description:      "update operator, ingress controller, and canary images, operator is not available - no match",
			oldVersions:      versions{"v1", "ic-v1", "c-v1"},
			curVersions:      versions{"v2", "ic-v2", "c-v2"},
			expectedVersions: versions{"v1", "ic-v1", "c-v1"},
		},
		{
			description:           "update operator, ingress controller, and canary images, operator is available",
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v2", "ic-v2", "c-v2"},
			allIngressesAvailable: true,
			expectedVersions:      versions{"v2", "ic-v2", "c-v2"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			var (
				oldVersions      []configv1.OperandVersion
				expectedVersions []configv1.OperandVersion
			)

			oldVersions = []configv1.OperandVersion{
				{
					Name:    OperatorVersionName,
					Version: tc.oldVersions.operator,
				},
				{
					Name:    IngressControllerVersionName,
					Version: tc.oldVersions.operand1,
				},
				{
					Name:    CanaryImageVersionName,
					Version: tc.oldVersions.operand2,
				},
			}
			expectedVersions = []configv1.OperandVersion{
				{
					Name:    OperatorVersionName,
					Version: tc.expectedVersions.operator,
				},
				{
					Name:    IngressControllerVersionName,
					Version: tc.expectedVersions.operand1,
				},
				{
					Name:    CanaryImageVersionName,
					Version: tc.expectedVersions.operand2,
				},
			}

			r := &reconciler{
				config: Config{
					OperatorReleaseVersion: tc.curVersions.operator,
					IngressControllerImage: tc.curVersions.operand1,
					CanaryImage:            tc.curVersions.operand2,
				},
			}
			versions := r.computeOperatorStatusVersions(oldVersions, tc.allIngressesAvailable)
			versionsCmpOpts := []cmp.Option{
				cmpopts.EquateEmpty(),
				cmpopts.SortSlices(func(a, b configv1.OperandVersion) bool { return a.Name < b.Name }),
			}
			if !cmp.Equal(versions, expectedVersions, versionsCmpOpts...) {
				t.Fatalf("expected %v, got %v", expectedVersions, versions)
			}
		})
	}
}

func Test_computeOperatorUpgradeableCondition(t *testing.T) {
	testCases := []struct {
		description                   string
		ingresscontrollersUpgradeable []bool
		expectUpgradeable             bool
	}{
		{
			description:                   "no ingresscontrollers exist",
			ingresscontrollersUpgradeable: []bool{},
			expectUpgradeable:             true,
		},
		{
			description:                   "no ingresscontrollers are upgradeable",
			ingresscontrollersUpgradeable: []bool{false, false},
			expectUpgradeable:             false,
		},
		{
			description:                   "some ingresscontrollers are upgradeable",
			ingresscontrollersUpgradeable: []bool{false, true},
			expectUpgradeable:             false,
		},
		{
			description:                   "all ingresscontrollers are upgradeable",
			ingresscontrollersUpgradeable: []bool{true, true},
			expectUpgradeable:             true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ingresscontrollers := []operatorv1.IngressController{}
			for _, upgradeable := range tc.ingresscontrollersUpgradeable {
				upgradeableStatus := operatorv1.ConditionFalse
				if upgradeable {
					upgradeableStatus = operatorv1.ConditionTrue
				}
				ic := operatorv1.IngressController{
					Status: operatorv1.IngressControllerStatus{
						Conditions: []operatorv1.OperatorCondition{{
							Type:   "Upgradeable",
							Status: upgradeableStatus,
						}},
					},
				}
				ingresscontrollers = append(ingresscontrollers, ic)
			}

			expected := configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorUpgradeable,
				Status: configv1.ConditionFalse,
			}
			if tc.expectUpgradeable {
				expected.Status = configv1.ConditionTrue
			}

			actual := computeOperatorUpgradeableCondition(ingresscontrollers)
			conditionsCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(configv1.ClusterOperatorStatusCondition{}, "LastTransitionTime", "Reason", "Message"),
			}
			if !cmp.Equal(actual, expected, conditionsCmpOpts...) {
				t.Fatalf("expected %#v, got %#v", expected, actual)
			}
		})
	}
}

func Test_computeOperatorEvaluationConditionsDetectedCondition(t *testing.T) {
	testCases := []struct {
		description                                string
		ingresscontrollersHaveEvaluationConditions []bool
		expectEvaluationConditionsDetected         bool
	}{
		{
			description: "no ingresscontrollers exist",
			ingresscontrollersHaveEvaluationConditions: []bool{},
			expectEvaluationConditionsDetected:         false,
		},
		{
			description: "all ingresscontrollers have evaluation conditions",
			ingresscontrollersHaveEvaluationConditions: []bool{true, true},
			expectEvaluationConditionsDetected:         true,
		},
		{
			description: "some ingresscontrollers have evaluation conditions",
			ingresscontrollersHaveEvaluationConditions: []bool{false, true},
			expectEvaluationConditionsDetected:         true,
		},
		{
			description: "no ingresscontrollers have evaluation conditions",
			ingresscontrollersHaveEvaluationConditions: []bool{false, false},
			expectEvaluationConditionsDetected:         false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ingresscontrollers := []operatorv1.IngressController{}
			for _, has := range tc.ingresscontrollersHaveEvaluationConditions {
				evaluationConditionsDetectedStatus := operatorv1.ConditionFalse
				if has {
					evaluationConditionsDetectedStatus = operatorv1.ConditionTrue
				}
				ic := operatorv1.IngressController{
					Status: operatorv1.IngressControllerStatus{
						Conditions: []operatorv1.OperatorCondition{{
							Type:   ingress.IngressControllerEvaluationConditionsDetectedConditionType,
							Status: evaluationConditionsDetectedStatus,
						}},
					},
				}
				ingresscontrollers = append(ingresscontrollers, ic)
			}

			expected := configv1.ClusterOperatorStatusCondition{
				Type:   ingress.IngressControllerEvaluationConditionsDetectedConditionType,
				Status: configv1.ConditionFalse,
			}
			if tc.expectEvaluationConditionsDetected {
				expected.Status = configv1.ConditionTrue
			}

			actual := computeOperatorEvaluationConditionsDetectedCondition(ingresscontrollers)
			conditionsCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(configv1.ClusterOperatorStatusCondition{}, "LastTransitionTime", "Reason", "Message"),
			}
			if !cmp.Equal(actual, expected, conditionsCmpOpts...) {
				t.Fatalf("expected %#v, got %#v", expected, actual)
			}
		})
	}
}

func Test_computeOperatorDegradedCondition(t *testing.T) {
	ic := func(name string) operatorv1.IngressController {
		return operatorv1.IngressController{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Status: operatorv1.IngressControllerStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:    "Degraded",
						Status:  operatorv1.ConditionUnknown,
						Reason:  "dummy",
						Message: "dummy",
					},
				},
			},
		}
	}
	icWithStatus := func(name string, condStatus bool) operatorv1.IngressController {
		ic := ic(name)
		ic.Status.Conditions[0].Status = operatorv1.ConditionFalse
		if condStatus {
			ic.Status.Conditions[0].Status = operatorv1.ConditionTrue
		}
		return ic
	}

	testCases := []struct {
		description     string
		state           operatorState
		expectCondition configv1.ClusterOperatorStatusCondition
	}{
		{
			description: "no ingresscontrollers exist",
			state:       operatorState{},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionTrue,
				Reason:  "IngressDoesNotExist",
				Message: `The "default" ingress controller does not exist.`,
			},
		},
		{
			description: "no default ingresscontroller",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					icWithStatus("test1", false),
					icWithStatus("test2", false),
				},
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionTrue,
				Reason:  "IngressDoesNotExist",
				Message: `The "default" ingress controller does not exist.`,
			},
		},
		{
			description: "default ingresscontroller degraded",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					icWithStatus("default", true),
					icWithStatus("test", false),
				},
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionTrue,
				Reason:  "IngressDegraded",
				Message: `The "default" ingress controller reports Degraded=True: dummy: dummy.`,
			},
		},
		{
			description: "default ingresscontroller not degraded",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					icWithStatus("default", false),
					icWithStatus("test2", true),
				},
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionFalse,
				Reason:  "IngressNotDegraded",
				Message: `The "default" ingress controller reports Degraded=False.`,
			},
		},
		{
			description: "default ingresscontroller status unknown",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					ic("default"),
				},
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionUnknown,
				Reason:  "IngressDegradedStatusUnknown",
				Message: `The "default" ingress controller reports Degraded=Unknown.`,
			},
		},
		{
			description: "default ingresscontroller not degraded but unmanaged gateway api crds exist",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					icWithStatus("default", false),
				},
				unmanagedGatewayAPICRDNames: "notvalid.gateway.networking.k8s.io",
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionTrue,
				Reason:  "GatewayAPICRDsDegraded",
				Message: "Unmanaged Gateway API CRDs found: notvalid.gateway.networking.k8s.io.",
			},
		},
		{
			description: "default ingresscontroller degraded and unmanaged gateway api crds exist",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					icWithStatus("default", true),
				},
				unmanagedGatewayAPICRDNames: "notvalid.gateway.networking.k8s.io",
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionTrue,
				Reason:  "IngressDegradedAndGatewayAPICRDsDegraded",
				Message: `The "default" ingress controller reports Degraded=True: dummy: dummy.` + "\n" + `Unmanaged Gateway API CRDs found: notvalid.gateway.networking.k8s.io.`,
			},
		},
		{
			description: "Gateway API Enabled",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					icWithStatus("default", false),
				},
				ossmSubscriptions: []operatorsv1alpha1.Subscription{
					sub("servicemeshoperator3", "servicemeshoperator3", "servicemeshoperator3.v3.1.0", true),
				},
				shouldInstallOSSM:                 true,
				expectedGatewayAPIOperatorVersion: "servicemeshoperator3.v3.1.0",
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionFalse,
				Reason:  "IngressNotDegraded",
				Message: "The \"default\" ingress controller reports Degraded=False.",
			},
		},
		{
			description: "Gateway API Enabled, Sail Operator present",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					icWithStatus("default", false),
				},
				ossmSubscriptions: []operatorsv1alpha1.Subscription{
					sub("servicemeshoperator3", "servicemeshoperator3", "servicemeshoperator3.v3.1.0", true),
					sub("sailoperator", "sailoperator", "sailoperator.v1.0.0", false),
				},
				shouldInstallOSSM:                 true,
				expectedGatewayAPIOperatorVersion: "servicemeshoperator3.v3.1.0",
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionTrue,
				Reason:  "GatewayAPIInstallConflict",
				Message: "Package sailoperator from subscription foo/sailoperator prevents enabling operator-managed Gateway API. Uninstall foo/sailoperator to enable functionality.",
			},
		},
		{
			description: "Gateway API Enabled, Sail Operator present, older OSSM3 present",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					icWithStatus("default", false),
				},
				ossmSubscriptions: []operatorsv1alpha1.Subscription{
					sub("servicemeshoperator3", "servicemeshoperator3", "", true),
					sub("servicemeshoperator3", "servicemeshoperator3", "servicemeshoperator3.v3.0.0", false),
					sub("sailoperator", "sailoperator", "", false),
				},
				shouldInstallOSSM:                 true,
				expectedGatewayAPIOperatorVersion: "servicemeshoperator3.v3.1.0",
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionTrue,
				Reason:  "GatewayAPIInstallConflict",
				Message: "Installed version servicemeshoperator3.v3.0.0 does not support operator-managed Gateway API. Install version servicemeshoperator3.v3.1.0 or uninstall foo/servicemeshoperator3 to enable functionality.\nPackage sailoperator from subscription foo/sailoperator prevents enabling operator-managed Gateway API. Uninstall foo/sailoperator to enable functionality.",
			},
		},
		{
			description: "Gateway API Enabled, newer OSSM3 present",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					icWithStatus("default", false),
				},
				ossmSubscriptions: []operatorsv1alpha1.Subscription{
					sub("servicemeshoperator3", "servicemeshoperator3", "servicemeshoperator3.v3.5.0", false),
				},
				shouldInstallOSSM:                 true,
				expectedGatewayAPIOperatorVersion: "servicemeshoperator3.v3.1.0",
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionFalse,
				Reason:  "IngressNotDegradedAndGatewayAPIInstallWarnings",
				Message: "The \"default\" ingress controller reports Degraded=False.\nFound version servicemeshoperator3.v3.5.0, but operator-managed Gateway API expects version servicemeshoperator3.v3.1.0. Operator-managed Gateway API may not work as intended.",
			},
		},
		{
			description: "Gateway API Enabled, OSSM3 present with unexpected version number",
			state: operatorState{
				IngressControllers: []operatorv1.IngressController{
					icWithStatus("default", false),
				},
				ossmSubscriptions: []operatorsv1alpha1.Subscription{
					sub("servicemeshoperator3", "servicemeshoperator3", "servicemeshoperator3.v3.5-beta", false),
				},
				shouldInstallOSSM:                 true,
				expectedGatewayAPIOperatorVersion: "servicemeshoperator3.v3.1.0",
			},
			expectCondition: configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionFalse,
				Reason:  "IngressNotDegradedAndGatewayAPIInstallWarnings",
				Message: "The \"default\" ingress controller reports Degraded=False.\nfailed to compare installed OSSM version to expected: \"servicemeshoperator3.v3.5-beta\" does not match expected format",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			actual := computeOperatorDegradedCondition(tc.state)
			if diff := cmp.Diff(actual, tc.expectCondition); diff != "" {
				t.Fatalf("actual condition differs from expected: %v", diff)
			}
		})
	}
}

func sub(name, pack, version string, cioOwned bool) operatorsv1alpha1.Subscription {
	subscription := operatorsv1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "foo",
		},
		Spec: &operatorsv1alpha1.SubscriptionSpec{
			Package: pack,
		},
		Status: operatorsv1alpha1.SubscriptionStatus{
			InstalledCSV: version,
		},
	}
	if cioOwned {
		subscription.Annotations = map[string]string{
			operatorcontroller.IngressOperatorOwnedAnnotation: "",
		}
	}
	return subscription
}

func Test_compareVersionNums(t *testing.T) {
	testCases := []struct {
		a              string
		b              string
		expectedErr    error
		expectedResult int
	}{
		{
			// Same version
			a:              "foo.v1.2.3",
			b:              "foo.v1.2.3",
			expectedErr:    nil,
			expectedResult: 0,
		},
		{
			// Differs in X
			a:              "foo.v1.2.3",
			b:              "foo.v2.0.0",
			expectedErr:    nil,
			expectedResult: 1,
		},
		{
			// Differs in Y
			a:              "foo.v1.5.3",
			b:              "foo.v1.2.3",
			expectedErr:    nil,
			expectedResult: -3,
		},
		{
			// Differs in Z; Includes number in package name that shouldn't be considered part of the version number
			a:              "foo7.v1.5.3",
			b:              "foo7.v1.5.9",
			expectedErr:    nil,
			expectedResult: 6,
		},
		{
			// Differs in package name; expects error
			a:              "foo.v1.5.3",
			b:              "foobar.v1.2.3",
			expectedErr:    fmt.Errorf(`"foo.v1.5.3" and "foobar.v1.2.3" are different packages. cannot compare version numbers`),
			expectedResult: 0,
		},
		{
			// 'b' contains too many version numbers
			a:              "foo.v1.5.3",
			b:              "foobar.v1.2.3.7",
			expectedErr:    fmt.Errorf(`"foobar.v1.2.3.7" does not match expected format`),
			expectedResult: 0,
		},
		{
			// 'a' contains too few version numbers
			a:              "foo.v1.5",
			b:              "foobar.v1.2.3",
			expectedErr:    fmt.Errorf(`"foo.v1.5" does not match expected format`),
			expectedResult: 0,
		},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%q<>%q", tc.a, tc.b), func(t *testing.T) {
			result, err := compareVersionNums(tc.a, tc.b)
			if tc.expectedErr != nil {
				if diff := cmp.Diff(err.Error(), tc.expectedErr.Error()); diff != "" {
					t.Fatalf("actual error differs from expected: %v", diff)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result != tc.expectedResult {
					t.Fatalf("actual result %d differs from expected result %d", result, tc.expectedResult)
				}
			}
		})
	}
}
