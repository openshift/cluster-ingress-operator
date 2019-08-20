package ingress

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	configv1 "github.com/openshift/api/config/v1"
)

// ensureRouterDeployment ensures the router deployment exists for a given
// ingresscontroller.
func (r *reconciler) ensureRouterDeployment(ci *operatorv1.IngressController, infraConfig *configv1.Infrastructure) (*appsv1.Deployment, error) {
	desired, err := desiredRouterDeployment(ci, r.Config.IngressControllerImage, infraConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build router deployment: %v", err)
	}
	current, err := r.currentRouterDeployment(ci)
	if err != nil {
		return nil, err
	}
	switch {
	case desired != nil && current == nil:
		if err := r.createRouterDeployment(desired); err != nil {
			return nil, err
		}
	case desired != nil && current != nil:
		if err := r.updateRouterDeployment(current, desired); err != nil {
			return nil, err
		}
	}
	return r.currentRouterDeployment(ci)
}

// ensureRouterDeleted ensures that any router resources associated with the
// ingresscontroller are deleted.
func (r *reconciler) ensureRouterDeleted(ci *operatorv1.IngressController) error {
	deployment := &appsv1.Deployment{}
	name := controller.RouterDeploymentName(ci)
	deployment.Name = name.Name
	deployment.Namespace = name.Namespace
	if err := r.client.Delete(context.TODO(), deployment); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// desiredRouterDeployment returns the desired router deployment.
func desiredRouterDeployment(ci *operatorv1.IngressController, ingressControllerImage string, infraConfig *configv1.Infrastructure) (*appsv1.Deployment, error) {
	deployment := manifests.RouterDeployment()
	name := controller.RouterDeploymentName(ci)
	deployment.Name = name.Name
	deployment.Namespace = name.Namespace

	deployment.Labels = map[string]string{
		// associate the deployment with the ingresscontroller
		manifests.OwningIngressControllerLabel: ci.Name,
	}

	// Ensure the deployment adopts only its own pods.
	deployment.Spec.Selector = controller.IngressControllerDeploymentPodSelector(ci)
	deployment.Spec.Template.Labels = deployment.Spec.Selector.MatchLabels

	// Prevent colocation of controller pods to enable simple horizontal scaling
	deployment.Spec.Template.Spec.Affinity = &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
				{
					TopologyKey: "kubernetes.io/hostname",
					LabelSelector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      controller.ControllerDeploymentLabel,
								Operator: metav1.LabelSelectorOpIn,
								Values:   []string{controller.IngressControllerDeploymentLabel(ci)},
							},
						},
					},
				},
			},
		},
	}

	// For now, all strategies use 25% max unavailable and 0 surge. This is because
	// distinct ingress controllers can't currently be colocated. Usually, replicas
	// will be equal to the node pool size. Under these conditions, surge requires
	// new nodes to support the rollout. This means a positive surge can cause the
	// rollout to wedge in the absence of auto-scaling.
	pointerTo := func(ios intstr.IntOrString) *intstr.IntOrString { return &ios }
	deployment.Spec.Strategy = appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxUnavailable: pointerTo(intstr.FromString("25%")),
			MaxSurge:       pointerTo(intstr.FromInt(0)),
		},
	}

	statsSecretName := fmt.Sprintf("router-stats-%s", ci.Name)
	env := []corev1.EnvVar{
		{Name: "ROUTER_SERVICE_NAME", Value: ci.Name},
		{Name: "STATS_USERNAME", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: statsSecretName,
				},
				Key: "statsUsername",
			},
		}},
		{Name: "STATS_PASSWORD", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: statsSecretName,
				},
				Key: "statsPassword",
			},
		}},
		{Name: "ROUTER_CIPHERS", Value: "modern"},
	}

	// Enable prometheus metrics
	certsSecretName := fmt.Sprintf("router-metrics-certs-%s", ci.Name)
	certsVolumeName := "metrics-certs"
	certsVolumeMountPath := "/etc/pki/tls/metrics-certs"

	volume := corev1.Volume{
		Name: certsVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: certsSecretName,
			},
		},
	}
	volumeMount := corev1.VolumeMount{
		Name:      certsVolumeName,
		MountPath: certsVolumeMountPath,
		ReadOnly:  true,
	}

	deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, volume)
	deployment.Spec.Template.Spec.Containers[0].VolumeMounts = append(deployment.Spec.Template.Spec.Containers[0].VolumeMounts, volumeMount)

	env = append(env, corev1.EnvVar{Name: "ROUTER_METRICS_TYPE", Value: "haproxy"})
	env = append(env, corev1.EnvVar{Name: "ROUTER_METRICS_TLS_CERT_FILE", Value: filepath.Join(certsVolumeMountPath, "tls.crt")})
	env = append(env, corev1.EnvVar{Name: "ROUTER_METRICS_TLS_KEY_FILE", Value: filepath.Join(certsVolumeMountPath, "tls.key")})

	if len(ci.Status.Domain) > 0 {
		env = append(env, corev1.EnvVar{Name: "ROUTER_CANONICAL_HOSTNAME", Value: ci.Status.Domain})
	}

	if ci.Status.EndpointPublishingStrategy.Type == operatorv1.LoadBalancerServiceStrategyType {
		// For now, check if we are on AWS. This can really be done for
		// for any external [cloud] LBs that support the proxy protocol.
		if infraConfig.Status.Platform == configv1.AWSPlatformType {
			env = append(env, corev1.EnvVar{Name: "ROUTER_USE_PROXY_PROTOCOL", Value: "true"})
		}
	}

	env = append(env, corev1.EnvVar{Name: "ROUTER_THREADS", Value: "4"})

	nodeSelector := map[string]string{
		"kubernetes.io/os":               "linux",
		"node-role.kubernetes.io/worker": "",
	}
	if ci.Spec.NodePlacement != nil {
		if ci.Spec.NodePlacement.NodeSelector != nil {
			var err error
			nodeSelector, err = metav1.LabelSelectorAsMap(ci.Spec.NodePlacement.NodeSelector)
			if err != nil {
				return nil, fmt.Errorf("ingresscontroller %q has invalid spec.nodePlacement.nodeSelector: %v",
					ci.Name, err)
			}
		}
		if ci.Spec.NodePlacement.Tolerations != nil {
			deployment.Spec.Template.Spec.Tolerations = ci.Spec.NodePlacement.Tolerations
		}
	}
	deployment.Spec.Template.Spec.NodeSelector = nodeSelector

	if ci.Spec.NamespaceSelector != nil {
		namespaceSelector, err := metav1.LabelSelectorAsSelector(ci.Spec.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("ingresscontroller %q has invalid spec.namespaceSelector: %v",
				ci.Name, err)
		}

		env = append(env, corev1.EnvVar{
			Name:  "NAMESPACE_LABELS",
			Value: namespaceSelector.String(),
		})
	}

	var desiredReplicas int32 = 2
	if ci.Spec.Replicas != nil {
		desiredReplicas = *ci.Spec.Replicas
	}
	deployment.Spec.Replicas = &desiredReplicas

	if ci.Spec.RouteSelector != nil {
		routeSelector, err := metav1.LabelSelectorAsSelector(ci.Spec.RouteSelector)
		if err != nil {
			return nil, fmt.Errorf("ingresscontroller %q has invalid spec.routeSelector: %v", ci.Name, err)
		}
		env = append(env, corev1.EnvVar{Name: "ROUTE_LABELS", Value: routeSelector.String()})
	}

	deployment.Spec.Template.Spec.Containers[0].Env = append(deployment.Spec.Template.Spec.Containers[0].Env, env...)

	deployment.Spec.Template.Spec.Containers[0].Image = ingressControllerImage

	if ci.Status.EndpointPublishingStrategy.Type == operatorv1.HostNetworkStrategyType {
		// Expose ports 80 and 443 on the host to provide endpoints for
		// the user's HA solution.
		deployment.Spec.Template.Spec.HostNetwork = true

		// With container networking, probes default to using the pod IP
		// address.  With host networking, probes default to using the
		// node IP address.  Using localhost avoids potential routing
		// problems or firewall restrictions.
		deployment.Spec.Template.Spec.Containers[0].LivenessProbe.Handler.HTTPGet.Host = "localhost"
		deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Handler.HTTPGet.Host = "localhost"
	}

	// Fill in the default certificate secret name.
	secretName := controller.RouterEffectiveDefaultCertificateSecretName(ci, deployment.Namespace)
	deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName = secretName.Name

	return deployment, nil
}

