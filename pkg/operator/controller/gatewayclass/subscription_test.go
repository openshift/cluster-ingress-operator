package gatewayclass

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func Test_ensureServiceMeshOperatorInstallPlan(t *testing.T) {
	tests := []struct {
		name            string
		channel         string
		version         string
		existingObjects []runtime.Object
		expectCreate    []client.Object
		expectUpdate    []client.Object
		expectDelete    []client.Object
	}{
		{
			// No InstallPlan found; no changes expected.
			name:            "No InstallPlan",
			channel:         "stable",
			version:         "servicemeshoperator.v1.0.0",
			existingObjects: []runtime.Object{},
			expectCreate:    []client.Object{},
			expectUpdate:    []client.Object{},
			expectDelete:    []client.Object{},
		},
		{
			// InstallPlan exists but is not yet approved but phase is incorrect. No changes expected.
			name:    "InstallPlan not approved with incorrect phase",
			channel: "stable",
			version: "servicemeshoperator.v1.0.0",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:      operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:       "foobar",
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.0",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			// InstallPlan exists but is already approved. No changes expected.
			name:    "InstallPlan already approved",
			channel: "stable",
			version: "servicemeshoperator.v1.0.0",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:      operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:       "foobar",
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.0",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			// InstallPlan exists and is not yet approved. Expect InstallPlan.Spec.Approved = true
			name:    "InstallPlan not yet approved",
			channel: "stable",
			version: "servicemeshoperator.v1.0.0",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:      operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:       "foobar",
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.0",
					},
					Status: operatorsv1alpha1.SubscriptionStatus{
						InstalledCSV: "servicemeshoperator.v1.0.0",
						CurrentCSV:   "servicemeshoperator.v1.0.0",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectDelete: []client.Object{},
		},
		{
			// Multiple InstallPlans exist, and the desired InstallPlan is not approved. Expect InstallPlan.Spec.Approved = true only for the desired InstallPlan
			name:    "Multiple InstallPlans, none approved",
			channel: "stable",
			version: "servicemeshoperator.v1.0.0",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:      operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:       "foobar",
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.0",
					},
					Status: operatorsv1alpha1.SubscriptionStatus{
						InstalledCSV: "servicemeshoperator.v1.0.0",
						CurrentCSV:   "servicemeshoperator.v1.0.0",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-bar",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.1.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectDelete: []client.Object{},
		},
		{
			// An InstallPlan with the correct version exists, but it's owned by a different subscription. Expect no change.
			name:    "InstallPlan is for different subscription",
			channel: "stable",
			version: "servicemeshoperator.v1.0.0",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:      operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:       "foobar",
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.0",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "bazquux",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			// InstallPlan exists but is the incorrect version. No changes expected.
			name:    "InstallPlan is incorrect version",
			channel: "stable",
			version: "servicemeshoperator.v1.0.0",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:      operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:       "foobar",
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.0",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.1.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{},
			expectDelete: []client.Object{},
		},
		{
			// Multiple InstallPlans exist, all with the correct cluster service version and owner reference. Expect
			// InstallPlan.Spec.Approved = true for only the most recent InstallPlan (by creation timestamp).
			name:    "Multiple valid InstallPlans, should approve latest",
			channel: "stable",
			version: "servicemeshoperator.v1.0.0",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:         operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:              operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:               "foobar",
						CreationTimestamp: metav1.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.0",
					},
					Status: operatorsv1alpha1.SubscriptionStatus{
						InstalledCSV: "servicemeshoperator.v1.0.0",
						CurrentCSV:   "servicemeshoperator.v1.0.0",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-bar",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
						CreationTimestamp: metav1.Date(2025, 1, 1, 2, 30, 0, 0, time.UTC),
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-baz",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
						CreationTimestamp: metav1.Date(2024, 12, 31, 23, 59, 0, 0, time.UTC),
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-bar",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
						CreationTimestamp: metav1.Date(2025, 1, 1, 2, 30, 0, 0, time.UTC),
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectDelete: []client.Object{},
		},
		{
			// Multiple InstallPlans exist, all with the correct cluster service version and owner reference, but only
			// one is in the "RequiresApproval" phase. Expect InstallPlan.Spec.Approved = true for only the one in the
			// "RequiresApproval" phase.
			name:    "Multiple unapproved InstallPlans, only one in correct phase",
			channel: "stable",
			version: "servicemeshoperator.v1.0.0",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:         operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:              operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:               "foobar",
						CreationTimestamp: metav1.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.0",
					},
					Status: operatorsv1alpha1.SubscriptionStatus{
						InstalledCSV: "servicemeshoperator.v1.0.0",
						CurrentCSV:   "servicemeshoperator.v1.0.0",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-bar",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
						CreationTimestamp: metav1.Date(2025, 1, 1, 2, 30, 0, 0, time.UTC),
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-baz",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
						CreationTimestamp: metav1.Date(2024, 12, 31, 23, 59, 0, 0, time.UTC),
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectCreate: []client.Object{},
			expectUpdate: []client.Object{
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-baz",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
						CreationTimestamp: metav1.Date(2024, 12, 31, 23, 59, 0, 0, time.UTC),
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectDelete: []client.Object{},
		},
		{
			name:    "Upgrade to next version",
			channel: "stable",
			version: "servicemeshoperator.v1.0.1",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:      operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:       "foobar",
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.0",
					},
					Status: operatorsv1alpha1.SubscriptionStatus{
						InstalledCSV: "servicemeshoperator.v1.0.0",
						CurrentCSV:   "servicemeshoperator.v1.0.1",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseComplete,
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-bar",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.1",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectUpdate: []client.Object{
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-bar",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.1",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
		},
		{
			name:    "Upgrade through intermediate version",
			channel: "stable",
			version: "servicemeshoperator.v1.0.2", // N+2 from the current
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:      operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:       "foobar",
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.2",
					},
					Status: operatorsv1alpha1.SubscriptionStatus{
						InstalledCSV: "servicemeshoperator.v1.0.0",
						CurrentCSV:   "servicemeshoperator.v1.0.1",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseComplete,
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-bar",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.1",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectUpdate: []client.Object{
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-bar",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.1",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
		},
		{
			name:    "Upgrade to next version when previous installplan is complete",
			channel: "stable",
			version: "servicemeshoperator.v1.0.2",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:      operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:       "foobar",
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.2",
					},
					Status: operatorsv1alpha1.SubscriptionStatus{
						InstalledCSV: "servicemeshoperator.v1.0.1",
						CurrentCSV:   "servicemeshoperator.v1.0.2",
					},
				},
				// N-1
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.0",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseComplete,
					},
				},
				// Current
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-bar",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.1",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseComplete,
					},
				},
				// N+1 (and expected).
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-baz",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.2",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: false,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
			expectUpdate: []client.Object{
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-baz",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.2",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseRequiresApproval,
					},
				},
			},
		},
		{
			name:    "Upgrade reached end",
			channel: "stable",
			version: "servicemeshoperator.v1.0.2",
			existingObjects: []runtime.Object{
				&operatorsv1alpha1.Subscription{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: operatorcontroller.ServiceMeshOperatorSubscriptionName().Namespace,
						Name:      operatorcontroller.ServiceMeshOperatorSubscriptionName().Name,
						UID:       "foobar",
					},
					Spec: &operatorsv1alpha1.SubscriptionSpec{
						Channel:                "stable",
						InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
						Package:                "servicemeshoperator",
						CatalogSource:          "redhat-operators",
						CatalogSourceNamespace: "openshift-marketplace",
						StartingCSV:            "servicemeshoperator.v1.0.2",
					},
					Status: operatorsv1alpha1.SubscriptionStatus{
						InstalledCSV: "servicemeshoperator.v1.0.2",
						CurrentCSV:   "servicemeshoperator.v1.0.2",
					},
				},
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-foo",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.2",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseComplete,
					},
				},
				// Current
				&operatorsv1alpha1.InstallPlan{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "Install-bar",
						Namespace: operatorcontroller.OpenshiftOperatorNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								UID: "foobar",
							},
						},
					},
					Spec: operatorsv1alpha1.InstallPlanSpec{
						ClusterServiceVersionNames: []string{
							"servicemeshoperator.v1.0.2",
						},
						Approval: operatorsv1alpha1.ApprovalManual,
						Approved: true,
					},
					Status: operatorsv1alpha1.InstallPlanStatus{
						Phase: operatorsv1alpha1.InstallPlanPhaseComplete,
					},
				},
			},
			expectUpdate: []client.Object{},
		},
	}

	scheme := runtime.NewScheme()
	configv1.Install(scheme)
	apiextensionsv1.AddToScheme(scheme)
	operatorsv1alpha1.AddToScheme(scheme)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tc.existingObjects...).
				Build()
			cl := &fakeClientRecorder{fakeClient, t, []client.Object{}, []client.Object{}, []client.Object{}}
			reconciler := &reconciler{
				client: cl,
				config: Config{
					OperatorNamespace:         "",
					OperandNamespace:          "",
					GatewayAPIOperatorChannel: tc.channel,
					GatewayAPIOperatorVersion: tc.version,
				},
			}
			_, _, err := reconciler.ensureServiceMeshOperatorInstallPlan(context.Background(), tc.version)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			cmpOpts := []cmp.Option{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Annotations", "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(apiextensionsv1.CustomResourceDefinition{}, "Spec"),
			}
			if diff := cmp.Diff(tc.expectCreate, cl.added, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual creates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectUpdate, cl.updated, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual updates: %s", diff)
			}
			if diff := cmp.Diff(tc.expectDelete, cl.deleted, cmpOpts...); diff != "" {
				t.Fatalf("found diff between expected and actual deletes: %s", diff)
			}
		})
	}
}

type fakeCache struct {
	cache.Informers
	client.Reader
}

type fakeClientRecorder struct {
	client.Client
	*testing.T

	added   []client.Object
	updated []client.Object
	deleted []client.Object
}

func (c *fakeClientRecorder) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *fakeClientRecorder) List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error {
	return c.Client.List(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Scheme() *runtime.Scheme {
	return c.Client.Scheme()
}

func (c *fakeClientRecorder) RESTMapper() meta.RESTMapper {
	return c.Client.RESTMapper()
}

func (c *fakeClientRecorder) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.added = append(c.added, obj)
	return c.Client.Create(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.deleted = append(c.deleted, obj)
	return c.Client.Delete(ctx, obj, opts...)
}

func (c *fakeClientRecorder) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return c.Client.DeleteAllOf(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.updated = append(c.updated, obj)
	return c.Client.Update(ctx, obj, opts...)
}

func (c *fakeClientRecorder) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c *fakeClientRecorder) Status() client.StatusWriter {
	return c.Client.Status()
}
