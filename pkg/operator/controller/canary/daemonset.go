package canary

import (
	"context"
	"fmt"

	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
)

// ensureCanaryDaemonSet ensures the canary daemonset exists
func (r *reconciler) ensureCanaryDaemonSet() (bool, *appsv1.DaemonSet, error) {
	desired := desiredCanaryDaemonSet(r.Config.CanaryImage)
	haveDs, current, err := r.currentCanaryDaemonSet()
	if err != nil {
		return false, nil, err
	}

	if haveDs {
		return true, current, nil
	}

	err = r.createCanaryDaemonSet(desired)
	if err != nil {
		return false, nil, err
	}

	return true, desired, nil
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
