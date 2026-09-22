package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"

	"github.com/istio-ecosystem/sail-operator/chart"
	"github.com/istio-ecosystem/sail-operator/pkg/helm"
	"github.com/istio-ecosystem/sail-operator/pkg/install"
	resources "github.com/istio-ecosystem/sail-operator/resources"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

const (
	generatedMarker = "# --- BEGIN GENERATED: do not edit below; run hack/update-sail-rbac.sh to regenerate ---"
	istiodChartPath = "charts/istiod"
	renderNamespace = "openshift-ingress"
	renderRelease   = "istiod"
)

type renderedRBACObject struct {
	Name  string
	Rules []rbacv1.PolicyRule
}

type staleManifestError struct {
	diff string
}

func (e *staleManifestError) Error() string {
	return fmt.Sprintf("generated RBAC is out of date; run hack/update-sail-rbac.sh to regenerate:\n%s", e.diff)
}

// NewSailRBACCommand returns the generator command used to keep the checked-in
// no-escalate RBAC superset synchronized with Sail's vendored inputs.
func NewSailRBACCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sail-rbac",
		Short: "Generate or verify istiod RBAC rules",
		Long:  "Renders the vendored istiod Helm chart and extracts ClusterRole/Role rules for the Sail Library ClusterRole manifest.",
	}
	cmd.AddCommand(newSailRBACGenerateCommand())
	cmd.AddCommand(newSailRBACVerifyCommand())
	return cmd
}

// newSailRBACGenerateCommand constructs regeneration separately so updates only
// replace the generated manifest section and preserve its reviewed manual rules.
func newSailRBACGenerateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "generate <manifest-path>",
		Short: "Generate istiod RBAC rules into the manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			generated, err := generateFromAllVersions()
			if err != nil {
				return err
			}
			return writeManifest(args[0], generated)
		},
	}
}

// newSailRBACVerifyCommand uses the production generator to detect when a Sail
// dependency update changes the RBAC superset that avoids requiring escalate.
func newSailRBACVerifyCommand() *cobra.Command {
	return newSailRBACVerifyCommandWithGenerate(generateFromAllVersions)
}

// newSailRBACVerifyCommandWithGenerate permits tests to exercise stale-manifest
// diagnostics without depending on the vendored chart contents.
func newSailRBACVerifyCommandWithGenerate(generate func() (string, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "verify <manifest-path>",
		Short: "Verify the manifest matches the vendored charts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			generated, err := generate()
			if err != nil {
				return err
			}
			err = verifyManifest(args[0], generated)
			var staleErr *staleManifestError
			if errors.As(err, &staleErr) {
				cmd.SilenceUsage = true
			}
			return err
		},
	}
}

// generateFromAllVersions builds the least-privilege no-escalate superset from
// every supported vendored Istiod chart and Sail/Istio user-facing RBAC aggregation rule.
func generateFromAllVersions() (string, error) {
	entries, err := resources.FS.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("reading resource FS root: %w", err)
	}

	var versions []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "v") {
			versions = append(versions, entry.Name())
		}
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("no versioned chart directories found")
	}
	sort.Strings(versions)

	// Preserve the first encountered rule so the generated manifest remains stable
	// while still covering every supported version and user-facing RBAC aggregation input.
	seen := map[string]struct{}{}
	var istiodRules []rbacv1.PolicyRule

	for _, version := range versions {
		chartPath := version + "/" + istiodChartPath
		objects, err := renderAndExtract(chartPath)
		if err != nil {
			return "", fmt.Errorf("version %s: %w", version, err)
		}
		for _, obj := range objects {
			istiodRules, err = appendUniqueRules(istiodRules, obj.Rules, seen)
			if err != nil {
				return "", err
			}
		}
	}

	userRBACRules, err := aggregationUserRBACRules()
	if err != nil {
		return "", err
	}
	userRBACRules, err = appendUniqueRules(nil, userRBACRules, seen)
	if err != nil {
		return "", err
	}

	return renderRules(versions, istiodRules, userRBACRules)
}

