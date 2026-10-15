package manifests

import (
	"os"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

func TestGatewayClassAdmissionPolicy(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../manifests/01-validating-admission-policy-gatewayclasses.yaml")
	require.NoError(t, err)
	var policy admissionv1.ValidatingAdmissionPolicy
	require.NoError(t, yaml.Unmarshal(data, &policy))
	require.Equal(t, admissionv1.Fail, *policy.Spec.FailurePolicy)
	require.Len(t, policy.Spec.MatchConstraints.ResourceRules, 1)
	rule := policy.Spec.MatchConstraints.ResourceRules[0]
	require.Equal(t, []string{"gatewayclasses"}, rule.Resources)
	require.Equal(t, []admissionv1.OperationType{admissionv1.Create, admissionv1.Update, admissionv1.Delete}, rule.Operations)
	require.NotContains(t, policy.Annotations, "feature-gate.release.openshift.io")
	data, err = os.ReadFile("../../manifests/01-validating-admission-policy-binding-gatewayclasses.yaml")
	require.NoError(t, err)
	var binding admissionv1.ValidatingAdmissionPolicyBinding
	require.NoError(t, yaml.Unmarshal(data, &binding))
	require.Equal(t, policy.Name, binding.Spec.PolicyName)
	require.Equal(t, []admissionv1.ValidationAction{admissionv1.Deny}, binding.Spec.ValidationActions)
	environment, err := cel.NewEnv(cel.Variable("request", cel.DynType), cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType), cel.Variable("variables", cel.DynType))
	require.NoError(t, err)
	for _, operation := range []string{"CREATE", "UPDATE", "DELETE"} {
		for _, test := range []struct {
			name, className, controller, username string
			node, pod, allowed                    bool
		}{
			{"operator pod", "openshift-clusterip", "openshift.io/gateway-controller/v1", "system:serviceaccount:openshift-ingress-operator:ingress-operator", true, true, true},
			{"user", "openshift-clusterip", "openshift.io/gateway-controller/v1", "admin", true, true, false},
			{"unbound operator", "openshift-clusterip", "openshift.io/gateway-controller/v1", "system:serviceaccount:openshift-ingress-operator:ingress-operator", false, false, false},
			{"missing node", "openshift-clusterip", "openshift.io/gateway-controller/v1", "system:serviceaccount:openshift-ingress-operator:ingress-operator", false, true, false},
			{"missing pod", "openshift-clusterip", "openshift.io/gateway-controller/v1", "system:serviceaccount:openshift-ingress-operator:ingress-operator", true, false, false},
			{"default", "openshift-default", "openshift.io/gateway-controller/v1", "admin", false, false, true},
			{"default wrong controller", "openshift-default", "example.com/other", "admin", false, false, false},
			{"operator wrong controller", "openshift-clusterip", "example.com/other", "system:serviceaccount:openshift-ingress-operator:ingress-operator", true, true, false},
			{"other prefix", "customer-class", "example.com/other", "admin", false, false, true},
			{"future openshift name", "openshift-future", "openshift.io/gateway-controller/v1", "admin", false, false, false},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				class := map[string]any{"metadata": map[string]any{"name": test.className}, "spec": map[string]any{"controllerName": test.controller}}
				extra := map[string]any{}
				if test.node {
					extra["authentication.kubernetes.io/node-name"] = []string{"node"}
				}
				if test.pod {
					extra["authentication.kubernetes.io/pod-name"] = []string{"pod"}
				}
				variables := map[string]any{}
				input := map[string]any{"request": map[string]any{"operation": operation, "userInfo": map[string]any{"username": test.username, "extra": extra}}, "object": class, "oldObject": class, "variables": variables}
				if operation == "DELETE" {
					input["object"] = nil
				}
				evaluate := func(expression string) any {
					ast, issues := environment.Compile(expression)
					require.NoError(t, issues.Err())
					program, err := environment.Program(ast)
					require.NoError(t, err)
					value, _, err := program.Eval(input)
					require.NoError(t, err)
					return value.Value()
				}
				for _, variable := range policy.Spec.Variables {
					variables[variable.Name] = evaluate(variable.Expression)
				}
				allowed := true
				for _, validation := range policy.Spec.Validations {
					allowed = evaluate(validation.Expression).(bool) && allowed
				}
				require.Equal(t, test.allowed, allowed)
			})
		}
	}
}
