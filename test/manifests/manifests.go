package manifests

import (
	"bytes"
	"io"
	"strings"

	coremanifests "github.com/openshift/cluster-ingress-operator/pkg/manifests"

	routev1 "github.com/openshift/api/route/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	AppIngressNamespace  = "test/assets/app-ingress/namespace.yaml"
	AppIngressDeployment = "test/assets/app-ingress/deployment.yaml"
	AppIngressRoute      = "test/assets/app-ingress/route.yaml"
	AppIngressService    = "test/assets/app-ingress/service.yaml"
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
	namespace   string
}

func NewFactory(clusterName, namespace string) *Factory {
	return &Factory{
		Factory:     coremanifests.NewFactory(),
		clusterName: clusterName,
		namespace:   namespace,
	}
}

func (f *Factory) AppIngressNamespace() *corev1.Namespace {
	ns, err := coremanifests.NewNamespace(MustAssetReader(AppIngressNamespace))
	if err != nil {
		panic(err)
	}
	ns.Name = f.namespace
	return ns
}

func (f *Factory) AppIngressDeployment() *appsv1.Deployment {
	d, err := coremanifests.NewDeployment(MustAssetReader(AppIngressDeployment))
	if err != nil {
		panic(err)
	}
	d.Namespace = f.namespace
	return d
}

func (f *Factory) AppIngressRoute() *routev1.Route {
	r, err := coremanifests.NewRoute(MustAssetReader(AppIngressRoute))
	if err != nil {
		panic(err)
	}
	r.Namespace = f.namespace
	r.Spec.Host = strings.Replace(r.Spec.Host, "NAMESPACE", f.namespace, -1)
	r.Spec.Host = strings.Replace(r.Spec.Host, "CLUSTER_NAME", f.clusterName, -1)
	return r
}

func (f *Factory) AppIngressService() *corev1.Service {
	s, err := coremanifests.NewService(MustAssetReader(AppIngressService))
	if err != nil {
		panic(err)
	}
	s.Namespace = f.namespace
	return s
}
