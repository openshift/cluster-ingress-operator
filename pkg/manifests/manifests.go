package manifests

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"path/filepath"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apiserver/pkg/storage/names"

	configv1 "github.com/openshift/api/config/v1"
	routev1 "github.com/openshift/api/route/v1"

	monv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
)

const (
	ClusterIngressDefaults   = "assets/defaults/cluster-ingress.yaml"
	RouterNamespace          = "assets/router/namespace.yaml"
	RouterServiceAccount     = "assets/router/service-account.yaml"
	RouterClusterRole        = "assets/router/cluster-role.yaml"
	RouterClusterRoleBinding = "assets/router/cluster-role-binding.yaml"
	RouterDeployment         = "assets/router/deployment.yaml"
	RouterServiceInternal    = "assets/router/service-internal.yaml"
	RouterServiceCloud       = "assets/router/service-cloud.yaml"
	OperatorRole             = "assets/router/operator-role.yaml"
	OperatorRoleBinding      = "assets/router/operator-role-binding.yaml"

	MetricsServiceMonitor     = "assets/router/metrics/service-monitor.yaml"
	MetricsClusterRole        = "assets/router/metrics/cluster-role.yaml"
	MetricsClusterRoleBinding = "assets/router/metrics/cluster-role-binding.yaml"
	MetricsRole               = "assets/router/metrics/role.yaml"
	MetricsRoleBinding        = "assets/router/metrics/role-binding.yaml"

	// Annotation used to inform the certificate generation service to
	// generate a cluster-signed certificate and populate the secret.
	ServingCertSecretAnnotation = "service.alpha.openshift.io/serving-cert-secret-name"

	// Annotation used to enable the proxy protocol on the AWS load balancer.
	AWSLBProxyProtocolAnnotation = "service.beta.kubernetes.io/aws-load-balancer-proxy-protocol"
)

func MustAssetReader(asset string) io.Reader {
	return bytes.NewReader(MustAsset(asset))
}

// Factory knows how to create ingress-related cluster resources from manifest
// files. It provides a point of control to mutate the static resources with
// provided configuration.
type Factory struct {
	config operatorconfig.Config
}

func NewFactory(config operatorconfig.Config) *Factory {
	return &Factory{config: config}
}

func (f *Factory) DefaultClusterIngress() (*ingressv1alpha1.ClusterIngress, error) {
	ci, err := NewClusterIngress(MustAssetReader(ClusterIngressDefaults))
	if err != nil {
		return nil, err
	}
	if len(f.config.DefaultIngressDomain) != 0 {
		ci.Spec.IngressDomain = &f.config.DefaultIngressDomain
	}
	if ci.Spec.HighAvailability == nil {
		ci.Spec.HighAvailability = &ingressv1alpha1.ClusterIngressHighAvailability{}
	}
	switch f.config.Platform {
	case configv1.AWSPlatform:
		ci.Spec.HighAvailability.Type = ingressv1alpha1.CloudClusterIngressHA
	default:
		ci.Spec.HighAvailability.Type = ingressv1alpha1.UserDefinedClusterIngressHA
	}
	return ci, nil
}

func (f *Factory) OperatorRole() (*rbacv1.Role, error) {
	crb, err := NewRole(MustAssetReader(OperatorRole))
	if err != nil {
		return nil, err
	}
	return crb, nil
}

func (f *Factory) OperatorRoleBinding() (*rbacv1.RoleBinding, error) {
	crb, err := NewRoleBinding(MustAssetReader(OperatorRoleBinding))
	if err != nil {
		return nil, err
	}
	return crb, nil
}

func (f *Factory) RouterNamespace() (*corev1.Namespace, error) {
	ns, err := NewNamespace(MustAssetReader(RouterNamespace))
	if err != nil {
		return nil, err
	}
	return ns, nil
}

func (f *Factory) RouterServiceAccount() (*corev1.ServiceAccount, error) {
	sa, err := NewServiceAccount(MustAssetReader(RouterServiceAccount))
	if err != nil {
		return nil, err
	}
	return sa, nil
}

func (f *Factory) RouterClusterRole() (*rbacv1.ClusterRole, error) {
	cr, err := NewClusterRole(MustAssetReader(RouterClusterRole))
	if err != nil {
		return nil, err
	}
	return cr, nil
}

func (f *Factory) RouterClusterRoleBinding() (*rbacv1.ClusterRoleBinding, error) {
	crb, err := NewClusterRoleBinding(MustAssetReader(RouterClusterRoleBinding))
	if err != nil {
		return nil, err
	}
	return crb, nil
}

