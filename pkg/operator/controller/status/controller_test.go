package status

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestComputeOperatorStatusConditions(t *testing.T) {
	type conditions struct {
		degraded, progressing, available bool
	}
	type versions struct {
		operator, operand string
	}

	testCases := []struct {
		description           string
		noNamespace           bool
		allIngressesAvailable bool
		reportedVersions      versions
		oldVersions           versions
		curVersions           versions
		expectedConditions    conditions
	}{
		{
			description:           "no operand namespace",
			noNamespace:           true,
			allIngressesAvailable: true,
			expectedConditions:    conditions{true, false, true},
		},
		{
			description:           "all ingress controllers are available",
			allIngressesAvailable: true,
			expectedConditions:    conditions{false, false, true},
		},
		{
			description:        "all ingress controllers are not available",
			expectedConditions: conditions{false, true, false},
		},
		{
			description:           "versions match",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v1", "ic-v1"},
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v1", "ic-v1"},
			expectedConditions:    conditions{false, false, true},
		},
		{
			description:           "operator upgrade in progress",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v1", "ic-v1"},
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v2", "ic-v1"},
			expectedConditions:    conditions{false, true, true},
		},
		{
			description:           "operand upgrade in progress",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v1", "ic-v1"},
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v1", "ic-v2"},
			expectedConditions:    conditions{false, true, true},
		},
		{
			description:           "operator and operand upgrade in progress",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v1", "ic-v1"},
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v2", "ic-v2"},
			expectedConditions:    conditions{false, true, true},
		},
		{
			description:           "operator upgrade done",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v2", "ic-v1"},
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v2", "ic-v1"},
			expectedConditions:    conditions{false, false, true},
		},
		{
			description:           "operand upgrade done",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v1", "ic-v2"},
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v1", "ic-v2"},
			expectedConditions:    conditions{false, false, true},
		},
		{
			description:           "operator and operand upgrade done",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v2", "ic-v2"},
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v2", "ic-v2"},
			expectedConditions:    conditions{false, false, true},
		},
		{
			description:           "operator upgrade in progress, operand upgrade done",
			allIngressesAvailable: true,
			reportedVersions:      versions{"v2", "ic-v1"},
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v2", "ic-v2"},
			expectedConditions:    conditions{false, true, true},
		},
	}

	for _, tc := range testCases {
		var namespace *corev1.Namespace
		if !tc.noNamespace {
			namespace = &corev1.Namespace{}
		}

		oldVersions := []configv1.OperandVersion{
			{
				Name:    OperatorVersionName,
				Version: tc.oldVersions.operator,
			},
			{
				Name:    IngressControllerVersionName,
				Version: tc.oldVersions.operand,
			},
		}
		reportedVersions := []configv1.OperandVersion{
			{
				Name:    OperatorVersionName,
				Version: tc.reportedVersions.operator,
			},
			{
				Name:    IngressControllerVersionName,
				Version: tc.reportedVersions.operand,
			},
		}
		r := &reconciler{
			Config: Config{
				OperatorReleaseVersion: tc.curVersions.operator,
				IngressControllerImage: tc.curVersions.operand,
			},
		}

		expectedConditions := []configv1.ClusterOperatorStatusCondition{
			{
				Type:   configv1.OperatorDegraded,
				Status: configv1.ConditionFalse,
			},
			{
				Type:   configv1.OperatorProgressing,
				Status: configv1.ConditionFalse,
			},
			{
				Type:   configv1.OperatorAvailable,
				Status: configv1.ConditionFalse,
			},
		}
		if tc.expectedConditions.degraded {
			expectedConditions[0].Status = configv1.ConditionTrue
		}
		if tc.expectedConditions.progressing {
			expectedConditions[1].Status = configv1.ConditionTrue
		}
		if tc.expectedConditions.available {
			expectedConditions[2].Status = configv1.ConditionTrue
		}

		conditions := r.computeOperatorStatusConditions([]configv1.ClusterOperatorStatusCondition{},
			namespace, tc.allIngressesAvailable, oldVersions, reportedVersions)
		conditionsCmpOpts := []cmp.Option{
			cmpopts.IgnoreFields(configv1.ClusterOperatorStatusCondition{}, "LastTransitionTime", "Reason", "Message"),
			cmpopts.EquateEmpty(),
			cmpopts.SortSlices(func(a, b configv1.ClusterOperatorStatusCondition) bool { return a.Type < b.Type }),
		}
		if !cmp.Equal(conditions, expectedConditions, conditionsCmpOpts...) {
			t.Fatalf("%q: expected %#v, got %#v", tc.description, expectedConditions, conditions)
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
	type versions struct {
		operator string
		operand  string
	}

	testCases := []struct {
		description           string
		oldVersions           versions
		curVersions           versions
		allIngressesAvailable bool
		expectedVersions      versions
	}{
		{
			description:           "initialize versions, operator is avaiable",
			oldVersions:           versions{UnknownVersionValue, UnknownVersionValue},
			curVersions:           versions{"v1", "ic-v1"},
			allIngressesAvailable: true,
			expectedVersions:      versions{"v1", "ic-v1"},
		},
		{
			description:      "initialize versions, operator is not available",
			oldVersions:      versions{UnknownVersionValue, UnknownVersionValue},
			curVersions:      versions{"v1", "ic-v1"},
			expectedVersions: versions{UnknownVersionValue, UnknownVersionValue},
		},
		{
			description:           "update with no change",
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v1", "ic-v1"},
			allIngressesAvailable: true,
			expectedVersions:      versions{"v1", "ic-v1"},
		},
		{
			description:      "update operator version, operator is not available",
			oldVersions:      versions{"v1", "ic-v1"},
			curVersions:      versions{"v2", "ic-v1"},
			expectedVersions: versions{"v1", "ic-v1"},
		},
		{
			description:           "update operator version, operator is available",
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v2", "ic-v1"},
			allIngressesAvailable: true,
			expectedVersions:      versions{"v2", "ic-v1"},
		},
		{
			description:      "update ingress controller image, operator is not available",
			oldVersions:      versions{"v1", "ic-v1"},
			curVersions:      versions{"v1", "ic-v2"},
			expectedVersions: versions{"v1", "ic-v1"},
		},
		{
			description:           "update ingress controller image, operator is available",
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v1", "ic-v2"},
			allIngressesAvailable: true,
			expectedVersions:      versions{"v1", "ic-v2"},
		},
		{
			description:      "update operator and ingress controller image, operator is not available",
			oldVersions:      versions{"v1", "ic-v1"},
			curVersions:      versions{"v2", "ic-v2"},
			expectedVersions: versions{"v1", "ic-v1"},
		},
		{
			description:           "update operator and ingress controller image, operator is available",
			oldVersions:           versions{"v1", "ic-v1"},
			curVersions:           versions{"v2", "ic-v2"},
			allIngressesAvailable: true,
			expectedVersions:      versions{"v2", "ic-v2"},
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
				Version: tc.oldVersions.operand,
			},
		}
		expectedVersions = []configv1.OperandVersion{
			{
				Name:    OperatorVersionName,
				Version: tc.expectedVersions.operator,
			},
			{
				Name:    IngressControllerVersionName,
				Version: tc.expectedVersions.operand,
			},
		}

		r := &reconciler{
			Config: Config{
				OperatorReleaseVersion: tc.curVersions.operator,
				IngressControllerImage: tc.curVersions.operand,
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
