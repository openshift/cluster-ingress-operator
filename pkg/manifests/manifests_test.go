package manifests

import (
	"bytes"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
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
	RouterDenyAllNetworkPolicy()
	RouterAllowNetworkPolicy()
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
	BackendTLSPolicyCRD()
	ListenerSetCRD()
	TLSRouteCRD()
	GatewayAPIAllowNetworkPolicy()
	IstiodAllowNetworkPolicy()

	CanaryNamespace()
	CanaryDaemonSet()
	CanaryService()
	CanaryRoute()
	CanaryDenyAllNetworkPolicy()
	CanaryAllowNetworkPolicy()

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

	CanaryServiceAccount()
}

func TestNoWildcardsInOperatorRBAC(t *testing.T) {
	checkPolicyRules := func(t *testing.T, label string, rules []rbacv1.PolicyRule) {
		t.Helper()
		for i, rule := range rules {
			for _, verb := range rule.Verbs {
				if verb == "*" {
					t.Errorf("%s rule %d has wildcard verb for resources %v in apiGroups %v",
						label, i, rule.Resources, rule.APIGroups)
				}
			}
			for _, resource := range rule.Resources {
				if resource == "*" {
					t.Errorf("%s rule %d has wildcard resource in apiGroups %v",
						label, i, rule.APIGroups)
				}
			}
		}
	}

	t.Run("ClusterRole", func(t *testing.T) {
		data, err := os.ReadFile("../../manifests/00-cluster-role.yaml")
		if err != nil {
			t.Fatalf("failed to read ClusterRole manifest: %v", err)
		}
		cr, err := NewClusterRole(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("failed to decode ClusterRole: %v", err)
		}
		checkPolicyRules(t, "ClusterRole "+cr.Name, cr.Rules)
	})

	t.Run("Roles", func(t *testing.T) {
		data, err := os.ReadFile("../../manifests/01-role.yaml")
		if err != nil {
			t.Fatalf("failed to read Role manifest: %v", err)
		}
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
		for {
			role := rbacv1.Role{}
			if err := decoder.Decode(&role); err != nil {
				if err == io.EOF {
					break
				}
				t.Fatalf("failed to decode Role: %v", err)
			}
			if role.Name == "" {
				continue
			}
			checkPolicyRules(t, "Role "+role.Name+" (ns: "+role.Namespace+")", role.Rules)
		}
	})
}
