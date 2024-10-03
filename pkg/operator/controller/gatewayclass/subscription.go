package gatewayclass

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var (
	serviceMeshOperatorDesiredVersion = "servicemeshoperator.v2.5.0"
	serviceMeshOperatorNamespace      = "openshift-operators"
)

// ensureServiceMeshOperatorSubscription attempts to ensure that a subscription
// for servicemeshoperator is present and returns a Boolean indicating whether
// it exists, the subscription if it exists, and an error value.
func (r *reconciler) ensureServiceMeshOperatorSubscription(ctx context.Context) (bool, *operatorsv1alpha1.Subscription, error) {
	name := operatorcontroller.ServiceMeshSubscriptionName()
	have, current, err := r.currentSubscription(ctx, name)
	if err != nil {
		return false, nil, err
	}

	desired, err := desiredSubscription(name)
	if err != nil {
		return have, current, err
	}

	switch {
	case !have:
		if err := r.createSubscription(ctx, desired); err != nil {
			return false, nil, err
		}
		return r.currentSubscription(ctx, name)
	case have:
		if updated, err := r.updateSubscription(ctx, current, desired); err != nil {
			return have, current, err
		} else if updated {
			return r.currentSubscription(ctx, name)
		}
	}
	return true, current, nil
}

// desiredSubscription returns the desired subscription.
func desiredSubscription(name types.NamespacedName) (*operatorsv1alpha1.Subscription, error) {
	subscription := operatorsv1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: name.Namespace,
			Name:      name.Name,
		},
		Spec: &operatorsv1alpha1.SubscriptionSpec{
			Channel:                "stable",
			InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
			Package:                "servicemeshoperator",
			CatalogSource:          "redhat-operators",
			CatalogSourceNamespace: "openshift-marketplace",
			StartingCSV:            serviceMeshOperatorDesiredVersion,
		},
	}
	return &subscription, nil
}

// currentSubscription returns the current subscription.
func (r *reconciler) currentSubscription(ctx context.Context, name types.NamespacedName) (bool, *operatorsv1alpha1.Subscription, error) {
	var subscription operatorsv1alpha1.Subscription
	if err := r.client.Get(ctx, name, &subscription); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("failed to get subscription %s: %w", name, err)
	}
	return true, &subscription, nil
}

// createSubscription creates a subscription.
func (r *reconciler) createSubscription(ctx context.Context, subscription *operatorsv1alpha1.Subscription) error {
	if err := r.client.Create(ctx, subscription); err != nil {
		return fmt.Errorf("failed to create subscription %s/%s: %w", subscription.Namespace, subscription.Name, err)
	}
	log.Info("created subscription", "namespace", subscription.Namespace, "name", subscription.Name)
	return nil
}

// updateSubscription updates a subscription.
func (r *reconciler) updateSubscription(ctx context.Context, current, desired *operatorsv1alpha1.Subscription) (bool, error) {
	changed, updated := subscriptionChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(ctx, updated); err != nil {
		return false, fmt.Errorf("failed to update subscription %s/%s: %w", updated.Namespace, updated.Name, err)
	}
	log.Info("updated subscription", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// subscriptionChanged returns a Boolean indicating whether the current
// subscription matches the expected subscription and the updated subscription
// if they do not match.
func subscriptionChanged(current, expected *operatorsv1alpha1.Subscription) (bool, *operatorsv1alpha1.Subscription) {
	if cmp.Equal(current.Spec, expected.Spec, cmpopts.EquateEmpty()) {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec

	return true, updated
}

// ensureServiceMeshOperatorInstallPlan attempts to ensure that the install plan for the appropriate OSSM operator
// version is approved.
func (r *reconciler) ensureServiceMeshOperatorInstallPlan(ctx context.Context) (bool, *operatorsv1alpha1.InstallPlan, error) {
	currentInstallPlan, err := r.getCurrentInstallPlan(ctx)
	if err != nil {
		return false, nil, err
	} else if currentInstallPlan == nil {
		return false, nil, nil
	}
	if !currentInstallPlan.Spec.Approved {
		currentInstallPlan.Spec.Approved = true
		if err := r.client.Update(ctx, currentInstallPlan); err != nil {
			return false, nil, fmt.Errorf("Failed to update %s/%s: %w", currentInstallPlan.Namespace, currentInstallPlan.Name, err)
		}
		return true, currentInstallPlan, nil
	}
	return false, currentInstallPlan, nil
}

func (r *reconciler) getCurrentInstallPlan(ctx context.Context) (*operatorsv1alpha1.InstallPlan, error) {
	InstallPlans := &operatorsv1alpha1.InstallPlanList{}
	if err := r.client.List(ctx, InstallPlans, client.InNamespace(serviceMeshOperatorNamespace)); err != nil {
		return nil, err
	}
	if InstallPlans == nil || len(InstallPlans.Items) == 0 {
		return nil, nil
	}
	for _, InstallPlan := range InstallPlans.Items {
		for _, CSVName := range InstallPlan.Spec.ClusterServiceVersionNames {
			if CSVName == serviceMeshOperatorDesiredVersion {
				return &InstallPlan, nil
			}
		}
	}
	return nil, fmt.Errorf("No InstallPlan with cluster service version %s found", serviceMeshOperatorDesiredVersion)
}
