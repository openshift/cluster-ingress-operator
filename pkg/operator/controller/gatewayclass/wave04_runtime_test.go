package gatewayclass

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	sailv1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/bundle"
	sailconfig "github.com/istio-ecosystem/sail-operator/pkg/config"
	"github.com/istio-ecosystem/sail-operator/pkg/helm"
	"github.com/istio-ecosystem/sail-operator/pkg/install"
	"github.com/istio-ecosystem/sail-operator/pkg/istioversion"
	"github.com/istio-ecosystem/sail-operator/pkg/revision"
	"github.com/istio-ecosystem/sail-operator/resources"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

func TestWave04PublishedRuntimeInputs(t *testing.T) {
	original := sailconfig.Config
	t.Cleanup(func() { sailconfig.Config = original })
	sailconfig.Config.ImageDigests = nil
	require.NoError(t, install.LoadImageDigestsFromCSV(ossmLiteRuntimeCSV))
	require.NoError(t, install.LoadImageDigestsFromCSV(bundle.CSV))
	require.NoError(t, install.LoadImageDigestsFromCSV(ossmLiteRuntimeCSV))
	reconciler := &reconciler{config: Config{OperandNamespace: "openshift-ingress"}}
	for _, target := range []struct{ alias, version, pilot, proxy string }{
		{"v1.27-latest", "v1.27.9", "8e98ca24cc00ccfcfdc66277e6e96aa5c8b77bd0561c1cbdbf5856871fa6908b", "e5262c8626da775e0e20118dce7c8ddfc339a1b9a46de80a4684fdeafc95a365"},
		{"v1.26-latest", "v1.26.8", "502d19f968c7beb81d022c232df68d0328ac6a15b6c3264e5dc6539082f5fdda", "8984bcbaa09f966f949cf96fb444c33a29321ae849a16bb68f9d87100f36e81c"},
	} {
		t.Run(target.alias, func(t *testing.T) {
			for _, inference := range []bool{false, true} {
				for _, proxy := range []struct {
					name   string
					config *extraIstioConfig
				}{
					{"absent", nil},
					{"empty", &extraIstioConfig{proxyConfig: &configv1.Proxy{}}},
					{"http", &extraIstioConfig{proxyConfig: &configv1.Proxy{Status: configv1.ProxyStatus{HTTPProxy: "http://proxy.example:3128"}}}},
					{"https", &extraIstioConfig{proxyConfig: &configv1.Proxy{Status: configv1.ProxyStatus{HTTPSProxy: "http://secure-proxy.example:3128"}}}},
					{"noproxy", &extraIstioConfig{proxyConfig: &configv1.Proxy{Status: configv1.ProxyStatus{NoProxy: "localhost,.svc"}}}},
					{"all", &extraIstioConfig{proxyConfig: &configv1.Proxy{Status: configv1.ProxyStatus{HTTPProxy: "http://proxy.example:3128", HTTPSProxy: "http://secure-proxy.example:3128", NoProxy: "localhost,.svc"}}}},
				} {
					t.Run(fmt.Sprintf("inference=%t/proxy=%s", inference, proxy.name), func(t *testing.T) {
						concrete, err := istioversion.Resolve(target.alias)
						require.NoError(t, err)
						require.Equal(t, target.version, concrete)
						opts := reconciler.buildInstallerOptions(inference, target.alias, proxy.config)
						require.Equal(t, concrete, opts.Version)
						require.Equal(t, "openshift-gateway", opts.Revision)
						require.NotEqual(t, sailv1.DefaultRevision, opts.Revision)
						require.NoError(t, install.ValidateVersion(resources.FS, concrete))
						values, err := revision.ComputeValues(opts.Values, opts.Namespace, concrete, sailconfig.PlatformOpenShift, "openshift", "", resources.FS, opts.Revision)
						require.NoError(t, err)
						require.Equal(t, "registry.redhat.io/openshift-service-mesh/istio-pilot-rhel9@sha256:"+target.pilot, *values.Pilot.Image)
						require.Equal(t, "registry.redhat.io/openshift-service-mesh/istio-proxyv2-rhel9@sha256:"+target.proxy, *values.Global.Proxy.Image)
						rendered, err := helm.RenderChart(resources.FS, fmt.Sprintf("%s/charts/istiod", concrete), helm.FromValues(values), opts.Namespace, opts.Revision+"-istiod")
						require.NoError(t, err)
						correctedValues := values.DeepCopy()
						correctedValues.DefaultRevision = nil
						corrected, err := helm.RenderChart(resources.FS, fmt.Sprintf("%s/charts/istiod", concrete), helm.FromValues(correctedValues), opts.Namespace, opts.Revision+"-istiod")
						require.NoError(t, err)
						assertCIOValidatorRenderEquivalent(t, rendered, corrected)
						t.Logf("published Sail: %s -> %s; %d chart templates; pilot=%s proxy=%s", target.alias, concrete, len(rendered), *values.Pilot.Image, *values.Global.Proxy.Image)
					})
				}
			}
		})
	}
	unknown := reconciler.buildInstallerOptions(false, "v99.99.99", nil)
	require.Equal(t, "v99.99.99", unknown.Version)
	require.Error(t, install.ValidateVersion(resources.FS, unknown.Version))
}