// appendUniqueRules preserves first-seen order while removing equivalent rules
// across chart versions and sources, which makes generated diffs reproducible.
func appendUniqueRules(allRules, rules []rbacv1.PolicyRule, seen map[string]struct{}) ([]rbacv1.PolicyRule, error) {
	for _, rule := range rules {
		key, err := json.Marshal(rule)
		if err != nil {
			return nil, fmt.Errorf("marshaling rule: %w", err)
		}
		if _, exists := seen[string(key)]; !exists {
			seen[string(key)] = struct{}{}
			allRules = append(allRules, rule)
		}
	}
	return allRules, nil
}

// aggregationUserRBACRules derives the rules for Sail Library, which uses Kubernetes RBAC aggregation labels (rbac.authorization.k8s.io/aggregate-to-*)
// to add user-facing view, edit, and admin permissions for Sail Operator CRDs.
// This is the standard way to ship user-facing RBAC with CRDs, and cluster-ingress-operator installs those CRDs when OSSM has not already installed them.
func aggregationUserRBACRules() ([]rbacv1.PolicyRule, error) {
	entries, err := fs.ReadDir(chart.CRDsFS, ".")
	if err != nil {
		return nil, fmt.Errorf("reading Sail CRDs: %w", err)
	}

	groupResources := map[string][]string{}
	var groups []string
	for _, entry := range entries {
		content, err := fs.ReadFile(chart.CRDsFS, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading Sail CRD %s: %w", entry.Name(), err)
		}
		crd := apiextensionsv1.CustomResourceDefinition{}
		if err := sigsyaml.Unmarshal(content, &crd); err != nil {
			return nil, fmt.Errorf("parsing Sail CRD %s: %w", entry.Name(), err)
		}
		if !strings.HasSuffix(crd.Spec.Group, ".istio.io") && !strings.HasSuffix(crd.Spec.Group, "sailoperator.io") {
			continue
		}
		if _, exists := groupResources[crd.Spec.Group]; !exists {
			groups = append(groups, crd.Spec.Group)
		}
		groupResources[crd.Spec.Group] = append(groupResources[crd.Spec.Group], crd.Spec.Names.Plural)
	}

	sort.Strings(groups)
	rules := make([]rbacv1.PolicyRule, 0, len(groups))
	for _, group := range groups {
		sort.Strings(groupResources[group])
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{group},
			Resources: groupResources[group],
			Verbs:     []string{"*"},
		})
	}
	return rules, nil
}

// renderAndExtract renders one supported Istiod chart with the same defaults the
// library uses, so the generated superset tracks the roles it will create.
func renderAndExtract(chartPath string) ([]renderedRBACObject, error) {
	values := install.GatewayAPIDefaults(renderNamespace)
	helmValues := helm.FromValues(values)

	rendered, err := helm.RenderChart(resources.FS, chartPath, helmValues, renderNamespace, renderRelease)
	if err != nil {
		return nil, fmt.Errorf("rendering chart: %w", err)
	}

	return extractRenderedRBACObjects(rendered)
}

// extractRenderedRBACObjects selects RBAC by object kind rather than template
// filename, so upstream template renames cannot silently omit permissions.
func extractRenderedRBACObjects(rendered map[string]string) ([]renderedRBACObject, error) {
	// Sort template names for deterministic output.
	var templateNames []string
	for name := range rendered {
		templateNames = append(templateNames, name)
	}
	sort.Strings(templateNames)

	var objects []renderedRBACObject
	for _, name := range templateNames {
		content := strings.TrimSpace(rendered[name])
		if content == "" {
			continue
		}
		parsed, err := extractRBACObjects(content)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		objects = append(objects, parsed...)
	}
	return objects, nil
}

