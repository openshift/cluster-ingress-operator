package manifests

import (
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManifests(t *testing.T) {
	var one int32 = 1
	ci := &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: operatorv1.IngressControllerSpec{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"foo": "bar",
				},
			},
			Replicas: &one,
			RouteSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"baz": "quux",
				},
			},
		},
	}

	RouterServiceAccount()
	RouterClusterRole()
	RouterClusterRoleBinding()
	RouterStatsSecret(ci)

	MetricsClusterRole()
	MetricsClusterRoleBinding()
	MetricsRole()
	MetricsRoleBinding()

	RouterNamespace()
	RouterDeployment()
	InternalIngressControllerService()
	LoadBalancerService()

	GatewayClassCRD()
	GatewayCRD()
	HTTPRouteCRD()
	ReferenceGrantCRD()
}