func TestWave04ReachableValidatorPaths(t *testing.T) {
	original := sailconfig.Config
	t.Cleanup(func() { sailconfig.Config = original })
	sailconfig.Config.ImageDigests = nil
	require.NoError(t, install.LoadImageDigestsFromCSV(ossmLiteRuntimeCSV))
	reconciler := &reconciler{config: Config{OperandNamespace: "openshift-ingress"}}
	selectors := []string{""}
	available := map[string]bool{}
	entries, err := resources.FS.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() {
			available[entry.Name()] = true
			selectors = append(selectors, entry.Name())
		}
	}
	for selector := range istioversion.Map {
		selectors = append(selectors, selector)
	}
	for _, version := range istioversion.List {
		selectors = append(selectors, strings.TrimPrefix(version.Name, "v"))
	}
	for _, selector := range selectors {
		for _, inference := range []bool{false, true} {
			t.Run(fmt.Sprintf("version=%s/inference=%t", selector, inference), func(t *testing.T) {
				proxy := &extraIstioConfig{proxyConfig: &configv1.Proxy{Status: configv1.ProxyStatus{HTTPProxy: "http://proxy.example:3128", HTTPSProxy: "http://secure-proxy.example:3128", NoProxy: "localhost,.svc"}}}
				opts := reconciler.buildInstallerOptions(inference, selector, proxy)
				require.Equal(t, "openshift-gateway", opts.Revision)
				require.NotEqual(t, sailv1.DefaultRevision, opts.Revision)
				require.True(t, *opts.ManageCRDs)
				require.True(t, *opts.IncludeAllCRDs)
				concrete := install.NormalizeVersion(opts.Version)
				if concrete == "" {
					var err error
					concrete, err = install.DefaultVersion(resources.FS)
					require.NoError(t, err)
				}
				validationErr := install.ValidateVersion(resources.FS, concrete)
				if !available[concrete] {
					require.Error(t, validationErr)
					t.Logf("catalog selector %q -> %s is rejected before values/rendering: %v", selector, concrete, validationErr)
					return
				}
				require.NoError(t, validationErr)
				values, err := revision.ComputeValues(opts.Values, opts.Namespace, concrete, sailconfig.PlatformOpenShift, "openshift", "", resources.FS, opts.Revision)
				require.NoError(t, err)
				rendered, err := helm.RenderChart(resources.FS, fmt.Sprintf("%s/charts/istiod", concrete), helm.FromValues(values), opts.Namespace, opts.Revision+"-istiod")
				require.NoError(t, err)
				correctedValues := values.DeepCopy()
				correctedValues.DefaultRevision = nil
				corrected, err := helm.RenderChart(resources.FS, fmt.Sprintf("%s/charts/istiod", concrete), helm.FromValues(correctedValues), opts.Namespace, opts.Revision+"-istiod")
				require.NoError(t, err)
				assertCIOValidatorRenderEquivalent(t, rendered, corrected)
				t.Logf("reachable selector %q -> %s; fixed revision=%s", selector, concrete, opts.Revision)
			})
		}
	}
	for _, unavailable := range []string{"v1.28.8", "v1.28.10", "v99.99.99"} {
		opts := reconciler.buildInstallerOptions(false, unavailable, nil)
		require.Error(t, install.ValidateVersion(resources.FS, install.NormalizeVersion(opts.Version)))
	}
	values, err := revision.ComputeValues(install.GatewayAPIDefaults(), "openshift-ingress", "v1.27.9", sailconfig.PlatformOpenShift, "openshift", "", resources.FS, sailv1.DefaultRevision)
	require.NoError(t, err)
	oldBase, err := helm.RenderChart(resources.FS, "v1.27.9/charts/base", helm.FromValues(values), "openshift-ingress", "default-base")
	require.NoError(t, err)
	values.DefaultRevision = nil
	newBase, err := helm.RenderChart(resources.FS, "v1.27.9/charts/base", helm.FromValues(values), "openshift-ingress", "default-base")
	require.NoError(t, err)
	const webhook = "base/templates/defaultrevision-validatingwebhookconfiguration.yaml"
	require.NotContains(t, oldBase[webhook], "kind: ValidatingWebhookConfiguration")
	require.Contains(t, newBase[webhook], "kind: ValidatingWebhookConfiguration", "generic Sail default-revision bug remains; CIO must not reach this path")
}

