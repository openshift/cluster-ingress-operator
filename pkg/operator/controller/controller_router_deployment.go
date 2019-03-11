package controller

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/manifests"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1 "github.com/openshift/api/config/v1"
)

// routerDeploymentName returns the namespaced name for the router deployment.
func routerDeploymentName(ci *operatorv1.IngressController) types.NamespacedName {
	return types.NamespacedName{
		Namespace: "openshift-ingress",
		Name:      "router-" + ci.Name,
	}
}

// ensureRouterDeployment ensures the router deployment exists for a given
// clusteringress.
func (r *reconciler) ensureRouterDeployment(ci *operatorv1.IngressController, infraConfig *configv1.Infrastructure) (*appsv1.Deployment, error) {
	desired, err := desiredRouterDeployment(ci, r.Config.RouterImage, infraConfig)
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
// clusteringress are deleted.
func (r *reconciler) ensureRouterDeleted(ci *operatorv1.IngressController) error {
	deployment := manifests.RouterDeployment(ci)
	name := routerDeploymentName(ci)
	deployment.Name = name.Name
	deployment.Namespace = name.Namespace
	if err := r.Client.Delete(context.TODO(), deployment); !errors.IsNotFound(err) {
		return err
	}
	return nil
}

// desiredRouterDeployment returns the desired router deployment.
func desiredRouterDeployment(ci *operatorv1.IngressController, routerImage string, infraConfig *configv1.Infrastructure) (*appsv1.Deployment, error) {
	deployment := manifests.RouterDeployment(ci)
	name := routerDeploymentName(ci)
	deployment.Name = name.Name
	deployment.Namespace = name.Namespace

	if deployment.Labels == nil {
		deployment.Labels = map[string]string{}
	}
	deployment.Labels[manifests.OwningClusterIngressLabel] = ci.Name

	if deployment.Spec.Template.Labels == nil {
		deployment.Spec.Template.Labels = map[string]string{}
	}
	deployment.Spec.Template.Labels["router"] = deployment.Name

	if deployment.Spec.Selector.MatchLabels == nil {
		deployment.Spec.Selector.MatchLabels = map[string]string{}
	}
	deployment.Spec.Selector.MatchLabels["router"] = deployment.Name

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
		if infraConfig.Status.Platform == configv1.AWSPlatform {
			env = append(env, corev1.EnvVar{Name: "ROUTER_USE_PROXY_PROTOCOL", Value: "true"})
		}
	}

	if ci.Spec.NodePlacement != nil {
		if ci.Spec.NodePlacement.NodeSelector != nil {
			nodeSelector, err := metav1.LabelSelectorAsMap(ci.Spec.NodePlacement.NodeSelector)
			if err != nil {
				return nil, fmt.Errorf("clusteringress %q has invalid spec.nodePlacement.nodeSelector: %v",
					ci.Name, err)
			}

			deployment.Spec.Template.Spec.NodeSelector = nodeSelector
		}
	}

	if ci.Spec.NamespaceSelector != nil {
		namespaceSelector, err := metav1.LabelSelectorAsSelector(ci.Spec.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("clusteringress %q has invalid spec.namespaceSelector: %v",
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
			return nil, fmt.Errorf("clusteringress %q has invalid spec.routeSelector: %v", ci.Name, err)
		}
		env = append(env, corev1.EnvVar{Name: "ROUTE_LABELS", Value: routeSelector.String()})
	}

	deployment.Spec.Template.Spec.Containers[0].Env = append(deployment.Spec.Template.Spec.Containers[0].Env, env...)

	deployment.Spec.Template.Spec.Containers[0].Image = routerImage

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
	secretName := RouterEffectiveDefaultCertificateSecretName(ci, deployment.Namespace)
	deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName = secretName.Name

	return deployment, nil
}

// currentRouterDeployment returns the current router deployment.
func (r *reconciler) currentRouterDeployment(ci *operatorv1.IngressController) (*appsv1.Deployment, error) {
	deployment := &appsv1.Deployment{}
	if err := r.Client.Get(context.TODO(), routerDeploymentName(ci), deployment); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
	}
	return deployment, nil
}

// createRouterDeployment creates a router deployment.
func (r *reconciler) createRouterDeployment(deployment *appsv1.Deployment) error {
	if err := r.Client.Create(context.TODO(), deployment); err != nil {
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

	if err := r.Client.Update(context.TODO(), updated); err != nil {
		return fmt.Errorf("failed to update router deployment %s/%s: %v", updated.Namespace, updated.Name, err)
	}
	log.Info("updated router deployment", "namespace", updated.Namespace, "name", updated.Name)
	return nil
}

// deploymentConfigChanged checks if current config matches the expected config
// for the cluster ingress deployment and if not returns the updated config.
func deploymentConfigChanged(current, expected *appsv1.Deployment) (bool, *appsv1.Deployment) {
	if cmp.Equal(current.Spec.Template.Spec.Volumes, expected.Spec.Template.Spec.Volumes, cmpopts.EquateEmpty()) &&
		cmp.Equal(current.Spec.Template.Spec.NodeSelector, expected.Spec.Template.Spec.NodeSelector, cmpopts.EquateEmpty()) &&
		cmp.Equal(current.Spec.Template.Spec.Containers[0].Env, expected.Spec.Template.Spec.Containers[0].Env, cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b corev1.EnvVar) bool { return a.Name < b.Name })) &&
		current.Spec.Template.Spec.Containers[0].Image == expected.Spec.Template.Spec.Containers[0].Image &&
		current.Spec.Replicas != nil &&
		*current.Spec.Replicas == *expected.Spec.Replicas {
		return false, nil
	}

	updated := current.DeepCopy()
	volumes := make([]corev1.Volume, len(expected.Spec.Template.Spec.Volumes))
	for i, vol := range expected.Spec.Template.Spec.Volumes {
		volumes[i] = *vol.DeepCopy()
	}
	updated.Spec.Template.Spec.Volumes = volumes
	updated.Spec.Template.Spec.NodeSelector = expected.Spec.Template.Spec.NodeSelector
	updated.Spec.Template.Spec.Containers[0].Env = expected.Spec.Template.Spec.Containers[0].Env
	updated.Spec.Template.Spec.Containers[0].Image = expected.Spec.Template.Spec.Containers[0].Image
	replicas := int32(1)
	if expected.Spec.Replicas != nil {
		replicas = *expected.Spec.Replicas
	}
	updated.Spec.Replicas = &replicas
	return true, updated
}