func (f *Factory) RouterStatsSecret(cr *ingressv1alpha1.ClusterIngress) (*corev1.Secret, error) {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("router-stats-%s", cr.Name),
			Namespace: "openshift-ingress",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{},
	}

	generatedUser := names.SimpleNameGenerator.GenerateName("user")
	generatedPassword := names.SimpleNameGenerator.GenerateName("pass")
	s.Data["statsUsername"] = []byte(base64.StdEncoding.EncodeToString([]byte(generatedUser)))
	s.Data["statsPassword"] = []byte(base64.StdEncoding.EncodeToString([]byte(generatedPassword)))
	return s, nil
}

func (f *Factory) RouterDeployment(cr *ingressv1alpha1.ClusterIngress) (*appsv1.Deployment, error) {
	deployment, err := NewDeployment(MustAssetReader(RouterDeployment))
	if err != nil {
		return nil, err
	}

	name := "router-" + cr.Name

	deployment.Name = name

	if deployment.Spec.Template.Labels == nil {
		deployment.Spec.Template.Labels = map[string]string{}
	}
	deployment.Spec.Template.Labels["router"] = name

	if deployment.Spec.Selector.MatchLabels == nil {
		deployment.Spec.Selector.MatchLabels = map[string]string{}
	}
	deployment.Spec.Selector.MatchLabels["router"] = name

	statsSecretName := fmt.Sprintf("router-stats-%s", cr.Name)
	env := []corev1.EnvVar{
		{Name: "ROUTER_SERVICE_NAME", Value: cr.Name},
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
	certsSecretName := fmt.Sprintf("router-metrics-certs-%s", cr.Name)
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

	if cr.Spec.IngressDomain != nil {
		env = append(env, corev1.EnvVar{Name: "ROUTER_CANONICAL_HOSTNAME", Value: *cr.Spec.IngressDomain})
	}

	if cr.Spec.HighAvailability != nil && cr.Spec.HighAvailability.Type == ingressv1alpha1.CloudClusterIngressHA {
		// For now, check if we are on AWS. This can really be done for
		// for any external [cloud] LBs that support the proxy protocol.
		if f.config.Platform == configv1.AWSPlatform {
			env = append(env, corev1.EnvVar{Name: "ROUTER_USE_PROXY_PROTOCOL", Value: "true"})
		}
	}

	if cr.Spec.NodePlacement != nil {
		if cr.Spec.NodePlacement.NodeSelector != nil {
			nodeSelector, err := metav1.LabelSelectorAsMap(cr.Spec.NodePlacement.NodeSelector)
			if err != nil {
				return nil, fmt.Errorf("clusteringress %q has invalid spec.nodePlacement.nodeSelector: %v",
					cr.Name, err)
			}

			deployment.Spec.Template.Spec.NodeSelector = nodeSelector
		}
	}

	if cr.Spec.NamespaceSelector != nil {
		namespaceSelector, err := metav1.LabelSelectorAsSelector(cr.Spec.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("clusteringress %q has invalid spec.namespaceSelector: %v",
				cr.Name, err)
		}

		env = append(env, corev1.EnvVar{
			Name:  "NAMESPACE_LABELS",
			Value: namespaceSelector.String(),
		})
	}

	replicas := cr.Spec.Replicas
	deployment.Spec.Replicas = &replicas

	if cr.Spec.RouteSelector != nil {
		routeSelector, err := metav1.LabelSelectorAsSelector(cr.Spec.RouteSelector)
		if err != nil {
			return nil, fmt.Errorf("clusteringress %q has invalid spec.routeSelector: %v", cr.Name, err)
		}
		env = append(env, corev1.EnvVar{Name: "ROUTE_LABELS", Value: routeSelector.String()})
	}

	deployment.Spec.Template.Spec.Containers[0].Env = append(deployment.Spec.Template.Spec.Containers[0].Env, env...)

	deployment.Spec.Template.Spec.Containers[0].Image = f.config.RouterImage

	if cr.Spec.HighAvailability != nil && cr.Spec.HighAvailability.Type == ingressv1alpha1.UserDefinedClusterIngressHA {
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
	secretName := fmt.Sprintf("router-certs-%s", cr.Name)
	if cr.Spec.DefaultCertificateSecret != nil && len(*cr.Spec.DefaultCertificateSecret) > 0 {
		secretName = *cr.Spec.DefaultCertificateSecret
	}
	deployment.Spec.Template.Spec.Volumes[0].Secret.SecretName = secretName

	return deployment, nil
}

func (f *Factory) RouterServiceInternal(cr *ingressv1alpha1.ClusterIngress) (*corev1.Service, error) {
	s, err := NewService(MustAssetReader(RouterServiceInternal))
	if err != nil {
		return nil, err
	}

	name := "router-internal-" + cr.Name

	s.Name = name

	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	s.Labels["router"] = name

	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[ServingCertSecretAnnotation] = fmt.Sprintf("router-metrics-certs-%s", cr.Name)

	if s.Spec.Selector == nil {
		s.Spec.Selector = map[string]string{}
	}
	s.Spec.Selector["router"] = "router-" + cr.Name

	return s, nil
}

func (f *Factory) RouterServiceCloud(cr *ingressv1alpha1.ClusterIngress) (*corev1.Service, error) {
	s, err := NewService(MustAssetReader(RouterServiceCloud))
	if err != nil {
		return nil, err
	}

	name := "router-" + cr.Name

	s.Name = name

	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	s.Labels["router"] = name

	if s.Spec.Selector == nil {
		s.Spec.Selector = map[string]string{}
	}
	s.Spec.Selector["router"] = name

	if f.config.Platform == configv1.AWSPlatform {
		if s.Annotations == nil {
			s.Annotations = map[string]string{}
		}
		s.Annotations[AWSLBProxyProtocolAnnotation] = "*"
	}

	return s, nil
}

func (f *Factory) MetricsServiceMonitor(ci *ingressv1alpha1.ClusterIngress, svc *corev1.Service) (*monv1.ServiceMonitor, error) {
	sm, err := NewServiceMonitor(MustAssetReader(MetricsServiceMonitor))
	if err != nil {
		return nil, err
	}
	sm.Name = "router-" + ci.Name

	for i := range sm.Spec.Endpoints {
		if sm.Spec.Endpoints[i].Path == "/metrics" {
			sm.Spec.Endpoints[i].TLSConfig.ServerName = fmt.Sprintf("%s.%s.svc", svc.Name, svc.Namespace)
		}
	}
	return sm, nil
}

func (f *Factory) MetricsClusterRole() (*rbacv1.ClusterRole, error) {
	cr, err := NewClusterRole(MustAssetReader(MetricsClusterRole))
	if err != nil {
		return nil, err
	}
	return cr, nil
}

func (f *Factory) MetricsClusterRoleBinding() (*rbacv1.ClusterRoleBinding, error) {
	crb, err := NewClusterRoleBinding(MustAssetReader(MetricsClusterRoleBinding))
	if err != nil {
		return nil, err
	}
	return crb, nil
}

func (f *Factory) MetricsRole() (*rbacv1.Role, error) {
	r, err := NewRole(MustAssetReader(MetricsRole))
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (f *Factory) MetricsRoleBinding() (*rbacv1.RoleBinding, error) {
	rb, err := NewRoleBinding(MustAssetReader(MetricsRoleBinding))
	if err != nil {
		return nil, err
	}
	return rb, nil
}

func NewServiceMonitor(manifest io.Reader) (*monv1.ServiceMonitor, error) {
	sm := monv1.ServiceMonitor{}
	err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&sm)
	if err != nil {
		return nil, err
	}
	return &sm, nil
}

func NewServiceAccount(manifest io.Reader) (*corev1.ServiceAccount, error) {
	sa := corev1.ServiceAccount{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&sa); err != nil {
		return nil, err
	}

	return &sa, nil
}

func NewRole(manifest io.Reader) (*rbacv1.Role, error) {
	r := rbacv1.Role{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&r); err != nil {
		return nil, err
	}

	return &r, nil
}

func NewRoleBinding(manifest io.Reader) (*rbacv1.RoleBinding, error) {
	rb := rbacv1.RoleBinding{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&rb); err != nil {
		return nil, err
	}

	return &rb, nil
}

func NewClusterRole(manifest io.Reader) (*rbacv1.ClusterRole, error) {
	cr := rbacv1.ClusterRole{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&cr); err != nil {
		return nil, err
	}

	return &cr, nil
}

func NewClusterRoleBinding(manifest io.Reader) (*rbacv1.ClusterRoleBinding, error) {
	crb := rbacv1.ClusterRoleBinding{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&crb); err != nil {
		return nil, err
	}

	return &crb, nil
}

func NewService(manifest io.Reader) (*corev1.Service, error) {
	s := corev1.Service{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&s); err != nil {
		return nil, err
	}

	return &s, nil
}

func NewNamespace(manifest io.Reader) (*corev1.Namespace, error) {
	ns := corev1.Namespace{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&ns); err != nil {
		return nil, err
	}

	return &ns, nil
}

func NewDeployment(manifest io.Reader) (*appsv1.Deployment, error) {
	o := appsv1.Deployment{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&o); err != nil {
		return nil, err
	}

	return &o, nil
}

func NewRoute(manifest io.Reader) (*routev1.Route, error) {
	o := routev1.Route{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&o); err != nil {
		return nil, err
	}

	return &o, nil
}

func NewClusterIngress(manifest io.Reader) (*ingressv1alpha1.ClusterIngress, error) {
	o := ingressv1alpha1.ClusterIngress{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&o); err != nil {
		return nil, err
	}

	return &o, nil
}

func NewCustomResourceDefinition(manifest io.Reader) (*apiextensionsv1beta1.CustomResourceDefinition, error) {
	crd := apiextensionsv1beta1.CustomResourceDefinition{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&crd); err != nil {
		return nil, err
	}
	return &crd, nil
}