// extractRBACObjects decodes multi-document Helm output and retains only roles,
// because bindings grant no permissions to include in the no-escalate superset.
func extractRBACObjects(content string) ([]renderedRBACObject, error) {
	var objects []renderedRBACObject
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(content), 4096)
	for {
		var object any
		if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		metadata, ok := object.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := metadata["kind"].(string)
		if kind != "ClusterRole" && kind != "Role" {
			continue
		}

		var cr rbacv1.ClusterRole
		data, err := json.Marshal(metadata)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &cr); err != nil {
			return nil, err
		}

		objects = append(objects, renderedRBACObject{
			Name:  fmt.Sprintf("%s/%s", strings.ToLower(cr.Kind), cr.Name),
			Rules: cr.Rules,
		})
	}
	return objects, nil
}

// renderRules labels each generated source so reviewers can distinguish Istiod
// runtime permissions from the Sail/Istio user-facing RBAC aggregation rules.
func renderRules(versions []string, istiodRules, userRBACAggregationRules []rbacv1.PolicyRule) (string, error) {
	var sb strings.Builder
	fmt.Fprintln(&sb, "# --- Istiod runtime RBAC (generated) ---")
	fmt.Fprintln(&sb, "# Source: every supported vendored Istiod chart at v*/charts/istiod.")
	fmt.Fprintf(&sb, "# Union across supported versions: %s\n", strings.Join(versions, ", "))

	out, err := sigsyaml.Marshal(istiodRules)
	if err != nil {
		return "", fmt.Errorf("marshaling Istiod rules: %w", err)
	}
	sb.Write(out)
	sb.WriteByte('\n')

	fmt.Fprintln(&sb, "# --- Sail/Istio user-facing RBAC aggregation (generated) ---")
	fmt.Fprintln(&sb, "# Source: Sail Library CRDs.")
	fmt.Fprintln(&sb, "# Sail Library uses Kubernetes RBAC aggregation labels (rbac.authorization.k8s.io/aggregate-to-*) to add user-facing view, edit, and admin permissions for Sail Operator CRDs.")
	fmt.Fprintln(&sb, "# This is the standard way to ship user-facing RBAC with CRDs, and cluster-ingress-operator installs those CRDs when OSSM has not already installed them.")

	out, err = sigsyaml.Marshal(userRBACAggregationRules)
	if err != nil {
		return "", fmt.Errorf("marshaling user-facing RBAC aggregation rules: %w", err)
	}
	sb.Write(out)

	return sb.String(), nil
}

// writeManifest replaces only the generated tail so manually curated Sail
// operational RBAC and its reviewer-facing explanation survive regeneration.
func writeManifest(manifestPath string, generated string) error {
	existing, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}

	idx := strings.Index(string(existing), generatedMarker)
	if idx == -1 {
		return fmt.Errorf("marker %q not found in %s", generatedMarker, manifestPath)
	}

	header := string(existing[:idx])
	return os.WriteFile(manifestPath, []byte(header+generatedMarker+"\n\n"+generated), 0640)
}

// verifyManifest compares the generated tail rather than the whole manifest so
// reviewers get a focused diff when vendored Sail inputs change required RBAC.
func verifyManifest(manifestPath string, generated string) error {
	existing, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}

	idx := strings.Index(string(existing), generatedMarker)
	if idx == -1 {
		return fmt.Errorf("marker %q not found in %s", generatedMarker, manifestPath)
	}

	existingGenerated := strings.TrimSpace(string(existing)[idx+len(generatedMarker):])
	expectedGenerated := strings.TrimSpace(generated)

	if existingGenerated != expectedGenerated {
		diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A:        difflib.SplitLines(existingGenerated + "\n"),
			B:        difflib.SplitLines(expectedGenerated + "\n"),
			FromFile: "checked-in generated manifest section",
			ToFile:   "newly rendered expected section",
			Context:  3,
		})
		if err != nil {
			return fmt.Errorf("generating generated RBAC diff: %w", err)
		}
		return &staleManifestError{diff: diff}
	}

	fmt.Println("Sail RBAC manifest is up to date.")
	return nil
}