func TestWave04PublishedInstallerInitialization(t *testing.T) {
	original := sailconfig.Config
	t.Cleanup(func() { sailconfig.Config = original })
	sailconfig.Config.ImageDigests = nil
	require.NoError(t, install.LoadImageDigestsFromCSV(ossmLiteRuntimeCSV))
	expected := sailconfig.Config.ImageDigests
	kubeConfig := &rest.Config{Host: "http://127.0.0.1:1"}
	for index := 0; index < 2; index++ {
		installer, err := install.New(kubeConfig, resources.FS)
		require.NoError(t, err)
		require.NotNil(t, installer)
		require.Equal(t, expected, sailconfig.Config.ImageDigests)
	}
	var constructors sync.WaitGroup
	errors := make(chan error, 4)
	for index := 0; index < cap(errors); index++ {
		constructors.Add(1)
		go func() {
			defer constructors.Done()
			_, err := install.New(kubeConfig, resources.FS)
			errors <- err
		}()
	}
	constructors.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, expected, sailconfig.Config.ImageDigests)
	sailconfig.Config.ImageDigests = nil
	require.NoError(t, install.LoadImageDigestsFromCSV(bundle.CSV))
	stock := sailconfig.Config.ImageDigests
	require.NoError(t, install.LoadImageDigestsFromCSV(ossmLiteRuntimeCSV))
	require.Equal(t, stock, sailconfig.Config.ImageDigests, "published API is first-writer-wins; late OSSM-Lite initialization cannot replace stock images")
	require.NotEqual(t, expected["v1.27.9"], stock["v1.27.9"], "constructor order is material")
}

func TestWave04CIOAggregationLifecycle(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, rbacv1.AddToScheme(scheme))
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &reconciler{client: client}
	ctx := context.Background()
	roles, err := istioAccessRoles()
	require.NoError(t, err)
	require.Len(t, roles, 3)
	expectedVerbs := [][]string{{"*"}, {"create", "delete", "patch", "update"}, {"get", "list", "watch"}}
	for index, role := range roles {
		require.NotEmpty(t, role.Rules)
		require.Len(t, role.Labels, 2)
		for _, rule := range role.Rules {
			require.NotEmpty(t, rule.APIGroups)
			require.NotEmpty(t, rule.Resources)
			require.NotEmpty(t, rule.Verbs)
			require.Equal(t, expectedVerbs[index], rule.Verbs)
		}
	}
	require.NoError(t, reconciler.reconcileIstioAccess(ctx, false))
	require.NoError(t, reconciler.reconcileIstioAccess(ctx, false))
	var current rbacv1.ClusterRole
	require.NoError(t, client.Get(ctx, clientKey(roles[0]), &current))
	current.Rules = nil
	current.Labels["example.org/extra"] = "preserved"
	current.Annotations = map[string]string{"example.org/extra": "preserved"}
	require.NoError(t, client.Update(ctx, &current))
	require.NoError(t, reconciler.reconcileIstioAccess(ctx, false))
	require.NoError(t, client.Get(ctx, clientKey(roles[0]), &current))
	require.Equal(t, roles[0].Rules, current.Rules)
	require.Equal(t, "preserved", current.Labels["example.org/extra"])
	require.Equal(t, "preserved", current.Annotations["example.org/extra"])
	require.NoError(t, reconciler.reconcileIstioAccess(ctx, true))
	require.NoError(t, reconciler.reconcileIstioAccess(ctx, true))
	var remaining rbacv1.ClusterRoleList
	require.NoError(t, client.List(ctx, &remaining))
	require.Empty(t, remaining.Items)
}

