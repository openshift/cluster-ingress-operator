package gatewayclass

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	securityv1 "github.com/openshift/api/security/v1"
	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// RequiredSCCRestrictedV2 is name of the "restricted-v2" SCC.
	RequiredSCCRestrictedV2 = "restricted-v2"
	// WorkloadPartitioningManagementAnnotationKey is the annotation key for
	// workload partitioning.
	WorkloadPartitioningManagementAnnotationKey = "target.workload.openshift.io/management"
	// WorkloadPartitioningManagementPreferredScheduling is the annotation
	// value for preferred scheduling of workload.
	WorkloadPartitioningManagementPreferredScheduling = `{"effect": "PreferredDuringScheduling"}`
)

// ensureServiceMeshOperatorSubscription attempts to ensure that a subscription
// for servicemeshoperator is present and returns a Boolean indicating whether
// it exists, the subscription if it exists, and an error value.
func (r *reconciler) ensureServiceMeshOperatorSubscription(ctx context.Context, catalog, channel, version string) (bool, *operatorsv1alpha1.Subscription, error) {
	name := operatorcontroller.ServiceMeshOperatorSubscriptionName()
	have, current, err := r.currentSubscription(ctx, name)
	if err != nil {
		return false, nil, err
	}

	desired, err := desiredSubscription(name, catalog, channel, version)
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
func desiredSubscription(name types.NamespacedName, gwapiOperatorCatalog, gwapiOperatorChannel, gwapiOperatorVersion string) (*operatorsv1alpha1.Subscription, error) {
	subscription := operatorsv1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: name.Namespace,
			Name:      name.Name,
			Annotations: map[string]string{
				operatorcontroller.IngressOperatorOwnedAnnotation: "",
			},
		},
		Spec: &operatorsv1alpha1.SubscriptionSpec{
			Channel: gwapiOperatorChannel,
			Config: &operatorsv1alpha1.SubscriptionConfig{
				// Resources is the default resources minus
				// limits, which pods in platform namespaces
				// are not permitted by OpenShift conventions
				// to set.
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
				Annotations: map[string]string{
					securityv1.RequiredSCCAnnotation:            RequiredSCCRestrictedV2,
					WorkloadPartitioningManagementAnnotationKey: WorkloadPartitioningManagementPreferredScheduling,
				},
			},
			InstallPlanApproval:    operatorsv1alpha1.ApprovalManual,
			Package:                "servicemeshoperator3",
			CatalogSource:          gwapiOperatorCatalog,
			CatalogSourceNamespace: "openshift-marketplace",
			StartingCSV:            gwapiOperatorVersion,
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
func (r *reconciler) ensureServiceMeshOperatorInstallPlan(ctx context.Context, version string) (bool, *operatorsv1alpha1.InstallPlan, error) {
	haveInstallPlan, current, err := r.currentInstallPlan(ctx, version)
	if err != nil {
		return false, nil, err
	}
	switch {
	case !haveInstallPlan:
		// The OLM operator creates the initial InstallPlan, so if it doesn't exist yet or it's been deleted, do nothing
		// and let the OLM operator handle it.
		return false, nil, nil
	case haveInstallPlan:
		desired := desiredInstallPlan(current)
		if updated, err := r.updateInstallPlan(ctx, current, desired); err != nil {
			return true, current, err
		} else if updated {
			return r.currentInstallPlan(ctx, version)
		}
	}
	return false, current, nil
}

// currentInstallPlan returns the InstallPlan that describes installing the expected version of the GatewayAPI
// implementation, if one exists.
func (r *reconciler) currentInstallPlan(ctx context.Context, version string) (bool, *operatorsv1alpha1.InstallPlan, error) {
	_, subscription, err := r.currentSubscription(ctx, operatorcontroller.ServiceMeshOperatorSubscriptionName())
	if err != nil {
		return false, nil, err
	}
	installPlans := &operatorsv1alpha1.InstallPlanList{}
	if err := r.client.List(ctx, installPlans, client.InNamespace(operatorcontroller.OpenshiftOperatorNamespace)); err != nil {
		return false, nil, err
	}
	if installPlans == nil || len(installPlans.Items) == 0 {
		return false, nil, nil
	}
	var currentInstallPlan *operatorsv1alpha1.InstallPlan
	multipleInstallPlans := false
	for _, installPlan := range installPlans.Items {
		if len(installPlan.OwnerReferences) == 0 || len(installPlan.Spec.ClusterServiceVersionNames) == 0 {
			continue
		}
		ownerRefMatches := false
		for _, ownerRef := range installPlan.OwnerReferences {
			if ownerRef.UID == subscription.UID {
				ownerRefMatches = true
				break
			}
		}
		if !ownerRefMatches {
			continue
		}
		// Ignore InstallPlans not in the "RequiresApproval" state. OLM may not be done setting them up.
		if installPlan.Status.Phase != operatorsv1alpha1.InstallPlanPhaseRequiresApproval {
			continue
		}
		for _, csvName := range installPlan.Spec.ClusterServiceVersionNames {
			if csvName == version {
				// Keep the newest InstallPlan to return at the end of the loop.
				if currentInstallPlan == nil {
					currentInstallPlan = &installPlan
					break
				}
				multipleInstallPlans = true
				if currentInstallPlan.ObjectMeta.CreationTimestamp.Before(&installPlan.ObjectMeta.CreationTimestamp) {
					currentInstallPlan = &installPlan
					break
				}
			}
		}
	}
	if multipleInstallPlans {
		log.Info(fmt.Sprintf("found multiple valid InstallPlans. using %s because it's the newest", currentInstallPlan.Name))
	}
	return (currentInstallPlan != nil), currentInstallPlan, nil
}

// desiredInstallPlan returns a version of the expected InstallPlan that is approved.
func desiredInstallPlan(current *operatorsv1alpha1.InstallPlan) *operatorsv1alpha1.InstallPlan {
	desired := current.DeepCopy()
	desired.Spec.Approved = true
	return desired
}

// updateInstallPlan updates an existing InstallPlan if it differs from the desired state.
func (r *reconciler) updateInstallPlan(ctx context.Context, current, desired *operatorsv1alpha1.InstallPlan) (bool, error) {
	changed, updated := installPlanChanged(current, desired)
	if !changed {
		return false, nil
	}
	diff := cmp.Diff(current.Spec, updated.Spec, cmpopts.EquateEmpty())
	if err := r.client.Update(ctx, updated); err != nil {
		return false, fmt.Errorf("failed to update InstallPlan %s/%s: %w", current.Namespace, current.Name, err)
	}
	log.Info("updated InstallPlan", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)
	return true, nil
}

// installPlanChanged returns a Boolean indicating whether the current InstallPlan matches the expected InstallPlan and
// the updated InstallPlan if they do not match.
func installPlanChanged(current, expected *operatorsv1alpha1.InstallPlan) (bool, *operatorsv1alpha1.InstallPlan) {
	if cmp.Equal(current.Spec, expected.Spec, cmpopts.EquateEmpty()) {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec

	return true, updated
}
