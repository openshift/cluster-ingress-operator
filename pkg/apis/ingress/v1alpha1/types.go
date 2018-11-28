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
	// status of Routes to inform downstream systems.
	IngressDomain *string `json:"ingressDomain"`

	// NodePlacement provides additional control over the
	// scheduling of ingress implementation pods.
	NodePlacement *NodePlacement `json:"nodePlacement"`

	// DefaultCertificateSecret is the name of a secret containing the
	// default certificate configuration (tls.{crt,key}).
	DefaultCertificateSecret *string `json:"defaultCertificateSecret"`

	// NamespaceSelector is a label selector which filters the
	// namespaces covered by this ClusterIngress.
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector"`

	// RouteSelector is a label selector which filters the Routes
	// covered by this ClusterIngress.
	RouteSelector *metav1.LabelSelector `json:"routeSelector"`

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
	NodeSelector *metav1.LabelSelector `json:"nodeSelector"`

	// TODO: affinity, tolerations
}

type ClusterIngressHAType string

const (
	// CloudClusterIngressHA is a type of HA implemented by fronting
	// the cluster ingress implementation with a Service Load Balancer.
	CloudClusterIngressHA ClusterIngressHAType = "Cloud"
)

type ClusterIngressHighAvailability struct {
	// Type is a kind of HA mechanism which can apply to a cluster
	// ingress.
	Type ClusterIngressHAType `json:"type"`
}

type ClusterIngressStatus struct {
	// Fill me
}
