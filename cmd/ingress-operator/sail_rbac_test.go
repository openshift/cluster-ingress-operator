package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	rbacv1 "k8s.io/api/rbac/v1"
)

func TestExtractRenderedRBACObjectsFiltersNonRBACObjects(t *testing.T) {
	rendered := map[string]string{
		"templates/another-resource.yaml": `
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: namespaced-reader
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get"]
`,
		"templates/does-not-look-like-rbac.yaml": `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-reader
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get"]
`,
		"templates/cluster-role-binding.yaml": `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ignored-binding
`,
		"templates/configmap.yaml": `
apiVersion: v1
kind: ConfigMap
metadata:
  name: ignored-configmap
data:
  template: |
    ---
    rendered: template
`,
		"templates/NOTES.txt": "This is a rendered Helm note.",
	}

	objects, err := extractRenderedRBACObjects(rendered)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []renderedRBACObject{
		{
			Name: "role/namespaced-reader",
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get"},
			}},
		},
		{
			Name: "clusterrole/cluster-reader",
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get"},
			}},
		},
	}
	if diff := cmp.Diff(expected, objects); diff != "" {
		t.Errorf("unexpected extracted RBAC objects (-want +got):\n%s", diff)
	}
}

func TestSailRBACVerifyStaleManifestShowsDiffWithoutUsage(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "sail-rbac.yaml")
	if err := os.WriteFile(manifestPath, []byte("apiVersion: rbac.authorization.k8s.io/v1\n"+generatedMarker+"\n\n# checked-in rules\n- verbs: [get]\n"), 0600); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	cmd := newSailRBACVerifyCommandWithGenerate(func() (string, error) {
		return "# rendered rules\n- verbs: [list]\n", nil
	})
	cmd.SetArgs([]string{manifestPath})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected stale manifest error")
	}
	var staleErr *staleManifestError
	if !errors.As(err, &staleErr) {
		t.Fatalf("expected stale manifest error, got %T: %v", err, err)
	}
	if got := err.Error(); !containsAll(got,
		"run hack/update-sail-rbac.sh to regenerate",
		"--- checked-in generated manifest section",
		"+++ newly rendered expected section",
		"-# checked-in rules",
		"+# rendered rules",
	) {
		t.Errorf("stale manifest error did not include the expected diagnostics:\n%s", got)
	}
	if got := stderr.String(); containsAll(got, "Usage:") {
		t.Errorf("expected Cobra usage to be suppressed, got:\n%s", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no successful verification output, got:\n%s", stdout.String())
	}
}

func TestRenderRulesIncludesProvenanceSectionsAndPreservesOrder(t *testing.T) {
	versions := []string{"v1.27.3", "v1.30.4"}
	istiodRules := []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}},
		{APIGroups: []string{"networking.istio.io"}, Resources: []string{"virtualservices"}, Verbs: []string{"list"}},
	}
	userRBACAggregationRules := []rbacv1.PolicyRule{
		{APIGroups: []string{"sailoperator.io"}, Resources: []string{"istios"}, Verbs: []string{"*"}},
	}

	got, err := renderRules(versions, istiodRules, userRBACAggregationRules)
	if err != nil {
		t.Fatalf("rendering rules: %v", err)
	}
	again, err := renderRules(versions, istiodRules, userRBACAggregationRules)
	if err != nil {
		t.Fatalf("rendering rules again: %v", err)
	}
	if got != again {
		t.Errorf("rendered output was not deterministic:\nfirst:\n%s\nsecond:\n%s", got, again)
	}
	if !containsAll(got,
		"# --- Istiod runtime RBAC (generated) ---",
		"# Source: every supported vendored Istiod chart at v*/charts/istiod.",
		"# Union across supported versions: v1.27.3, v1.30.4",
		"# --- Sail/Istio user-facing RBAC aggregation (generated) ---",
		"# Source: Sail Library CRDs.",
		"# Sail Library uses Kubernetes RBAC aggregation labels (rbac.authorization.k8s.io/aggregate-to-*) to add user-facing view, edit, and admin permissions for Sail Operator CRDs.",
		"# This is the standard way to ship user-facing RBAC with CRDs, and cluster-ingress-operator installs those CRDs when OSSM has not already installed them.",
	) {
		t.Errorf("rendered output did not include expected provenance:\n%s", got)
	}
	if configMap := strings.Index(got, "configmaps"); configMap == -1 || configMap > strings.Index(got, "virtualservices") || strings.Index(got, "virtualservices") > strings.Index(got, "istios") {
		t.Errorf("rendered rules did not retain source order:\n%s", got)
	}
}

func containsAll(s string, substrings ...string) bool {
	for _, substring := range substrings {
		if !strings.Contains(s, substring) {
			return false
		}
	}
	return true
}
