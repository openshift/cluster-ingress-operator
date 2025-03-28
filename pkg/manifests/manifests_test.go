package manifests

import (
	"slices"
	"strings"
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
	GRPCRouteCRD()
	HTTPRouteCRD()
	ReferenceGrantCRD()

	adminOnlyVerbs := []string{"create", "update", "patch", "delete", "deletecollection"}
	GatewayAPIAdminClusterRole()
	viewClusterRole := GatewayAPIViewClusterRole()
	for _, policyRule := range viewClusterRole.Rules {
		for _, adminOnlyVerb := range adminOnlyVerbs {
			if slices.ContainsFunc(policyRule.Verbs, func(verb string) bool {
				return strings.EqualFold(verb, adminOnlyVerb)
			}) {
				t.Errorf("view role %s should only contain read verbs, found: %s", viewClusterRole.Name, adminOnlyVerb)
			}
		}
	}

	MustAsset(CustomResourceDefinitionManifest)
	MustAsset(NamespaceManifest)
}
