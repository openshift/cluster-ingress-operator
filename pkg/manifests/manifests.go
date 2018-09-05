package manifests

import (
	"bytes"
	"fmt"
	"io"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	RouterNamespace          = "assets/router/namespace.yaml"
	RouterServiceAccount     = "assets/router/service-account.yaml"
	RouterClusterRole        = "assets/router/cluster-role.yaml"
	RouterClusterRoleBinding = "assets/router/cluster-role-binding.yaml"
	RouterDaemonSet          = "assets/router/daemonset.yaml"
	RouterServiceCloud       = "assets/router/service-cloud.yaml"
)

func MustAssetReader(asset string) io.Reader {
	return bytes.NewReader(MustAsset(asset))
}

// Factory knows how to create ingress-related cluster resources from manifest
// files. It provides a point of control to mutate the static resources with
// provided configuration.
type Factory struct {
}

func NewFactory() *Factory {
	return &Factory{}
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

func (f *Factory) RouterDaemonSet(cr *ingressv1alpha1.ClusterIngress) (*appsv1.DaemonSet, error) {
	ds, err := NewDaemonSet(MustAssetReader(RouterDaemonSet))
	if err != nil {
		return nil, err
	}

	name := "router-" + cr.Name

	ds.Name = name

	ds.Spec.Template.Labels = map[string]string{
		"app":    "router",
		"router": name,
	}

	ds.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app":    "router",
			"router": name,
		},
	}

	env := []corev1.EnvVar{
		{Name: "ROUTER_SERVICE_NAME", Value: cr.Name},
	}

	if cr.Spec.IngressDomain != nil {
		env = append(env, corev1.EnvVar{Name: "ROUTER_CANONICAL_HOSTNAME", Value: *cr.Spec.IngressDomain})
	}

	if cr.Spec.RouteSelector != nil {
		routeSelector, err := metav1.LabelSelectorAsSelector(cr.Spec.RouteSelector)
		if err != nil {
			return nil, fmt.Errorf("clusteringress %q has invalid spec.routeSelector: %v", cr.Name, err)
		}
		env = append(env, corev1.EnvVar{Name: "ROUTE_LABELS", Value: routeSelector.String()})
	}

	ds.Spec.Template.Spec.Containers[0].Env = append(ds.Spec.Template.Spec.Containers[0].Env, env...)

	return ds, nil
}

func (f *Factory) RouterServiceCloud(cr *ingressv1alpha1.ClusterIngress) (*corev1.Service, error) {
	s, err := NewService(MustAssetReader(RouterServiceCloud))
	if err != nil {
		return nil, err
	}

	name := "router-" + cr.Name

	s.Name = name

	s.Labels = map[string]string{
		"app":    "router",
		"router": name,
	}

	s.Spec.Selector = map[string]string{
		"app":    "router",
		"router": name,
	}

	return s, nil
}

func NewServiceAccount(manifest io.Reader) (*corev1.ServiceAccount, error) {
	sa := corev1.ServiceAccount{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&sa); err != nil {
		return nil, err
	}

	return &sa, nil
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

func NewDaemonSet(manifest io.Reader) (*appsv1.DaemonSet, error) {
	ds := appsv1.DaemonSet{}
	if err := yaml.NewYAMLOrJSONDecoder(manifest, 100).Decode(&ds); err != nil {
		return nil, err
	}

	return &ds, nil
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
