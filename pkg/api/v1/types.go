// +k8s:deepcopy-gen=package,register
// +k8s:defaulter-gen=TypeMeta
// +k8s:openapi-gen=true
// +groupName=ingress.operator.openshift.io
package v1

import (
	configv1 "github.com/openshift/api/config/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// DNSRecord represents a DNS record.
type DNSRecord struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DNSRecordSpec   `json:"spec,omitempty"`
	Status DNSRecordStatus `json:"status,omitempty"`
}

type DNSRecordSpec struct {
	// The hostname of the DNS record
	DNSName string `json:"dnsName,omitempty"`
	// The targets the DNS record points to
	Targets []string `json:"targets,omitempty"`
	// RecordType type of record, e.g. CNAME, A, SRV, TXT etc
	RecordType string `json:"recordType,omitempty"`
	// TTL for the record
	//
	// +optional
	RecordTTL int64 `json:"recordTTL,omitempty"`
}

type DNSRecordStatus struct {
	Zones []DNSZoneStatus `json:"zones,omitempty"`
}

type DNSZoneStatus struct {
	DNSZone    configv1.DNSZone   `json:"dnsZone"`
	Conditions []DNSZoneCondition `json:"conditions,omitempty"`
}

var (
	DNSRecordFailedConditionType = "Failed"
)

type DNSZoneCondition struct {
	Type               string      `json:"type"`
	Status             string      `json:"status"`
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
	Reason             string      `json:"reason,omitempty"`
	Message            string      `json:"message,omitempty"`
}

type DNSRecordType string

const (
	CNAMERecordType string = "CNAME"

	ARecordType string = "A"
)

// +kubebuilder:object:root=true

type DNSRecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DNSRecord `json:"items"`
}

const (
	IngressControllerAdmittedConditionType           = "Admitted"
	IngressControllerDeploymentDegradedConditionType = "DeploymentDegraded"
)

func init() {
	SchemeBuilder.Register(&DNSRecord{}, &DNSRecordList{})
}
