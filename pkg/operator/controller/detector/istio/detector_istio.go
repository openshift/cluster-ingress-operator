package detector

import (
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/crd"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/detector"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller/detector/label"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const IstioRevisionLabel = "istio.io/rev"

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

type ControllerStatus struct {
	Controller controller.Controller
	Error      error
}

func AddCRDControlFuncs(config Config, mgr manager.Manager, mappings map[metav1.GroupKind][]crd.ControllerFunc) {

	for _, watchResource := range config.WatchedResources {

		c, err := label.NewLabelMatch(mgr, statusReporter, label.Config{
			LabelName:       IstioRevisionLabel,
			LabelValue:      config.Revision,
			WatchResource:   watchResource,
			IgnoreOwnerRefs: config.IgnoreOwnerRefs,
			IgnoreLabels:    config.IgnoreLabels,
		})

}

func Setup(mgr manager.Manager, config Config, statusReporter detector.StatusReporter) []ControllerStatus {
	status := []ControllerStatus{}

	for _, watchResource := range config.WatchedResources {
		c, err := label.NewLabelMatch(mgr, statusReporter, label.Config{
			LabelName:       IstioRevisionLabel,
			LabelValue:      config.Revision,
			WatchResource:   watchResource,
			IgnoreOwnerRefs: config.IgnoreOwnerRefs,
			IgnoreLabels:    config.IgnoreLabels,
		})
		status = append(status, ControllerStatus{Controller: c, Error: err})
	}

	return status
}

// Config holds all the configuration that must be provided when creating the
// controller.
type Config struct {
	Revision         string
	WatchedResources []metav1.TypeMeta

	IgnoreOwnerRefs []metav1.OwnerReference
	IgnoreLabels    map[string]string
}
