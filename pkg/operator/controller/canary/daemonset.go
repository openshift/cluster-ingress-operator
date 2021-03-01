package canary

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
)

// ensureCanaryDaemonSet ensures the canary daemonset exists
func (r *reconciler) ensureCanaryDaemonSet() (bool, *appsv1.DaemonSet, error) {
	desired := desiredCanaryDaemonSet(r.config.CanaryImage)
	haveDs, current, err := r.currentCanaryDaemonSet()
	if err != nil {
		return false, nil, err
	}

	switch {
	case !haveDs:
		if err := r.createCanaryDaemonSet(desired); err != nil {
			return false, nil, err
		}
		return r.currentCanaryDaemonSet()
	case haveDs:
		if updated, err := r.updateCanaryDaemonSet(current, desired); err != nil {
			return true, current, err
		} else if updated {
			return r.currentCanaryDaemonSet()
		}
	}

	return true, current, nil
}

// currentCanaryDaemonSet returns the current canary daemonset
func (r *reconciler) currentCanaryDaemonSet() (bool, *appsv1.DaemonSet, error) {
	daemonset := &appsv1.DaemonSet{}
	if err := r.client.Get(context.TODO(), controller.CanaryDaemonSetName(), daemonset); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, daemonset, nil
}

// createCanaryDaemonSet creates the given daemonset resource
func (r *reconciler) createCanaryDaemonSet(daemonset *appsv1.DaemonSet) error {
	if err := r.client.Create(context.TODO(), daemonset); err != nil {
		return fmt.Errorf("failed to create canary daemonset %s/%s: %v", daemonset.Namespace, daemonset.Name, err)
	}

	log.Info("created canary daemonset", "namespace", daemonset.Namespace, "name", daemonset.Name)
	return nil
}

// updateCanaryDaemonSet updates the canary daemonset if an appropriate change
// has been detected
func (r *reconciler) updateCanaryDaemonSet(current, desired *appsv1.DaemonSet) (bool, error) {
	changed, updated := canaryDaemonSetChanged(current, desired)
	if !changed {
		return false, nil
	}

	if err := r.client.Update(context.TODO(), updated); err != nil {
		return false, fmt.Errorf("failed to update canary daemonset %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	log.Info("updated canary daemonset", "namespace", updated.Namespace, "name", updated.Name)
	return true, nil
}

// desiredCanaryDaemonSet returns the desired canary daemonset read in
// from manifests
func desiredCanaryDaemonSet(canaryImage string) *appsv1.DaemonSet {
	daemonset := manifests.CanaryDaemonSet()
	name := controller.CanaryDaemonSetName()
	daemonset.Name = name.Name
	daemonset.Namespace = name.Namespace

	daemonset.Labels = map[string]string{
		// associate the daemonset with the ingress canary controller
		manifests.OwningIngressCanaryCheckLabel: canaryControllerName,
	}

	daemonset.Spec.Selector = controller.CanaryDaemonSetPodSelector(canaryControllerName)
	daemonset.Spec.Template.Labels = controller.CanaryDaemonSetPodSelector(canaryControllerName).MatchLabels

	daemonset.Spec.Template.Spec.Containers[0].Image = canaryImage

	return daemonset
}

// canaryDaemonSetChanged returns true if current and expected differ by the pod template's
// node selector, tolerations, or container image reference.
func canaryDaemonSetChanged(current, expected *appsv1.DaemonSet) (bool, *appsv1.DaemonSet) {
	changed := false
	updated := current.DeepCopy()

	// Update the canary daemonset when the canary server image changes
	if len(current.Spec.Template.Spec.Containers) > 0 && len(expected.Spec.Template.Spec.Containers) > 0 &&
		current.Spec.Template.Spec.Containers[0].Image != expected.Spec.Template.Spec.Containers[0].Image {
		updated.Spec.Template.Spec.Containers[0].Image = expected.Spec.Template.Spec.Containers[0].Image
		changed = true
	}

	if !cmp.Equal(current.Spec.Template.Spec.NodeSelector, expected.Spec.Template.Spec.NodeSelector, cmpopts.EquateEmpty()) {
		updated.Spec.Template.Spec.NodeSelector = expected.Spec.Template.Spec.NodeSelector
		changed = true
	}

	if !cmp.Equal(current.Spec.Template.Spec.Tolerations, expected.Spec.Template.Spec.Tolerations, cmpopts.EquateEmpty(), cmpopts.SortSlices(cmpTolerations)) {
		updated.Spec.Template.Spec.Tolerations = expected.Spec.Template.Spec.Tolerations
		changed = true
	}

	if !changed {
		return false, nil
	}

	return true, updated
}

// cmpTolerations compares two Tolerations values and returns a Boolean
// indicating whether they are equal.
func cmpTolerations(a, b corev1.Toleration) bool {
	if a.Key != b.Key {
		return false
	}
	if a.Value != b.Value {
		return false
	}
	if a.Operator != b.Operator {
		return false
	}
	if a.Effect != b.Effect {
		return false
	}
	if a.Effect == corev1.TaintEffectNoExecute {
		if (a.TolerationSeconds == nil) != (b.TolerationSeconds == nil) {
			return false
		}
		// Field is ignored unless effect is NoExecute.
		if a.TolerationSeconds != nil && *a.TolerationSeconds != *b.TolerationSeconds {
			return false
		}
	}
	return true
}
