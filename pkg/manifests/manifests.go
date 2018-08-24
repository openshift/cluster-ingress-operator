package manifests

import (
	"bytes"
	"io"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	DefaultRouterNamespace = "openshift-cluster-ingress-router"

	RouterServiceAccount     = "assets/router/service-account.yaml"
	RouterClusterRole        = "assets/router/cluster-role.yaml"
	RouterClusterRoleBinding = "assets/router/cluster-role-binding.yaml"
	RouterDaemonSet          = "assets/router/daemonset.yaml"
	RouterServiceCloud       = "assets/router/service-cloud.yaml"
)

func MustAssetReader(asset string) io.Reader {
	return bytes.NewReader(MustAsset(asset))
}

type Factory struct {
	routerNamespace string
}

func NewFactory() *Factory {
	return &Factory{
		routerNamespace: DefaultRouterNamespace,
	}
}

func (f *Factory) RouterNamespace() (*corev1.Namespace, error) {
	ns := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: f.routerNamespace,
			Annotations: map[string]string{
				"openshift.io/node-selector": "",
			},
		},
	}
	return ns, nil
}

func (f *Factory) RouterServiceAccount() (*corev1.ServiceAccount, error) {
	sa, err := NewServiceAccount(MustAssetReader(RouterServiceAccount))
	if err != nil {
		return nil, err
	}
	sa.Namespace = f.routerNamespace
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
	crb.Subjects[0].Namespace = f.routerNamespace
	return crb, nil
}

func (f *Factory) RouterDaemonSet(cr *ingressv1alpha1.ClusterIngress) (*appsv1.DaemonSet, error) {
	ds, err := NewDaemonSet(MustAssetReader(RouterDaemonSet))
	if err != nil {
		return nil, err
	}

	name := "router-" + cr.Name

	ds.ObjectMeta = metav1.ObjectMeta{
		Name:      name,
		Namespace: f.routerNamespace,
		Labels: map[string]string{
			"app": "router",
		},
	}

	ds.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app": name,
		},
	}

	ds.Spec.Template.ObjectMeta.Labels["app"] = name

	return ds, nil
}

func (f *Factory) RouterServiceCloud(cr *ingressv1alpha1.ClusterIngress) (*corev1.Service, error) {
	s, err := NewService(MustAssetReader(RouterServiceCloud))
	if err != nil {
		return nil, err
	}

	name := "router-" + cr.Name

	s.ObjectMeta = metav1.ObjectMeta{
		Name:      name,
		Namespace: f.routerNamespace,
		Labels: map[string]string{
			"app": "router",
		},
	}

	s.Spec.Selector = map[string]string{
		"app": name,
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
