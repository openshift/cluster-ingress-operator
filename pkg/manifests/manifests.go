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

	routev1 "github.com/openshift/api/route/v1"
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

	if ds.Spec.Template.Labels == nil {
		ds.Spec.Template.Labels = map[string]string{}
	}
	ds.Spec.Template.Labels["router"] = name

	if ds.Spec.Selector.MatchLabels == nil {
		ds.Spec.Selector.MatchLabels = map[string]string{}
	}
	ds.Spec.Selector.MatchLabels["router"] = name

	env := []corev1.EnvVar{
		{Name: "ROUTER_SERVICE_NAME", Value: cr.Name},
	}

	if cr.Spec.IngressDomain != nil {
		env = append(env, corev1.EnvVar{Name: "ROUTER_CANONICAL_HOSTNAME", Value: *cr.Spec.IngressDomain})
	}

	if cr.Spec.NodePlacement != nil {
		if cr.Spec.NodePlacement.NodeSelector != nil {
			nodeSelector, err := metav1.LabelSelectorAsMap(cr.Spec.NodePlacement.NodeSelector)
			if err != nil {
				return nil, fmt.Errorf("clusteringress %q has invalid spec.nodePlacement.nodeSelector: %v",
				                       cr.Name, err)
			}

			ds.Spec.Template.Spec.NodeSelector = nodeSelector
		}
	}

	if cr.Spec.NamespaceSelector != nil {
		namespaceSelector, err := metav1.LabelSelectorAsSelector(cr.Spec.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("clusteringress %q has invalid spec.namespaceSelector: %v",
			                       cr.Name, err)
		}

		env = append(env, corev1.EnvVar{
			Name: "NAMESPACE_LABELS",
			Value: namespaceSelector.String(),
		})
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

	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	s.Labels["router"] = name

	if s.Spec.Selector == nil {
		s.Spec.Selector = map[string]string{}
	}
	s.Spec.Selector["router"] = name

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
