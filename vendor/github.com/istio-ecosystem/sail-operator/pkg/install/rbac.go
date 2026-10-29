// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package install

import rbacv1 "k8s.io/api/rbac/v1"

// LibraryRBACRules returns the RBAC PolicyRules required when using the
// install library. Consumers should aggregate these into their own ClusterRole.
//
// These rules are derived from chart/templates/rbac/role.yaml with
// sailoperator.io CRs filtered out (those are for the operator, not the library).
//
// Example usage:
//
//	rules := append(myOperatorRules, install.LibraryRBACRules()...)
//
// TODO: Consider generating this from role.yaml or adding a verification test
// to ensure these rules stay in sync with the Helm template.
func LibraryRBACRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{
				"configmaps",
				"endpoints",
				"events",
				"namespaces",
				"nodes",
				"persistentvolumeclaims",
				"pods",
				"replicationcontrollers",
				"resourcequotas",
				"secrets",
				"serviceaccounts",
				"services",
			},
			Verbs: []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"admissionregistration.k8s.io"},
			Resources: []string{
				"mutatingwebhookconfigurations",
				"validatingadmissionpolicies",
				"validatingadmissionpolicybindings",
				"validatingwebhookconfigurations",
			},
			Verbs: []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"apiextensions.k8s.io"},
			Resources: []string{"customresourcedefinitions"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"apps"},
			Resources: []string{"daemonsets", "deployments"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"autoscaling"},
			Resources: []string{"horizontalpodautoscalers"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"discovery.k8s.io"},
			Resources: []string{"endpointslices"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"k8s.cni.cncf.io"},
			Resources: []string{"network-attachment-definitions"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"networking.istio.io"},
			Resources: []string{"envoyfilters"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"networking.k8s.io"},
			Resources: []string{"networkpolicies"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"policy"},
			Resources: []string{"poddisruptionbudgets"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"rbac.authorization.k8s.io"},
			Resources: []string{"clusterrolebindings", "clusterroles", "rolebindings", "roles"},
			Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch", "bind", "escalate"},
		},
		{
			APIGroups:     []string{"security.openshift.io"},
			Resources:     []string{"securitycontextconstraints"},
			ResourceNames: []string{"privileged"},
			Verbs:         []string{"use"},
		},
	}
}
