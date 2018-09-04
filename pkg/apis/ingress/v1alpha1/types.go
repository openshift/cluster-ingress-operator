package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type ClusterIngressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []ClusterIngress `json:"items"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type ClusterIngress struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              ClusterIngressSpec   `json:"spec"`
	Status            ClusterIngressStatus `json:"status,omitempty"`
}

type ClusterIngressSpec struct {
	// IngressDomain is a DNS name suffix serviced by the
	// ClusterIngress. This value is added automatically to the
	// status of Routes to inform downstream systems. Required.
	IngressDomain string `json:"ingressDomain"`

	// NodePlacement provides additional control over the
	// scheduling of ingress implementation pods.
	NodePlacement *NodePlacement `json:"nodePlacement"`

	// NamespaceSelector is a label selector which filters the
	// namespaces covered by this ClusterIngress.
	NamespaceSelector *map[string]string `json:"namespaceSelector"`

	// RouteSelector is a label selector which filters the Routes
	// covered by this ClusterIngress.
	RouteSelector *map[string]string `json:"routeSelector"`

	// HighAvailability describes the kind of HA mechanism to apply to
	// the cluster ingress. For cloud environments which support it,
	// CloudClusterIngressHA is the default.
	HighAvailability *ClusterIngressHighAvailability `json:"highAvailability"`

	// UnsupportedExtensions is a list of JSON patches to router
	// deployments created for this ClusterIngress. This field is
	// to facilitate backwards compatibility and when used results in
	// an unsupported cluster ingress configuration.
	UnsupportedExtensions *[]string `json:"unsupportedExtensions"`
}

// NodePlacement wraps node scheduling constructs like selectors
// and affinity rules.
type NodePlacement struct {
	// NodeSelector is a label selector used to inform pod
	// scheduling.
	NodeSelector map[string]string `json:"nodeSelector"`

	// TODO: affinity, tolerations
}

type ClusterIngressHAType string

const (
	// CloudClusterIngressHA is a type of HA implemented by fronting
	// the cluster ingress implementation with a Service Load Balancer.
	CloudClusterIngressHA ClusterIngressHAType = "Cloud"

	// IPFailoverClusterIngressHighAvailability is a type of HA
	// implemented by fronting the cluster ingress implementation with
	// a keepalived cluster.
	//
	// When using this HA type, a VRRP resource must exist to provide
	// the configuration and state management for the keepalived
	// cluster.
	//
	// OpenShift will create IPFailover resources as necessary to
	// materialize the keepalived cluster based on existing managed
	// ClusterIngress instances.
	IPFailoverClusterIngressHA ClusterIngressHAType = "IPFailover"
)

type ClusterIngressHighAvailability struct {
	// Type is a kind of HA mechanism which can apply to a cluster
	// ingress.
	Type ClusterIngressHAType `json:"type"`
}

type ClusterIngressStatus struct {
	// Fill me
}
