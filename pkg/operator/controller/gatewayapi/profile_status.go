package gatewayapi

import (
	"context"
	"fmt"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func (r *reconciler) markProfilesUnmanaged(ctx context.Context) error {
	var classes gatewayapiv1.GatewayClassList
	if err := r.client.List(ctx, &classes, client.MatchingLabels{operatorcontroller.ManagedGatewayClassProfileLabel: "true"}); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return err
	}
	var errs []error
	for _, class := range classes.Items {
		name := class.Name
		if name != operatorcontroller.OpenShiftExternalGatewayClassName && name != operatorcontroller.OpenShiftInternalGatewayClassName && name != operatorcontroller.OpenShiftClusterIPGatewayClassName {
			continue
		}
		if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			var current gatewayapiv1.GatewayClass
			if err := r.client.Get(ctx, types.NamespacedName{Name: name}, &current); err != nil {
				if apierrors.IsNotFound(err) {
					return nil
				}
				return err
			}
			if !current.DeletionTimestamp.IsZero() || current.Spec.ControllerName != operatorcontroller.OpenShiftGatewayClassControllerName || current.Labels[operatorcontroller.ManagedGatewayClassProfileLabel] != "true" {
				return nil
			}
			updated := current.DeepCopy()
			if !meta.SetStatusCondition(&updated.Status.Conditions, metav1.Condition{Type: "Customized", Status: metav1.ConditionFalse, Reason: reasonUnmanaged, Message: "Customization is not managed by the Ingress Operator in Unmanaged mode.", ObservedGeneration: current.Generation, LastTransitionTime: metav1.Now()}) {
				return nil
			}
			return r.client.Status().Patch(ctx, updated, client.MergeFromWithOptions(&current, client.MergeFromWithOptimisticLock{}))
		}); err != nil {
			errs = append(errs, fmt.Errorf("cannot mark GatewayClass %q customization Unmanaged: %w", name, err))
		}
	}
	return utilerrors.NewAggregate(errs)
}
