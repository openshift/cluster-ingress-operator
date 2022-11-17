package ingressclass

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	networkingv1 "k8s.io/api/networking/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ensureIngressClass ensures an IngressClass exists for the IngressController
// with the given name if the IngressController exists, or ensures that such an
// IngressClass doesn't exist if the IngressController does not exist.  Returns
// a Boolean indicating whether the IngressClass exists, the current
// IngressClass if it does exist, and an error value.
func (r *reconciler) ensureIngressClass(icName types.NamespacedName, ingressClasses []networkingv1.IngressClass) (bool, *networkingv1.IngressClass, error) {
	haveIngressController := false
	ic := &operatorv1.IngressController{}
	if err := r.cache.Get(context.TODO(), icName, ic); err != nil {
		if !errors.IsNotFound(err) {
			return false, nil, fmt.Errorf("failed to get ingresscontroller: %w", err)
		}
	} else if ic.DeletionTimestamp == nil {
		haveIngressController = true
	}

	want, desired := desiredIngressClass(haveIngressController, icName.Name, ingressClasses)

	have, current, err := r.currentIngressClass(icName.Name)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !want && !have:
		return false, nil, nil
	case !want && have:
		if err := r.client.Delete(context.TODO(), current); err != nil {
			if !errors.IsNotFound(err) {
				return true, current, fmt.Errorf("failed to delete IngressClass: %w", err)
			}
		} else {
			log.Info("deleted IngressClass", "ingressclass", current)
		}
		return false, nil, nil
	case want && !have:
		if err := r.client.Create(context.TODO(), desired); err != nil {
			return false, nil, fmt.Errorf("failed to create IngressClass: %w", err)
		}
		log.Info("created IngressClass", "ingressclass", desired)
		return r.currentIngressClass(icName.Name)
	case want && have:
		if updated, err := r.updateIngressClass(current, desired); err != nil {
			return true, current, fmt.Errorf("failed to update IngressClass: %w", err)
		} else if updated {
			return r.currentIngressClass(icName.Name)
		}
	}

	return true, current, nil
}

// desiredIngressClass returns a Boolean indicating whether an IngressClass
// is desired, as well as the IngressClass if one is desired.
func desiredIngressClass(haveIngressController bool, ingressControllerName string, ingressClasses []networkingv1.IngressClass) (bool, *networkingv1.IngressClass) {
	if !haveIngressController {
		return false, nil
	}

	name := controller.IngressClassName(ingressControllerName)
	scope := networkingv1.IngressClassParametersReferenceScopeCluster
	class := &networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name.Name,
		},
		Spec: networkingv1.IngressClassSpec{
			Controller: routev1.IngressToRouteIngressClassControllerName,
			Parameters: &networkingv1.IngressClassParametersReference{
				APIGroup: &operatorv1.GroupName,
				Kind:     "IngressController",
				Name:     ingressControllerName,
				Scope:    &scope,
			},
		},
	}
	// When creating an IngressClass for the "default" IngressController,
	// annotate the IngressClass as the default IngressClass if no other
	// IngressClass has the annotation.
	//
	// TODO This is commented out because it breaks "[sig-network]
	// IngressClass [Feature:Ingress] should not set default value if no
	// default IngressClass"; we need to fix that test and then re-enable
	// this logic.
	//
	// if ingressControllerName == "default" {
	// 	const defaultAnnotation = "ingressclass.kubernetes.io/is-default-class"
	// 	someIngressClassIsDefault := false
	// 	for _, class := range ingressClasses {
	// 		if class.Annotations[defaultAnnotation] == "true" {
	// 			someIngressClassIsDefault = true
	// 			break
	// 		}
	// 	}
	// 	if !someIngressClassIsDefault {
	// 		class.ObjectMeta.Annotations = map[string]string{
	// 			defaultAnnotation: "true",
	// 		}
	// 	}
	// }
	return true, class
}

// currentIngressClass returns a Boolean indicating whether an IngressClass
// exists for the IngressController with the given name, as well as the
// IngressClass if it does exist and an error value.
func (r *reconciler) currentIngressClass(ingressControllerName string) (bool, *networkingv1.IngressClass, error) {
	name := controller.IngressClassName(ingressControllerName)
	class := &networkingv1.IngressClass{}
	if err := r.client.Get(context.TODO(), name, class); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, class, nil
}

// updateIngressClass updates an IngressClass.  Returns a Boolean indicating
// whether the IngressClass was updated, and an error value.
func (r *reconciler) updateIngressClass(current, desired *networkingv1.IngressClass) (bool, error) {
	changed, updated := ingressClassChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Diff before updating because the client may mutate the object.
	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(context.TODO(), updated); err != nil {
		log.Info("updated IngressClass", "name", updated.Name, "diff", diff)
		return false, err
	}
	return true, nil
}

// ingressClassChanged checks if the current IngressClass spec matches
// the expected spec and if not returns an updated one.
func ingressClassChanged(current, expected *networkingv1.IngressClass) (bool, *networkingv1.IngressClass) {
	if cmp.Equal(current.Spec, expected.Spec, cmpopts.EquateEmpty()) {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec = expected.Spec

	return true, updated
}
