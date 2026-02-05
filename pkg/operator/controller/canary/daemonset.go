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
func (r *reconciler) ensureCanaryDaemonSet(ctx context.Context) (bool, *appsv1.DaemonSet, error) {
	// Attempt to read the canary serving cert secret and compute a content hash.
	// If the secret is missing or incomplete, proceed without the annotation but
	// surface a log entry so operators can investigate.
	var certHash string
	secret := &corev1.Secret{}
	if err := r.client.Get(ctx, controller.CanaryCertificateName(), secret); err != nil {
		if errors.IsNotFound(err) {
			log.Info("canary serving cert secret not found; skipping canary-serving-cert-hash annotation")
		} else {
			return false, nil, fmt.Errorf("failed to get canary serving cert secret: %v", err)
		}
	} else {
		if h, err := ComputeTLSSecretHash(secret); err != nil {
			log.Info("canary serving cert secret is incomplete; skipping canary-serving-cert-hash annotation", "error", err)
		} else {
			certHash = h
		}
	}

	desired := desiredCanaryDaemonSet(r.config.CanaryImage, certHash)
	haveDs, current, err := r.currentCanaryDaemonSet(ctx)
	if err != nil {
		return false, nil, err
	}

	switch {
	case !haveDs:
		if err := r.createCanaryDaemonSet(ctx, desired); err != nil {
			return false, nil, err
		}
		return r.currentCanaryDaemonSet(ctx)
	case haveDs:
		if updated, err := r.updateCanaryDaemonSet(ctx, current, desired); err != nil {
			return true, current, err
		} else if updated {
			return r.currentCanaryDaemonSet(ctx)
		}
	}

	return true, current, nil
}

// currentCanaryDaemonSet returns the current canary daemonset
func (r *reconciler) currentCanaryDaemonSet(ctx context.Context) (bool, *appsv1.DaemonSet, error) {
	daemonset := &appsv1.DaemonSet{}
	if err := r.client.Get(ctx, controller.CanaryDaemonSetName(), daemonset); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, daemonset, nil
}

// createCanaryDaemonSet creates the given daemonset resource
func (r *reconciler) createCanaryDaemonSet(ctx context.Context, daemonset *appsv1.DaemonSet) error {
	if err := r.client.Create(ctx, daemonset); err != nil {
		return fmt.Errorf("failed to create canary daemonset %s/%s: %v", daemonset.Namespace, daemonset.Name, err)
	}

	log.Info("created canary daemonset", "namespace", daemonset.Namespace, "name", daemonset.Name)
	return nil
}

// updateCanaryDaemonSet updates the canary daemonset if an appropriate change
// has been detected
func (r *reconciler) updateCanaryDaemonSet(ctx context.Context, current, desired *appsv1.DaemonSet) (bool, error) {
	changed, updated := canaryDaemonSetChanged(current, desired)
	if !changed {
		return false, nil
	}

	// Capture annotation change for events.
	var oldHash, newHash string
	if current.Spec.Template.Annotations != nil {
		oldHash = current.Spec.Template.Annotations[CanaryServingCertHashAnnotation]
	}
	if updated.Spec.Template.Annotations != nil {
		newHash = updated.Spec.Template.Annotations[CanaryServingCertHashAnnotation]
	}

	diff := cmp.Diff(current, updated, cmpopts.EquateEmpty())
	if err := r.client.Update(ctx, updated); err != nil {
		return false, fmt.Errorf("failed to update canary daemonset %s/%s: %v", updated.Namespace, updated.Name, err)
	}

	log.Info("updated canary daemonset", "namespace", updated.Namespace, "name", updated.Name, "diff", diff)

	// If the only meaningful change (or one of the changes) was the canary cert
	// annotation, emit an event for traceability.
	if newHash != "" && newHash != oldHash {
		short := newHash
		if len(short) > 8 {
			short = short[:8]
		}
		if r.recorder != nil {
			r.recorder.Eventf(updated, "Normal", "CanaryCertRotated", "Canary serving cert rotated, updated pod template annotation hash: %s", short)
		}
	}

	return true, nil
}

// desiredCanaryDaemonSet returns the desired canary daemonset read in
// from manifests
func desiredCanaryDaemonSet(canaryImage string, certHash string) *appsv1.DaemonSet {
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
	daemonset.Spec.Template.Spec.Containers[0].Command = []string{"ingress-operator", CanaryHealthcheckCommand}

	if certHash != "" {
		if daemonset.Spec.Template.Annotations == nil {
			daemonset.Spec.Template.Annotations = map[string]string{}
		}
		daemonset.Spec.Template.Annotations[CanaryServingCertHashAnnotation] = certHash
	}

	return daemonset
}

