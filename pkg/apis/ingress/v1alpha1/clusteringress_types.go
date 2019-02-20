package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ClusterIngressSpec defines the desired state of ClusterIngress
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

	// Replicas is the desired number of router instances.
	Replicas int32 `json:"replicas"`

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
	// UserDefinedClusterIngressHA configures the ingress implementation
	// pods to use host networking, exposing ports 80 and 443 as endpoints
	// on the host nodes.  Implementing HA for these endpoints is left to
	// the user.
	UserDefinedClusterIngressHA ClusterIngressHAType = "UserDefined"
)

type ClusterIngressHighAvailability struct {
	// Type is a kind of HA mechanism which can apply to a cluster
	// ingress.
	Type ClusterIngressHAType `json:"type"`
}

// ClusterIngressStatus defines the observed state of ClusterIngress
type ClusterIngressStatus struct {
	// Replicas is the actual number of observed router instances.
	Replicas int32 `json:"replicas"`

	// Selector is a label selector, in string format, for pods
	// corresponding to the ClusterIngress. The number of matching pods
	// should equal the value of replicas.
	Selector string `json:"labelSelector"`

	// IngressDomain is the actual ingress domain.  If an ingress domain is
	// specified in the spec, it will be used as the actual ingress domain.
	// Otherwise the actual ingress domain will be assigned a default value
	// based on the cluster ingress config.
	IngressDomain string `json:"ingressDomain"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterIngress is the Schema for the clusteringresses API
// +k8s:openapi-gen=true
type ClusterIngress struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterIngressSpec   `json:"spec,omitempty"`
	Status ClusterIngressStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterIngressList contains a list of ClusterIngress
type ClusterIngressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterIngress `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterIngress{}, &ClusterIngressList{})
}