func TestWave04CIOAggregationDrift(t *testing.T) {
	for _, drift := range []string{"membership", "aggregation-rule", "both"} {
		t.Run(drift, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, rbacv1.AddToScheme(scheme))
			client := fake.NewClientBuilder().WithScheme(scheme).Build()
			reconciler := &reconciler{client: client}
			ctx := context.Background()
			require.NoError(t, reconciler.reconcileIstioAccess(ctx, false))
			roles, err := istioAccessRoles()
			require.NoError(t, err)
			for _, desired := range roles {
				var current rbacv1.ClusterRole
				require.NoError(t, client.Get(ctx, clientKey(desired), &current))
				if drift != "aggregation-rule" {
					for _, membership := range []string{"admin", "edit", "view"} {
						current.Labels["rbac.authorization.k8s.io/aggregate-to-"+membership] = "true"
					}
				}
				if drift != "membership" {
					current.AggregationRule = &rbacv1.AggregationRule{ClusterRoleSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"example.org/foreign": "true"}}}}
					current.Rules = nil
				}
				current.Labels["example.org/extra"] = "preserved"
				require.NoError(t, client.Update(ctx, &current))
			}
			require.NoError(t, reconciler.reconcileIstioAccess(ctx, false))
			for _, desired := range roles {
				var repaired rbacv1.ClusterRole
				require.NoError(t, client.Get(ctx, clientKey(desired), &repaired))
				expected := desired.DeepCopy()
				expected.Labels["example.org/extra"] = "preserved"
				require.Equal(t, expected.Labels, repaired.Labels)
				require.Equal(t, desired.Rules, repaired.Rules)
				require.Nil(t, repaired.AggregationRule)
				require.NoError(t, reconciler.reconcileIstioAccess(ctx, false))
				var repeated rbacv1.ClusterRole
				require.NoError(t, client.Get(ctx, clientKey(desired), &repeated))
				require.Equal(t, repaired, repeated, "drift recovery must be idempotent")
			}
		})
	}
}

func TestWave04CIOAggregationOwnership(t *testing.T) {
	for _, remove := range []bool{false, true} {
		for _, owned := range []bool{false, true} {
			t.Run(fmt.Sprintf("remove=%t-owner-reference=%t", remove, owned), func(t *testing.T) {
				scheme := runtime.NewScheme()
				require.NoError(t, rbacv1.AddToScheme(scheme))
				roles, err := istioAccessRoles()
				require.NoError(t, err)
				conflict := roles[0].DeepCopy()
				if owned {
					conflict.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: "unrelated", UID: "unrelated"}}
				} else {
					delete(conflict.Labels, istioAccessOwnerLabel)
				}
				client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(conflict).Build()
				reconciler := &reconciler{client: client}
				require.ErrorContains(t, reconciler.reconcileIstioAccess(context.Background(), remove), "conflicting ownership")
				var current rbacv1.ClusterRole
				require.NoError(t, client.Get(context.Background(), clientKey(conflict), &current))
				require.Equal(t, conflict.Labels, current.Labels)
				require.Equal(t, conflict.OwnerReferences, current.OwnerReferences)
				require.Equal(t, conflict.Rules, current.Rules)
			})
		}
	}
}

func clientKey(role *rbacv1.ClusterRole) client.ObjectKey {
	return client.ObjectKey{Name: role.Name}
}

func assertCIOValidatorRenderEquivalent(t *testing.T, original, corrected map[string]string) {
	t.Helper()
	require.Len(t, corrected, len(original))
	for name, content := range original {
		require.Contains(t, corrected, name)
		if name != "istiod/templates/configmap-values.yaml" {
			require.Equal(t, content, corrected[name], name)
			continue
		}
		var before, after corev1.ConfigMap
		require.NoError(t, yaml.Unmarshal([]byte(content), &before))
		require.NoError(t, yaml.Unmarshal([]byte(corrected[name]), &after))
		require.Len(t, after.Data, len(before.Data))
		for field, beforeJSON := range before.Data {
			var beforeValues, afterValues map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(beforeJSON), &beforeValues))
			require.NoError(t, json.Unmarshal([]byte(after.Data[field]), &afterValues))
			require.Contains(t, beforeValues, "defaultRevision")
			require.Equal(t, "", beforeValues["defaultRevision"])
			if value, present := afterValues["defaultRevision"]; present {
				require.Equal(t, "", value)
			}
			delete(beforeValues, "defaultRevision")
			delete(afterValues, "defaultRevision")
			require.Equal(t, beforeValues, afterValues, "only diagnostic defaultRevision may differ")
			before.Data[field] = after.Data[field]
		}
		require.Equal(t, before, after, "diagnostic ConfigMap metadata must remain equal")
		t.Log("validator correction changes only diagnostic defaultRevision serialization for the fixed non-default CIO revision")
	}
}
