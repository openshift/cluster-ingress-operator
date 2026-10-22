package manifests

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	operatorv1 "github.com/openshift/api/operator/v1"
	releasemanifest "github.com/openshift/library-go/pkg/manifest"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestGatewayClassAdmissionManifestsIncludedInProfilesAndFeatureSets(t *testing.T) {
	t.Parallel()
	// Load every profile variant and the shared binding, detecting extra overlapping files.
	filenames, err := filepath.Glob("../../manifests/01-validating-admission-policy*-gatewayclasses*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var manifests []releasemanifest.Manifest
	for _, filename := range filenames {
		// CVO parses files separately before selecting mutually exclusive profiles.
		parsed, err := releasemanifest.ManifestsFromFiles([]string{filename})
		if err != nil {
			t.Fatalf("failed to load admission manifest %s: %v", filename, err)
		}
		manifests = append(manifests, parsed...)
	}
	if len(manifests) != 3 {
		t.Fatalf("expected two policy variants and one shared binding, got %d", len(manifests))
	}

	for _, profile := range []string{"self-managed-high-availability", "single-node-developer", "ibm-cloud-managed"} {
		for _, featureSet := range []string{"Default", "TechPreviewNoUpgrade"} {
			t.Run(profile+"/"+featureSet, func(t *testing.T) {
				policies, bindings := 0, 0
				for _, manifest := range manifests {
					if err := manifest.Include(nil, &featureSet, &profile, nil, nil, nil, nil); err != nil {
						continue
					}
					if manifest.Obj.GetName() != "openshift-ingress-operator-gatewayclasses" || manifest.Obj.GetAnnotations()["capability.openshift.io/name"] != "Ingress" {
						t.Fatalf("unexpected included object: %s", manifest.Obj.GetName())
					}

					// The same logical policy is selected once, with only the profile's claim requirement.
					switch manifest.Obj.GetKind() {
					case "ValidatingAdmissionPolicy":
						policies++
						validations, _, err := unstructured.NestedSlice(manifest.Obj.Object, "spec", "validations")
						if err != nil {
							t.Fatal(err)
						}
						wantValidations := 2
						if profile == "ibm-cloud-managed" {
							wantValidations = 1
						}
						if len(validations) != wantValidations {
							t.Errorf("expected %d validations for profile, got %d", wantValidations, len(validations))
						}
					case "ValidatingAdmissionPolicyBinding":
						// Both variants use one binding, not two simultaneously enforced policies.
						bindings++
						policyName, _, err := unstructured.NestedString(manifest.Obj.Object, "spec", "policyName")
						if err != nil || policyName != "openshift-ingress-operator-gatewayclasses" {
							t.Fatalf("incorrect binding reference %q: %v", policyName, err)
						}
					default:
						t.Fatalf("unexpected manifest kind %s", manifest.Obj.GetKind())
					}
				}
				if policies != 1 || bindings != 1 {
					t.Fatalf("expected exactly one policy and shared binding; got %d policies and %d bindings", policies, bindings)
				}
			})
		}
	}
}

func TestGatewayClassAdmissionPolicyScope(t *testing.T) {
	t.Parallel()
	for _, filename := range []string{
		"../../manifests/01-validating-admission-policy-gatewayclasses.yaml",
		"../../manifests/01-validating-admission-policy-gatewayclasses-ibm-cloud-managed.yaml",
	} {
		t.Run(filename, func(t *testing.T) {
			// Decode the actual CVO input, not a duplicate policy fixture.
			data, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			var policy admissionregistrationv1.ValidatingAdmissionPolicy
			if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096).Decode(&policy); err != nil {
				t.Fatal(err)
			}

			// Keep the reservation static, fail-closed and restricted to class creation.
			if policy.Spec.ParamKind != nil || policy.Spec.FailurePolicy == nil || *policy.Spec.FailurePolicy != admissionregistrationv1.Fail {
				t.Fatal("expected a parameter-free Fail policy")
			}
			if policy.Spec.MatchConstraints == nil || len(policy.Spec.MatchConstraints.ResourceRules) != 1 || len(policy.Spec.MatchConditions) != 0 {
				t.Fatal("expected one resource rule without match conditions")
			}

			// Follow the EP's shared name variables and per-validation reserved-name guard.
			var variableNames []string
			for _, variable := range policy.Spec.Variables {
				variableNames = append(variableNames, variable.Name)
			}
			if !slices.Equal(variableNames, []string{"gatewayClass", "hasOpenShiftPrefix", "hasManagedProfileName"}) {
				t.Fatalf("unexpected policy variables: %v", variableNames)
			}
			for _, validation := range policy.Spec.Validations {
				if !strings.HasPrefix(validation.Expression, "!variables.hasManagedProfileName ||") {
					t.Fatalf("expected reserved-name guard on validation: %s", validation.Expression)
				}
			}

			rule := policy.Spec.MatchConstraints.ResourceRules[0]
			if !slices.Equal(rule.Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Create}) ||
				!slices.Equal(rule.APIGroups, []string{"gateway.networking.k8s.io"}) ||
				!slices.Equal(rule.APIVersions, []string{"v1"}) ||
				!slices.Equal(rule.Resources, []string{"gatewayclasses"}) || rule.Scope == nil || *rule.Scope != admissionregistrationv1.ClusterScope {
				t.Fatalf("unexpected reservation scope: %+v", rule)
			}
		})
	}
}

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

// TestNoWildcardsInOperatorRBAC verifies that the CVO-managed ClusterRole
// and Role manifests do not use wildcard apiGroups, resources, or verbs.
// This prevents re-introduction of overly broad RBAC permissions after the
// OCPBUGS-6718 wildcard elimination.
func TestNoWildcardsInOperatorRBAC(t *testing.T) {
	checkPolicyRules := func(t *testing.T, label string, rules []rbacv1.PolicyRule) {
		t.Helper()
		if len(rules) == 0 {
			t.Errorf("%s: expected at least one RBAC rule, got none", label)
			return
		}
		for i, rule := range rules {
			for _, group := range rule.APIGroups {
				if group == "*" {
					t.Errorf("%s rule %d: wildcard apiGroup for resources %v",
						label, i, rule.Resources)
				}
			}
			for _, resource := range rule.Resources {
				if resource == "*" {
					t.Errorf("%s rule %d: wildcard resource in apiGroups %v",
						label, i, rule.APIGroups)
				}
			}
			for _, verb := range rule.Verbs {
				if verb == "*" {
					t.Errorf("%s rule %d: wildcard verb for resources %v in apiGroups %v",
						label, i, rule.Resources, rule.APIGroups)
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
		var count int
		for {
			role := rbacv1.Role{}
			if err := decoder.Decode(&role); err != nil {
				if err == io.EOF {
					break
				}
				t.Fatalf("failed to decode Role document: %v", err)
			}
			if role.Name == "" {
				continue
			}
			count++
			label := "Role " + role.Name + " (ns: " + role.Namespace + ")"
			checkPolicyRules(t, label, role.Rules)
		}
		if count == 0 {
			t.Fatal("no Role documents found in 01-role.yaml")
		}
	})
}
