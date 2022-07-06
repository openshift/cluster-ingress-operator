package status

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestComputeOperatorProgressingCondition(t *testing.T) {
	type versions struct {
		operator, operand1, operand2 string
	}

	testCases := []struct {
		description            string
		noNamespace            bool
		allIngressesAvailable  error
		someIngressProgressing bool
		reportedVersions       versions
		oldVersions            versions
		curVersions            versions
		expectProgressing      configv1.ConditionStatus
	}{
		{
			description:       "all ingress controllers are available",
			expectProgressing: configv1.ConditionFalse,
		},
		{
			description:            "some ingress controller is progressing",
			someIngressProgressing: true,
			expectProgressing:      configv1.ConditionTrue,
		},
		{
			description:           "all ingress controllers are not available",
			allIngressesAvailable: errors.New("ingress controller test-1 is Available=False"),
			expectProgressing:     configv1.ConditionTrue,
		},
		{
			description:       "versions match",
			reportedVersions:  versions{"v1", "ic-v1", "c-v1"},
			oldVersions:       versions{"v1", "ic-v1", "c-v1"},
			curVersions:       versions{"v1", "ic-v1", "c-v1"},
			expectProgressing: configv1.ConditionFalse,
		},
		{
			description:       "operator upgrade in progress",
			reportedVersions:  versions{"v1", "ic-v1", "c-v1"},
			oldVersions:       versions{"v1", "ic-v1", "c-v1"},
			curVersions:       versions{"v2", "ic-v1", "c-v1"},
			expectProgressing: configv1.ConditionTrue,
		},
		{
			description:       "operand upgrade in progress",
			reportedVersions:  versions{"v1", "ic-v1", "c-v1"},
			oldVersions:       versions{"v1", "ic-v1", "c-v1"},
			curVersions:       versions{"v1", "ic-v2", "c-v2"},
			expectProgressing: configv1.ConditionTrue,
		},
		{
			description:       "operator and operand upgrade in progress",
			reportedVersions:  versions{"v1", "ic-v1", "c-v1"},
			oldVersions:       versions{"v1", "ic-v1", "c-v1"},
			curVersions:       versions{"v2", "ic-v2", "c-v2"},
			expectProgressing: configv1.ConditionTrue,
		},
		{
			description:       "operator upgrade done",
			reportedVersions:  versions{"v2", "ic-v1", "c-v1"},
			oldVersions:       versions{"v1", "ic-v1", "c-v1"},
			curVersions:       versions{"v2", "ic-v1", "c-v1"},
			expectProgressing: configv1.ConditionFalse,
		},
		{
			description:       "operand upgrade done",
			reportedVersions:  versions{"v1", "ic-v2", "c-v2"},
			oldVersions:       versions{"v1", "ic-v1", "c-v1"},
			curVersions:       versions{"v1", "ic-v2", "c-v2"},
			expectProgressing: configv1.ConditionFalse,
		},
		{
			description:       "operator and operand upgrade done",
			reportedVersions:  versions{"v2", "ic-v2", "c-v2"},
			oldVersions:       versions{"v1", "ic-v1", "c-v1"},
			curVersions:       versions{"v2", "ic-v2", "c-v2"},
			expectProgressing: configv1.ConditionFalse,
		},
		{
			description:       "operator upgrade in progress, operand upgrade done",
			reportedVersions:  versions{"v2", "ic-v1", "c-v1"},
			oldVersions:       versions{"v1", "ic-v1", "c-v1"},
			curVersions:       versions{"v2", "ic-v2", "c-v2"},
			expectProgressing: configv1.ConditionTrue,
		},
	}

	for _, tc := range testCases {
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
			t.Fatalf("%q: expected %#v, got %#v", tc.description, expected, actual)
		}
	}
}

func TestOperatorStatusesEqual(t *testing.T) {
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
		if actual := operatorStatusesEqual(tc.a, tc.b); actual != tc.expected {
			t.Fatalf("%q: expected %v, got %v", tc.description, tc.expected, actual)
		}
	}
}

func TestComputeOperatorStatusVersions(t *testing.T) {
	allIngressesAvailable := errors.New("ingress controller test-1 is Available=False")

	type versions struct {
		operator string
		operand1 string
		operand2 string
	}

	testCases := []struct {
		description           string
		oldVersions           versions
		curVersions           versions
		allIngressesAvailable error
		expectedVersions      versions
	}{
		{
			description:      "initialize versions, operator is available",
			oldVersions:      versions{UnknownVersionValue, UnknownVersionValue, UnknownVersionValue},
			curVersions:      versions{"v1", "ic-v1", "c-v1"},
			expectedVersions: versions{"v1", "ic-v1", "c-v1"},
		},
		{
			description:           "initialize versions, operator is not available",
			oldVersions:           versions{UnknownVersionValue, UnknownVersionValue, UnknownVersionValue},
			curVersions:           versions{"v1", "ic-v1", "c-v1"},
			allIngressesAvailable: allIngressesAvailable,
			expectedVersions:      versions{UnknownVersionValue, UnknownVersionValue, UnknownVersionValue},
		},
		{
			description:      "update with no change",
			oldVersions:      versions{"v1", "ic-v1", "c-v1"},
			curVersions:      versions{"v1", "ic-v1", "c-v1"},
			expectedVersions: versions{"v1", "ic-v1", "c-v1"},
		},
		{
			description:           "update operator version, operator is not available",
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v2", "ic-v2", "c-v2"},
			allIngressesAvailable: allIngressesAvailable,
			expectedVersions:      versions{"v1", "ic-v1", "c-v1"},
		},
		{
			description:      "update operator version, operator is available",
			oldVersions:      versions{"v1", "ic-v1", "c-v1"},
			curVersions:      versions{"v2", "ic-v2", "c-v2"},
			expectedVersions: versions{"v2", "ic-v2", "c-v2"},
		},
		{
			description:           "update ingress controller and canary images, operator is not available",
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v1", "ic-v2", "c-v2"},
			allIngressesAvailable: allIngressesAvailable,
			expectedVersions:      versions{"v1", "ic-v1", "c-v1"},
		},
		{
			description:      "update ingress controller and canary images, operator is available",
			oldVersions:      versions{"v1", "ic-v1", "c-v1"},
			curVersions:      versions{"v1", "ic-v2", "c-v2"},
			expectedVersions: versions{"v1", "ic-v2", "c-v2"},
		},
		{
			description:           "update operator, ingress controller, and canary images, operator is not available - no match",
			oldVersions:           versions{"v1", "ic-v1", "c-v1"},
			curVersions:           versions{"v2", "ic-v2", "c-v2"},
			allIngressesAvailable: allIngressesAvailable,
			expectedVersions:      versions{"v1", "ic-v1", "c-v1"},
		},
		{
			description:      "update operator, ingress controller, and canary images, operator is available",
			oldVersions:      versions{"v1", "ic-v1", "c-v1"},
			curVersions:      versions{"v2", "ic-v2", "c-v2"},
			expectedVersions: versions{"v2", "ic-v2", "c-v2"},
		},
	}

	for _, tc := range testCases {
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
			t.Fatalf("%q: expected %v, got %v", tc.description, expectedVersions, versions)
		}
	}
}

func TestComputeOperatorUpgradeableCondition(t *testing.T) {
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
			t.Fatalf("%q: expected %#v, got %#v", tc.description, expected, actual)
		}
	}
}
