package manifests

import (
	"bytes"
	"io"
	"strings"

	ingressv1alpha1 "github.com/openshift/cluster-ingress-operator/pkg/apis/ingress/v1alpha1"
	coremanifests "github.com/openshift/cluster-ingress-operator/pkg/manifests"

	routev1 "github.com/openshift/api/route/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	AppIngressNamespace     = "test/assets/app-ingress/namespace.yaml"
	AppIngressDeployment    = "test/assets/app-ingress/deployment.yaml"
	AppIngressRouteDefault  = "test/assets/app-ingress/route-default.yaml"
	AppIngressRouteInternal = "test/assets/app-ingress/route-internal.yaml"
	AppIngressService       = "test/assets/app-ingress/service.yaml"

	ClusterIngressDefault  = "test/assets/cluster-ingress-default.yaml"
	ClusterIngressInternal = "test/assets/cluster-ingress-internal.yaml"
)

func MustAssetReader(asset string) io.Reader {
	return bytes.NewReader(MustAsset(asset))
}

// Factory knows how to create ingress-related cluster resources from manifest
// files. It provides a point of control to mutate the static resources with
// provided configuration.
type Factory struct {
	*coremanifests.Factory

	clusterName string
}

func NewFactory(clusterName string) *Factory {
	return &Factory{
		Factory:     coremanifests.NewFactory(),
		clusterName: clusterName,
	}
}

func (f *Factory) AppIngressNamespace() (*corev1.Namespace, error) {
	ns, err := coremanifests.NewNamespace(MustAssetReader(AppIngressNamespace))
	if err != nil {
		return nil, err
	}
	return ns, nil
}

func (f *Factory) AppIngressDeployment() (*appsv1.Deployment, error) {
	d, err := coremanifests.NewDeployment(MustAssetReader(AppIngressDeployment))
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (f *Factory) AppIngressRouteDefault() (*routev1.Route, error) {
	r, err := coremanifests.NewRoute(MustAssetReader(AppIngressRouteDefault))
	if err != nil {
		return nil, err
	}
	r.Spec.Host = strings.Replace(r.Spec.Host, "CLUSTER_NAME", f.clusterName, -1)
	return r, nil
}

func (f *Factory) AppIngressRouteInternal() (*routev1.Route, error) {
	r, err := coremanifests.NewRoute(MustAssetReader(AppIngressRouteInternal))
	if err != nil {
		return nil, err
	}
	r.Spec.Host = strings.Replace(r.Spec.Host, "CLUSTER_NAME", f.clusterName, -1)
	return r, nil
}

func (f *Factory) AppIngressService() (*corev1.Service, error) {
	s, err := coremanifests.NewService(MustAssetReader(AppIngressService))
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (f *Factory) ClusterIngressDefault() (*ingressv1alpha1.ClusterIngress, error) {
	ci, err := coremanifests.NewClusterIngress(MustAssetReader(ClusterIngressDefault))
	if err != nil {
		return nil, err
	}
	ingressDomain := strings.Replace(*ci.Spec.IngressDomain, "CLUSTER_NAME", f.clusterName, -1)
	ci.Spec.IngressDomain = &ingressDomain
	return ci, nil
}

func (f *Factory) ClusterIngressInternal() (*ingressv1alpha1.ClusterIngress, error) {
	ci, err := coremanifests.NewClusterIngress(MustAssetReader(ClusterIngressInternal))
	if err != nil {
		return nil, err
	}
	ingressDomain := strings.Replace(*ci.Spec.IngressDomain, "CLUSTER_NAME", f.clusterName, -1)
	ci.Spec.IngressDomain = &ingressDomain
	return ci, nil
}
