package manifests

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	operatorconfig "github.com/openshift/cluster-ingress-operator/pkg/operator/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apiserver/pkg/storage/names"

	routev1 "github.com/openshift/api/route/v1"
)

const (
	ClusterIngressDefaults   = "assets/defaults/cluster-ingress.yaml"
	RouterNamespace          = "assets/router/namespace.yaml"
	RouterServiceAccount     = "assets/router/service-account.yaml"
	RouterClusterRole        = "assets/router/cluster-role.yaml"
	RouterClusterRoleBinding = "assets/router/cluster-role-binding.yaml"
	RouterDeploymentAsset    = "assets/router/deployment.yaml"
	RouterServiceInternal    = "assets/router/service-internal.yaml"
	RouterServiceCloud       = "assets/router/service-cloud.yaml"
	OperatorRole             = "assets/router/operator-role.yaml"
	OperatorRoleBinding      = "assets/router/operator-role-binding.yaml"

	MetricsClusterRole        = "assets/router/metrics/cluster-role.yaml"
	MetricsClusterRoleBinding = "assets/router/metrics/cluster-role-binding.yaml"
	MetricsRole               = "assets/router/metrics/role.yaml"
	MetricsRoleBinding        = "assets/router/metrics/role-binding.yaml"

	// Annotation used to inform the certificate generation service to
	// generate a cluster-signed certificate and populate the secret.
	ServingCertSecretAnnotation = "service.alpha.openshift.io/serving-cert-secret-name"

	// OwningClusterIngressLabel should be applied to any objects "owned by" a
	// clusteringress to aid in selection (especially in cases where an ownerref
	// can't be established due to namespace boundaries).
	OwningClusterIngressLabel = "ingress.openshift.io/clusteringress"
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

func DefaultClusterIngress() *ingressv1alpha1.ClusterIngress {
	ci, err := NewClusterIngress(MustAssetReader(ClusterIngressDefaults))
	if err != nil {
		panic(err)
	}
	return ci
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

func RouterDeployment(cr *ingressv1alpha1.ClusterIngress) *appsv1.Deployment {
	deployment, err := NewDeployment(MustAssetReader(RouterDeploymentAsset))
	if err != nil {
		panic(err)
	}
	return deployment
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
	s.Labels[OwningClusterIngressLabel] = cr.Name

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

func LoadBalancerService() *corev1.Service {
	s, err := NewService(MustAssetReader(RouterServiceCloud))
	if err != nil {
		panic(err)
	}
	return s
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