// currentRouterDeployment returns the current router deployment.
func (r *reconciler) currentRouterDeployment(ci *operatorv1.IngressController) (*appsv1.Deployment, error) {
	deployment := &appsv1.Deployment{}
	if err := r.client.Get(context.TODO(), controller.RouterDeploymentName(ci), deployment); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return deployment, nil
}

// createRouterDeployment creates a router deployment.
func (r *reconciler) createRouterDeployment(deployment *appsv1.Deployment) error {
	if err := r.client.Create(context.TODO(), deployment); err != nil {
		return fmt.Errorf("failed to create router deployment %s/%s: %v", deployment.Namespace, deployment.Name, err)
	}
	log.Info("created router deployment", "namespace", deployment.Namespace, "name", deployment.Name)
	return nil
}

// updateRouterDeployment updates a router deployment.
func (r *reconciler) updateRouterDeployment(current, desired *appsv1.Deployment) error {
	changed, updated := deploymentConfigChanged(current, desired)
	if !changed {
		return nil
	}

	if err := r.client.Update(context.TODO(), updated); err != nil {
		return fmt.Errorf("failed to update router deployment %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	log.Info("updated router deployment", "namespace", updated.Namespace, "name", updated.Name)
	return nil
}

// deploymentConfigChanged checks if current config matches the expected config
// for the ingress controller deployment and if not returns the updated config.
func deploymentConfigChanged(current, expected *appsv1.Deployment) (bool, *appsv1.Deployment) {
	if cmp.Equal(current.Spec.Template.Spec.Volumes, expected.Spec.Template.Spec.Volumes, cmpopts.EquateEmpty(), cmpopts.SortSlices(cmpVolumes), cmp.Comparer(cmpSecretVolumeSource)) &&
		cmp.Equal(current.Spec.Template.Spec.NodeSelector, expected.Spec.Template.Spec.NodeSelector, cmpopts.EquateEmpty()) &&
		cmp.Equal(current.Spec.Template.Spec.Containers[0].Env, expected.Spec.Template.Spec.Containers[0].Env, cmpopts.EquateEmpty(), cmpopts.SortSlices(cmpEnvs)) &&
		current.Spec.Template.Spec.Containers[0].Image == expected.Spec.Template.Spec.Containers[0].Image &&
		cmp.Equal(current.Spec.Template.Spec.Tolerations, expected.Spec.Template.Spec.Tolerations, cmpopts.EquateEmpty(), cmpopts.SortSlices(cmpTolerations)) &&
		cmp.Equal(current.Spec.Template.Spec.Affinity, expected.Spec.Template.Spec.Affinity, cmpopts.EquateEmpty()) &&
		cmp.Equal(current.Spec.Strategy, expected.Spec.Strategy, cmpopts.EquateEmpty()) &&
		current.Spec.Replicas != nil &&
		*current.Spec.Replicas == *expected.Spec.Replicas {
		return false, nil
	}

	updated := current.DeepCopy()
	updated.Spec.Strategy = expected.Spec.Strategy
	volumes := make([]corev1.Volume, len(expected.Spec.Template.Spec.Volumes))
	for i, vol := range expected.Spec.Template.Spec.Volumes {
		volumes[i] = *vol.DeepCopy()
	}
	updated.Spec.Template.Spec.Volumes = volumes
	updated.Spec.Template.Spec.NodeSelector = expected.Spec.Template.Spec.NodeSelector
	updated.Spec.Template.Spec.Containers[0].Env = expected.Spec.Template.Spec.Containers[0].Env
	updated.Spec.Template.Spec.Containers[0].Image = expected.Spec.Template.Spec.Containers[0].Image
	updated.Spec.Template.Spec.Tolerations = expected.Spec.Template.Spec.Tolerations
	updated.Spec.Template.Spec.Affinity = expected.Spec.Template.Spec.Affinity
	replicas := int32(1)
	if expected.Spec.Replicas != nil {
		replicas = *expected.Spec.Replicas
	}
	updated.Spec.Replicas = &replicas
	return true, updated
}

func cmpEnvs(a, b corev1.EnvVar) bool    { return a.Name < b.Name }
func cmpVolumes(a, b corev1.Volume) bool { return a.Name < b.Name }
func cmpSecretVolumeSource(a, b corev1.SecretVolumeSource) bool {
	if a.SecretName != b.SecretName {
		return false
	}
	if !cmp.Equal(a.Items, b.Items, cmpopts.EquateEmpty()) {
		return false
	}
	aDefaultMode := int32(420)
	if a.DefaultMode != nil {
		aDefaultMode = *a.DefaultMode
	}
	bDefaultMode := int32(420)
	if b.DefaultMode != nil {
		bDefaultMode = *b.DefaultMode
	}
	if aDefaultMode != bDefaultMode {
		return false
	}
	if !cmp.Equal(a.Optional, b.Optional, cmpopts.EquateEmpty()) {
		return false
	}
	return true
}

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
