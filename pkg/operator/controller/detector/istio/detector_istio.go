package istio

import (
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/crd"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/detector"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/detector/label"

	operatorcontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var AllIstioWatchTypes []metav1.TypeMeta = []metav1.TypeMeta{
	{APIVersion: "networking.istio.io/v1", Kind: "Gateway"},
	{APIVersion: "networking.istio.io/v1", Kind: "VirtualService"},
	{APIVersion: "networking.istio.io/v1", Kind: "DestinationRule"},
	{APIVersion: "networking.istio.io/v1", Kind: "ServiceEntry"},
	{APIVersion: "networking.istio.io/v1", Kind: "Sidecar"},
	{APIVersion: "networking.istio.io/v1", Kind: "WorkloadEntry"},
	{APIVersion: "networking.istio.io/v1", Kind: "WorkloadGroup"},
	{APIVersion: "networking.istio.io/v1beta1", Kind: "ProxyConfig"},
	{APIVersion: "networking.istio.io/v1alpha3", Kind: "EnvoyFilter"},
	{APIVersion: "security.istio.io/v1", Kind: "AuthorizationPolicy"},
	{APIVersion: "security.istio.io/v1", Kind: "PeerAuthentication"},
	{APIVersion: "security.istio.io/v1", Kind: "RequestAuthentication"},
	{APIVersion: "telemetry.istio.io/v1", Kind: "Telemetry"},
}

func SetupDetectors(mgr manager.Manager, statusReporter detector.StatusReporter, mappings map[metav1.GroupKind][]crd.ControllerFunc) {

	ignoreOwnerRefs := []metav1.OwnerReference{}
	ignoreLabels := map[string]string{
		"kuadrant.io/managed": "true", // kuadrant is allowed to configure our control plane
	}

	for i := range AllIstioWatchTypes {
		watchedResource := AllIstioWatchTypes[i]

		gk := metav1.GroupKind{
			Group: watchedResource.GetObjectKind().GroupVersionKind().Group,
			Kind:  watchedResource.GroupVersionKind().Kind}

		ctrlFunc := func(mgr manager.Manager, statusReporter detector.StatusReporter) func() (controller.Controller, error) {
			return func() (controller.Controller, error) {
				return label.NewLabelMatch(mgr, statusReporter, label.Config{
					LabelName:       operatorcontroller.IstioRevLabelKey,
					LabelValue:      operatorcontroller.IstioName("").Name,
					WatchResource:   watchedResource,
					IgnoreOwnerRefs: ignoreOwnerRefs,
					IgnoreLabels:    ignoreLabels,
				})
			}
		}(mgr, statusReporter)

		mappings[gk] = append(mappings[gk], ctrlFunc)
	}
}