// canaryDaemonSetChanged returns true if current and expected differ by the pod template's
// node selector, tolerations, or container image reference.
func canaryDaemonSetChanged(current, expected *appsv1.DaemonSet) (bool, *appsv1.DaemonSet) {
	changed := false
	updated := current.DeepCopy()

	// Update the canary daemonset when the canary server image, command, or container name changes
	if len(current.Spec.Template.Spec.Containers) > 0 && len(expected.Spec.Template.Spec.Containers) > 0 {
		if current.Spec.Template.Spec.Containers[0].Image != expected.Spec.Template.Spec.Containers[0].Image {
			updated.Spec.Template.Spec.Containers[0].Image = expected.Spec.Template.Spec.Containers[0].Image
			changed = true
		}
		if !cmp.Equal(current.Spec.Template.Spec.Containers[0].Command, expected.Spec.Template.Spec.Containers[0].Command) {
			updated.Spec.Template.Spec.Containers[0].Command = expected.Spec.Template.Spec.Containers[0].Command
			changed = true
		}
		if current.Spec.Template.Spec.Containers[0].Name != expected.Spec.Template.Spec.Containers[0].Name {
			updated.Spec.Template.Spec.Containers[0].Name = expected.Spec.Template.Spec.Containers[0].Name
			changed = true
		}
		if !cmp.Equal(current.Spec.Template.Spec.Containers[0].SecurityContext, expected.Spec.Template.Spec.Containers[0].SecurityContext) {
			updated.Spec.Template.Spec.Containers[0].SecurityContext = expected.Spec.Template.Spec.Containers[0].SecurityContext
			changed = true
		}
		if !cmp.Equal(current.Spec.Template.Spec.Containers[0].Env, expected.Spec.Template.Spec.Containers[0].Env, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b corev1.EnvVar) bool { return a.Name < b.Name })) {
			updated.Spec.Template.Spec.Containers[0].Env = expected.Spec.Template.Spec.Containers[0].Env
			changed = true
		}
		if !cmp.Equal(current.Spec.Template.Spec.Containers[0].VolumeMounts, expected.Spec.Template.Spec.Containers[0].VolumeMounts, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b corev1.VolumeMount) bool { return a.Name < b.Name })) {
			updated.Spec.Template.Spec.Containers[0].VolumeMounts = expected.Spec.Template.Spec.Containers[0].VolumeMounts
			changed = true
		}
		if !cmp.Equal(current.Spec.Template.Spec.Containers[0].Ports, expected.Spec.Template.Spec.Containers[0].Ports, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b corev1.ContainerPort) bool { return a.Name < b.Name })) {
			updated.Spec.Template.Spec.Containers[0].Ports = expected.Spec.Template.Spec.Containers[0].Ports
			changed = true
		}
	}

	if !cmp.Equal(current.Spec.Template.Spec.NodeSelector, expected.Spec.Template.Spec.NodeSelector, cmpopts.EquateEmpty()) {
		updated.Spec.Template.Spec.NodeSelector = expected.Spec.Template.Spec.NodeSelector
		changed = true
	}

	if !cmp.Equal(current.Spec.Template.Spec.SecurityContext, expected.Spec.Template.Spec.SecurityContext, cmpopts.EquateEmpty()) {
		updated.Spec.Template.Spec.SecurityContext = expected.Spec.Template.Spec.SecurityContext
		changed = true
	}

	if !cmp.Equal(current.Spec.Template.Spec.Tolerations, expected.Spec.Template.Spec.Tolerations, cmpopts.EquateEmpty(), cmpopts.SortSlices(cmpTolerations)) {
		updated.Spec.Template.Spec.Tolerations = expected.Spec.Template.Spec.Tolerations
		changed = true
	}

	if current.Spec.Template.Spec.PriorityClassName != expected.Spec.Template.Spec.PriorityClassName {
		updated.Spec.Template.Spec.PriorityClassName = expected.Spec.Template.Spec.PriorityClassName
		changed = true
	}

	if !cmp.Equal(current.Spec.Template.Spec.Volumes, expected.Spec.Template.Spec.Volumes, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b corev1.Volume) bool { return a.Name < b.Name })) {
		updated.Spec.Template.Spec.Volumes = expected.Spec.Template.Spec.Volumes
		changed = true
	}

	// Update when the canary-serving-cert hash annotation changes on the pod template.
	var currentHash, expectedHash string
	if current.Spec.Template.Annotations != nil {
		currentHash = current.Spec.Template.Annotations[CanaryServingCertHashAnnotation]
	}
	if expected.Spec.Template.Annotations != nil {
		expectedHash = expected.Spec.Template.Annotations[CanaryServingCertHashAnnotation]
	}
	if currentHash != expectedHash {
		if updated.Spec.Template.Annotations == nil {
			updated.Spec.Template.Annotations = map[string]string{}
		}
		if expectedHash == "" {
			delete(updated.Spec.Template.Annotations, CanaryServingCertHashAnnotation)
		} else {
			updated.Spec.Template.Annotations[CanaryServingCertHashAnnotation] = expectedHash
		}
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
